package service

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/veloce-ailab/veloce/internal/cache"
	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// ChatExecutorMessage is a single message in a server-side chat completion call.
// Content may be empty when the assistant message only carries tool calls.
// ToolCalls, ToolCallID and Name are provider-neutral tool-loop state; each
// protocol-specific request builder translates them into its own wire format.
type ChatExecutorMessage struct {
	Role       string                    `json:"role"`
	Content    string                    `json:"content"`
	Parts      []ChatExecutorContentPart `json:"parts,omitempty"`
	ToolCalls  []map[string]interface{}  `json:"tool_calls,omitempty"`
	ToolCallID string                    `json:"tool_call_id,omitempty"`
	Name       string                    `json:"name,omitempty"`
}

type ChatExecutorContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
	Data     string `json:"data,omitempty"`
	URL      string `json:"url,omitempty"`
}

// ChatExecutorTool describes a tool exposed to the upstream model. Schema is the
// JSON Schema object for the tool's parameters.
type ChatExecutorTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Schema      map[string]interface{} `json:"schema"`
}

// ChatExecutorRequest is one server-side, billed completion turn against a model.
// UserChannelID, when non-zero, pins the request to a single user-facing channel
// (渠道) exactly like an API key bound to that channel would.
type ChatExecutorRequest struct {
	Context         context.Context
	ModelName       string
	UserChannelID   uint
	Messages        []ChatExecutorMessage
	System          string
	Tools           []ChatExecutorTool
	MaxTokens       int
	Temperature     *float64
	ReasoningEffort string
	Stream          bool
	OnTextDelta     func(delta string) error
	// ChargeBalance keeps governed Studio operations billable in personal mode.
	ChargeBalance bool
}

// ChatExecutorToolCall is a tool invocation requested by the model.
type ChatExecutorToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// ChatExecutorResult is the outcome of a single completion turn.
type ChatExecutorResult struct {
	Content   string
	ToolCalls []ChatExecutorToolCall
	// AssistantMessage is the raw assistant message as returned upstream, suitable
	// for appending verbatim to the next turn's message list.
	AssistantMessage map[string]interface{}
	FinishReason     string
	Cost             decimal.Decimal
}

// ChatExecutorError carries an HTTP status so callers can surface upstream and
// billing failures with the right code.
type ChatExecutorError struct {
	Status            int
	Message           string
	ChannelID         uint
	UserChannelID     uint
	ModelName         string
	UpstreamModelName string
	UpstreamURL       string
}

func (e *ChatExecutorError) Error() string { return e.Message }

func newChatExecutorError(status int, message string) *ChatExecutorError {
	return &ChatExecutorError{Status: status, Message: message}
}

func withChatExecutorChannel(err *ChatExecutorError, channel model.Channel, modelName string, upstreamModelName string, upstreamURL string) *ChatExecutorError {
	if err == nil {
		return err
	}
	err.ChannelID = channel.ID
	if channel.UserChannelID != nil {
		err.UserChannelID = *channel.UserChannelID
	}
	err.ModelName = strings.TrimSpace(modelName)
	err.UpstreamModelName = strings.TrimSpace(upstreamModelName)
	err.UpstreamURL = strings.TrimSpace(upstreamURL)
	return err
}

