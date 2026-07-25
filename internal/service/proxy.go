package service

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/WindyPear-Team/veloce/internal/adapters"
	"github.com/WindyPear-Team/veloce/internal/cache"
	"github.com/WindyPear-Team/veloce/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type ProxyService struct {
	routingMu       sync.Mutex
	routingCounters map[string]uint64
}

const maxBufferedStreamBytes = 4 << 20

func NewProxyService() *ProxyService {
	return &ProxyService{routingCounters: map[string]uint64{}}
}

type modelListResponse struct {
	Object string              `json:"object"`
	Data   []modelListDataItem `json:"data"`
}

type modelListDataItem struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type proxyProtocol string

const (
	protocolOpenAI      proxyProtocol = "openai"
	protocolResponses   proxyProtocol = "responses"
	protocolOpenAIVideo proxyProtocol = "openai-video"
	protocolKling       proxyProtocol = "kling"
	protocolMidjourney  proxyProtocol = "midjourney"
	protocolClaude      proxyProtocol = "claude"
	protocolGemini      proxyProtocol = "gemini"
)

const (
	RoutingPriority           = "priority"
	RoutingRoundRobin         = "round_robin"
	RoutingWeightedRoundRobin = "weighted_round_robin"
)

type proxyTarget struct {
	User             *model.User
	APIKey           *model.APIKey
	ModelName        string
	ModelConfig      model.ModelConfig
	Channel          model.Channel
	BillingModel     *model.Model
	BillingModelName string
}

type normalizedAIRequest struct {
	Model       string
	Messages    []normalizedAIMessage
	System      string
	MaxTokens   int
	Temperature *float64
	Stream      bool
	Tools       []interface{}
	ToolChoice  interface{}
}

type normalizedAIMessage struct {
	Role      string
	Content   string
	ToolCalls []interface{}
}

type preparedUpstreamRequest struct {
	Method  string
	URL     string
	Body    []byte
	Header  http.Header
	Context context.Context
}

type providerResponseData struct {
	ID                      string
	Text                    string
	ToolCalls               []interface{}
	InputTokens             int
	OutputTokens            int
	CachedInputTokens       int
	CacheWriteInputTokens   int
	CacheWrite1hInputTokens int
	ImageInputTokens        int
	ImageOutputTokens       int
	AudioInputTokens        int
	AudioOutputTokens       int
}

type usageTokenCounts struct {
	InputTokens             int
	OutputTokens            int
	CachedInputTokens       int
	CacheReadInputTokens    int
	CacheWriteInputTokens   int
	CacheWrite1hInputTokens int
	ImageInputTokens        int
	ImageOutputTokens       int
	AudioInputTokens        int
	AudioOutputTokens       int
	BillableCost            decimal.Decimal
}

type videoGenerationResult struct {
	Target       *proxyTarget
	Response     *http.Response
	Body         []byte
	ResponseData map[string]interface{}
	Cost         decimal.Decimal
}

func (s *ProxyService) ListModels(c *gin.Context) {
	var modelNames []string
	value, _ := c.Get("user")
	user, ok := value.(*model.User)
	if !ok || user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	apiKey := currentAPIKey(c)
	query := model.DB.Table("model_configs").
		Joins("JOIN models ON models.id = model_configs.model_id").
		Joins("JOIN channels ON channels.id = model_configs.channel_id").
		Joins("JOIN user_channels ON user_channels.id = channels.user_channel_id").
		Where("channels.enabled = ? AND model_configs.enabled = ? AND models.enabled = ? AND user_channels.enabled = ?", true, true, true, true)
	allowedUserChannels, ok := requiredAPIKeyUserChannels(apiKey)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "API key must be bound to exactly one user channel"})
		return
	}
	if len(allowedUserChannels) > 0 {
		query = query.Where("channels.user_channel_id IN ?", allowedUserChannels)
	}
	query, err := filterUserChannelGroupAccess(query, user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to evaluate user channel access"})
		return
	}
	if err := query.
		Distinct("models.model_name").
		Order("models.model_name ASC").
		Pluck("models.model_name", &modelNames).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list models"})
		return
	}
	if metaModelNames, err := ListMetaModelNames(c); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list meta models"})
		return
	} else if len(metaModelNames) > 0 {
		modelNames = append(modelNames, metaModelNames...)
	}
	modelNames = filterModelsForAPIKey(modelNames, currentAPIKey(c))
	modelNames = uniqueSortedModelNames(modelNames)

	items := make([]modelListDataItem, 0, len(modelNames))
	for _, name := range modelNames {
		items = append(items, modelListDataItem{
			ID:      name,
			Object:  "model",
			Created: 0,
			OwnedBy: "flai",
		})
	}

	c.JSON(http.StatusOK, modelListResponse{
		Object: "list",
		Data:   items,
	})
}

func (s *ProxyService) createVideoUpstreamGeneration(c *gin.Context, requestBody map[string]interface{}) (videoGenerationResult, bool) {
	modelName := videoRequestModelName(requestBody)
	ok := modelName != ""
	if !ok || strings.TrimSpace(modelName) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Model not specified"})
		return videoGenerationResult{}, false
	}

	target, ok := s.resolveTarget(c, modelName)
	if !ok {
		return videoGenerationResult{}, false
	}

	if SensitiveFilterEnabled() {
		if _, matched := MatchSensitiveWords(videoRequestText(requestBody)); matched {
			c.JSON(http.StatusForbidden, gin.H{"error": "Request blocked by content policy", "type": "content_policy"})
			return videoGenerationResult{}, false
		}
	}

	upstreamProtocol := channelProtocol(target.Channel.Type)
	if !supportsVideoEndpoint(upstreamProtocol) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Video generation is only supported for video upstream channels", "type": "unsupported_upstream"})
		return videoGenerationResult{}, false
	}

	if err := ValidateConfiguredHTTPURL(target.Channel.BaseURL); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Upstream URL blocked by SSRF protection", "type": "upstream_error"})
		return videoGenerationResult{}, false
	}

	prepared, err := prepareVideoGenerationRequest(&target.Channel, upstreamProtocol, target.upstreamModelName(), requestBody)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "type": "invalid_request"})
		return videoGenerationResult{}, false
	}

	resp, err := s.doUpstreamRequest(prepared, &target.Channel)
	if err != nil {
		logUpstreamRequestFailure(c, &target.Channel, prepared.URL, prepared.Body, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "Upstream request failed", "type": "upstream_error"})
		return videoGenerationResult{}, false
	}

	if resp.StatusCode >= http.StatusBadRequest {
		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to read upstream response"})
			return videoGenerationResult{}, false
		}
		logUpstreamError(c, &target.Channel, prepared.URL, resp.StatusCode, prepared.Body, respBody)
		c.JSON(resp.StatusCode, gin.H{"error": "Upstream request failed", "type": "upstream_error"})
		return videoGenerationResult{}, false
	}

	if isStreamingResponse(resp) {
		resp.Body.Close()
		c.JSON(http.StatusBadRequest, gin.H{"error": "Streaming is not supported for this endpoint", "type": "unsupported_stream"})
		return videoGenerationResult{}, false
	}

	respBody, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to read upstream response"})
		return videoGenerationResult{}, false
	}

	var responseData map[string]interface{}
	_ = json.Unmarshal(respBody, &responseData)
	if SensitiveFilterEnabled() && SensitiveFilterScope() == SensitiveFilterScopeRequestResponse && responseData != nil {
		if _, matched := MatchSensitiveWords(videoResponseText(responseData)); matched {
			c.JSON(http.StatusForbidden, gin.H{"error": "Response blocked by content policy", "type": "content_policy"})
			return videoGenerationResult{}, false
		}
	}

	billingModel := target.billingModel()
	usage, status, message, ok := videoUsageTokenCounts(target.ModelName, requestBody, responseData, billingModel)
	if !ok {
		c.JSON(status, gin.H{"error": message, "type": "invalid_request"})
		return videoGenerationResult{}, false
	}
	cost, status, message, err := s.billUsageAndReturnCost(c, target.User, target.APIKey, &target.Channel, &target.ModelConfig, target.billingModelName(), usage, billingModel)
	if err != nil {
		c.JSON(status, gin.H{"error": message})
		return videoGenerationResult{}, false
	}

	return videoGenerationResult{
		Target:       target,
		Response:     resp,
		Body:         respBody,
		ResponseData: responseData,
		Cost:         cost,
	}, true
}

// HandleRequest handles the incoming API request, routes it to an upstream, and manages billing
func (s *ProxyService) HandleRequest(c *gin.Context) {
	requestBody, bodyBytes, ok := readProxyJSONBody(c)
	if !ok {
		return
	}

	modelName, ok := requestBody["model"].(string)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Model not specified"})
		return
	}

	if isResponsesPath(c.Request.URL.Path) {
		s.handleConvertedProviderRequest(c, protocolResponses, modelName, requestBody, bodyBytes)
		return
	}
	s.handleConvertedProviderRequest(c, protocolOpenAI, modelName, requestBody, bodyBytes)
}

func (s *ProxyService) HandleImageGeneration(c *gin.Context) {
	requestBody, _, ok := readProxyJSONBody(c)
	if !ok {
		return
	}

	modelName, ok := requestBody["model"].(string)
	if !ok || strings.TrimSpace(modelName) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Model not specified"})
		return
	}

	target, ok := s.resolveTarget(c, modelName)
	if !ok {
		return
	}

	if SensitiveFilterEnabled() {
		if _, matched := MatchSensitiveWords(imageRequestText(requestBody)); matched {
			c.JSON(http.StatusForbidden, gin.H{"error": "Request blocked by content policy", "type": "content_policy"})
			return
		}
	}

	upstreamProtocol := channelProtocol(target.Channel.Type)
	if !supportsOpenAIImageEndpoint(upstreamProtocol) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Image generation is only supported for OpenAI-compatible upstream channels", "type": "unsupported_upstream"})
		return
	}

	if err := ValidateConfiguredHTTPURL(target.Channel.BaseURL); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Upstream URL blocked by SSRF protection", "type": "upstream_error"})
		return
	}

	prepared, err := prepareOpenAIImageGenerationRequest(&target.Channel, target.upstreamModelName(), requestBody)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "type": "invalid_request"})
		return
	}

	resp, err := s.doUpstreamRequest(prepared, &target.Channel)
	if err != nil {
		logUpstreamRequestFailure(c, &target.Channel, prepared.URL, prepared.Body, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "Upstream request failed", "type": "upstream_error"})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to read upstream response"})
			return
		}
		logUpstreamError(c, &target.Channel, prepared.URL, resp.StatusCode, prepared.Body, respBody)
		c.JSON(resp.StatusCode, gin.H{"error": "Upstream request failed", "type": "upstream_error"})
		return
	}

	if isStreamingResponse(resp) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Streaming is not supported for this endpoint", "type": "unsupported_stream"})
		return
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to read upstream response"})
		return
	}

	var responseData map[string]interface{}
	_ = json.Unmarshal(respBody, &responseData)
	if SensitiveFilterEnabled() && SensitiveFilterScope() == SensitiveFilterScopeRequestResponse && responseData != nil {
		if _, matched := MatchSensitiveWords(imageResponseText(responseData)); matched {
			c.JSON(http.StatusForbidden, gin.H{"error": "Response blocked by content policy", "type": "content_policy"})
			return
		}
	}

	usage := imageUsageTokenCounts(target.ModelName, requestBody, responseData)
	if status, message, err := s.billUsage(c, target.User, target.APIKey, &target.Channel, &target.ModelConfig, target.billingModelName(), usage, target.billingModel()); err != nil {
		c.JSON(status, gin.H{"error": message})
		return
	}

	ApplyGeneratedAssetHook(c.Request.Context(), GeneratedAssetInput{
		UserID:       target.User.ID,
		Kind:         "image",
		ModelName:    target.ModelName,
		ResponseData: responseData,
		ResponseBody: respBody,
		Source:       "image_generation",
	})

	writeUpstreamResponse(c, resp, respBody)
}

func (s *ProxyService) HandleImageEdit(c *gin.Context) {
	if err := c.Request.ParseMultipartForm(128 << 20); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid multipart form", "type": "invalid_request"})
		return
	}
	if c.Request.MultipartForm == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Multipart form is required", "type": "invalid_request"})
		return
	}

	modelName := strings.TrimSpace(c.PostForm("model"))
	if modelName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Model not specified"})
		return
	}

	target, ok := s.resolveTarget(c, modelName)
	if !ok {
		return
	}

	requestBody := multipartFormValues(c.Request.MultipartForm)
	if SensitiveFilterEnabled() {
		if _, matched := MatchSensitiveWords(imageRequestText(requestBody)); matched {
			c.JSON(http.StatusForbidden, gin.H{"error": "Request blocked by content policy", "type": "content_policy"})
			return
		}
	}

	upstreamProtocol := channelProtocol(target.Channel.Type)
	if !supportsOpenAIImageEndpoint(upstreamProtocol) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Image editing is only supported for OpenAI-compatible upstream channels", "type": "unsupported_upstream"})
		return
	}

	if err := ValidateConfiguredHTTPURL(target.Channel.BaseURL); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Upstream URL blocked by SSRF protection", "type": "upstream_error"})
		return
	}

	prepared, err := prepareOpenAIImageEditRequest(&target.Channel, target.upstreamModelName(), c.Request.MultipartForm)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "type": "invalid_request"})
		return
	}

	resp, err := s.doUpstreamRequest(prepared, &target.Channel)
	if err != nil {
		logUpstreamRequestFailure(c, &target.Channel, prepared.URL, nil, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "Upstream request failed", "type": "upstream_error"})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to read upstream response"})
			return
		}
		logUpstreamError(c, &target.Channel, prepared.URL, resp.StatusCode, nil, respBody)
		c.JSON(resp.StatusCode, gin.H{"error": "Upstream request failed", "type": "upstream_error"})
		return
	}

	if isStreamingResponse(resp) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Streaming is not supported for this endpoint", "type": "unsupported_stream"})
		return
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to read upstream response"})
		return
	}

	var responseData map[string]interface{}
	_ = json.Unmarshal(respBody, &responseData)
	if SensitiveFilterEnabled() && SensitiveFilterScope() == SensitiveFilterScopeRequestResponse && responseData != nil {
		if _, matched := MatchSensitiveWords(imageResponseText(responseData)); matched {
			c.JSON(http.StatusForbidden, gin.H{"error": "Response blocked by content policy", "type": "content_policy"})
			return
		}
	}

	usage := imageUsageTokenCounts(target.ModelName, requestBody, responseData)
	if status, message, err := s.billUsage(c, target.User, target.APIKey, &target.Channel, &target.ModelConfig, target.billingModelName(), usage, target.billingModel()); err != nil {
		c.JSON(status, gin.H{"error": message})
		return
	}

	ApplyGeneratedAssetHook(c.Request.Context(), GeneratedAssetInput{
		UserID:       target.User.ID,
		Kind:         "image",
		ModelName:    target.ModelName,
		ResponseData: responseData,
		ResponseBody: respBody,
		Source:       "image_edit",
	})

	writeUpstreamResponse(c, resp, respBody)
}

func (s *ProxyService) HandleVideoGeneration(c *gin.Context) {
	requestBody, _, ok := readProxyJSONBody(c)
	if !ok {
		return
	}

	result, ok := s.createVideoUpstreamGeneration(c, requestBody)
	if !ok {
		return
	}

	ApplyGeneratedAssetHook(c.Request.Context(), GeneratedAssetInput{
		UserID:       result.Target.User.ID,
		Kind:         "video",
		ModelName:    result.Target.ModelName,
		ResponseData: result.ResponseData,
		ResponseBody: result.Body,
		Source:       "video_generation",
	})

	writeUpstreamResponse(c, result.Response, result.Body)
}