// ExecuteServerChatCompletion runs one billed chat-completion turn for a user
// directly against a channel, without going through the public /v1 HTTP endpoint
// or a user token. It selects a channel for (model, userChannel) using the same
// routing the proxy uses, translates server-side tools into the selected provider
// protocol, bills the user via the shared usage-charge path, and returns the
// assistant message plus any tool calls.
func ExecuteServerChatCompletion(c *gin.Context, user *model.User, req ChatExecutorRequest) (*ChatExecutorResult, error) {
	if user == nil {
		return nil, newChatExecutorError(http.StatusUnauthorized, "Unauthorized")
	}
	modelName := strings.TrimSpace(strings.TrimPrefix(req.ModelName, "models/"))
	if modelName == "" {
		return nil, newChatExecutorError(http.StatusBadRequest, "Model not specified")
	}
	allowedByDepartment, err := DepartmentModelAllowed(user.ID, modelName)
	if err != nil {
		return nil, newChatExecutorError(http.StatusInternalServerError, "Failed to evaluate department model policy")
	}
	if !allowedByDepartment {
		return nil, newChatExecutorError(http.StatusForbidden, "Department policy does not allow this model")
	}
	if (req.ChargeBalance || !PersonalModeEnabled()) && !HasSpendableCredit(user) {
		return nil, newChatExecutorError(http.StatusPaymentRequired, "Insufficient balance")
	}

	candidates, err := serverChatCandidates(user, modelName, req.UserChannelID)
	if err != nil {
		return nil, newChatExecutorError(http.StatusInternalServerError, "Failed to find available channels")
	}
	if len(candidates) == 0 {
		return nil, newChatExecutorError(http.StatusServiceUnavailable, "No available channel for this model")
	}

	executor := serverChatExecutor()
	modelConfig := executor.selectModelConfig(candidates, modelName)
	channel := modelConfig.Channel
	if channel.ID == 0 {
		return nil, newChatExecutorError(http.StatusServiceUnavailable, "No enabled model configuration for this model")
	}

	protocol := channelProtocol(channel.Type)

	if err := ValidateConfiguredHTTPURL(channel.BaseURL); err != nil {
		return nil, newChatExecutorError(http.StatusBadGateway, "Upstream URL blocked by SSRF protection")
	}

	upstreamModelName := strings.TrimSpace(modelConfig.UpstreamModelName)
	if upstreamModelName == "" {
		upstreamModelName = modelName
	}

	upstreamReq := req
	upstreamReq.Stream = req.Stream && req.OnTextDelta != nil && serverChatStreamSupported(protocol)
	prepared, err := prepareServerChatRequest(&channel, protocol, upstreamModelName, upstreamReq)
	if err != nil {
		return nil, newChatExecutorError(http.StatusBadRequest, err.Error())
	}
	if req.Context != nil {
		prepared.Context = req.Context
	}
	if prepared.Context == nil && c != nil && c.Request != nil {
		prepared.Context = c.Request.Context()
	}
	beginTokenLogTiming(c)

	resp, err := executor.doUpstreamRequest(prepared, &channel)
	if err != nil {
		logUpstreamRequestFailure(c, &channel, prepared.URL, prepared.Body, err)
		return nil, withChatExecutorChannel(newChatExecutorError(http.StatusBadGateway, "Upstream request failed"), channel, modelName, upstreamModelName, prepared.URL)
	}
	markTokenLogFirstResponse(c)
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, withChatExecutorChannel(newChatExecutorError(http.StatusBadGateway, "Failed to read upstream response"), channel, modelName, upstreamModelName, prepared.URL)
		}
		logUpstreamError(c, &channel, prepared.URL, resp.StatusCode, prepared.Body, respBody)
		return nil, withChatExecutorChannel(newChatExecutorError(resp.StatusCode, "Upstream request failed"), channel, modelName, upstreamModelName, prepared.URL)
	}

	if upstreamReq.Stream && isStreamingResponse(resp) {
		result, usage, usageOK, streamErr := readServerChatStream(resp.Body, protocol, req.OnTextDelta)
		if streamErr != nil && !billableStreamPartial(result, usageOK) {
			return nil, withChatExecutorChannel(newChatExecutorError(http.StatusBadGateway, "Failed to read upstream stream"), channel, modelName, upstreamModelName, prepared.URL)
		}
		if !usageOK {
			usage = estimatedServerChatUsage(modelName, req, result.Content)
		}

		apiKey := currentAPIKey(c)
		cost, status, message, err := executor.billServerUsage(c, user, apiKey, &channel, &modelConfig, modelName, usage, req.ChargeBalance)
		if err != nil {
			return nil, newChatExecutorError(status, message)
		}
		result.Cost = cost
		if streamErr != nil && strings.TrimSpace(result.FinishReason) == "" {
			result.FinishReason = "stream_error"
		}
		return result, nil
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, withChatExecutorChannel(newChatExecutorError(http.StatusBadGateway, "Failed to read upstream response"), channel, modelName, upstreamModelName, prepared.URL)
	}
	var responseData map[string]interface{}
	if err := json.Unmarshal(respBody, &responseData); err != nil {
		return nil, withChatExecutorChannel(newChatExecutorError(http.StatusBadGateway, "Failed to parse upstream response"), channel, modelName, upstreamModelName, prepared.URL)
	}

	usage, ok := parseUsageTokens(responseData)
	if !ok {
		usage = estimatedServerChatUsage(modelName, req, string(respBody))
	}

	apiKey := currentAPIKey(c)
	cost, status, message, err := executor.billServerUsage(c, user, apiKey, &channel, &modelConfig, modelName, usage, req.ChargeBalance)
	if err != nil {
		return nil, newChatExecutorError(status, message)
	}

	result := parseServerChatResponse(protocol, responseData)
	result.Cost = cost
	return result, nil
}

func serverChatExecutor() *ProxyService {
	return serverChatProxyService
}

var serverChatProxyService = NewProxyService()

func serverChatCandidates(user *model.User, modelName string, userChannelID uint) ([]model.ModelConfig, error) {
	var candidates []model.ModelConfig
	query := model.DB.
		Preload("Channel.UserChannel").
		Preload("Model").
		Joins("JOIN channels ON channels.id = model_configs.channel_id").
		Joins("JOIN models ON models.id = model_configs.model_id").
		Joins("JOIN user_channels ON user_channels.id = channels.user_channel_id").
		Where("channels.enabled = ? AND model_configs.enabled = ? AND models.enabled = ? AND models.model_name = ? AND user_channels.enabled = ?", true, true, true, modelName, true)
	if userChannelID != 0 {
		query = query.Where("channels.user_channel_id = ?", userChannelID)
	}
	query, err := filterUserChannelGroupAccess(query, user)
	if err != nil {
		return nil, err
	}
	if err := query.Order("channels.priority DESC, channels.weight DESC, channels.id ASC").Find(&candidates).Error; err != nil {
		return nil, err
	}
	return candidates, nil
}

// billServerUsage mirrors ProxyService.billUsage but returns the computed cost so
// the caller can accumulate it across an agent loop. It reuses the same group and
// channel multipliers, balance deduction, token log, and referral commission.
func (s *ProxyService) billServerUsage(c *gin.Context, user *model.User, apiKey *model.APIKey, channel *model.Channel, modelConfig *model.ModelConfig, modelName string, usage usageTokenCounts, chargeBalance bool) (decimal.Decimal, int, string, error) {
	groupMultiplier, err := effectiveUserGroupMultiplier(user, channel.ID, modelConfig.ID)
	if err != nil {
		return decimal.Zero, http.StatusInternalServerError, "User group not found", err
	}
	usage = normalizeUsageTokenCounts(usage)
	billingModel := modelConfig.Model
	baseCost := calculateModelUsageCost(usage, billingModel)
	channelMultiplier := userChannelMultiplier(channel)
	cost := baseCost.Mul(groupMultiplier).Mul(channelMultiplier)
	referralRate := referralCommissionRate(user, cost)

	billingContext := billingContext(c)
	releaseBillingLock := cache.AcquireUserBillingLock(billingContext, user.ID)
	defer releaseBillingLock()
	tx := model.DB.Begin()
	if tx.Error != nil {
		return decimal.Zero, http.StatusInternalServerError, "Failed to start transaction", tx.Error
	}
	if exceeded, err := APIKeyQuotaExceededInTx(tx, apiKey, cost); err != nil {
		tx.Rollback()
		return decimal.Zero, http.StatusInternalServerError, "Failed to check API key quota", err
	} else if exceeded {
		tx.Rollback()
		return decimal.Zero, http.StatusPaymentRequired, "API key quota exceeded", ErrAPIKeyQuotaExceeded
	}
	if err := applyChatExecutorUsageCharge(tx, user.ID, cost, chargeBalance); err != nil {
		tx.Rollback()
		cache.InvalidateUserBillingBalance(billingContext, user.ID)
		if errors.Is(err, ErrInsufficientBalance) {
			return decimal.Zero, http.StatusPaymentRequired, "Insufficient balance", err
		}
		return decimal.Zero, http.StatusInternalServerError, "Failed to update balance", err
	}
	tokenLog := newUsageTokenLog(c, user, apiKey, channel, modelConfig, modelName, usage, billingModel, baseCost, groupMultiplier, channelMultiplier, cost)
	if err := applyReferralCommission(tx, user, tokenLog.ID, cost, referralRate); err != nil {
		tx.Rollback()
		cache.InvalidateUserBillingBalance(billingContext, user.ID)
		return decimal.Zero, http.StatusInternalServerError, "Failed to apply referral commission", err
	}
	balance, err := committedBillingBalance(tx, user.ID)
	if err != nil {
		tx.Rollback()
		cache.InvalidateUserBillingBalance(billingContext, user.ID)
		return decimal.Zero, http.StatusInternalServerError, "Failed to read updated balance", err
	}
	if err := tx.Commit().Error; err != nil {
		cache.InvalidateUserBillingBalance(billingContext, user.ID)
		return decimal.Zero, http.StatusInternalServerError, "Failed to commit usage", err
	}
	cache.StoreUserBillingBalance(billingContext, user.ID, balance)
	if err := model.RecordTokenLog(tokenLog); err != nil {
		log.Printf("failed to record chat token log: %v", err)
	}
	return cost, 0, "", nil
}

func applyChatExecutorUsageCharge(tx *gorm.DB, userID uint, cost decimal.Decimal, chargeBalance bool) error {
	if !chargeBalance {
		return ApplyUsageCharge(tx, userID, cost)
	}
	if cost.LessThanOrEqual(decimal.Zero) {
		return nil
	}
	// chargeBalance forces settlement even in personal mode, so this bypasses
	// ApplyUsageCharge's personal-mode exemption but keeps the same order:
	// subscription quota first, wallet balance as the fallback.
	return applySubscriptionUsageCharge(tx, userID, cost)
}

func clientIPForLog(c *gin.Context) string {
	if c == nil {
		return ""
	}
	return c.ClientIP()
}

func userAgentForLog(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	return c.Request.UserAgent()
}

func serverChatStreamSupported(protocol proxyProtocol) bool {
	return protocol == protocolOpenAI || protocol == protocolResponses || protocol == protocolClaude
}

func billableStreamPartial(result *ChatExecutorResult, usageOK bool) bool {
	if usageOK {
		return true
	}
	if result == nil {
		return false
	}
	return strings.TrimSpace(result.Content) != "" || len(result.ToolCalls) > 0
}

func estimatedServerChatUsage(modelName string, req ChatExecutorRequest, outputText string) usageTokenCounts {
	return usageTokenCounts{
		InputTokens:  CountTokens(modelName, serverChatMessagesText(req)),
		OutputTokens: CountTokens(modelName, outputText),
	}
}

// prepareServerChatRequest builds the upstream HTTP request and translates the
// provider-neutral MCP tool loop into the selected upstream protocol.
func prepareServerChatRequest(channel *model.Channel, protocol proxyProtocol, upstreamModelName string, req ChatExecutorRequest) (preparedUpstreamRequest, error) {
	switch protocol {
	case protocolOpenAI:
		return prepareServerOpenAIChatRequest(channel, upstreamModelName, req)
	case protocolResponses:
		return prepareServerOpenAIResponsesRequest(channel, upstreamModelName, req)
	case protocolClaude:
		return prepareServerClaudeMessagesRequest(channel, upstreamModelName, req)
	case protocolGemini:
		return prepareServerGeminiGenerateContentRequest(channel, upstreamModelName, req)
	default:
		return preparedUpstreamRequest{}, fmt.Errorf("unsupported upstream protocol: %s", protocol)
	}
}