func (s *ProxyService) HandleVideoTaskCreate(c *gin.Context) {
	requestBody, _, ok := readProxyJSONBody(c)
	if !ok {
		return
	}

	result, ok := s.createVideoUpstreamGeneration(c, requestBody)
	if !ok {
		return
	}

	responsePayload := string(result.Body)
	taskID := newVideoTaskID()
	upstreamTaskID := upstreamTaskIDFromPayload(result.ResponseData)
	status := videoTaskStatusFromPayload(result.ResponseData)
	if upstreamTaskID == "" {
		upstreamTaskID = taskID
	}
	if status == "" {
		status = "queued"
	}

	requestPayload, _ := json.Marshal(requestBody)
	task := model.VideoTask{
		ID:                taskID,
		UserID:            result.Target.User.ID,
		APIKeyID:          apiKeyID(result.Target.APIKey),
		UserChannelID:     result.Target.Channel.UserChannelID,
		ChannelID:         result.Target.Channel.ID,
		ModelConfigID:     result.Target.ModelConfig.ID,
		ModelName:         result.Target.ModelName,
		BillingModelName:  result.Target.billingModelName(),
		UpstreamTaskID:    upstreamTaskID,
		Status:            status,
		Cost:              result.Cost,
		RequestPayload:    string(requestPayload),
		ResponsePayload:   responsePayload,
		LastStatusPayload: responsePayload,
	}
	if err := model.DB.Create(&task).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create video task"})
		return
	}

	response := videoTaskResponse(task, result.ResponseData)
	ApplyGeneratedAssetHook(c.Request.Context(), GeneratedAssetInput{
		UserID:       result.Target.User.ID,
		Kind:         "video",
		ModelName:    result.Target.ModelName,
		ResponseData: result.ResponseData,
		ResponseBody: result.Body,
		Source:       "video_task_create:" + task.ID,
	})

	c.JSON(http.StatusOK, response)
}

func (s *ProxyService) HandleVideoTaskStatus(c *gin.Context) {
	taskID := strings.TrimSpace(c.Param("id"))
	if taskID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Task id is required"})
		return
	}
	user, ok := currentUserFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	var task model.VideoTask
	if err := model.DB.Where("id = ? AND user_id = ?", taskID, user.ID).First(&task).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Video task not found"})
		return
	}

	var payload map[string]interface{}
	if !terminalVideoTaskStatus(task.Status) && strings.TrimSpace(task.UpstreamTaskID) != "" && task.UpstreamTaskID != task.ID {
		var channel model.Channel
		if err := model.DB.First(&channel, task.ChannelID).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load video task channel"})
			return
		}
		if err := ValidateConfiguredHTTPURL(channel.BaseURL); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "Upstream URL blocked by SSRF protection", "type": "upstream_error"})
			return
		}
		resp, body, statusPayload, ok := s.fetchUpstreamVideoTask(c, &channel, task.UpstreamTaskID)
		if !ok {
			return
		}
		_ = resp.Body.Close()
		payload = statusPayload
		nextStatus := videoTaskStatusFromPayload(statusPayload)
		if nextStatus == "" {
			nextStatus = task.Status
		}
		updates := map[string]interface{}{
			"status":              nextStatus,
			"last_status_payload": string(body),
		}
		if err := model.DB.Model(&task).Updates(updates).Error; err == nil {
			task.Status = nextStatus
			task.LastStatusPayload = string(body)
		}
	} else {
		_ = json.Unmarshal([]byte(firstNonEmptyString(task.LastStatusPayload, task.ResponsePayload)), &payload)
	}

	response := videoTaskResponse(task, payload)
	if normalizeVideoTaskStatus(task.Status) == "succeeded" {
		ApplyGeneratedAssetHook(c.Request.Context(), GeneratedAssetInput{
			UserID:       user.ID,
			Kind:         "video",
			ModelName:    task.ModelName,
			ResponseData: payload,
			ResponseBody: []byte(firstNonEmptyString(task.LastStatusPayload, task.ResponsePayload)),
			Source:       "video_task_status:" + task.ID,
		})
	}

	c.JSON(http.StatusOK, response)
}

func (s *ProxyService) fetchUpstreamVideoTask(c *gin.Context, channel *model.Channel, upstreamTaskID string) (*http.Response, []byte, map[string]interface{}, bool) {
	upstreamProtocol := channelProtocol(channel.Type)
	if !supportsVideoEndpoint(upstreamProtocol) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Video task status is only supported for video upstream channels", "type": "unsupported_upstream"})
		return nil, nil, nil, false
	}
	path, ok := adapters.Path(channel.Type, adapters.EndpointVideoStatus, upstreamTaskID)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Video task status is not supported for this upstream channel", "type": "unsupported_upstream"})
		return nil, nil, nil, false
	}
	prepared := preparedUpstreamRequest{
		Method: http.MethodGet,
		URL:    upstreamURLForRequest(channel.BaseURL, path),
		Header: adapters.Headers(channel.Type, channel.APIKey, adapters.Protocol(upstreamProtocol), false),
	}
	resp, err := s.doUpstreamRequest(prepared, channel)
	if err != nil {
		logUpstreamRequestFailure(c, channel, prepared.URL, nil, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "Upstream request failed", "type": "upstream_error"})
		return nil, nil, nil, false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		resp.Body.Close()
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to read upstream response"})
		return nil, nil, nil, false
	}
	if resp.StatusCode >= http.StatusBadRequest {
		resp.Body.Close()
		logUpstreamError(c, channel, prepared.URL, resp.StatusCode, nil, body)
		c.JSON(resp.StatusCode, gin.H{"error": "Upstream request failed", "type": "upstream_error"})
		return nil, nil, nil, false
	}
	var payload map[string]interface{}
	_ = json.Unmarshal(body, &payload)
	return resp, body, payload, true
}

func videoTaskResponse(task model.VideoTask, payload map[string]interface{}) gin.H {
	response := gin.H{
		"id":          task.ID,
		"object":      "video.generation",
		"model":       task.ModelName,
		"status":      task.Status,
		"cost":        task.Cost,
		"upstream_id": task.UpstreamTaskID,
		"created_at":  task.CreatedAt.Unix(),
		"updated_at":  task.UpdatedAt.Unix(),
	}
	if payload != nil {
		response["upstream_response"] = payload
		if data, exists := payload["data"]; exists {
			response["data"] = data
		}
		if data := videoTaskDataFromPayload(payload); data != nil {
			response["data"] = data
		}
	}
	return response
}

func upstreamTaskIDFromPayload(payload map[string]interface{}) string {
	if payload == nil {
		return ""
	}
	if id := firstNonEmptyString(
		stringFromValue(payload["id"]),
		stringFromValue(payload["task_id"]),
		stringFromValue(payload["taskId"]),
		stringFromValue(payload["generation_id"]),
		stringFromValue(payload["generationId"]),
		stringFromValue(payload["request_id"]),
		stringFromValue(payload["requestId"]),
	); id != "" {
		return id
	}
	if data, ok := payload["data"].(map[string]interface{}); ok {
		return upstreamTaskIDFromPayload(data)
	}
	return ""
}

func videoTaskStatusFromPayload(payload map[string]interface{}) string {
	if payload == nil {
		return ""
	}
	status := strings.ToLower(strings.TrimSpace(firstNonEmptyString(
		stringFromValue(payload["status"]),
		stringFromValue(payload["state"]),
		stringFromValue(payload["task_status"]),
		stringFromValue(payload["taskStatus"]),
	)))
	if status != "" {
		return normalizeVideoTaskStatus(status)
	}
	if data, ok := payload["data"].(map[string]interface{}); ok {
		return videoTaskStatusFromPayload(data)
	}
	if _, ok := payload["data"].([]interface{}); ok {
		return "succeeded"
	}
	return ""
}

func videoTaskDataFromPayload(payload map[string]interface{}) interface{} {
	if payload == nil {
		return nil
	}
	if data, exists := payload["data"]; exists {
		if dataMap, ok := data.(map[string]interface{}); ok {
			if result := klingTaskResultVideos(dataMap); len(result) > 0 {
				return result
			}
		}
		return data
	}
	if result := klingTaskResultVideos(payload); len(result) > 0 {
		return result
	}
	return nil
}

func klingTaskResultVideos(payload map[string]interface{}) []map[string]interface{} {
	if payload == nil {
		return nil
	}
	result, ok := payload["task_result"].(map[string]interface{})
	if !ok {
		result, ok = payload["taskResult"].(map[string]interface{})
	}
	if !ok {
		return nil
	}
	rawVideos, ok := result["videos"].([]interface{})
	if !ok {
		return nil
	}
	videos := make([]map[string]interface{}, 0, len(rawVideos))
	for _, raw := range rawVideos {
		item, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		video := make(map[string]interface{}, len(item)+1)
		for key, value := range item {
			video[key] = value
		}
		if _, exists := video["url"]; !exists {
			if urlValue := firstNonEmptyString(
				stringFromValue(item["url"]),
				stringFromValue(item["video_url"]),
				stringFromValue(item["videoUrl"]),
			); urlValue != "" {
				video["url"] = urlValue
			}
		}
		videos = append(videos, video)
	}
	return videos
}

func normalizeVideoTaskStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "succeed", "succeeded", "success", "completed", "complete", "done":
		return "succeeded"
	case "fail", "failed", "error":
		return "failed"
	case "submitted", "created", "pending", "queued":
		return "queued"
	case "processing", "running", "in_progress":
		return "processing"
	case "cancelled", "canceled":
		return "cancelled"
	default:
		return strings.ToLower(strings.TrimSpace(status))
	}
}

func terminalVideoTaskStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "succeeded", "success", "completed", "complete", "done", "failed", "error", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

func currentUserFromContext(c *gin.Context) (*model.User, bool) {
	if c == nil {
		return nil, false
	}
	value, exists := c.Get("user")
	if !exists {
		return nil, false
	}
	user, ok := value.(*model.User)
	return user, ok && user != nil
}

func newVideoTaskID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("vtask_%d", time.Now().UnixNano())
	}
	return "vtask_" + hex.EncodeToString(raw[:])
}

func (s *ProxyService) HandleClaudeMessages(c *gin.Context) {
	requestBody, bodyBytes, ok := readProxyJSONBody(c)
	if !ok {
		return
	}
	modelName, ok := requestBody["model"].(string)
	if !ok || strings.TrimSpace(modelName) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Model not specified"})
		return
	}

	s.handleConvertedProviderRequest(c, protocolClaude, modelName, requestBody, bodyBytes)
}

func (s *ProxyService) HandleGeminiGenerateContent(c *gin.Context) {
	modelName, action, ok := geminiModelAction(c.Param("modelAction"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "Not found"})
		return
	}
	if action == "streamGenerateContent" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Streaming is not supported for this endpoint", "type": "unsupported_stream"})
		return
	}
	if action != "generateContent" {
		c.JSON(http.StatusNotFound, gin.H{"error": "Not found"})
		return
	}

	requestBody, bodyBytes, ok := readProxyJSONBody(c)
	if !ok {
		return
	}
	s.handleConvertedProviderRequest(c, protocolGemini, modelName, requestBody, bodyBytes)
}

func readProxyJSONBody(c *gin.Context) (map[string]interface{}, []byte, bool) {
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to read request body"})
		return nil, nil, false
	}
	c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	var requestBody map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &requestBody); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request body"})
		return nil, nil, false
	}
	return requestBody, bodyBytes, true
}

func (s *ProxyService) handleConvertedProviderRequest(c *gin.Context, clientProtocol proxyProtocol, modelName string, requestBody map[string]interface{}, originalBody []byte) {
	beginTokenLogTiming(c)
	metaResolution, err := ResolveMetaModel(c, MetaModelResolveInput{
		ModelName:    modelName,
		RequestBody:  requestBody,
		OriginalBody: originalBody,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to resolve meta model"})
		return
	}
	if metaResolution.ErrorStatus != 0 {
		c.JSON(metaResolution.ErrorStatus, gin.H{"error": metaResolution.ErrorMessage})
		return
	}
	if metaResolution.Matched {
		resolvedModelName := strings.TrimSpace(metaResolution.ModelName)
		if resolvedModelName == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Meta model did not resolve to a model"})
			return
		}
		modelName = resolvedModelName
		requestBody["model"] = resolvedModelName
		if metaResolution.SkipAPIKeyModelCheck {
			c.Set("skip_api_key_model_check", true)
		}
	}

	target, ok := s.resolveTarget(c, modelName)
	if !ok {
		return
	}
	if metaResolution.Matched && metaResolution.BillingMode == "meta" && metaResolution.BillingModel != nil {
		target.BillingModel = metaResolution.BillingModel
		target.BillingModelName = metaResolution.BillingModel.ModelName
	}

	normalized := normalizeProviderRequest(clientProtocol, c.Request.URL.Path, requestBody, target.upstreamModelName())
	if SensitiveFilterEnabled() {
		if _, matched := MatchSensitiveWords(normalizedRequestText(normalized)); matched {
			c.JSON(http.StatusForbidden, gin.H{"error": "Request blocked by content policy", "type": "content_policy"})
			return
		}
	}
	pluginUpstream, isPluginUpstream := PluginUpstreamForType(target.Channel.Type)
	if strings.HasPrefix(strings.TrimSpace(target.Channel.Type), pluginUpstreamPrefix) && !isPluginUpstream {
		c.JSON(http.StatusBadRequest, gin.H{"error": "The plugin upstream channel is unavailable", "type": "unsupported_upstream"})
		return
	}
	upstreamProtocol := channelProtocol(target.Channel.Type)
	if isPluginUpstream {
		upstreamProtocol = protocolResponses
		if clientProtocol != protocolResponses {
			c.JSON(http.StatusBadRequest, gin.H{"error": "This plugin upstream only supports the OpenAI Responses API", "type": "unsupported_upstream"})
			return
		}
	}
	compactRequest := strings.HasSuffix(strings.TrimRight(c.Request.URL.Path, "/"), "/responses/compact")
	if normalized.Stream && upstreamProtocol != clientProtocol {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Streaming is not supported when converting upstream protocol", "type": "unsupported_stream"})
		return
	}

	if !isPluginUpstream {
		if err := ValidateConfiguredHTTPURL(target.Channel.BaseURL); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "Upstream URL blocked by SSRF protection", "type": "upstream_error"})
			return
		}
	}

	var prepared preparedUpstreamRequest
	if isPluginUpstream {
		payload, payloadErr := openAIResponsesPayloadMap(normalized)
		if payloadErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": payloadErr.Error(), "type": "invalid_request"})
			return
		}
		pluginPrepared, pluginErr := PreparePluginUpstreamRequest(c.Request.Context(), pluginUpstream, target.User.ID, newPluginRequestID(), target.Channel, payload, normalized.Stream, compactRequest)
		if pluginErr != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": pluginErr.Error(), "type": "upstream_error"})
			return
		}
		if pluginPrepared.APIKey != "" && pluginPrepared.APIKey != target.Channel.APIKey {
			if err := model.DB.Model(&model.Channel{}).Where("id = ?", target.Channel.ID).Update("api_key", pluginPrepared.APIKey).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save refreshed upstream credential"})
				return
			}
			target.Channel.APIKey = pluginPrepared.APIKey
		}
		prepared = preparedUpstreamRequest{Method: pluginPrepared.Method, URL: pluginPrepared.URL, Body: pluginPrepared.Body, Header: pluginPrepared.Headers, Context: c.Request.Context()}
	} else {
		prepared, err = prepareProviderRequest(&target.Channel, upstreamProtocol, normalized)
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "type": "invalid_request"})
		return
	}
	resp, err := s.doUpstreamRequest(prepared, &target.Channel)
	if err != nil {
		logUpstreamRequestFailure(c, &target.Channel, prepared.URL, prepared.Body, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "Upstream request failed", "type": "upstream_error"})
		return
	}
	markTokenLogFirstResponse(c)
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to read upstream response"})
			return
		}
		logUpstreamError(c, &target.Channel, prepared.URL, resp.StatusCode, prepared.Body, respBody)
		c.JSON(resp.StatusCode, gin.H{"error": "Upstream request failed", "type": "upstream_error"})
		return
	}

	if normalized.Stream || isStreamingResponse(resp) {
		if upstreamProtocol != clientProtocol {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Streaming is not supported when converting upstream protocol", "type": "unsupported_stream"})
			return
		}
		respBody, err := streamUpstreamResponse(c, resp)
		if err != nil {
			log.Printf("failed to stream upstream response: %v", err)
			return
		}
		usage, ok := parseUsageTokensFromStream(respBody)
		if !ok {
			usage = usageTokenCounts{
				InputTokens:  CountTokens(target.ModelName, string(originalBody)),
				OutputTokens: CountTokens(target.ModelName, string(respBody)),
			}
		}
		if _, _, err := s.billUsage(c, target.User, target.APIKey, &target.Channel, &target.ModelConfig, target.billingModelName(), usage, target.billingModel()); err != nil {
			log.Printf("failed to bill streaming usage for user=%d model=%s: %v", target.User.ID, target.ModelName, err)
		}
		return
	}

	s.handleNonStreamingResponse(c, resp, target, originalBody, prepared.URL, prepared.Body, upstreamProtocol, clientProtocol, clientProtocol == protocolResponses)
}