func prepareServerOpenAIChatRequest(channel *model.Channel, upstreamModelName string, req ChatExecutorRequest) (preparedUpstreamRequest, error) {
	messages := make([]map[string]interface{}, 0, len(req.Messages)+1)
	if strings.TrimSpace(req.System) != "" {
		messages = append(messages, map[string]interface{}{"role": "system", "content": req.System})
	}
	for _, message := range req.Messages {
		entry := map[string]interface{}{"role": message.Role}
		if len(message.Parts) > 0 && strings.EqualFold(message.Role, "user") {
			entry["content"] = openAIChatContentParts(message)
		} else if message.Content != "" || (len(message.ToolCalls) == 0 && message.Role != "assistant") {
			entry["content"] = message.Content
		}
		if len(message.ToolCalls) > 0 {
			entry["tool_calls"] = message.ToolCalls
		}
		if message.ToolCallID != "" {
			entry["tool_call_id"] = message.ToolCallID
		}
		if message.Name != "" && !strings.EqualFold(message.Role, "tool") {
			entry["name"] = message.Name
		}
		messages = append(messages, entry)
	}
	if len(messages) == 0 {
		return preparedUpstreamRequest{}, errors.New("messages are required")
	}
	payload := map[string]interface{}{
		"model":    upstreamModelName,
		"messages": messages,
	}
	if req.MaxTokens > 0 {
		payload["max_tokens"] = req.MaxTokens
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if effort := normalizeReasoningEffort(req.ReasoningEffort); effort != "" {
		payload["reasoning_effort"] = effort
	}
	if len(req.Tools) > 0 {
		payload["tools"] = openAIChatTools(req.Tools)
		payload["tool_choice"] = "auto"
	}
	if req.Stream {
		payload["stream"] = true
		payload["stream_options"] = map[string]interface{}{"include_usage": true}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return preparedUpstreamRequest{}, err
	}
	headers := jsonHeaders()
	if req.Stream {
		headers.Set("Accept", "text/event-stream")
	}
	headers.Set("Authorization", "Bearer "+strings.TrimSpace(channel.APIKey))
	return preparedUpstreamRequest{Method: http.MethodPost, URL: upstreamURLForRequest(channel.BaseURL, "/v1/chat/completions"), Body: body, Header: headers}, nil
}

func prepareServerOpenAIResponsesRequest(channel *model.Channel, upstreamModelName string, req ChatExecutorRequest) (preparedUpstreamRequest, error) {
	input := make([]map[string]interface{}, 0, len(req.Messages)+1)
	if strings.TrimSpace(req.System) != "" {
		input = append(input, map[string]interface{}{"role": "system", "content": req.System})
	}
	for _, message := range req.Messages {
		switch strings.ToLower(strings.TrimSpace(message.Role)) {
		case "assistant":
			if strings.TrimSpace(message.Content) != "" {
				input = append(input, map[string]interface{}{"role": "assistant", "content": message.Content})
			}
			for _, call := range toolCallsFromOpenAIMaps(message.ToolCalls) {
				input = append(input, map[string]interface{}{
					"type":      "function_call",
					"call_id":   call.ID,
					"name":      call.Name,
					"arguments": call.Arguments,
				})
			}
		case "tool":
			input = append(input, map[string]interface{}{
				"type":    "function_call_output",
				"call_id": message.ToolCallID,
				"output":  message.Content,
			})
		default:
			role := responseInputRole(message.Role)
			if len(message.Parts) > 0 && role == "user" {
				input = append(input, map[string]interface{}{"role": role, "content": openAIResponsesContentParts(message)})
			} else if strings.TrimSpace(message.Content) != "" || role == "user" {
				input = append(input, map[string]interface{}{"role": role, "content": message.Content})
			}
		}
	}
	if len(input) == 0 {
		return preparedUpstreamRequest{}, errors.New("input is required")
	}
	payload := map[string]interface{}{"model": upstreamModelName, "input": input}
	if req.MaxTokens > 0 {
		payload["max_output_tokens"] = req.MaxTokens
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if effort := normalizeReasoningEffort(req.ReasoningEffort); effort != "" {
		payload["reasoning"] = map[string]interface{}{"effort": effort}
	}
	if len(req.Tools) > 0 {
		payload["tools"] = openAIResponsesTools(req.Tools)
		payload["tool_choice"] = "auto"
	}
	if req.Stream {
		payload["stream"] = true
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return preparedUpstreamRequest{}, err
	}
	headers := jsonHeaders()
	if req.Stream {
		headers.Set("Accept", "text/event-stream")
	}
	headers.Set("Authorization", "Bearer "+strings.TrimSpace(channel.APIKey))
	return preparedUpstreamRequest{Method: http.MethodPost, URL: upstreamURLForRequest(channel.BaseURL, "/v1/responses"), Body: body, Header: headers}, nil
}

func prepareServerClaudeMessagesRequest(channel *model.Channel, upstreamModelName string, req ChatExecutorRequest) (preparedUpstreamRequest, error) {
	messages := make([]map[string]interface{}, 0, len(req.Messages))
	for _, message := range req.Messages {
		switch strings.ToLower(strings.TrimSpace(message.Role)) {
		case "assistant":
			blocks := []map[string]interface{}{}
			if strings.TrimSpace(message.Content) != "" {
				blocks = append(blocks, map[string]interface{}{"type": "text", "text": message.Content})
			}
			for _, call := range toolCallsFromOpenAIMaps(message.ToolCalls) {
				blocks = append(blocks, map[string]interface{}{"type": "tool_use", "id": call.ID, "name": call.Name, "input": toolArgumentsObject(call.Arguments)})
			}
			if len(blocks) > 0 {
				messages = append(messages, map[string]interface{}{"role": "assistant", "content": blocks})
			}
		case "tool":
			messages = append(messages, map[string]interface{}{
				"role": "user",
				"content": []map[string]interface{}{{
					"type":        "tool_result",
					"tool_use_id": message.ToolCallID,
					"content":     message.Content,
				}},
			})
		default:
			if len(message.Parts) > 0 {
				messages = append(messages, map[string]interface{}{"role": "user", "content": claudeContentBlocks(message)})
			} else if strings.TrimSpace(message.Content) != "" {
				messages = append(messages, map[string]interface{}{"role": "user", "content": message.Content})
			}
		}
	}
	if len(messages) == 0 {
		return preparedUpstreamRequest{}, errors.New("messages are required")
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	payload := map[string]interface{}{"model": upstreamModelName, "max_tokens": maxTokens, "messages": messages}
	if strings.TrimSpace(req.System) != "" {
		payload["system"] = req.System
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if effort := claudeThinkingEffort(req.ReasoningEffort); effort > 0 {
		payload["thinking"] = map[string]interface{}{"type": "enabled", "budget_tokens": effort}
	}
	if len(req.Tools) > 0 {
		payload["tools"] = claudeTools(req.Tools)
	}
	if req.Stream {
		payload["stream"] = true
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return preparedUpstreamRequest{}, err
	}
	headers := jsonHeaders()
	if req.Stream {
		headers.Set("Accept", "text/event-stream")
	}
	headers.Set("x-api-key", strings.TrimSpace(channel.APIKey))
	headers.Set("Authorization", "Bearer "+strings.TrimSpace(channel.APIKey))
	headers.Set("anthropic-version", "2023-06-01")
	return preparedUpstreamRequest{Method: http.MethodPost, URL: upstreamURLForRequest(channel.BaseURL, "/v1/messages"), Body: body, Header: headers}, nil
}

func prepareServerGeminiGenerateContentRequest(channel *model.Channel, upstreamModelName string, req ChatExecutorRequest) (preparedUpstreamRequest, error) {
	contents := make([]map[string]interface{}, 0, len(req.Messages))
	for _, message := range req.Messages {
		switch strings.ToLower(strings.TrimSpace(message.Role)) {
		case "assistant":
			parts := []map[string]interface{}{}
			if strings.TrimSpace(message.Content) != "" {
				parts = append(parts, map[string]interface{}{"text": message.Content})
			}
			for _, call := range toolCallsFromOpenAIMaps(message.ToolCalls) {
				parts = append(parts, map[string]interface{}{"functionCall": map[string]interface{}{"name": call.Name, "args": toolArgumentsObject(call.Arguments)}})
			}
			if len(parts) > 0 {
				contents = append(contents, map[string]interface{}{"role": "model", "parts": parts})
			}
		case "tool":
			name := strings.TrimSpace(message.Name)
			if name == "" {
				name = strings.TrimSpace(message.ToolCallID)
			}
			contents = append(contents, map[string]interface{}{
				"role": "function",
				"parts": []map[string]interface{}{{"functionResponse": map[string]interface{}{
					"name":     name,
					"response": map[string]interface{}{"content": message.Content},
				}}},
			})
		default:
			if len(message.Parts) > 0 {
				parts := geminiContentParts(message)
				if len(parts) > 0 {
					contents = append(contents, map[string]interface{}{"role": "user", "parts": parts})
				}
			} else if strings.TrimSpace(message.Content) != "" {
				contents = append(contents, map[string]interface{}{"role": "user", "parts": []map[string]interface{}{{"text": message.Content}}})
			}
		}
	}
	if len(contents) == 0 {
		return preparedUpstreamRequest{}, errors.New("contents are required")
	}
	payload := map[string]interface{}{"contents": contents}
	if strings.TrimSpace(req.System) != "" {
		payload["systemInstruction"] = map[string]interface{}{"parts": []map[string]interface{}{{"text": req.System}}}
	}
	generationConfig := map[string]interface{}{}
	if req.MaxTokens > 0 {
		generationConfig["maxOutputTokens"] = req.MaxTokens
	}
	if req.Temperature != nil {
		generationConfig["temperature"] = *req.Temperature
	}
	if effort := normalizeReasoningEffort(req.ReasoningEffort); effort != "" {
		generationConfig["thinkingConfig"] = map[string]interface{}{"thinkingBudget": geminiThinkingBudget(effort)}
	}
	if len(generationConfig) > 0 {
		payload["generationConfig"] = generationConfig
	}
	if len(req.Tools) > 0 {
		payload["tools"] = geminiTools(req.Tools)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return preparedUpstreamRequest{}, err
	}
	fullURL := upstreamURLForRequest(channel.BaseURL, "/v1beta/models/"+url.PathEscape(strings.TrimPrefix(upstreamModelName, "models/"))+":generateContent")
	if strings.TrimSpace(channel.APIKey) != "" {
		fullURL = withQueryParam(fullURL, "key", strings.TrimSpace(channel.APIKey))
	}
	return preparedUpstreamRequest{Method: http.MethodPost, URL: fullURL, Body: body, Header: jsonHeaders()}, nil
}

func parseServerChatResponse(protocol proxyProtocol, responseData map[string]interface{}) *ChatExecutorResult {
	switch protocol {
	case protocolClaude:
		return parseServerClaudeMessagesResponse(responseData)
	case protocolGemini:
		return parseServerGeminiGenerateContentResponse(responseData)
	case protocolResponses:
		return parseServerOpenAIResponsesResponse(responseData)
	default:
		return parseServerOpenAIChatResponse(responseData)
	}
}

func parseServerOpenAIChatResponse(responseData map[string]interface{}) *ChatExecutorResult {
	result := &ChatExecutorResult{}
	choices, ok := responseData["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		result.Content = openAIResponseText(responseData)
		return result
	}
	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		return result
	}
	result.FinishReason = stringFromValue(choice["finish_reason"])
	message, ok := choice["message"].(map[string]interface{})
	if !ok {
		result.Content = openAIResponseText(responseData)
		return result
	}
	result.AssistantMessage = message
	result.Content = stringFromValue(message["content"])
	result.ToolCalls = toolCallsFromOpenAIInterface(message["tool_calls"])
	return result
}

func parseServerOpenAIResponsesResponse(responseData map[string]interface{}) *ChatExecutorResult {
	result := &ChatExecutorResult{FinishReason: stringFromValue(responseData["status"])}
	parts := []string{}
	if output, ok := responseData["output"].([]interface{}); ok {
		for _, raw := range output {
			item, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			switch stringFromValue(item["type"]) {
			case "message":
				if text := contentToText(item["content"]); strings.TrimSpace(text) != "" {
					parts = append(parts, text)
				}
			case "function_call":
				id := firstNonEmptyString(stringFromValue(item["call_id"]), stringFromValue(item["id"]))
				call := ChatExecutorToolCall{ID: id, Name: stringFromValue(item["name"]), Arguments: stringFromValue(item["arguments"])}
				result.ToolCalls = append(result.ToolCalls, call)
			}
		}
	}
	result.Content = strings.TrimSpace(strings.Join(parts, "\n"))
	if result.Content == "" {
		result.Content = openAIResponseText(responseData)
	}
	result.AssistantMessage = assistantMessageFromToolCalls(result.Content, result.ToolCalls)
	return result
}

func parseServerClaudeMessagesResponse(responseData map[string]interface{}) *ChatExecutorResult {
	result := &ChatExecutorResult{FinishReason: stringFromValue(responseData["stop_reason"])}
	parts := []string{}
	if content, ok := responseData["content"].([]interface{}); ok {
		for _, raw := range content {
			item, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			switch stringFromValue(item["type"]) {
			case "text":
				if text := contentToText(item); strings.TrimSpace(text) != "" {
					parts = append(parts, text)
				}
			case "tool_use":
				arguments, _ := json.Marshal(item["input"])
				result.ToolCalls = append(result.ToolCalls, ChatExecutorToolCall{ID: stringFromValue(item["id"]), Name: stringFromValue(item["name"]), Arguments: string(arguments)})
			}
		}
	}
	result.Content = strings.TrimSpace(strings.Join(parts, "\n"))
	result.AssistantMessage = assistantMessageFromToolCalls(result.Content, result.ToolCalls)
	return result
}

func parseServerGeminiGenerateContentResponse(responseData map[string]interface{}) *ChatExecutorResult {
	result := &ChatExecutorResult{}
	parts := []string{}
	if candidates, ok := responseData["candidates"].([]interface{}); ok && len(candidates) > 0 {
		if candidate, ok := candidates[0].(map[string]interface{}); ok {
			result.FinishReason = stringFromValue(candidate["finishReason"])
			if content, ok := candidate["content"].(map[string]interface{}); ok {
				if rawParts, ok := content["parts"].([]interface{}); ok {
					for index, rawPart := range rawParts {
						part, ok := rawPart.(map[string]interface{})
						if !ok {
							continue
						}
						if text := contentToText(part); strings.TrimSpace(text) != "" {
							parts = append(parts, text)
						}
						if functionCall, ok := part["functionCall"].(map[string]interface{}); ok {
							name := stringFromValue(functionCall["name"])
							arguments, _ := json.Marshal(functionCall["args"])
							result.ToolCalls = append(result.ToolCalls, ChatExecutorToolCall{ID: fmt.Sprintf("%s_%d", name, index), Name: name, Arguments: string(arguments)})
						}
					}
				}
			}
		}
	}
	result.Content = strings.TrimSpace(strings.Join(parts, "\n"))
	result.AssistantMessage = assistantMessageFromToolCalls(result.Content, result.ToolCalls)
	return result
}

func readServerChatStream(body io.Reader, protocol proxyProtocol, onTextDelta func(string) error) (*ChatExecutorResult, usageTokenCounts, bool, error) {
	result := &ChatExecutorResult{}
	var usage usageTokenCounts
	var usageOK bool
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxBufferedStreamBytes)

	var event string
	var dataLines []string
	flush := func() error {
		if len(dataLines) == 0 {
			event = ""
			return nil
		}
		payload := strings.TrimSpace(strings.Join(dataLines, "\n"))
		eventName := event
		event = ""
		dataLines = dataLines[:0]
		if payload == "" || payload == "[DONE]" {
			return nil
		}
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(payload), &data); err != nil {
			return nil
		}
		delta, parsedUsage, parsedUsageOK := applyServerChatStreamEvent(result, protocol, eventName, data)
		if parsedUsageOK {
			usage = parsedUsage
			usageOK = true
		}
		if delta != "" && onTextDelta != nil {
			if err := onTextDelta(delta); err != nil {
				return err
			}
		}
		return nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			if err := flush(); err != nil {
				return result, usage, usageOK, err
			}
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return result, usage, usageOK, err
	}
	if err := flush(); err != nil {
		return result, usage, usageOK, err
	}
	finalizeServerChatStreamResult(result)
	return result, usage, usageOK, nil
}

func applyServerChatStreamEvent(result *ChatExecutorResult, protocol proxyProtocol, eventName string, data map[string]interface{}) (string, usageTokenCounts, bool) {
	if usage, ok := parseUsageTokens(data); ok {
		switch protocol {
		case protocolOpenAI:
			return applyOpenAIChatStreamEvent(result, data), usage, true
		case protocolResponses:
			return applyOpenAIResponsesStreamEvent(result, eventName, data), usage, true
		case protocolClaude:
			return applyClaudeStreamEvent(result, data), usage, true
		default:
			return "", usage, true
		}
	}
	switch protocol {
	case protocolOpenAI:
		return applyOpenAIChatStreamEvent(result, data), usageTokenCounts{}, false
	case protocolResponses:
		return applyOpenAIResponsesStreamEvent(result, eventName, data), usageTokenCounts{}, false
	case protocolClaude:
		return applyClaudeStreamEvent(result, data), usageTokenCounts{}, false
	default:
		return "", usageTokenCounts{}, false
	}
}

func applyOpenAIChatStreamEvent(result *ChatExecutorResult, data map[string]interface{}) string {
	choices, ok := data["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return ""
	}
	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		return ""
	}
	if finishReason := stringFromValue(choice["finish_reason"]); finishReason != "" {
		result.FinishReason = finishReason
	}
	delta, ok := choice["delta"].(map[string]interface{})
	if !ok {
		return ""
	}
	content := contentToText(delta["content"])
	if content != "" {
		result.Content += content
	}
	mergeOpenAIStreamToolCalls(result, delta["tool_calls"])
	return content
}

func applyOpenAIResponsesStreamEvent(result *ChatExecutorResult, eventName string, data map[string]interface{}) string {
	eventType := firstNonEmptyString(eventName, stringFromValue(data["type"]))
	if status := stringFromValue(data["status"]); status != "" {
		result.FinishReason = status
	}
	switch eventType {
	case "response.output_text.delta", "response.refusal.delta":
		delta := stringFromValue(data["delta"])
		result.Content += delta
		return delta
	case "response.function_call_arguments.delta":
		index := intFromStreamValue(data["output_index"], data["item_index"], data["index"])
		mergeResponsesStreamToolCall(result, index, data)
	case "response.output_item.added", "response.output_item.done":
		if item, ok := data["item"].(map[string]interface{}); ok && stringFromValue(item["type"]) == "function_call" {
			index := intFromStreamValue(data["output_index"], data["item_index"], data["index"])
			mergeResponsesStreamToolCall(result, index, item)
		}
	case "response.completed":
		if response, ok := data["response"].(map[string]interface{}); ok {
			parsed := parseServerOpenAIResponsesResponse(response)
			if strings.TrimSpace(parsed.Content) != "" {
				result.Content = parsed.Content
			}
			if len(parsed.ToolCalls) > 0 {
				result.ToolCalls = parsed.ToolCalls
			}
			result.FinishReason = parsed.FinishReason
		}
	}
	return ""
}