func (s *ProxyService) resolveTarget(c *gin.Context, modelName string) (*proxyTarget, bool) {
	val, _ := c.Get("user")
	user, ok := val.(*model.User)
	if !ok || user == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return nil, false
	}

	modelName = strings.TrimSpace(strings.TrimPrefix(modelName, "models/"))
	if modelName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Model not specified"})
		return nil, false
	}
	allowedByDepartment, err := DepartmentModelAllowed(user.ID, modelName)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to evaluate department model policy"})
		return nil, false
	}
	if !allowedByDepartment {
		c.JSON(http.StatusForbidden, gin.H{"error": "Department policy does not allow this model"})
		return nil, false
	}

	apiKey := currentAPIKey(c)
	skipAPIKeyModelCheck, _ := c.Get("skip_api_key_model_check")
	if skip, _ := skipAPIKeyModelCheck.(bool); !skip && !APIKeyAllowsModel(apiKey, modelName) {
		c.JSON(http.StatusForbidden, gin.H{"error": "API key is not allowed to use this model"})
		return nil, false
	}

	var candidates []model.ModelConfig
	query := model.DB.
		Preload("Channel.UserChannel").
		Preload("Model").
		Joins("JOIN channels ON channels.id = model_configs.channel_id").
		Joins("JOIN models ON models.id = model_configs.model_id").
		Joins("JOIN user_channels ON user_channels.id = channels.user_channel_id").
		Where("channels.enabled = ? AND model_configs.enabled = ? AND models.enabled = ? AND models.model_name = ? AND user_channels.enabled = ?", true, true, true, modelName, true)
	allowedUserChannels, ok := requiredAPIKeyUserChannels(apiKey)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "API key must be bound to exactly one user channel"})
		return nil, false
	}
	if len(allowedUserChannels) > 0 {
		query = query.Where("channels.user_channel_id IN ?", allowedUserChannels)
	}
	query, err = filterUserChannelGroupAccess(query, user)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to evaluate user channel access"})
		return nil, false
	}
	if err := query.Order("channels.priority DESC, channels.weight DESC, channels.id ASC").Find(&candidates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to find available channels"})
		return nil, false
	}
	if len(candidates) == 0 {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "No available channel for this model"})
		return nil, false
	}
	modelConfig := s.selectModelConfig(candidates, modelName)

	channel := modelConfig.Channel
	if channel.ID == 0 {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "No enabled model configuration for this model"})
		return nil, false
	}
	if !PersonalModeEnabled() && user.Balance.LessThanOrEqual(decimal.Zero) {
		c.JSON(http.StatusPaymentRequired, gin.H{"error": "Insufficient balance"})
		return nil, false
	}
	if exceeded, err := APIKeyQuotaExceeded(apiKey, decimal.Zero); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check API key quota"})
		return nil, false
	} else if exceeded {
		c.JSON(http.StatusPaymentRequired, gin.H{"error": "API key quota exceeded"})
		return nil, false
	}

	return &proxyTarget{
		User:        user,
		APIKey:      apiKey,
		ModelName:   modelName,
		ModelConfig: modelConfig,
		Channel:     channel,
	}, true
}

func (target *proxyTarget) upstreamModelName() string {
	if target == nil {
		return ""
	}
	upstreamModelName := strings.TrimSpace(target.ModelConfig.UpstreamModelName)
	if upstreamModelName == "" {
		return target.ModelName
	}
	return upstreamModelName
}

func (target *proxyTarget) billingModel() model.Model {
	if target != nil && target.BillingModel != nil {
		return *target.BillingModel
	}
	if target != nil {
		return target.ModelConfig.Model
	}
	return model.Model{}
}

func (target *proxyTarget) billingModelName() string {
	if target != nil && strings.TrimSpace(target.BillingModelName) != "" {
		return strings.TrimSpace(target.BillingModelName)
	}
	if target != nil {
		return target.ModelName
	}
	return ""
}

func (s *ProxyService) selectModelConfig(candidates []model.ModelConfig, modelName string) model.ModelConfig {
	if len(candidates) == 1 {
		return candidates[0]
	}
	algorithm := RoutingAlgorithm(candidates[0].Channel.UserChannel.RoutingAlgorithm)
	userChannelID := uint(0)
	if candidates[0].Channel.UserChannelID != nil {
		userChannelID = *candidates[0].Channel.UserChannelID
	}

	switch algorithm {
	case RoutingRoundRobin:
		index := s.nextRoutingCounter(algorithm, userChannelID, modelName) % uint64(len(candidates))
		return candidates[int(index)]
	case RoutingWeightedRoundRobin:
		totalWeight := 0
		for _, candidate := range candidates {
			totalWeight += normalizedChannelWeight(candidate.Channel.Weight)
		}
		if totalWeight <= 0 {
			return candidates[0]
		}
		position := int(s.nextRoutingCounter(algorithm, userChannelID, modelName) % uint64(totalWeight))
		for _, candidate := range candidates {
			weight := normalizedChannelWeight(candidate.Channel.Weight)
			if position < weight {
				return candidate
			}
			position -= weight
		}
		return candidates[0]
	default:
		return candidates[0]
	}
}

func (s *ProxyService) nextRoutingCounter(algorithm string, userChannelID uint, modelName string) uint64 {
	key := fmt.Sprintf("%s:%d:%s", algorithm, userChannelID, modelName)
	s.routingMu.Lock()
	defer s.routingMu.Unlock()
	value := s.routingCounters[key]
	s.routingCounters[key] = value + 1
	return value
}

func normalizedChannelWeight(weight int) int {
	if weight <= 0 {
		return 1
	}
	return weight
}

func RoutingAlgorithm(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case RoutingRoundRobin, "roundrobin", "rr":
		return RoutingRoundRobin
	case RoutingWeightedRoundRobin, "weighted", "weighted_roundrobin", "wrr":
		return RoutingWeightedRoundRobin
	default:
		return RoutingPriority
	}
}

func (s *ProxyService) handleNonStreamingResponse(c *gin.Context, resp *http.Response, target *proxyTarget, originalBody []byte, upstreamURL string, upstreamBody []byte, upstreamProtocol proxyProtocol, clientProtocol proxyProtocol, responses bool) {
	if resp.StatusCode >= http.StatusBadRequest {
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to read upstream response"})
			return
		}
		logUpstreamError(c, &target.Channel, upstreamURL, resp.StatusCode, upstreamBody, respBody)
		c.JSON(resp.StatusCode, gin.H{"error": "Upstream request failed", "type": "upstream_error"})
		return
	}

	if isStreamingResponse(resp) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Streaming is not supported for this endpoint", "type": "unsupported_stream"})
		return
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to read upstream response"})
		return
	}
	clientBody := respBody
	if upstreamProtocol != clientProtocol {
		clientBody, err = transformProviderResponse(respBody, upstreamProtocol, clientProtocol, responses, target.ModelName)
		if err != nil {
			log.Printf(
				"failed to transform upstream response: method=%s path=%s channel_id=%d upstream_url=%s error=%v response_body=%q",
				c.Request.Method,
				c.Request.URL.RequestURI(),
				target.Channel.ID,
				redactedUpstreamURL(upstreamURL),
				err,
				proxyBodyPreview(respBody, 1000),
			)
			c.JSON(http.StatusBadGateway, gin.H{"error": "Upstream request failed", "type": "upstream_error"})
			return
		}
	}

	var responseData map[string]interface{}
	_ = json.Unmarshal(clientBody, &responseData)
	if SensitiveFilterEnabled() && SensitiveFilterScope() == SensitiveFilterScopeRequestResponse && responseData != nil {
		text := providerResponseFromPayload(responseData, clientProtocol).Text
		if _, matched := MatchSensitiveWords(text); matched {
			c.JSON(http.StatusForbidden, gin.H{"error": "Response blocked by content policy", "type": "content_policy"})
			return
		}
	}
	usage, ok := parseUsageTokens(responseData)
	if !ok {
		usage = usageTokenCounts{
			InputTokens:  CountTokens(target.ModelName, string(originalBody)),
			OutputTokens: CountTokens(target.ModelName, string(clientBody)),
		}
	}
	if status, message, err := s.billUsage(c, target.User, target.APIKey, &target.Channel, &target.ModelConfig, target.billingModelName(), usage, target.billingModel()); err != nil {
		c.JSON(status, gin.H{"error": message})
		return
	}

	if upstreamProtocol == clientProtocol {
		writeUpstreamResponse(c, resp, clientBody)
		return
	}
	writeJSONProxyResponse(c, resp.StatusCode, clientBody)
}

func (s *ProxyService) billUsage(c *gin.Context, user *model.User, apiKey *model.APIKey, channel *model.Channel, modelConfig *model.ModelConfig, modelName string, usage usageTokenCounts, billingModel model.Model) (int, string, error) {
	_, status, message, err := s.billUsageAndReturnCost(c, user, apiKey, channel, modelConfig, modelName, usage, billingModel)
	return status, message, err
}

func (s *ProxyService) billUsageAndReturnCost(c *gin.Context, user *model.User, apiKey *model.APIKey, channel *model.Channel, modelConfig *model.ModelConfig, modelName string, usage usageTokenCounts, billingModel model.Model) (decimal.Decimal, int, string, error) {
	groupMultiplier, err := effectiveUserGroupMultiplier(user, channel.ID, modelConfig.ID)
	if err != nil {
		return decimal.Zero, http.StatusInternalServerError, "User group not found", err
	}
	usage = normalizeUsageTokenCounts(usage)

	// Prices are stored per 1M tokens.
	baseCost := calculateModelUsageCost(usage, billingModel)
	channelMultiplier := userChannelMultiplier(channel)
	cost := baseCost.Mul(groupMultiplier).Mul(channelMultiplier)
	referralRate := referralCommissionRate(user, cost)

	// 7. Deduct balance and log
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

	if err := ApplyUsageCharge(tx, user.ID, cost); err != nil {
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
		log.Printf("failed to record token log: %v", err)
	}

	return cost, 0, "", nil
}

const (
	tokenLogStartKey         = "token_log_started_at"
	tokenLogFirstResponseKey = "token_log_first_response_at"
)

func beginTokenLogTiming(c *gin.Context) {
	if c == nil {
		return
	}
	if _, exists := c.Get(tokenLogStartKey); !exists {
		c.Set(tokenLogStartKey, time.Now())
	}
}

func markTokenLogFirstResponse(c *gin.Context) {
	if c == nil {
		return
	}
	if _, exists := c.Get(tokenLogFirstResponseKey); !exists {
		c.Set(tokenLogFirstResponseKey, time.Now())
	}
}

func newUsageTokenLog(c *gin.Context, user *model.User, apiKey *model.APIKey, channel *model.Channel, modelConfig *model.ModelConfig, modelName string, usage usageTokenCounts, billingModel model.Model, baseCost, groupMultiplier, channelMultiplier, cost decimal.Decimal) model.TokenLog {
	responseTimeMS, firstResponseTimeMS := tokenLogTiming(c)
	modelConfigID := uint(0)
	if modelConfig != nil {
		modelConfigID = modelConfig.ID
	}
	return model.TokenLog{
		ID:                      model.NextLogID(),
		UserID:                  user.ID,
		APIKeyID:                apiKeyID(apiKey),
		UserChannelID:           channel.UserChannelID,
		ChannelID:               channel.ID,
		ModelConfigID:           modelConfigID,
		ModelName:               modelName,
		InputTokens:             usage.InputTokens,
		OutputTokens:            usage.OutputTokens,
		CachedInputTokens:       usage.CachedInputTokens,
		CacheWriteInputTokens:   usage.CacheWriteInputTokens,
		CacheWrite1hInputTokens: usage.CacheWrite1hInputTokens,
		ImageInputTokens:        usage.ImageInputTokens,
		ImageOutputTokens:       usage.ImageOutputTokens,
		AudioInputTokens:        usage.AudioInputTokens,
		AudioOutputTokens:       usage.AudioOutputTokens,
		ResponseTimeMs:          responseTimeMS,
		FirstResponseTimeMs:     firstResponseTimeMS,
		BaseCost:                baseCost,
		GroupMultiplier:         groupMultiplier,
		UserChannelMultiplier:   channelMultiplier,
		InputPrice:              billingModel.InputPrice,
		OutputPrice:             billingModel.OutputPrice,
		CachedInputPrice:        billingModel.CachedInputPrice,
		CacheWriteInputPrice:    billingModel.CacheWriteInputPrice,
		CacheWrite1hInputPrice:  billingModel.CacheWrite1hInputPrice,
		PricingFormula:          usagePricingFormula(usage, baseCost, groupMultiplier, channelMultiplier, cost),
		Cost:                    cost,
		IP:                      clientIPForLog(c),
		UserAgent:               userAgentForLog(c),
		CreatedAt:               time.Now(),
	}
}

func tokenLogTiming(c *gin.Context) (int64, int64) {
	if c == nil {
		return 0, 0
	}
	now := time.Now()
	start, _ := c.Get(tokenLogStartKey)
	first, _ := c.Get(tokenLogFirstResponseKey)
	var responseTimeMS, firstResponseTimeMS int64
	if startedAt, ok := start.(time.Time); ok {
		responseTimeMS = now.Sub(startedAt).Milliseconds()
		if firstResponseAt, ok := first.(time.Time); ok {
			firstResponseTimeMS = firstResponseAt.Sub(startedAt).Milliseconds()
		}
	}
	return responseTimeMS, firstResponseTimeMS
}

func usagePricingFormula(usage usageTokenCounts, baseCost, groupMultiplier, channelMultiplier, cost decimal.Decimal) string {
	return fmt.Sprintf("base=%s (input=%d, output=%d, cache_read=%d, cache_write=%d, cache_write_1h=%d) × group=%s × user_channel=%s = %s", baseCost.String(), usage.InputTokens, usage.OutputTokens, usage.CachedInputTokens, usage.CacheWriteInputTokens, usage.CacheWrite1hInputTokens, groupMultiplier.String(), channelMultiplier.String(), cost.String())
}