func applyClaudeStreamEvent(result *ChatExecutorResult, data map[string]interface{}) string {
	eventType := stringFromValue(data["type"])
	switch eventType {
	case "content_block_start":
		if block, ok := data["content_block"].(map[string]interface{}); ok && stringFromValue(block["type"]) == "tool_use" {
			index := intFromStreamValue(data["index"])
			mergeClaudeStreamToolCall(result, index, block)
		}
	case "content_block_delta":
		delta, _ := data["delta"].(map[string]interface{})
		switch stringFromValue(delta["type"]) {
		case "text_delta":
			text := stringFromValue(delta["text"])
			result.Content += text
			return text
		case "input_json_delta":
			index := intFromStreamValue(data["index"])
			mergeClaudeStreamToolCall(result, index, map[string]interface{}{"partial_json": stringFromValue(delta["partial_json"])})
		}
	case "message_delta":
		if delta, ok := data["delta"].(map[string]interface{}); ok {
			if stopReason := stringFromValue(delta["stop_reason"]); stopReason != "" {
				result.FinishReason = stopReason
			}
		}
	}
	return ""
}

func mergeOpenAIStreamToolCalls(result *ChatExecutorResult, raw interface{}) {
	items, ok := raw.([]interface{})
	if !ok {
		return
	}
	for _, item := range items {
		call, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		index := intFromStreamValue(call["index"])
		for len(result.ToolCalls) <= index {
			result.ToolCalls = append(result.ToolCalls, ChatExecutorToolCall{})
		}
		current := &result.ToolCalls[index]
		if id := stringFromValue(call["id"]); id != "" {
			current.ID = id
		}
		function, _ := call["function"].(map[string]interface{})
		if name := stringFromValue(function["name"]); name != "" {
			current.Name = name
		}
		if arguments := stringFromValue(function["arguments"]); arguments != "" {
			current.Arguments += arguments
		}
	}
}