func calculateModelUsageCost(usage usageTokenCounts, modelConfig model.Model) decimal.Decimal {
	if modelConfig.QuotaType == model.QuotaTypeVideoResolutionDuration {
		if usage.BillableCost.LessThan(decimal.Zero) {
			return decimal.Zero
		}
		return usage.BillableCost
	}
	if modelConfig.QuotaType == 1 {
		count := usage.OutputTokens / 1000000
		if usage.OutputTokens%1000000 != 0 {
			count++
		}
		if count <= 0 {
			count = 1
		}
		return modelConfig.OutputPrice.Mul(decimal.NewFromInt(int64(count)))
	}

	metrics := PriceTierMetrics{
		FullInputTokens:      usage.InputTokens,
		FullRequestTokens:    usage.InputTokens + usage.OutputTokens,
		BillableInputTokens:  billableInputTokens(usage),
		BillableOutputTokens: usage.OutputTokens,
	}

	inputTokens := billableInputTokens(usage)
	outputTokens := usage.OutputTokens
	total := decimal.Zero

	imageInputTokens := 0
	if hasDedicatedPrice(modelConfig.ImageInputPrice, modelConfig.ImageInputPriceTiers) {
		imageInputTokens = clampTokenCount(usage.ImageInputTokens, inputTokens)
		inputTokens -= imageInputTokens
		total = total.Add(CalculateTieredTokenCostWithMetrics(imageInputTokens, modelConfig.ImageInputPrice, modelConfig.ImageInputPriceTiers, metrics))
	}
	audioInputTokens := 0
	if hasDedicatedPrice(modelConfig.AudioInputPrice, modelConfig.AudioInputPriceTiers) {
		audioInputTokens = clampTokenCount(usage.AudioInputTokens, inputTokens)
		inputTokens -= audioInputTokens
		total = total.Add(CalculateTieredTokenCostWithMetrics(audioInputTokens, modelConfig.AudioInputPrice, modelConfig.AudioInputPriceTiers, metrics))
	}

	imageOutputTokens := 0
	if hasDedicatedPrice(modelConfig.ImageOutputPrice, modelConfig.ImageOutputPriceTiers) {
		imageOutputTokens = clampTokenCount(usage.ImageOutputTokens, outputTokens)
		outputTokens -= imageOutputTokens
		total = total.Add(CalculateTieredTokenCostWithMetrics(imageOutputTokens, modelConfig.ImageOutputPrice, modelConfig.ImageOutputPriceTiers, metrics))
	}
	audioOutputTokens := 0
	if hasDedicatedPrice(modelConfig.AudioOutputPrice, modelConfig.AudioOutputPriceTiers) {
		audioOutputTokens = clampTokenCount(usage.AudioOutputTokens, outputTokens)
		outputTokens -= audioOutputTokens
		total = total.Add(CalculateTieredTokenCostWithMetrics(audioOutputTokens, modelConfig.AudioOutputPrice, modelConfig.AudioOutputPriceTiers, metrics))
	}

	return total.
		Add(CalculateTieredTokenCostWithMetrics(inputTokens, modelConfig.InputPrice, modelConfig.InputPriceTiers, metrics)).
		Add(CalculateTieredTokenCostWithMetrics(usage.CacheReadInputTokens, modelConfig.CachedInputPrice, modelConfig.CachedInputPriceTiers, metrics)).
		Add(CalculateTieredTokenCostWithMetrics(usage.CacheWriteInputTokens, modelConfig.CacheWriteInputPrice, modelConfig.CacheWriteInputPriceTiers, metrics)).
		Add(CalculateTieredTokenCostWithMetrics(usage.CacheWrite1hInputTokens, modelConfig.CacheWrite1hInputPrice, modelConfig.CacheWrite1hInputPriceTiers, metrics)).
		Add(CalculateTieredTokenCostWithMetrics(outputTokens, modelConfig.OutputPrice, modelConfig.OutputPriceTiers, metrics))
}

func billableInputTokens(usage usageTokenCounts) int {
	tokens := usage.InputTokens - usage.CacheReadInputTokens - usage.CacheWriteInputTokens - usage.CacheWrite1hInputTokens
	if tokens < 0 {
		return 0
	}
	return tokens
}

func hasDedicatedPrice(price decimal.Decimal, tiers model.PriceTierList) bool {
	return !price.IsZero() || len(model.NormalizePriceTiers(tiers)) > 0
}

func clampTokenCount(tokens int, maxTokens int) int {
	if tokens < 0 {
		return 0
	}
	if tokens > maxTokens {
		return maxTokens
	}
	return tokens
}

func referralCommissionRate(user *model.User, cost decimal.Decimal) decimal.Decimal {
	if user == nil || user.ReferrerID == nil || *user.ReferrerID == 0 || cost.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero
	}
	if !settingBool("referral_enabled", false) {
		return decimal.Zero
	}
	rate, err := decimal.NewFromString(strings.TrimSpace(model.GetSystemSetting("referral_commission_rate", "0")))
	if err != nil || rate.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero
	}
	if rate.GreaterThan(decimal.NewFromInt(1)) {
		rate = rate.Div(decimal.NewFromInt(100))
	}
	return rate
}

func applyReferralCommission(tx *gorm.DB, user *model.User, tokenLogID uint, cost decimal.Decimal, rate decimal.Decimal) error {
	if user == nil || user.ReferrerID == nil || *user.ReferrerID == 0 || cost.LessThanOrEqual(decimal.Zero) || rate.LessThanOrEqual(decimal.Zero) {
		return nil
	}
	amount := cost.Mul(rate)
	if amount.LessThanOrEqual(decimal.Zero) {
		return nil
	}

	commission := model.ReferralCommissionLog{
		ReferrerID:     *user.ReferrerID,
		ReferredUserID: user.ID,
		TokenLogID:     tokenLogID,
		BaseCost:       cost,
		Rate:           rate,
		Amount:         amount,
	}
	if err := tx.Create(&commission).Error; err != nil {
		return err
	}
	return tx.Model(&model.User{}).
		Where("id = ?", *user.ReferrerID).
		UpdateColumn("balance", gorm.Expr("balance + ?", amount)).Error
}

func userChannelMultiplier(channel *model.Channel) decimal.Decimal {
	if PersonalModeEnabled() {
		return decimal.NewFromInt(1)
	}
	if channel == nil || channel.UserChannel.ID == 0 || channel.UserChannel.Multiplier.IsZero() {
		return decimal.NewFromInt(1)
	}
	return channel.UserChannel.Multiplier
}

func effectiveUserGroupMultiplier(user *model.User, channelID uint, modelConfigID uint) (decimal.Decimal, error) {
	if PersonalModeEnabled() {
		return decimal.NewFromInt(1), nil
	}
	multipliers, err := activeGroupMultipliers(user, channelID, modelConfigID)
	if err != nil {
		return decimal.Zero, err
	}
	if len(multipliers) == 0 {
		return decimal.NewFromInt(1), nil
	}

	selected := multipliers[0]
	mode := strings.ToLower(strings.TrimSpace(model.GetSystemSetting("group_multiplier_mode", "min")))
	for _, multiplier := range multipliers[1:] {
		if mode == "max" || mode == "high" || mode == "higher" {
			if multiplier.GreaterThan(selected) {
				selected = multiplier
			}
			continue
		}
		if multiplier.LessThan(selected) {
			selected = multiplier
		}
	}
	departmentMultiplier, err := EffectiveDepartmentMultiplier(user.ID)
	if err != nil {
		return decimal.Zero, err
	}
	return selected.Mul(departmentMultiplier), nil
}

// filterUserChannelGroupAccess keeps unrestricted user channels and channels
// explicitly granted to at least one active group of the user.
func filterUserChannelGroupAccess(query *gorm.DB, user *model.User) (*gorm.DB, error) {
	groupIDs, err := activeUserGroupIDs(user)
	if err != nil {
		return nil, err
	}
	if len(groupIDs) == 0 {
		return query.Where(`NOT EXISTS (
			SELECT 1 FROM user_channel_group_accesses access
			WHERE access.user_channel_id = user_channels.id
		)`), nil
	}
	return query.Where(`NOT EXISTS (
		SELECT 1 FROM user_channel_group_accesses access
		WHERE access.user_channel_id = user_channels.id
	) OR EXISTS (
		SELECT 1 FROM user_channel_group_accesses access
		WHERE access.user_channel_id = user_channels.id AND access.group_id IN ?
	)`, groupIDs), nil
}

func activeUserGroupIDs(user *model.User) ([]uint, error) {
	if user == nil {
		return nil, errors.New("User is required")
	}
	now := time.Now()
	var memberships []model.UserGroupMembership
	if err := model.DB.
		Where("user_id = ? AND (expires_at IS NULL OR expires_at > ?)", user.ID, now).
		Find(&memberships).Error; err != nil {
		return nil, err
	}
	if len(memberships) == 0 && user.GroupID != 0 {
		var group model.Group
		if err := model.DB.First(&group, user.GroupID).Error; err != nil {
			return nil, err
		}
		memberships = append(memberships, model.UserGroupMembership{GroupID: group.ID})
	}
	seen := make(map[uint]struct{}, len(memberships))
	groupIDs := make([]uint, 0, len(memberships))
	for _, membership := range memberships {
		if membership.GroupID == 0 {
			continue
		}
		if _, exists := seen[membership.GroupID]; exists {
			continue
		}
		seen[membership.GroupID] = struct{}{}
		groupIDs = append(groupIDs, membership.GroupID)
	}
	return groupIDs, nil
}

func activeGroupMultipliers(user *model.User, channelID uint, modelConfigID uint) ([]decimal.Decimal, error) {
	now := time.Now()
	var memberships []model.UserGroupMembership
	err := model.DB.Preload("Group").
		Where("user_id = ? AND (expires_at IS NULL OR expires_at > ?)", user.ID, now).
		Find(&memberships).Error
	if err != nil {
		return nil, err
	}
	if len(memberships) == 0 && user.GroupID != 0 {
		var group model.Group
		if err := model.DB.First(&group, user.GroupID).Error; err != nil {
			return nil, err
		}
		memberships = append(memberships, model.UserGroupMembership{UserID: user.ID, GroupID: group.ID, Group: group})
	}

	groupIDs := make([]uint, 0, len(memberships))
	for _, membership := range memberships {
		groupIDs = append(groupIDs, membership.GroupID)
	}

	channelOverrides := map[uint]decimal.Decimal{}
	if len(groupIDs) > 0 {
		var overrides []model.ChannelGroupMultiplier
		if err := model.DB.Where("channel_id = ? AND group_id IN ?", channelID, groupIDs).Find(&overrides).Error; err != nil {
			return nil, err
		}
		for _, override := range overrides {
			channelOverrides[override.GroupID] = override.Multiplier
		}
	}

	modelOverrides := map[uint]decimal.Decimal{}
	if len(groupIDs) > 0 {
		var overrides []model.ModelGroupMultiplier
		if err := model.DB.Where("model_config_id = ? AND group_id IN ?", modelConfigID, groupIDs).Find(&overrides).Error; err != nil {
			return nil, err
		}
		for _, override := range overrides {
			modelOverrides[override.GroupID] = override.Multiplier
		}
	}

	multipliers := make([]decimal.Decimal, 0, len(memberships))
	for _, membership := range memberships {
		multiplier := membership.Group.Multiplier
		if multiplier.IsZero() {
			multiplier = decimal.NewFromInt(1)
		}
		if override, ok := channelOverrides[membership.GroupID]; ok && !override.IsZero() {
			multiplier = override
		}
		if override, ok := modelOverrides[membership.GroupID]; ok && !override.IsZero() {
			multiplier = override
		}
		multipliers = append(multipliers, multiplier)
	}
	return multipliers, nil
}

func filterModelsForAPIKey(modelNames []string, apiKey *model.APIKey) []string {
	if apiKey == nil {
		return modelNames
	}
	allowed := ParseList(apiKey.AllowedModels)
	if len(allowed) == 0 {
		return modelNames
	}
	allowedSet := map[string]struct{}{}
	for _, name := range allowed {
		allowedSet[name] = struct{}{}
	}
	filtered := make([]string, 0, len(modelNames))
	for _, name := range modelNames {
		if _, ok := allowedSet[name]; ok {
			filtered = append(filtered, name)
		}
	}
	return filtered
}

func uniqueSortedModelNames(modelNames []string) []string {
	seen := map[string]struct{}{}
	unique := make([]string, 0, len(modelNames))
	for _, name := range modelNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		unique = append(unique, name)
	}
	sort.Strings(unique)
	return unique
}

func currentAPIKey(c *gin.Context) *model.APIKey {
	if c == nil {
		return nil
	}
	value, exists := c.Get("api_key")
	if !exists {
		return nil
	}
	apiKey, ok := value.(*model.APIKey)
	if !ok {
		return nil
	}
	return apiKey
}

func apiKeyID(apiKey *model.APIKey) *uint {
	if apiKey == nil || apiKey.ID == 0 {
		return nil
	}
	return &apiKey.ID
}

func apiKeyAllowedUserChannels(apiKey *model.APIKey) string {
	if apiKey == nil {
		return ""
	}
	return apiKey.AllowedUserChannels
}

func requiredAPIKeyUserChannels(apiKey *model.APIKey) ([]uint, bool) {
	if PersonalModeEnabled() {
		return nil, true
	}
	if apiKey == nil {
		return nil, true
	}
	allowed := ParseUintList(apiKeyAllowedUserChannels(apiKey))
	return allowed, len(allowed) == 1
}

func isStreamingRequest(requestBody map[string]interface{}) bool {
	stream, ok := requestBody["stream"].(bool)
	return ok && stream
}

func isStreamingResponse(resp *http.Response) bool {
	return strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream")
}

func streamUpstreamResponse(c *gin.Context, resp *http.Response) ([]byte, error) {
	for k, v := range resp.Header {
		if !shouldSkipProxyResponseHeader(k, true) {
			c.Writer.Header()[k] = v
		}
	}
	c.Writer.WriteHeader(resp.StatusCode)

	flusher, _ := c.Writer.(http.Flusher)
	var buffered bytes.Buffer
	chunk := make([]byte, 32*1024)

	for {
		n, readErr := resp.Body.Read(chunk)
		if n > 0 {
			data := chunk[:n]
			if buffered.Len() < maxBufferedStreamBytes {
				remaining := maxBufferedStreamBytes - buffered.Len()
				if len(data) > remaining {
					buffered.Write(data[:remaining])
				} else {
					buffered.Write(data)
				}
			}
			if _, err := c.Writer.Write(data); err != nil {
				return buffered.Bytes(), err
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return buffered.Bytes(), readErr
		}
	}

	return buffered.Bytes(), nil
}

func parseUsageTokensFromStream(body []byte) (usageTokenCounts, bool) {
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), maxBufferedStreamBytes)

	var usage usageTokenCounts
	var found bool
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}

		var data map[string]interface{}
		if err := json.Unmarshal([]byte(payload), &data); err != nil {
			continue
		}
		if parsedUsage, ok := parseUsageTokens(data); ok {
			usage = parsedUsage
			found = true
		}
	}

	return usage, found
}

func writeUpstreamResponse(c *gin.Context, resp *http.Response, respBody []byte) {
	for k, v := range resp.Header {
		if !shouldSkipProxyResponseHeader(k, false) {
			c.Writer.Header()[k] = v
		}
	}
	c.Writer.WriteHeader(resp.StatusCode)
	c.Writer.Write(respBody)
}

func (s *ProxyService) forwardToUpstream(channel *model.Channel, method, path string, body []byte, originalHeader http.Header) (*http.Response, error) {
	if path == "" || !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	fullURL := upstreamURLForRequest(channel.BaseURL, path)

	req, err := http.NewRequest(method, fullURL, bytes.NewBuffer(body))
	if err != nil {
		return nil, err
	}

	// Copy headers and set Authorization
	for k, v := range originalHeader {
		if !shouldSkipProxyHeader(k) {
			req.Header[k] = v
		}
	}
	req.Header.Set("Authorization", "Bearer "+channel.APIKey)

	client := &http.Client{Timeout: 60 * time.Second}
	return client.Do(req)
}

func (s *ProxyService) doUpstreamRequest(prepared preparedUpstreamRequest, channel *model.Channel) (*http.Response, error) {
	ctx := prepared.Context
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, prepared.Method, prepared.URL, bytes.NewBuffer(prepared.Body))
	if err != nil {
		return nil, err
	}
	for key, values := range prepared.Header {
		req.Header[key] = values
	}
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	recordUpstreamResult(channel, resp, err)
	return resp, err
}

func rawProviderRequest(channel *model.Channel, protocol proxyProtocol, method, path string, body []byte, originalHeader http.Header) preparedUpstreamRequest {
	fullURL := upstreamURLForRequest(channel.BaseURL, path)
	if protocol == protocolGemini && strings.TrimSpace(channel.APIKey) != "" {
		fullURL = withQueryParam(fullURL, "key", strings.TrimSpace(channel.APIKey))
	}
	return preparedUpstreamRequest{
		Method: method,
		URL:    fullURL,
		Body:   body,
		Header: providerHeadersFromOriginal(channel, protocol, originalHeader),
	}
}

func providerHeadersFromOriginal(channel *model.Channel, protocol proxyProtocol, originalHeader http.Header) http.Header {
	headers := http.Header{}
	for key, values := range originalHeader {
		if shouldSkipProxyHeader(key) || shouldSkipProviderAuthHeader(key) {
			continue
		}
		headers[key] = values
	}
	if headers.Get("Content-Type") == "" {
		headers.Set("Content-Type", "application/json")
	}
	if headers.Get("Accept") == "" {
		headers.Set("Accept", "application/json")
	}

	adapterHeaders := adapters.Headers(channel.Type, channel.APIKey, adapters.Protocol(protocol), false)
	for key, values := range adapterHeaders {
		if _, exists := headers[key]; !exists || len(headers[key]) == 0 {
			headers[key] = values
		}
	}
	return headers
}

func shouldSkipProviderAuthHeader(key string) bool {
	switch strings.ToLower(key) {
	case "x-api-key", "x-goog-api-key", "api-key":
		return true
	default:
		return false
	}
}

func upstreamURLForRequest(baseURL string, path string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	for _, prefix := range []string{"/v1", "/v1beta"} {
		if strings.HasSuffix(strings.ToLower(base), prefix) {
			if path == prefix {
				return base
			}
			if strings.HasPrefix(path, prefix+"/") {
				return base + strings.TrimPrefix(path, prefix)
			}
			return base[:len(base)-len(prefix)] + path
		}
	}
	return base + path
}

func prepareProviderRequest(channel *model.Channel, protocol proxyProtocol, request normalizedAIRequest) (preparedUpstreamRequest, error) {
	switch protocol {
	case protocolResponses:
		payload, err := openAIResponsesPayloadMap(request)
		if err != nil {
			return preparedUpstreamRequest{}, err
		}
		adapters.ApplyPayload(channel.Type, adapters.EndpointResponses, payload)
		body, err := json.Marshal(payload)
		if err != nil {
			return preparedUpstreamRequest{}, err
		}
		path, ok := adapters.Path(channel.Type, adapters.EndpointResponses, request.Model)
		if !ok {
			return preparedUpstreamRequest{}, fmt.Errorf("responses endpoint is not supported for channel type %q", channel.Type)
		}
		return preparedUpstreamRequest{
			Method: http.MethodPost,
			URL:    upstreamURLForRequest(channel.BaseURL, path),
			Body:   body,
			Header: adapters.Headers(channel.Type, channel.APIKey, adapters.Protocol(protocol), request.Stream),
		}, nil
	case protocolClaude:
		payload, err := claudeMessagesPayloadMap(request)
		if err != nil {
			return preparedUpstreamRequest{}, err
		}
		adapters.ApplyPayload(channel.Type, adapters.EndpointClaudeMessages, payload)
		body, err := json.Marshal(payload)
		if err != nil {
			return preparedUpstreamRequest{}, err
		}
		path, ok := adapters.Path(channel.Type, adapters.EndpointClaudeMessages, request.Model)
		if !ok {
			return preparedUpstreamRequest{}, fmt.Errorf("claude messages endpoint is not supported for channel type %q", channel.Type)
		}
		return preparedUpstreamRequest{
			Method: http.MethodPost,
			URL:    upstreamURLForRequest(channel.BaseURL, path),
			Body:   body,
			Header: adapters.Headers(channel.Type, channel.APIKey, adapters.Protocol(protocol), request.Stream),
		}, nil
	case protocolGemini:
		body, err := geminiGenerateContentPayload(request)
		if err != nil {
			return preparedUpstreamRequest{}, err
		}
		path, ok := adapters.Path(channel.Type, adapters.EndpointGeminiGenerate, request.Model)
		if !ok {
			return preparedUpstreamRequest{}, fmt.Errorf("gemini generate endpoint is not supported for channel type %q", channel.Type)
		}
		fullURL := upstreamURLForRequest(channel.BaseURL, path)
		if strings.TrimSpace(channel.APIKey) != "" {
			fullURL = withQueryParam(fullURL, "key", strings.TrimSpace(channel.APIKey))
		}
		return preparedUpstreamRequest{
			Method: http.MethodPost,
			URL:    fullURL,
			Body:   body,
			Header: jsonHeaders(),
		}, nil
	case protocolOpenAI:
		payload, err := openAIChatCompletionsPayloadMap(request)
		if err != nil {
			return preparedUpstreamRequest{}, err
		}
		adapters.ApplyPayload(channel.Type, adapters.EndpointChat, payload)
		body, err := json.Marshal(payload)
		if err != nil {
			return preparedUpstreamRequest{}, err
		}
		path, ok := adapters.Path(channel.Type, adapters.EndpointChat, request.Model)
		if !ok {
			return preparedUpstreamRequest{}, fmt.Errorf("chat endpoint is not supported for channel type %q", channel.Type)
		}
		return preparedUpstreamRequest{
			Method: http.MethodPost,
			URL:    upstreamURLForRequest(channel.BaseURL, path),
			Body:   body,
			Header: adapters.Headers(channel.Type, channel.APIKey, adapters.Protocol(protocol), request.Stream),
		}, nil
	default:
		return preparedUpstreamRequest{}, fmt.Errorf("unsupported upstream protocol: %s", protocol)
	}
}

func prepareOpenAIImageGenerationRequest(channel *model.Channel, upstreamModelName string, requestBody map[string]interface{}) (preparedUpstreamRequest, error) {
	upstreamModelName = strings.TrimSpace(upstreamModelName)
	if upstreamModelName == "" {
		return preparedUpstreamRequest{}, errors.New("model is required")
	}

	payload := make(map[string]interface{}, len(requestBody))
	for key, value := range requestBody {
		payload[key] = value
	}
	payload["model"] = upstreamModelName

	body, err := json.Marshal(payload)
	if err != nil {
		return preparedUpstreamRequest{}, err
	}
	adapters.ApplyPayload(channel.Type, adapters.EndpointImageGeneration, payload)
	body, err = json.Marshal(payload)
	if err != nil {
		return preparedUpstreamRequest{}, err
	}
	path, ok := adapters.Path(channel.Type, adapters.EndpointImageGeneration, upstreamModelName)
	if !ok {
		return preparedUpstreamRequest{}, fmt.Errorf("image generation endpoint is not supported for channel type %q", channel.Type)
	}
	return preparedUpstreamRequest{
		Method: http.MethodPost,
		URL:    upstreamURLForRequest(channel.BaseURL, path),
		Body:   body,
		Header: adapters.Headers(channel.Type, channel.APIKey, adapters.ProtocolOpenAI, false),
	}, nil
}

func prepareOpenAIImageEditRequest(channel *model.Channel, upstreamModelName string, form *multipart.Form) (preparedUpstreamRequest, error) {
	upstreamModelName = strings.TrimSpace(upstreamModelName)
	if upstreamModelName == "" {
		return preparedUpstreamRequest{}, errors.New("model is required")
	}
	if form == nil {
		return preparedUpstreamRequest{}, errors.New("multipart form is required")
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, values := range form.Value {
		if key == "model" {
			continue
		}
		for _, value := range values {
			if err := writer.WriteField(key, value); err != nil {
				return preparedUpstreamRequest{}, err
			}
		}
	}
	if err := writer.WriteField("model", upstreamModelName); err != nil {
		return preparedUpstreamRequest{}, err
	}
	for key, files := range form.File {
		for _, fileHeader := range files {
			source, err := fileHeader.Open()
			if err != nil {
				return preparedUpstreamRequest{}, err
			}
			part, err := writer.CreateFormFile(key, fileHeader.Filename)
			if err != nil {
				source.Close()
				return preparedUpstreamRequest{}, err
			}
			if _, err := io.Copy(part, source); err != nil {
				source.Close()
				return preparedUpstreamRequest{}, err
			}
			source.Close()
		}
	}
	if len(form.File["image"]) == 0 {
		return preparedUpstreamRequest{}, errors.New("image is required")
	}
	if err := writer.Close(); err != nil {
		return preparedUpstreamRequest{}, err
	}

	path, ok := adapters.Path(channel.Type, adapters.EndpointImageEdit, upstreamModelName)
	if !ok {
		return preparedUpstreamRequest{}, fmt.Errorf("image edit endpoint is not supported for channel type %q", channel.Type)
	}
	headers := adapters.Headers(channel.Type, channel.APIKey, adapters.ProtocolOpenAI, false)
	headers.Set("Content-Type", writer.FormDataContentType())
	return preparedUpstreamRequest{
		Method: http.MethodPost,
		URL:    upstreamURLForRequest(channel.BaseURL, path),
		Body:   body.Bytes(),
		Header: headers,
	}, nil
}

func prepareOpenAIVideoGenerationRequest(channel *model.Channel, upstreamModelName string, requestBody map[string]interface{}) (preparedUpstreamRequest, error) {
	upstreamModelName = strings.TrimSpace(upstreamModelName)
	if upstreamModelName == "" {
		return preparedUpstreamRequest{}, errors.New("model is required")
	}

	payload := make(map[string]interface{}, len(requestBody))
	for key, value := range requestBody {
		payload[key] = value
	}
	payload["model"] = upstreamModelName

	body, err := json.Marshal(payload)
	if err != nil {
		return preparedUpstreamRequest{}, err
	}
	path, ok := adapters.Path(channel.Type, adapters.EndpointVideoGeneration, upstreamModelName)
	if !ok {
		return preparedUpstreamRequest{}, fmt.Errorf("video generation endpoint is not supported for channel type %q", channel.Type)
	}
	return preparedUpstreamRequest{
		Method: http.MethodPost,
		URL:    upstreamURLForRequest(channel.BaseURL, path),
		Body:   body,
		Header: adapters.Headers(channel.Type, channel.APIKey, adapters.ProtocolOpenAIVideo, false),
	}, nil
}

func prepareVideoGenerationRequest(channel *model.Channel, protocol proxyProtocol, upstreamModelName string, requestBody map[string]interface{}) (preparedUpstreamRequest, error) {
	switch protocol {
	case protocolKling:
		return prepareKlingImageToVideoRequest(channel, upstreamModelName, requestBody)
	default:
		return prepareOpenAIVideoGenerationRequest(channel, upstreamModelName, requestBody)
	}
}

func prepareKlingImageToVideoRequest(channel *model.Channel, upstreamModelName string, requestBody map[string]interface{}) (preparedUpstreamRequest, error) {
	upstreamModelName = strings.TrimSpace(upstreamModelName)
	if upstreamModelName == "" {
		return preparedUpstreamRequest{}, errors.New("model is required")
	}

	payload := make(map[string]interface{}, len(requestBody)+4)
	for key, value := range requestBody {
		switch key {
		case "model", "model_name":
			continue
		case "size", "resolution":
			if _, exists := requestBody["aspect_ratio"]; !exists {
				if ratio := klingAspectRatioFromValue(value); ratio != "" {
					payload["aspect_ratio"] = ratio
				}
			}
		case "image_url":
			if _, exists := requestBody["image"]; !exists {
				payload["image"] = value
			}
		case "n":
			if _, exists := requestBody["num_videos"]; !exists {
				payload["num_videos"] = value
			}
		default:
			payload[key] = value
		}
	}
	payload["model_name"] = upstreamModelName
	if image := firstNonEmptyString(
		stringFromValue(requestBody["image"]),
		stringFromValue(requestBody["image_url"]),
		stringFromValue(requestBody["first_frame_image"]),
		stringFromValue(requestBody["firstFrameImage"]),
	); image != "" {
		payload["image"] = image
	}
	if _, exists := payload["duration"]; !exists {
		if seconds := videoDurationSecondsFromPayload(requestBody); seconds > 0 {
			payload["duration"] = strconv.Itoa(seconds)
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return preparedUpstreamRequest{}, err
	}
	return preparedUpstreamRequest{
		Method: http.MethodPost,
		URL:    upstreamURLForRequest(channel.BaseURL, "/v1/videos/image2video"),
		Body:   body,
		Header: adapters.Headers(channel.Type, channel.APIKey, adapters.ProtocolKling, false),
	}, nil
}

func klingAspectRatioFromValue(value interface{}) string {
	raw := strings.TrimSpace(stringFromValue(value))
	if raw == "" || strings.EqualFold(raw, "auto") {
		return ""
	}
	normalized := strings.ToLower(raw)
	switch normalized {
	case "16:9", "9:16", "1:1", "4:3", "3:4", "21:9":
		return normalized
	case "landscape":
		return "16:9"
	case "portrait":
		return "9:16"
	case "square":
		return "1:1"
	}
	if strings.Contains(normalized, ":") {
		return normalized
	}
	return raw
}

func videoTaskStatusPath(protocol proxyProtocol, upstreamTaskID string) string {
	escaped := url.PathEscape(strings.TrimSpace(upstreamTaskID))
	if protocol == protocolKling {
		return "/v1/videos/image2video/" + escaped
	}
	return "/v1/video/generations/" + escaped
}

func jsonHeaders() http.Header {
	headers := http.Header{}
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "application/json")
	return headers
}

func withQueryParam(rawURL, key, value string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		separator := "?"
		if strings.Contains(rawURL, "?") {
			separator = "&"
		}
		return rawURL + separator + url.QueryEscape(key) + "=" + url.QueryEscape(value)
	}
	query := parsed.Query()
	query.Set(key, value)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func channelProtocol(channelType string) proxyProtocol {
	return proxyProtocol(adapters.ProtocolFor(channelType))
}

func supportsOpenAIImageEndpoint(protocol proxyProtocol) bool {
	return protocol == protocolOpenAI || protocol == protocolResponses
}

func supportsOpenAIVideoEndpoint(protocol proxyProtocol) bool {
	return protocol == protocolOpenAIVideo
}

func supportsVideoEndpoint(protocol proxyProtocol) bool {
	return protocol == protocolOpenAIVideo || protocol == protocolKling
}

func videoRequestModelName(requestBody map[string]interface{}) string {
	return firstNonEmptyString(
		stringFromValue(requestBody["model"]),
		stringFromValue(requestBody["model_name"]),
		stringFromValue(requestBody["modelName"]),
	)
}

func normalizeProviderRequest(protocol proxyProtocol, path string, requestBody map[string]interface{}, modelName string) normalizedAIRequest {
	switch protocol {
	case protocolClaude:
		return normalizeClaudeRequest(requestBody, modelName)
	case protocolGemini:
		return normalizeGeminiRequest(requestBody, modelName)
	default:
		return normalizeOpenAIRequest(path, requestBody, modelName)
	}
}

func normalizeOpenAIRequest(path string, requestBody map[string]interface{}, modelName string) normalizedAIRequest {
	normalized := normalizedAIRequest{
		Model:       modelName,
		MaxTokens:   intFromRequest(requestBody, "max_tokens", "max_completion_tokens"),
		Temperature: floatPtrFromRequest(requestBody, "temperature"),
		Stream:      isStreamingRequest(requestBody),
		Tools:       interfaceSliceFromRequest(requestBody, "tools"),
		ToolChoice:  requestBody["tool_choice"],
	}
	if isResponsesPath(path) {
		input := normalizeResponsesInput(requestBody["input"])
		for _, item := range input {
			addNormalizedMessage(&normalized, responseInputRole(stringFromValue(item["role"])), contentToText(item["content"]), item["tool_calls"])
		}
		return normalized
	}
	if messages, ok := requestBody["messages"].([]interface{}); ok {
		for _, raw := range messages {
			item, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			addNormalizedMessage(&normalized, stringFromValue(item["role"]), contentToText(item["content"]), item["tool_calls"])
		}
		return normalized
	}
	if prompt := contentToText(requestBody["prompt"]); strings.TrimSpace(prompt) != "" {
		addNormalizedMessage(&normalized, "user", prompt, nil)
	}
	return normalized
}

func normalizeClaudeRequest(requestBody map[string]interface{}, modelName string) normalizedAIRequest {
	normalized := normalizedAIRequest{
		Model:       modelName,
		System:      contentToText(requestBody["system"]),
		MaxTokens:   intFromRequest(requestBody, "max_tokens"),
		Temperature: floatPtrFromRequest(requestBody, "temperature"),
		Stream:      isStreamingRequest(requestBody),
		Tools:       interfaceSliceFromRequest(requestBody, "tools"),
		ToolChoice:  requestBody["tool_choice"],
	}
	if messages, ok := requestBody["messages"].([]interface{}); ok {
		for _, raw := range messages {
			item, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			addNormalizedMessage(&normalized, stringFromValue(item["role"]), contentToText(item["content"]), item["tool_calls"])
		}
	}
	return normalized
}

func normalizeGeminiRequest(requestBody map[string]interface{}, modelName string) normalizedAIRequest {
	normalized := normalizedAIRequest{Model: modelName}
	if config, ok := requestBody["generationConfig"].(map[string]interface{}); ok {
		normalized.MaxTokens = intFromRequest(config, "maxOutputTokens")
		normalized.Temperature = floatPtrFromRequest(config, "temperature")
	}
	normalized.System = geminiSystemInstructionText(requestBody["systemInstruction"])
	if contents, ok := requestBody["contents"].([]interface{}); ok {
		for _, raw := range contents {
			item, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			role := "user"
			if strings.EqualFold(stringFromValue(item["role"]), "model") {
				role = "assistant"
			}
			addNormalizedMessage(&normalized, role, geminiPartsText(item["parts"]))
		}
	} else if text := contentToText(requestBody["contents"]); strings.TrimSpace(text) != "" {
		addNormalizedMessage(&normalized, "user", text)
	}
	return normalized
}

func addNormalizedMessage(request *normalizedAIRequest, role string, content string, toolCalls interface{}) {
	content = strings.TrimSpace(content)
	toolCallsSlice, _ := toolCalls.([]interface{})
	if content == "" && len(toolCallsSlice) == 0 {
		return
	}
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "system", "developer":
		if request.System != "" {
			request.System += "\n"
		}
		request.System += content
	case "assistant", "model":
		request.Messages = append(request.Messages, normalizedAIMessage{Role: "assistant", Content: content, ToolCalls: toolCallsSlice})
	default:
		request.Messages = append(request.Messages, normalizedAIMessage{Role: "user", Content: content, ToolCalls: toolCallsSlice})
	}
}