func mergeResponsesStreamToolCall(result *ChatExecutorResult, index int, item map[string]interface{}) {
	for len(result.ToolCalls) <= index {
		result.ToolCalls = append(result.ToolCalls, ChatExecutorToolCall{})
	}
	current := &result.ToolCalls[index]
	if id := firstNonEmptyString(stringFromValue(item["call_id"]), stringFromValue(item["id"])); id != "" {
		current.ID = id
	}
	if name := stringFromValue(item["name"]); name != "" {
		current.Name = name
	}
	if arguments := firstNonEmptyString(stringFromValue(item["arguments"]), stringFromValue(item["delta"])); arguments != "" {
		if stringFromValue(item["arguments"]) != "" {
			current.Arguments = arguments
		} else {
			current.Arguments += arguments
		}
	}
}

func mergeClaudeStreamToolCall(result *ChatExecutorResult, index int, item map[string]interface{}) {
	for len(result.ToolCalls) <= index {
		result.ToolCalls = append(result.ToolCalls, ChatExecutorToolCall{})
	}
	current := &result.ToolCalls[index]
	if id := stringFromValue(item["id"]); id != "" {
		current.ID = id
	}
	if name := stringFromValue(item["name"]); name != "" {
		current.Name = name
	}
	if partial := stringFromValue(item["partial_json"]); partial != "" {
		current.Arguments += partial
	}
}

func finalizeServerChatStreamResult(result *ChatExecutorResult) {
	calls := make([]ChatExecutorToolCall, 0, len(result.ToolCalls))
	for _, call := range result.ToolCalls {
		if strings.TrimSpace(call.ID) == "" && strings.TrimSpace(call.Name) == "" && strings.TrimSpace(call.Arguments) == "" {
			continue
		}
		calls = append(calls, call)
	}
	result.ToolCalls = calls
	result.AssistantMessage = assistantMessageFromToolCalls(result.Content, result.ToolCalls)
}

func openAIChatContentParts(message ChatExecutorMessage) []map[string]interface{} {
	parts := make([]map[string]interface{}, 0, len(message.Parts)+1)
	for _, part := range normalizedChatExecutorParts(message) {
		switch part.Type {
		case "image":
			if url := imagePartURL(part); url != "" {
				parts = append(parts, map[string]interface{}{
					"type":      "image_url",
					"image_url": map[string]interface{}{"url": url},
				})
			}
		default:
			if strings.TrimSpace(part.Text) != "" {
				parts = append(parts, map[string]interface{}{"type": "text", "text": part.Text})
			}
		}
	}
	return parts
}

func openAIResponsesContentParts(message ChatExecutorMessage) []map[string]interface{} {
	parts := make([]map[string]interface{}, 0, len(message.Parts)+1)
	for _, part := range normalizedChatExecutorParts(message) {
		switch part.Type {
		case "image":
			if url := imagePartURL(part); url != "" {
				parts = append(parts, map[string]interface{}{"type": "input_image", "image_url": url})
			}
		default:
			if strings.TrimSpace(part.Text) != "" {
				parts = append(parts, map[string]interface{}{"type": "input_text", "text": part.Text})
			}
		}
	}
	return parts
}

func claudeContentBlocks(message ChatExecutorMessage) []map[string]interface{} {
	blocks := make([]map[string]interface{}, 0, len(message.Parts)+1)
	for _, part := range normalizedChatExecutorParts(message) {
		switch part.Type {
		case "image":
			if strings.TrimSpace(part.Data) != "" && strings.TrimSpace(part.MIMEType) != "" {
				blocks = append(blocks, map[string]interface{}{
					"type": "image",
					"source": map[string]interface{}{
						"type":       "base64",
						"media_type": part.MIMEType,
						"data":       part.Data,
					},
				})
			}
		default:
			if strings.TrimSpace(part.Text) != "" {
				blocks = append(blocks, map[string]interface{}{"type": "text", "text": part.Text})
			}
		}
	}
	return blocks
}

func geminiContentParts(message ChatExecutorMessage) []map[string]interface{} {
	parts := make([]map[string]interface{}, 0, len(message.Parts)+1)
	for _, part := range normalizedChatExecutorParts(message) {
		switch part.Type {
		case "image":
			if strings.TrimSpace(part.Data) != "" && strings.TrimSpace(part.MIMEType) != "" {
				parts = append(parts, map[string]interface{}{
					"inlineData": map[string]interface{}{
						"mimeType": part.MIMEType,
						"data":     part.Data,
					},
				})
			}
		default:
			if strings.TrimSpace(part.Text) != "" {
				parts = append(parts, map[string]interface{}{"text": part.Text})
			}
		}
	}
	return parts
}