func openAIChatCompletionsPayload(request normalizedAIRequest) ([]byte, error) {
	payload, err := openAIChatCompletionsPayloadMap(request)
	if err != nil {
		return nil, err
	}
	return json.Marshal(payload)
}

func openAIChatCompletionsPayloadMap(request normalizedAIRequest) (map[string]interface{}, error) {
	messages := make([]map[string]interface{}, 0, len(request.Messages)+1)
	if strings.TrimSpace(request.System) != "" {
		messages = append(messages, map[string]interface{}{"role": "system", "content": request.System})
	}
	for _, message := range request.Messages {
		msg := map[string]interface{}{"role": message.Role, "content": message.Content}
		if len(message.ToolCalls) > 0 {
			msg["tool_calls"] = message.ToolCalls
		}
		messages = append(messages, msg)
	}
	if len(messages) == 0 {
		return nil, errors.New("messages are required")
	}
	payload := map[string]interface{}{
		"model":    request.Model,
		"messages": messages,
	}
	if request.MaxTokens > 0 {
		payload["max_tokens"] = request.MaxTokens
	}
	if request.Temperature != nil {
		payload["temperature"] = *request.Temperature
	}
	if request.Stream {
		payload["stream"] = true
	}
	if len(request.Tools) > 0 {
		payload["tools"] = request.Tools
	}
	if request.ToolChoice != nil {
		payload["tool_choice"] = request.ToolChoice
	}
	return payload, nil
}

func openAIResponsesPayload(request normalizedAIRequest) ([]byte, error) {
	payload, err := openAIResponsesPayloadMap(request)
	if err != nil {
		return nil, err
	}
	return json.Marshal(payload)
}

func openAIResponsesPayloadMap(request normalizedAIRequest) (map[string]interface{}, error) {
	input := make([]map[string]interface{}, 0, len(request.Messages)+1)
	if strings.TrimSpace(request.System) != "" {
		input = append(input, map[string]interface{}{"role": "system", "content": request.System})
	}
	for _, message := range request.Messages {
		msg := map[string]interface{}{"role": message.Role, "content": message.Content}
		if len(message.ToolCalls) > 0 {
			msg["tool_calls"] = message.ToolCalls
		}
		input = append(input, msg)
	}
	if len(input) == 0 {
		return nil, errors.New("input is required")
	}
	payload := map[string]interface{}{
		"model": request.Model,
		"input": input,
	}
	if request.MaxTokens > 0 {
		payload["max_output_tokens"] = request.MaxTokens
	}
	if request.Temperature != nil {
		payload["temperature"] = *request.Temperature
	}
	if request.Stream {
		payload["stream"] = true
	}
	if len(request.Tools) > 0 {
		payload["tools"] = request.Tools
	}
	if request.ToolChoice != nil {
		payload["tool_choice"] = request.ToolChoice
	}
	return payload, nil
}

func claudeMessagesPayload(request normalizedAIRequest) ([]byte, error) {
	payload, err := claudeMessagesPayloadMap(request)
	if err != nil {
		return nil, err
	}
	return json.Marshal(payload)
}

func claudeMessagesPayloadMap(request normalizedAIRequest) (map[string]interface{}, error) {
	messages := make([]map[string]interface{}, 0, len(request.Messages))
	for _, message := range request.Messages {
		role := "user"
		if message.Role == "assistant" {
			role = "assistant"
		}
		messages = append(messages, map[string]interface{}{"role": role, "content": message.Content})
	}
	if len(messages) == 0 {
		return nil, errors.New("messages are required")
	}
	maxTokens := request.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	payload := map[string]interface{}{
		"model":      request.Model,
		"max_tokens": maxTokens,
		"messages":   messages,
	}
	if strings.TrimSpace(request.System) != "" {
		payload["system"] = request.System
	}
	if request.Temperature != nil {
		payload["temperature"] = *request.Temperature
	}
	return payload, nil
}

func geminiGenerateContentPayload(request normalizedAIRequest) ([]byte, error) {
	contents := make([]map[string]interface{}, 0, len(request.Messages))
	for _, message := range request.Messages {
		role := "user"
		if message.Role == "assistant" {
			role = "model"
		}
		contents = append(contents, map[string]interface{}{
			"role":  role,
			"parts": []map[string]string{{"text": message.Content}},
		})
	}
	if len(contents) == 0 {
		return nil, errors.New("contents are required")
	}
	payload := map[string]interface{}{"contents": contents}
	if strings.TrimSpace(request.System) != "" {
		payload["systemInstruction"] = map[string]interface{}{
			"parts": []map[string]string{{"text": request.System}},
		}
	}
	generationConfig := map[string]interface{}{}
	if request.MaxTokens > 0 {
		generationConfig["maxOutputTokens"] = request.MaxTokens
	}
	if request.Temperature != nil {
		generationConfig["temperature"] = *request.Temperature
	}
	if len(generationConfig) > 0 {
		payload["generationConfig"] = generationConfig
	}
	return json.Marshal(payload)
}

func transformProviderResponse(body []byte, upstreamProtocol, clientProtocol proxyProtocol, responses bool, modelName string) ([]byte, error) {
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	data := providerResponseFromPayload(payload, upstreamProtocol)
	switch clientProtocol {
	case protocolOpenAI:
		if responses {
			return json.Marshal(openAIResponsesBody(data, modelName))
		}
		return json.Marshal(openAIChatBody(data, modelName))
	case protocolResponses:
		return json.Marshal(openAIResponsesBody(data, modelName))
	case protocolClaude:
		return json.Marshal(claudeResponseBody(data, modelName))
	case protocolGemini:
		return json.Marshal(geminiResponseBody(data, modelName))
	default:
		return nil, fmt.Errorf("unsupported client protocol: %s", clientProtocol)
	}
}

func providerResponseFromPayload(payload map[string]interface{}, protocol proxyProtocol) providerResponseData {
	switch protocol {
	case protocolClaude:
		return providerResponseFromClaude(payload)
	case protocolGemini:
		return providerResponseFromGemini(payload)
	default:
		return providerResponseFromOpenAI(payload)
	}
}

func providerResponseFromOpenAI(payload map[string]interface{}) providerResponseData {
	usage, _ := parseUsageTokens(payload)
	var toolCalls []interface{}
	if choices, ok := payload["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if message, ok := choice["message"].(map[string]interface{}); ok {
				toolCalls, _ = message["tool_calls"].([]interface{})
			}
		}
	}
	return providerResponseData{
		ID:                      stringFromValue(payload["id"]),
		Text:                    openAIResponseText(payload),
		ToolCalls:               toolCalls,
		InputTokens:             usage.InputTokens,
		OutputTokens:            usage.OutputTokens,
		CachedInputTokens:       usage.CachedInputTokens,
		CacheWriteInputTokens:   usage.CacheWriteInputTokens,
		CacheWrite1hInputTokens: usage.CacheWrite1hInputTokens,
		ImageInputTokens:        usage.ImageInputTokens,
		ImageOutputTokens:       usage.ImageOutputTokens,
		AudioInputTokens:        usage.AudioInputTokens,
		AudioOutputTokens:       usage.AudioOutputTokens,
	}
}

func providerResponseFromClaude(payload map[string]interface{}) providerResponseData {
	usage, _ := parseUsageTokens(payload)
	parts := []string{}
	if content, ok := payload["content"].([]interface{}); ok {
		for _, raw := range content {
			if item, ok := raw.(map[string]interface{}); ok {
				if text := contentToText(item); strings.TrimSpace(text) != "" {
					parts = append(parts, text)
				}
			}
		}
	}
	return providerResponseData{
		ID:                      stringFromValue(payload["id"]),
		Text:                    strings.TrimSpace(strings.Join(parts, "\n")),
		InputTokens:             usage.InputTokens,
		OutputTokens:            usage.OutputTokens,
		CachedInputTokens:       usage.CachedInputTokens,
		CacheWriteInputTokens:   usage.CacheWriteInputTokens,
		CacheWrite1hInputTokens: usage.CacheWrite1hInputTokens,
		ImageInputTokens:        usage.ImageInputTokens,
		ImageOutputTokens:       usage.ImageOutputTokens,
		AudioInputTokens:        usage.AudioInputTokens,
		AudioOutputTokens:       usage.AudioOutputTokens,
	}
}

func providerResponseFromGemini(payload map[string]interface{}) providerResponseData {
	usage, _ := parseUsageTokens(payload)
	parts := []string{}
	if candidates, ok := payload["candidates"].([]interface{}); ok {
		for _, rawCandidate := range candidates {
			candidate, ok := rawCandidate.(map[string]interface{})
			if !ok {
				continue
			}
			content, ok := candidate["content"].(map[string]interface{})
			if !ok {
				continue
			}
			if text := geminiPartsText(content["parts"]); strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
		}
	}
	return providerResponseData{
		Text:                    strings.TrimSpace(strings.Join(parts, "\n")),
		InputTokens:             usage.InputTokens,
		OutputTokens:            usage.OutputTokens,
		CachedInputTokens:       usage.CachedInputTokens,
		CacheWriteInputTokens:   usage.CacheWriteInputTokens,
		CacheWrite1hInputTokens: usage.CacheWrite1hInputTokens,
		ImageInputTokens:        usage.ImageInputTokens,
		ImageOutputTokens:       usage.ImageOutputTokens,
		AudioInputTokens:        usage.AudioInputTokens,
		AudioOutputTokens:       usage.AudioOutputTokens,
	}
}

func openAIChatBody(data providerResponseData, modelName string) map[string]interface{} {
	message := map[string]interface{}{
		"role":    "assistant",
		"content": data.Text,
	}
	finishReason := "stop"
	if len(data.ToolCalls) > 0 {
		message["tool_calls"] = data.ToolCalls
		finishReason = "tool_calls"
	}
	return map[string]interface{}{
		"id":      fallbackID(data.ID, "chatcmpl"),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   modelName,
		"choices": []map[string]interface{}{{
			"index":         0,
			"finish_reason": finishReason,
			"message":       message,
		}},
		"usage": openAIUsage(data),
	}
}

func openAIResponsesBody(data providerResponseData, modelName string) map[string]interface{} {
	content := []map[string]interface{}{{
		"type": "output_text",
		"text": data.Text,
	}}
	if len(data.ToolCalls) > 0 {
		for _, tc := range data.ToolCalls {
			if toolCall, ok := tc.(map[string]interface{}); ok {
				content = append(content, map[string]interface{}{
					"type": "tool_call",
					"tool_call": map[string]interface{}{
						"id":   toolCall["id"],
						"name": toolCall["function"].(map[string]interface{})["name"],
						"args": toolCall["function"].(map[string]interface{})["arguments"],
					},
				})
			}
		}
	}
	return map[string]interface{}{
		"id":          fallbackID(data.ID, "resp"),
		"object":      "response",
		"created_at":  time.Now().Unix(),
		"model":       modelName,
		"output_text": data.Text,
		"output": []map[string]interface{}{{
			"type":    "message",
			"role":    "assistant",
			"content": content,
		}},
		"usage": map[string]interface{}{
			"input_tokens":  data.InputTokens,
			"output_tokens": data.OutputTokens,
			"total_tokens":  data.InputTokens + data.OutputTokens,
			"input_tokens_details": map[string]interface{}{
				"cached_tokens": data.CachedInputTokens,
				"image_tokens":  data.ImageInputTokens,
				"audio_tokens":  data.AudioInputTokens,
			},
			"output_tokens_details": map[string]interface{}{
				"image_tokens": data.ImageOutputTokens,
				"audio_tokens": data.AudioOutputTokens,
			},
			"cache_creation_input_tokens":    data.CacheWriteInputTokens,
			"cache_creation_1h_input_tokens": data.CacheWrite1hInputTokens,
		},
	}
}

func claudeResponseBody(data providerResponseData, modelName string) map[string]interface{} {
	return map[string]interface{}{
		"id":            fallbackID(data.ID, "msg"),
		"type":          "message",
		"role":          "assistant",
		"model":         modelName,
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"content": []map[string]interface{}{{
			"type": "text",
			"text": data.Text,
		}},
		"usage": map[string]interface{}{
			"input_tokens":                   providerDataNonCacheInput(data),
			"cache_read_input_tokens":        data.CachedInputTokens,
			"cache_creation_input_tokens":    data.CacheWriteInputTokens,
			"cache_creation_1h_input_tokens": data.CacheWrite1hInputTokens,
			"output_tokens":                  data.OutputTokens,
		},
	}
}

func geminiResponseBody(data providerResponseData, modelName string) map[string]interface{} {
	return map[string]interface{}{
		"candidates": []map[string]interface{}{{
			"content": map[string]interface{}{
				"role":  "model",
				"parts": []map[string]string{{"text": data.Text}},
			},
			"finishReason": "STOP",
			"index":        0,
		}},
		"usageMetadata": map[string]interface{}{
			"promptTokenCount":        data.InputTokens,
			"cachedContentTokenCount": data.CachedInputTokens,
			"candidatesTokenCount":    data.OutputTokens,
			"totalTokenCount":         data.InputTokens + data.OutputTokens,
			"promptTokensDetails": map[string]interface{}{
				"imageTokens": data.ImageInputTokens,
				"audioTokens": data.AudioInputTokens,
			},
			"candidatesTokensDetails": map[string]interface{}{
				"imageTokens": data.ImageOutputTokens,
				"audioTokens": data.AudioOutputTokens,
			},
		},
		"modelVersion": modelName,
	}
}

func openAIUsage(data providerResponseData) map[string]interface{} {
	return map[string]interface{}{
		"prompt_tokens":     data.InputTokens,
		"completion_tokens": data.OutputTokens,
		"total_tokens":      data.InputTokens + data.OutputTokens,
		"prompt_tokens_details": map[string]interface{}{
			"cached_tokens": data.CachedInputTokens,
			"image_tokens":  data.ImageInputTokens,
			"audio_tokens":  data.AudioInputTokens,
		},
		"completion_tokens_details": map[string]interface{}{
			"image_tokens": data.ImageOutputTokens,
			"audio_tokens": data.AudioOutputTokens,
		},
		"cache_creation_input_tokens":    data.CacheWriteInputTokens,
		"cache_creation_1h_input_tokens": data.CacheWrite1hInputTokens,
	}
}

func providerDataNonCacheInput(data providerResponseData) int {
	tokens := data.InputTokens - data.CachedInputTokens - data.CacheWriteInputTokens - data.CacheWrite1hInputTokens
	if tokens < 0 {
		return 0
	}
	return tokens
}

func imageUsageTokenCounts(modelName string, requestBody map[string]interface{}, responseData map[string]interface{}) usageTokenCounts {
	if responseData != nil {
		if usage, ok := parseUsageTokens(responseData); ok {
			return usage
		}
		if usage, ok := parseImageTotalUsageTokens(responseData); ok {
			return usage
		}
	}
	return estimateImageUsageTokens(modelName, requestBody, responseData)
}

func parseImageTotalUsageTokens(responseData map[string]interface{}) (usageTokenCounts, bool) {
	usage, ok := responseData["usage"].(map[string]interface{})
	if !ok {
		return usageTokenCounts{}, false
	}

	inputTokens, inputOK := firstTokenValue(usage, "prompt_tokens", "input_tokens", "inputTokens")
	outputTokens, outputOK := firstTokenValue(usage, "completion_tokens", "output_tokens", "outputTokens", "image_tokens", "imageTokens")
	totalTokens, totalOK := firstTokenValue(usage, "total_tokens", "totalTokens")
	if !outputOK && totalOK {
		if inputOK {
			outputTokens = totalTokens - inputTokens
			if outputTokens < 0 {
				outputTokens = 0
			}
		} else {
			outputTokens = totalTokens
		}
		outputOK = true
	}
	if !inputOK && totalOK && outputOK {
		inputTokens = totalTokens - outputTokens
		if inputTokens < 0 {
			inputTokens = 0
		}
		inputOK = true
	}

	if !outputOK && !totalOK {
		return usageTokenCounts{}, false
	}
	return normalizeUsageTokenCounts(usageTokenCounts{
		InputTokens:          inputTokens,
		OutputTokens:         outputTokens,
		CachedInputTokens:    cachedInputTokensFromUsage(usage),
		CacheReadInputTokens: cachedInputTokensFromUsage(usage),
		ImageOutputTokens:    outputTokens,
	}), true
}

func estimateImageUsageTokens(modelName string, requestBody map[string]interface{}, responseData map[string]interface{}) usageTokenCounts {
	imageCount := imageCountFromPayloads(requestBody, responseData)
	return usageTokenCounts{
		InputTokens:       CountTokens(modelName, imageRequestText(requestBody)),
		OutputTokens:      imageCount * 1000000,
		ImageOutputTokens: imageCount * 1000000,
	}
}

func videoUsageTokenCounts(modelName string, requestBody map[string]interface{}, responseData map[string]interface{}, billingModel model.Model) (usageTokenCounts, int, string, bool) {
	if billingModel.QuotaType == model.QuotaTypeVideoResolutionDuration {
		usage := estimateVideoUsageTokens(modelName, requestBody, responseData)
		cost, err := calculateVideoBillingCost(requestBody, responseData, billingModel.VideoBillingConfig)
		if err != nil {
			return usageTokenCounts{}, http.StatusBadRequest, err.Error(), false
		}
		usage.BillableCost = cost
		return usage, 0, "", true
	}
	if billingModel.QuotaType != 1 && responseData != nil {
		if usage, ok := parseUsageTokens(responseData); ok {
			return usage, 0, "", true
		}
	}
	return estimateVideoUsageTokens(modelName, requestBody, responseData), 0, "", true
}

func estimateVideoUsageTokens(modelName string, requestBody map[string]interface{}, responseData map[string]interface{}) usageTokenCounts {
	videoCount := videoCountFromPayloads(requestBody, responseData)
	return usageTokenCounts{
		InputTokens:  CountTokens(modelName, videoRequestText(requestBody)),
		OutputTokens: videoCount * 1000000,
	}
}

func calculateVideoBillingCost(requestBody map[string]interface{}, responseData map[string]interface{}, config model.VideoBillingConfig) (decimal.Decimal, error) {
	config = model.NormalizeVideoBillingConfig(config)
	if len(config.Resolutions) == 0 {
		return decimal.Zero, errors.New("Video billing resolutions are not configured")
	}

	resolution := videoResolutionFromPayload(requestBody)
	resolutionConfig, ok := videoResolutionConfig(config.Resolutions, resolution)
	if !ok {
		return decimal.Zero, fmt.Errorf("Video resolution %q is not allowed", resolution)
	}

	seconds := videoDurationSecondsFromPayload(requestBody)
	if seconds <= 0 {
		return decimal.Zero, errors.New("Video duration is required")
	}

	durationPrice, ok := videoDurationPrice(resolutionConfig, seconds)
	if !ok {
		return decimal.Zero, fmt.Errorf("Video duration %d seconds is not allowed", seconds)
	}

	count := videoCountFromPayloads(requestBody, responseData)
	if count <= 0 {
		count = 1
	}
	return durationPrice.Mul(decimal.NewFromInt(int64(count))), nil
}

func videoResolutionConfig(prices []model.VideoResolutionPrice, resolution string) (model.VideoResolutionPrice, bool) {
	resolution = model.NormalizeVideoResolution(resolution)
	if resolution == "" {
		return model.VideoResolutionPrice{}, false
	}
	for _, item := range prices {
		if model.NormalizeVideoResolution(item.Resolution) == resolution {
			return item, true
		}
	}
	return model.VideoResolutionPrice{}, false
}

func videoDurationPrice(resolution model.VideoResolutionPrice, seconds int) (decimal.Decimal, bool) {
	if len(resolution.Durations) > 0 {
		for _, item := range resolution.Durations {
			if item.Seconds == seconds {
				return item.Price, true
			}
		}
		return decimal.Zero, false
	}
	if resolution.DurationUnitPrice.LessThan(decimal.Zero) || resolution.DurationUnitPrice.IsZero() {
		return decimal.Zero, false
	}
	return resolution.DurationUnitPrice.Mul(decimal.NewFromInt(int64(seconds))), true
}

func videoResolutionFromPayload(requestBody map[string]interface{}) string {
	for _, key := range []string{"size", "resolution", "quality", "aspect_ratio", "aspectRatio"} {
		if value, ok := requestBody[key].(string); ok {
			normalized := model.NormalizeVideoResolution(value)
			if normalized != "" && normalized != "auto" {
				return normalized
			}
		}
	}
	width := intFromRequest(requestBody, "width", "video_width")
	height := intFromRequest(requestBody, "height", "video_height")
	if width > 0 && height > 0 {
		return fmt.Sprintf("%dx%d", width, height)
	}
	return ""
}

func videoDurationSecondsFromPayload(requestBody map[string]interface{}) int {
	if value := intFromRequest(requestBody, "duration", "duration_seconds", "seconds", "video_duration"); value > 0 {
		return value
	}
	if duration, ok := requestBody["duration"].(string); ok {
		duration = strings.TrimSpace(strings.TrimSuffix(strings.ToLower(duration), "s"))
		if parsed, err := strconv.Atoi(duration); err == nil && parsed > 0 {
			return parsed
		}
	}
	return 0
}

func videoCountFromPayloads(requestBody map[string]interface{}, responseData map[string]interface{}) int {
	if responseData != nil {
		if data, ok := responseData["data"].([]interface{}); ok && len(data) > 0 {
			return len(data)
		}
	}
	for _, key := range []string{"n", "num_videos", "count"} {
		if value, ok := tokenValueAsInt(requestBody[key]); ok && value > 0 {
			return value
		}
	}
	return 1
}