func normalizedChatExecutorParts(message ChatExecutorMessage) []ChatExecutorContentPart {
	parts := make([]ChatExecutorContentPart, 0, len(message.Parts)+1)
	hasText := false
	for _, part := range message.Parts {
		part.Type = strings.ToLower(strings.TrimSpace(part.Type))
		if part.Type == "" {
			part.Type = "text"
		}
		part.Text = strings.TrimSpace(part.Text)
		part.MIMEType = strings.TrimSpace(part.MIMEType)
		part.Data = strings.TrimSpace(part.Data)
		part.URL = strings.TrimSpace(part.URL)
		if part.Type == "text" && part.Text != "" {
			hasText = true
		}
		if part.Type == "image" && part.MIMEType == "" {
			part.MIMEType = "image/png"
		}
		parts = append(parts, part)
	}
	if !hasText && strings.TrimSpace(message.Content) != "" {
		return append([]ChatExecutorContentPart{{Type: "text", Text: message.Content}}, parts...)
	}
	return parts
}

func imagePartURL(part ChatExecutorContentPart) string {
	if strings.TrimSpace(part.URL) != "" {
		return strings.TrimSpace(part.URL)
	}
	if strings.TrimSpace(part.Data) == "" {
		return ""
	}
	mimeType := strings.TrimSpace(part.MIMEType)
	if mimeType == "" {
		mimeType = "image/png"
	}
	return "data:" + mimeType + ";base64," + strings.TrimSpace(part.Data)
}

func intFromStreamValue(values ...interface{}) int {
	for _, value := range values {
		switch v := value.(type) {
		case float64:
			if v >= 0 {
				return int(v)
			}
		case int:
			if v >= 0 {
				return v
			}
		case json.Number:
			parsed, err := v.Int64()
			if err == nil && parsed >= 0 {
				return int(parsed)
			}
		}
	}
	return 0
}

func openAIChatTools(tools []ChatExecutorTool) []map[string]interface{} {
	items := make([]map[string]interface{}, 0, len(tools))
	for _, tool := range tools {
		items = append(items, map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  toolSchema(tool),
			},
		})
	}
	return items
}

func openAIResponsesTools(tools []ChatExecutorTool) []map[string]interface{} {
	items := make([]map[string]interface{}, 0, len(tools))
	for _, tool := range tools {
		items = append(items, map[string]interface{}{
			"type":        "function",
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  toolSchema(tool),
		})
	}
	return items
}

func claudeTools(tools []ChatExecutorTool) []map[string]interface{} {
	items := make([]map[string]interface{}, 0, len(tools))
	for _, tool := range tools {
		items = append(items, map[string]interface{}{
			"name":         tool.Name,
			"description":  tool.Description,
			"input_schema": toolSchema(tool),
		})
	}
	return items
}

func geminiTools(tools []ChatExecutorTool) []map[string]interface{} {
	declarations := make([]map[string]interface{}, 0, len(tools))
	for _, tool := range tools {
		declarations = append(declarations, map[string]interface{}{
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  toolSchema(tool),
		})
	}
	return []map[string]interface{}{{"functionDeclarations": declarations}}
}

func toolSchema(tool ChatExecutorTool) map[string]interface{} {
	if tool.Schema != nil {
		return tool.Schema
	}
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
}

func normalizeReasoningEffort(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "minimal":
		return "minimal"
	case "low":
		return "low"
	case "medium":
		return "medium"
	case "high":
		return "high"
	default:
		return ""
	}
}

func claudeThinkingEffort(value string) int {
	switch normalizeReasoningEffort(value) {
	case "minimal":
		return 1024
	case "low":
		return 2048
	case "medium":
		return 4096
	case "high":
		return 8192
	default:
		return 0
	}
}

func geminiThinkingBudget(value string) int {
	switch normalizeReasoningEffort(value) {
	case "minimal":
		return 512
	case "low":
		return 1024
	case "medium":
		return 4096
	case "high":
		return 8192
	default:
		return 0
	}
}

func toolCallsFromOpenAIInterface(raw interface{}) []ChatExecutorToolCall {
	items, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	calls := make([]ChatExecutorToolCall, 0, len(items))
	for _, item := range items {
		call, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		function, _ := call["function"].(map[string]interface{})
		calls = append(calls, ChatExecutorToolCall{ID: stringFromValue(call["id"]), Name: stringFromValue(function["name"]), Arguments: stringFromValue(function["arguments"])})
	}
	return calls
}

func toolCallsFromOpenAIMaps(items []map[string]interface{}) []ChatExecutorToolCall {
	calls := make([]ChatExecutorToolCall, 0, len(items))
	for _, call := range items {
		function, _ := call["function"].(map[string]interface{})
		calls = append(calls, ChatExecutorToolCall{ID: stringFromValue(call["id"]), Name: stringFromValue(function["name"]), Arguments: stringFromValue(function["arguments"])})
	}
	return calls
}

func assistantMessageFromToolCalls(content string, calls []ChatExecutorToolCall) map[string]interface{} {
	message := map[string]interface{}{"role": "assistant"}
	if strings.TrimSpace(content) != "" {
		message["content"] = content
	}
	if len(calls) == 0 {
		return message
	}
	toolCalls := make([]interface{}, 0, len(calls))
	for _, call := range calls {
		toolCalls = append(toolCalls, openAIToolCallMap(call))
	}
	message["tool_calls"] = toolCalls
	return message
}

func openAIToolCallMap(call ChatExecutorToolCall) map[string]interface{} {
	return map[string]interface{}{
		"id":   call.ID,
		"type": "function",
		"function": map[string]interface{}{
			"name":      call.Name,
			"arguments": call.Arguments,
		},
	}
}

func toolArgumentsObject(raw string) map[string]interface{} {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]interface{}{}
	}
	var object map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &object); err == nil && object != nil {
		return object
	}
	return map[string]interface{}{"value": raw}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func serverChatMessagesText(req ChatExecutorRequest) string {
	parts := make([]string, 0, len(req.Messages)+1)
	if strings.TrimSpace(req.System) != "" {
		parts = append(parts, req.System)
	}
	for _, message := range req.Messages {
		if message.Content != "" {
			parts = append(parts, message.Content)
		}
	}
	return strings.Join(parts, "\n")
}