func videoRequestText(requestBody map[string]interface{}) string {
	parts := []string{}
	for _, key := range []string{"prompt", "negative_prompt", "input", "first_frame_prompt"} {
		if text := contentToText(requestBody[key]); strings.TrimSpace(text) != "" {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func videoResponseText(responseData map[string]interface{}) string {
	parts := []string{}
	if data, ok := responseData["data"].([]interface{}); ok {
		for _, raw := range data {
			item, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			for _, key := range []string{"revised_prompt", "prompt", "status"} {
				if text := contentToText(item[key]); strings.TrimSpace(text) != "" {
					parts = append(parts, text)
				}
			}
		}
	}
	if errorValue, ok := responseData["error"]; ok {
		if text := contentToText(errorValue); strings.TrimSpace(text) != "" {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func imageCountFromPayloads(requestBody map[string]interface{}, responseData map[string]interface{}) int {
	if responseData != nil {
		if data, ok := responseData["data"].([]interface{}); ok && len(data) > 0 {
			return len(data)
		}
	}
	for _, key := range []string{"n", "num_images", "count"} {
		if value, ok := tokenValueAsInt(requestBody[key]); ok && value > 0 {
			return value
		}
	}
	return 1
}

func multipartFormValues(form *multipart.Form) map[string]interface{} {
	values := map[string]interface{}{}
	if form == nil {
		return values
	}
	for key, items := range form.Value {
		if len(items) == 1 {
			values[key] = items[0]
			continue
		}
		copied := make([]interface{}, 0, len(items))
		for _, item := range items {
			copied = append(copied, item)
		}
		values[key] = copied
	}
	return values
}

func imageRequestText(requestBody map[string]interface{}) string {
	parts := []string{}
	for _, key := range []string{"prompt", "negative_prompt", "input"} {
		if text := contentToText(requestBody[key]); strings.TrimSpace(text) != "" {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func imageResponseText(responseData map[string]interface{}) string {
	parts := []string{}
	if data, ok := responseData["data"].([]interface{}); ok {
		for _, raw := range data {
			item, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			for _, key := range []string{"revised_prompt", "prompt"} {
				if text := contentToText(item[key]); strings.TrimSpace(text) != "" {
					parts = append(parts, text)
				}
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func openAIResponseText(payload map[string]interface{}) string {
	if text, ok := payload["output_text"].(string); ok {
		return text
	}
	if choices, ok := payload["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if message, ok := choice["message"].(map[string]interface{}); ok {
				if text := contentToText(message["content"]); strings.TrimSpace(text) != "" {
					return text
				}
			}
			if text := contentToText(choice["text"]); strings.TrimSpace(text) != "" {
				return text
			}
		}
	}
	if output, ok := payload["output"].([]interface{}); ok {
		parts := []string{}
		for _, raw := range output {
			if item, ok := raw.(map[string]interface{}); ok {
				if text := contentToText(item["content"]); strings.TrimSpace(text) != "" {
					parts = append(parts, text)
				}
				if text := contentToText(item["text"]); strings.TrimSpace(text) != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	}
	return ""
}

func fallbackID(id, prefix string) string {
	id = strings.TrimSpace(id)
	if id != "" {
		return id
	}
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func geminiModelAction(value string) (string, string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", false
	}
	parts := strings.SplitN(value, ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	modelName := strings.TrimSpace(strings.TrimPrefix(parts[0], "models/"))
	action := strings.TrimSpace(parts[1])
	return modelName, action, modelName != "" && action != ""
}

func geminiSystemInstructionText(raw interface{}) string {
	if item, ok := raw.(map[string]interface{}); ok {
		if text := geminiPartsText(item["parts"]); strings.TrimSpace(text) != "" {
			return text
		}
	}
	return contentToText(raw)
}

func geminiPartsText(raw interface{}) string {
	parts, ok := raw.([]interface{})
	if !ok {
		return contentToText(raw)
	}
	texts := make([]string, 0, len(parts))
	for _, rawPart := range parts {
		if text := contentToText(rawPart); strings.TrimSpace(text) != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, "\n")
}

func intFromRequest(values map[string]interface{}, keys ...string) int {
	for _, key := range keys {
		if value, ok := tokenValueAsInt(values[key]); ok {
			return value
		}
	}
	return 0
}

func floatPtrFromRequest(values map[string]interface{}, keys ...string) *float64 {
	for _, key := range keys {
		switch value := values[key].(type) {
		case float64:
			return &value
		case json.Number:
			parsed, err := value.Float64()
			if err == nil {
				return &parsed
			}
		}
	}
	return nil
}

func stringFromValue(value interface{}) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func writeJSONProxyResponse(c *gin.Context, status int, body []byte) {
	c.Header("Content-Type", "application/json")
	c.Writer.WriteHeader(status)
	_, _ = c.Writer.Write(body)
}

func logUpstreamRequestFailure(c *gin.Context, channel *model.Channel, upstreamURL string, body []byte, err error) {
	log.Printf(
		"upstream request failed: method=%s path=%s channel_id=%d upstream_url=%s request_body=%s error=%v",
		requestMethodForLog(c),
		requestPathForLog(c),
		channel.ID,
		redactedUpstreamURL(upstreamURL),
		redactedRequestBodyPreview(body),
		err,
	)
}

func logUpstreamError(c *gin.Context, channel *model.Channel, upstreamURL string, status int, requestBody []byte, responseBody []byte) {
	log.Printf(
		"upstream returned error: method=%s path=%s channel_id=%d upstream_url=%s status=%d request_body=%s response_body=%q",
		requestMethodForLog(c),
		requestPathForLog(c),
		channel.ID,
		redactedUpstreamURL(upstreamURL),
		status,
		redactedRequestBodyPreview(requestBody),
		proxyBodyPreview(responseBody, 1000),
	)
}

func requestMethodForLog(c *gin.Context) string {
	if c == nil || c.Request == nil {
		return ""
	}
	return c.Request.Method
}

func requestPathForLog(c *gin.Context) string {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return ""
	}
	return c.Request.URL.RequestURI()
}

func redactedUpstreamURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	query := parsed.Query()
	for _, key := range []string{"key", "api_key", "access_token"} {
		if query.Has(key) {
			query.Set(key, "<redacted>")
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func isResponsesPath(path string) bool {
	path = strings.TrimRight(path, "/")
	return strings.HasSuffix(path, "/responses") || strings.HasSuffix(path, "/responses/compact")
}

func normalizeResponsesRequest(requestBody map[string]interface{}) {
	if input, exists := requestBody["input"]; exists {
		requestBody["input"] = normalizeResponsesInput(input)
	} else {
		if messages, ok := requestBody["messages"].([]interface{}); ok {
			requestBody["input"] = messagesToResponsesInput(messages)
		}
	}
	delete(requestBody, "messages")
}

func normalizeResponsesInput(input interface{}) []map[string]interface{} {
	switch value := input.(type) {
	case []interface{}:
		items := make([]map[string]interface{}, 0, len(value))
		for _, raw := range value {
			if item, ok := responseInputItem(raw); ok {
				items = append(items, item)
			}
		}
		return items
	case string:
		if strings.TrimSpace(value) == "" {
			return []map[string]interface{}{}
		}
		return []map[string]interface{}{{"role": "user", "content": value}}
	default:
		text := contentToText(value)
		if strings.TrimSpace(text) == "" {
			return []map[string]interface{}{}
		}
		return []map[string]interface{}{{"role": "user", "content": text}}
	}
}

func messagesToResponsesInput(messages []interface{}) []map[string]interface{} {
	items := make([]map[string]interface{}, 0, len(messages))
	for _, raw := range messages {
		item, ok := responseInputItem(raw)
		if !ok {
			continue
		}
		items = append(items, item)
	}
	return items
}

func responseInputItem(raw interface{}) (map[string]interface{}, bool) {
	item, ok := raw.(map[string]interface{})
	if !ok {
		text := contentToText(raw)
		if strings.TrimSpace(text) == "" {
			return nil, false
		}
		return map[string]interface{}{"role": "user", "content": text}, true
	}
	role, _ := item["role"].(string)
	content := contentToText(item["content"])
	if strings.TrimSpace(content) == "" {
		content = contentToText(item["text"])
	}
	if strings.TrimSpace(content) == "" {
		return nil, false
	}
	return map[string]interface{}{"role": responseInputRole(role), "content": content}, true
}

func responseInputRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "assistant":
		return "assistant"
	case "system":
		return "system"
	case "developer":
		return "developer"
	default:
		return "user"
	}
}

func contentToText(raw interface{}) string {
	switch value := raw.(type) {
	case string:
		return value
	case []interface{}:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			if text := contentToText(item); strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]interface{}:
		if text, ok := value["text"].(string); ok {
			return text
		}
		if text, ok := value["content"].(string); ok {
			return text
		}
	}
	return ""
}

func redactedRequestBodyPreview(body []byte) string {
	var payload interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return proxyBodyPreview(body, 1000)
	}
	redacted := redactRequestPayload(payload)
	encoded, err := json.Marshal(redacted)
	if err != nil {
		return "<unavailable>"
	}
	return proxyBodyPreview(encoded, 1000)
}

func redactRequestPayload(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		next := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			lowerKey := strings.ToLower(key)
			switch lowerKey {
			case "content", "text", "input", "prompt", "negative_prompt", "image", "mask":
				next[key] = redactTextLikeValue(item)
			default:
				next[key] = redactRequestPayload(item)
			}
		}
		return next
	case []interface{}:
		next := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			next = append(next, redactRequestPayload(item))
		}
		return next
	default:
		return typed
	}
}

func redactTextLikeValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case string:
		return fmt.Sprintf("<text len=%d>", len([]rune(typed)))
	case []interface{}:
		next := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			next = append(next, redactRequestPayload(item))
		}
		return next
	default:
		return redactRequestPayload(typed)
	}
}

func proxyBodyPreview(body []byte, limit int) string {
	preview := strings.TrimSpace(string(body))
	preview = strings.Join(strings.Fields(preview), " ")
	if limit <= 0 {
		limit = 1000
	}
	if len(preview) > limit {
		return preview[:limit] + "..."
	}
	return preview
}

func parseUsageTokens(responseData map[string]interface{}) (usageTokenCounts, bool) {
	usage, ok := responseData["usage"].(map[string]interface{})
	if !ok {
		if usageMetadata, ok := responseData["usageMetadata"].(map[string]interface{}); ok {
			inputTokens, inputOK := firstTokenValue(usageMetadata, "promptTokenCount", "prompt_token_count", "inputTokenCount", "input_token_count")
			outputTokens, outputOK := firstTokenValue(usageMetadata, "candidatesTokenCount", "candidates_token_count", "outputTokenCount", "output_token_count")
			cachedInputTokens, _ := firstTokenValue(usageMetadata, "cachedContentTokenCount", "cached_content_token_count", "cachedInputTokenCount", "cached_input_token_count")
			return normalizeUsageTokenCounts(usageTokenCounts{
				InputTokens:          inputTokens,
				OutputTokens:         outputTokens,
				CachedInputTokens:    cachedInputTokens,
				CacheReadInputTokens: cachedInputTokens,
			}), inputOK && outputOK
		}
		if response, ok := responseData["response"].(map[string]interface{}); ok {
			return parseUsageTokens(response)
		}
		return usageTokenCounts{}, false
	}

	inputTokens, inputOK := firstTokenValue(usage, "prompt_tokens", "input_tokens", "inputTokens")
	outputTokens, outputOK := firstTokenValue(usage, "completion_tokens", "output_tokens", "outputTokens")
	cacheReadTokens := cachedInputTokensFromUsage(usage)
	explicitCacheReadOK := false

	if explicitCacheReadTokens, ok := firstTokenValue(usage, "cache_read_input_tokens", "cacheReadInputTokens", "cache_read_tokens", "cacheReadTokens"); ok {
		cacheReadTokens = explicitCacheReadTokens
		explicitCacheReadOK = true
	}
	cacheWriteTokens, cacheWriteOK := cacheWriteInputTokensFromUsage(usage)
	cacheWrite1hTokens, cacheWrite1hOK := cacheWrite1hInputTokensFromUsage(usage)
	if explicitCacheReadOK || cacheWriteOK || cacheWrite1hOK {
		// OpenAI-compatible providers report prompt_tokens as the complete input,
		// including cached tokens. Anthropic-style providers report input_tokens
		// separately from cache reads/writes. Prefer the declared total when it is
		// available, then fall back to the field convention.
		cacheTokens := cacheReadTokens + cacheWriteTokens + cacheWrite1hTokens
		totalTokens, totalOK := firstTokenValue(usage, "total_tokens", "totalTokens")
		inputIncludesCache := totalOK && totalTokens == inputTokens+outputTokens
		if !totalOK {
			_, inputIncludesCache = firstTokenValue(usage, "prompt_tokens", "promptTokens")
		}
		if !inputIncludesCache {
			inputTokens += cacheTokens
		}
	}

	imageInputTokens := inputModalityTokensFromUsage(usage, "image")
	imageOutputTokens := outputModalityTokensFromUsage(usage, "image")
	audioInputTokens := inputModalityTokensFromUsage(usage, "audio")
	audioOutputTokens := outputModalityTokensFromUsage(usage, "audio")

	return normalizeUsageTokenCounts(usageTokenCounts{
		InputTokens:             inputTokens,
		OutputTokens:            outputTokens,
		CachedInputTokens:       cacheReadTokens,
		CacheReadInputTokens:    cacheReadTokens,
		CacheWriteInputTokens:   cacheWriteTokens,
		CacheWrite1hInputTokens: cacheWrite1hTokens,
		ImageInputTokens:        imageInputTokens,
		ImageOutputTokens:       imageOutputTokens,
		AudioInputTokens:        audioInputTokens,
		AudioOutputTokens:       audioOutputTokens,
	}), inputOK && outputOK
}

func cachedInputTokensFromUsage(usage map[string]interface{}) int {
	if cachedInputTokens, ok := firstTokenValue(usage, "cached_input_tokens", "cachedInputTokens", "cached_tokens", "cachedTokens"); ok {
		return cachedInputTokens
	}
	for _, key := range []string{"prompt_tokens_details", "promptTokensDetails", "input_tokens_details", "inputTokensDetails", "input_token_details", "inputTokenDetails"} {
		if details, ok := usage[key].(map[string]interface{}); ok {
			if cachedInputTokens, ok := firstTokenValue(details, "cached_tokens", "cachedTokens", "cached_input_tokens", "cachedInputTokens", "cache_read", "cacheRead", "cache_read_input_tokens", "cacheReadInputTokens"); ok {
				return cachedInputTokens
			}
		}
	}
	return 0
}

func cacheWriteInputTokensFromUsage(usage map[string]interface{}) (int, bool) {
	if tokens, ok := firstTokenValue(usage,
		"cache_write_input_tokens", "cacheWriteInputTokens",
		"cache_creation_input_tokens", "cacheCreationInputTokens",
		"cache_write_5m_input_tokens", "cacheWrite5mInputTokens",
		"cache_creation_5m_input_tokens", "cacheCreation5mInputTokens",
	); ok {
		return tokens, true
	}
	if cacheCreation, ok := usage["cache_creation"].(map[string]interface{}); ok {
		if tokens, ok := firstTokenValue(cacheCreation,
			"ephemeral_5m_input_tokens", "ephemeral5mInputTokens",
			"cache_write_5m_input_tokens", "cacheWrite5mInputTokens",
			"input_tokens", "inputTokens",
		); ok {
			return tokens, true
		}
	}
	return 0, false
}

func cacheWrite1hInputTokensFromUsage(usage map[string]interface{}) (int, bool) {
	if tokens, ok := firstTokenValue(usage,
		"cache_write_1h_input_tokens", "cacheWrite1hInputTokens",
		"cache_creation_1h_input_tokens", "cacheCreation1hInputTokens",
		"cache_write_1_hour_input_tokens", "cacheWrite1HourInputTokens",
		"cache_creation_1_hour_input_tokens", "cacheCreation1HourInputTokens",
	); ok {
		return tokens, true
	}
	if cacheCreation, ok := usage["cache_creation"].(map[string]interface{}); ok {
		if tokens, ok := firstTokenValue(cacheCreation,
			"ephemeral_1h_input_tokens", "ephemeral1hInputTokens",
			"ephemeral_1_hour_input_tokens", "ephemeral1HourInputTokens",
			"cache_write_1h_input_tokens", "cacheWrite1hInputTokens",
		); ok {
			return tokens, true
		}
	}
	return 0, false
}

func inputModalityTokensFromUsage(usage map[string]interface{}, modality string) int {
	keys := modalityTokenKeys(modality, "input")
	if tokens, ok := firstTokenValue(usage, keys...); ok {
		return tokens
	}
	if tokens, ok := firstTokenValueFromUsageDetails(usage, inputTokenDetailKeys(), modalityTokenKeys(modality, "")...); ok {
		return tokens
	}
	return 0
}

func outputModalityTokensFromUsage(usage map[string]interface{}, modality string) int {
	keys := modalityTokenKeys(modality, "output")
	if tokens, ok := firstTokenValue(usage, keys...); ok {
		return tokens
	}
	if tokens, ok := firstTokenValueFromUsageDetails(usage, outputTokenDetailKeys(), modalityTokenKeys(modality, "")...); ok {
		return tokens
	}
	return 0
}

func inputTokenDetailKeys() []string {
	return []string{"prompt_tokens_details", "promptTokensDetails", "input_tokens_details", "inputTokensDetails", "input_token_details", "inputTokenDetails"}
}

func outputTokenDetailKeys() []string {
	return []string{"completion_tokens_details", "completionTokensDetails", "output_tokens_details", "outputTokensDetails", "output_token_details", "outputTokenDetails"}
}

func modalityTokenKeys(modality string, direction string) []string {
	switch strings.ToLower(strings.TrimSpace(modality)) {
	case "image":
		switch direction {
		case "input":
			return []string{"image_input_tokens", "imageInputTokens", "input_image_tokens", "inputImageTokens", "prompt_image_tokens", "promptImageTokens"}
		case "output":
			return []string{"image_output_tokens", "imageOutputTokens", "output_image_tokens", "outputImageTokens", "completion_image_tokens", "completionImageTokens", "image_tokens", "imageTokens"}
		default:
			return []string{"image_tokens", "imageTokens", "image_input_tokens", "imageInputTokens", "input_image_tokens", "inputImageTokens"}
		}
	case "audio":
		switch direction {
		case "input":
			return []string{"audio_input_tokens", "audioInputTokens", "input_audio_tokens", "inputAudioTokens", "prompt_audio_tokens", "promptAudioTokens"}
		case "output":
			return []string{"audio_output_tokens", "audioOutputTokens", "output_audio_tokens", "outputAudioTokens", "completion_audio_tokens", "completionAudioTokens", "audio_tokens", "audioTokens"}
		default:
			return []string{"audio_tokens", "audioTokens", "audio_input_tokens", "audioInputTokens", "input_audio_tokens", "inputAudioTokens"}
		}
	default:
		return nil
	}
}

func firstTokenValueFromUsageDetails(usage map[string]interface{}, detailKeys []string, tokenKeys ...string) (int, bool) {
	for _, key := range detailKeys {
		if details, ok := usage[key].(map[string]interface{}); ok {
			if tokens, ok := firstTokenValue(details, tokenKeys...); ok {
				return tokens, true
			}
		}
	}
	return 0, false
}

func normalizeUsageTokenCounts(usage usageTokenCounts) usageTokenCounts {
	if usage.InputTokens < 0 {
		usage.InputTokens = 0
	}
	if usage.OutputTokens < 0 {
		usage.OutputTokens = 0
	}
	if usage.CachedInputTokens < 0 {
		usage.CachedInputTokens = 0
	}
	if usage.CacheReadInputTokens < 0 {
		usage.CacheReadInputTokens = 0
	}
	if usage.CacheWriteInputTokens < 0 {
		usage.CacheWriteInputTokens = 0
	}
	if usage.CacheWrite1hInputTokens < 0 {
		usage.CacheWrite1hInputTokens = 0
	}
	if usage.ImageInputTokens < 0 {
		usage.ImageInputTokens = 0
	}
	if usage.ImageOutputTokens < 0 {
		usage.ImageOutputTokens = 0
	}
	if usage.AudioInputTokens < 0 {
		usage.AudioInputTokens = 0
	}
	if usage.AudioOutputTokens < 0 {
		usage.AudioOutputTokens = 0
	}
	if usage.CacheReadInputTokens == 0 && usage.CachedInputTokens > 0 {
		usage.CacheReadInputTokens = usage.CachedInputTokens
	}
	if usage.CachedInputTokens == 0 && usage.CacheReadInputTokens > 0 {
		usage.CachedInputTokens = usage.CacheReadInputTokens
	}
	minInputTokens := usage.CacheReadInputTokens + usage.CacheWriteInputTokens + usage.CacheWrite1hInputTokens
	if usage.ImageInputTokens+usage.AudioInputTokens > minInputTokens {
		minInputTokens = usage.ImageInputTokens + usage.AudioInputTokens
	}
	if minInputTokens > usage.InputTokens {
		usage.InputTokens = minInputTokens
	}
	if usage.ImageOutputTokens+usage.AudioOutputTokens > usage.OutputTokens {
		usage.OutputTokens = usage.ImageOutputTokens + usage.AudioOutputTokens
	}
	return usage
}

func firstTokenValue(usage map[string]interface{}, keys ...string) (int, bool) {
	for _, key := range keys {
		if value, ok := tokenValueAsInt(usage[key]); ok {
			return value, true
		}
	}
	return 0, false
}

func tokenValueAsInt(value interface{}) (int, bool) {
	switch v := value.(type) {
	case float64:
		if v < 0 {
			return 0, false
		}
		return int(v), true
	case int:
		if v < 0 {
			return 0, false
		}
		return v, true
	case json.Number:
		parsed, err := v.Int64()
		if err != nil || parsed < 0 {
			return 0, false
		}
		return int(parsed), true
	default:
		return 0, false
	}
}

func shouldSkipProxyHeader(key string) bool {
	switch strings.ToLower(key) {
	case "authorization", "connection", "content-length", "host", "keep-alive", "proxy-authenticate",
		"proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func shouldSkipProxyResponseHeader(key string, streaming bool) bool {
	switch strings.ToLower(key) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer",
		"transfer-encoding", "upgrade":
		return true
	case "content-length":
		return streaming
	default:
		return false
	}
}

func interfaceSliceFromRequest(requestBody map[string]interface{}, key string) []interface{} {
	if value, ok := requestBody[key].([]interface{}); ok {
		return value
	}
	return nil
}
