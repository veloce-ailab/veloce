package service

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const (
	advancedChatModeChat        = "chat"
	advancedChatModeAssistant   = "assistant"
	advancedChatModeAgentGroup  = "agent_group"
	advancedChatMaxToolRounds   = 8
	assistantMaxToolRounds      = 20
	assistantModelMaxRetries    = 10
	assistantModelRetryDelay    = 500 * time.Millisecond
	assistantModelRetryMaxDelay = 30 * time.Second
	advancedChatRequestTimeout  = 120 * time.Second
)

var toolNameUnsafeChars = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

type advancedChatCompletionInput struct {
	SessionID                string                          `json:"session_id"`
	Title                    string                          `json:"title"`
	ModelName                string                          `json:"model"`
	UserChannelID            uint                            `json:"user_channel_id"`
	Messages                 []advancedChatCompletionMessage `json:"messages"`
	Mode                     string                          `json:"mode"`
	AgentID                  string                          `json:"agent_id"`
	AgentGroupID             string                          `json:"agent_group_id"`
	SkillIDs                 []string                        `json:"skill_ids"`
	MCPServerIDs             []string                        `json:"mcp_server_ids"`
	KnowledgeBaseIDs         []string                        `json:"knowledge_base_ids"`
	ConnectorDeviceID        string                          `json:"connector_device_id"`
	ConnectorWorkspacePath   string                          `json:"connector_workspace_path"`
	CloudSandboxID           string                          `json:"cloud_sandbox_id"`
	ConnectorAutoApprove     bool                            `json:"connector_auto_approve"`
	ConnectorApprovalMode    string                          `json:"connector_approval_mode"`
	ConnectorCommandPrefixes []string                        `json:"connector_command_prefixes"`
	MaxTokens                int                             `json:"max_tokens"`
	Temperature              *float64                        `json:"temperature"`
	ReasoningEffort          string                          `json:"reasoning_effort"`
	AutoCompressContext      bool                            `json:"auto_compress_context"`
	DisabledToolGroups       []string                        `json:"disabled_tool_groups"`
	Stream                   bool                            `json:"stream"`
	ChargeBalance            bool                            `json:"-"`
}

type advancedChatCompletionMessage struct {
	ID        string                           `json:"id,omitempty"`
	Role      string                           `json:"role"`
	Content   string                           `json:"content"`
	Parts     []advancedChatContentPart        `json:"content_parts,omitempty"`
	ToolCalls []advancedChatCompletionToolCall `json:"tool_calls,omitempty"`
}

type advancedChatCompletionResponse struct {
	Message         advancedChatCompletionMessage    `json:"message"`
	Cost            decimal.Decimal                  `json:"cost"`
	ToolCalls       int                              `json:"tool_calls"`
	ToolCallDetails []advancedChatCompletionToolCall `json:"tool_call_details,omitempty"`
}

type advancedChatCompletionToolCall struct {
	ID        string                 `json:"id,omitempty"`
	Round     int                    `json:"round,omitempty"`
	Name      string                 `json:"name"`
	Server    string                 `json:"server,omitempty"`
	Tool      string                 `json:"tool,omitempty"`
	Status    string                 `json:"status"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
	Result    string                 `json:"result,omitempty"`
}

type mcpToolBinding struct {
	Server AdvancedChatMCPServer
	Client mcpToolClient
	Tool   mcpTool
}

type advancedChatRuntimeSkill struct {
	ID          string
	Name        string
	Description string
	Source      string
}

type advancedChatSSEWriter struct {
	c       *gin.Context
	flusher http.Flusher
}

func newAdvancedChatSSEWriter(c *gin.Context) (*advancedChatSSEWriter, bool) {
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return nil, false
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)
	flusher.Flush()
	return &advancedChatSSEWriter{c: c, flusher: flusher}, true
}

func (writer *advancedChatSSEWriter) send(event string, payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer.c.Writer, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return err
	}
	writer.flusher.Flush()
	return nil
}

func (writer *advancedChatSSEWriter) sendError(message string) {
	_ = writer.send("error", gin.H{"error": message})
}

func (api *advancedChatAPI) completeChat(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	var input advancedChatCompletionInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if personalCompanyInternalSession(user.ID, input.SessionID) {
		c.JSON(http.StatusConflict, gin.H{"error": "Internal Studio sessions are immutable"})
		return
	}
	messages := normalizeAdvancedChatCompletionMessages(input.Messages)
	if len(messages) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Messages are required"})
		return
	}
	mode := normalizeAdvancedChatCompletionMode(input.Mode)
	modelName := strings.TrimSpace(input.ModelName)
	if modelName == "" && mode != advancedChatModeAgentGroup {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Model is required"})
		return
	}
	if mode == advancedChatModeAssistant || mode == advancedChatModeAgentGroup {
		if !advancedChatAssistantModeEnabled() {
			c.JSON(http.StatusForbidden, gin.H{"error": "Assistant mode is disabled"})
			return
		}
		api.startAssistantCompletionRun(c, user, input, messages, modelName)
		return
	}
	maxToolRounds := advancedChatCompletionMaxToolRounds(mode)
	if strings.TrimSpace(input.AgentID) == "" {
		input.AgentID = advancedChatDefaultAgentID
	}
	input.ConnectorDeviceID = ""
	input.ConnectorWorkspacePath = ""
	input.CloudSandboxID = ""
	input.ConnectorAutoApprove = false
	input.ConnectorApprovalMode = advancedChatConnectorApprovalManual
	input.ConnectorCommandPrefixes = nil

	agent, err := loadAdvancedChatAgent(user.ID, input.AgentID)
	if err != nil {
		status := http.StatusInternalServerError
		message := "Failed to load agent"
		if errors.Is(err, gorm.ErrRecordNotFound) {
			status = http.StatusBadRequest
			message = "Agent not found"
		}
		c.JSON(status, gin.H{"error": message})
		return
	}
	if input.UserChannelID == 0 && agent != nil && agent.UserChannelID > 0 {
		input.UserChannelID = agent.UserChannelID
	}
	skillIDs := input.SkillIDs
	mcpServerIDs := input.MCPServerIDs
	if agent != nil {
		skillIDs = uniqueStringsLocal(append(decodeStringList(agent.SkillIDs), skillIDs...))
		mcpServerIDs = uniqueStringsLocal(append(decodeStringList(agent.MCPServerIDs), mcpServerIDs...))
	}
	sessionKnowledgeBaseIDs, valid := normalizeAdvancedChatKnowledgeBaseIDs(c, user.ID, input.KnowledgeBaseIDs)
	if !valid {
		return
	}
	input.KnowledgeBaseIDs = sessionKnowledgeBaseIDs
	knowledgeBaseIDs := sessionKnowledgeBaseIDs
	if agent != nil {
		knowledgeBaseIDs = uniqueStringsLocal(append(decodeStringList(agent.KnowledgeBaseIDs), knowledgeBaseIDs...))
	}
	knowledgeContext, err := advancedChatKnowledgeContext(c.Request.Context(), c, user, knowledgeBaseIDs, messages)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	skills, err := loadAdvancedChatSkills(user.ID, skillIDs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load skills"})
		return
	}
	if len(skills) != len(uniqueStringsLocal(skillIDs)) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Unknown skill"})
		return
	}

	serverIDs := uniqueStringsLocal(append(mcpServerIDs, skillMCPIDs(skills)...))
	servers, err := loadAdvancedChatMCPServersForCall(user.ID, serverIDs)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	persistedSessionID, persistedAssistantMessageID, status, message, err := createPersistedAdvancedChatCompletionSession(user.ID, input, messages, mode, modelName)
	if err != nil {
		c.JSON(status, gin.H{"error": message})
		return
	}

	var streamWriter *advancedChatSSEWriter
	if input.Stream {
		writer, ok := newAdvancedChatSSEWriter(c)
		if !ok {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Streaming is not supported by this server"})
			return
		}
		streamWriter = writer
		if err := streamWriter.send("status", gin.H{"message": "stream_started"}); err != nil {
			return
		}
		if mode == advancedChatModeAssistant {
			if err := streamWriter.send("status", gin.H{"message": "assistant_started"}); err != nil {
				return
			}
		}
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), advancedChatCompletionTimeout(mode))
	defer cancel()
	if streamWriter != nil {
		if err := streamWriter.send("status", gin.H{"message": "loading_tools"}); err != nil {
			return
		}
	}
	tools, bindings, err := listAdvancedChatMCPTools(ctx, user.ID, "", nil, servers)
	if err != nil {
		message := "Failed to load MCP tools: " + err.Error()
		if streamWriter != nil {
			streamWriter.sendError(message)
		} else {
			c.JSON(http.StatusBadGateway, gin.H{"error": message})
		}
		return
	}

	systemPrompt := buildAdvancedChatCompletionSystemPrompt(agent, skills, nil, mode)
	if strings.TrimSpace(knowledgeContext) != "" {
		if strings.TrimSpace(systemPrompt) == "" {
			systemPrompt = knowledgeContext
		} else {
			systemPrompt = strings.Join([]string{systemPrompt, knowledgeContext}, "\n\n")
		}
	}
	input.DisabledToolGroups = normalizeAdvancedChatDisabledToolGroups(input.DisabledToolGroups)
	extension, err := BuildAdvancedChatRuntimeExtension(ctx, AdvancedChatRuntimeContext{
		UserID:             user.ID,
		Mode:               mode,
		AgentID:            input.AgentID,
		SessionID:          persistedSessionID,
		DisabledToolGroups: input.DisabledToolGroups,
	})
	if err != nil {
		message := "Failed to load assistant extensions: " + err.Error()
		if streamWriter != nil {
			streamWriter.sendError(message)
		} else {
			c.JSON(http.StatusBadGateway, gin.H{"error": message})
		}
		return
	}
	if strings.TrimSpace(extension.SystemPrompt) != "" {
		if strings.TrimSpace(systemPrompt) == "" {
			systemPrompt = extension.SystemPrompt
		} else {
			systemPrompt = strings.Join([]string{systemPrompt, extension.SystemPrompt}, "\n\n")
		}
	}
	tools = append(tools, extension.Tools...)
	tools = filterAdvancedChatToolsByDisabledGroups(tools, input.DisabledToolGroups)
	presetMessages := []AdvancedChatAgentPresetMessage{}
	if agent != nil {
		presetMessages = agent.Presets
	}
	executorMessages := make([]ChatExecutorMessage, 0, len(presetMessages)+len(messages)+maxToolRounds*2)
	executorMessages = append(executorMessages, advancedChatPresetExecutorMessages(presetMessages)...)
	modelMessages := messages
	if input.AutoCompressContext { modelMessages = compressAdvancedChatMessages(modelMessages) }
	for _, message := range modelMessages {
		executorMessages = append(executorMessages, advancedChatExecutorMessage(user.ID, message))
	}

	totalCost := decimal.Zero
	totalToolCalls := 0
	toolCallDetails := []advancedChatCompletionToolCall{}
	var lastContent string
	for round := 0; round < maxToolRounds; round++ {
		if ctx.Err() != nil {
			return
		}
		if streamWriter != nil {
			if err := streamWriter.send("status", gin.H{"message": "model_round", "round": round + 1, "mode": mode}); err != nil {
				return
			}
		}
		streamedText := false
		result, err := ExecuteServerChatCompletion(c, user, ChatExecutorRequest{
			ModelName:       modelName,
			UserChannelID:   input.UserChannelID,
			Messages:        executorMessages,
			System:          systemPrompt,
			Tools:           tools,
			MaxTokens:       normalizeAdvancedChatMaxTokens(input.MaxTokens),
			Temperature:     normalizeAdvancedChatTemperature(input.Temperature),
			ReasoningEffort: normalizeAdvancedChatReasoningEffort(input.ReasoningEffort),
			Stream:          streamWriter != nil,
			OnTextDelta: func(delta string) error {
				if streamWriter == nil || delta == "" {
					return nil
				}
				streamedText = true
				return streamWriter.send("text", gin.H{"delta": delta})
			},
		})
		if err != nil {
			if streamWriter != nil {
				if ctx.Err() == nil {
					message := errorMessageFromAdvancedChatCompletion(err)
					failPersistedAdvancedChatCompletionMessage(persistedSessionID, persistedAssistantMessageID, user.ID, message)
					streamWriter.sendError(message)
				}
			} else {
				failPersistedAdvancedChatCompletionMessage(persistedSessionID, persistedAssistantMessageID, user.ID, errorMessageFromAdvancedChatCompletion(err))
				writeAdvancedChatCompletionError(c, err)
			}
			return
		}
		totalCost = totalCost.Add(result.Cost)
		lastContent = result.Content
		if streamWriter != nil && !streamedText && strings.TrimSpace(result.Content) != "" {
			if persistedAssistantMessageID != "" {
				if err := appendAdvancedChatAssistantContent(persistedAssistantMessageID, user.ID, result.Content, 1); err != nil {
					streamWriter.sendError(err.Error())
					return
				}
			}
			if err := streamWriter.send("text", gin.H{"delta": result.Content, "round": 1}); err != nil {
				return
			}
		}
		if len(result.ToolCalls) == 0 {
			response := advancedChatCompletionResponse{
				Message:         advancedChatCompletionMessage{ID: persistedAssistantMessageID, Role: "assistant", Content: result.Content, Parts: normalizeAdvancedChatContentParts(nil, result.Content)},
				Cost:            totalCost,
				ToolCalls:       totalToolCalls,
				ToolCallDetails: toolCallDetails,
			}
			finishPersistedAdvancedChatCompletionMessage(persistedSessionID, persistedAssistantMessageID, user.ID, response)
			if streamWriter != nil {
				_ = streamWriter.send("done", response)
			} else {
				c.JSON(http.StatusOK, response)
			}
			return
		}

		totalToolCalls += len(result.ToolCalls)
		executorMessages = append(executorMessages, ChatExecutorMessage{
			Role:      "assistant",
			Content:   result.Content,
			ToolCalls: normalizeAssistantToolCalls(result.AssistantMessage),
		})
		for _, toolCall := range result.ToolCalls {
			binding, exists := bindings[toolCall.Name]
			extensionExists := AdvancedChatToolHandlerExists(toolCall.Name) &&
				!advancedChatToolGroupDisabled(input.DisabledToolGroups, advancedChatToolGroupForTool(toolCall.Name))
			detail := advancedChatCompletionToolCall{ID: toolCall.ID, Round: round + 1, Name: toolCall.Name, Status: "running"}
			if exists {
				detail.Server = binding.Server.Name
				detail.Tool = binding.Tool.Name
			} else if extensionExists {
				detail.Server = "agent chat"
				detail.Tool = toolCall.Name
			}
			arguments, argumentsErr := parseToolArguments(toolCall.Arguments)
			if argumentsErr == nil {
				detail.Arguments = arguments
			}
			if streamWriter != nil {
				if err := streamWriter.send("tool_call", detail); err != nil {
					return
				}
			}
			_ = mergeAdvancedChatMessageToolCall(persistedAssistantMessageID, user.ID, detail)
			detail.Status = "missing"
			toolResultText := "Tool not found: " + toolCall.Name
			if exists {
				detail.Server = binding.Server.Name
				detail.Tool = binding.Tool.Name
				if argumentsErr != nil {
					detail.Status = "invalid_arguments"
					toolResultText = "Invalid tool arguments: " + argumentsErr.Error()
				} else {
					toolResult, err := binding.Client.callTool(ctx, binding.Tool.Name, arguments)
					if err != nil {
						detail.Status = "error"
						toolResultText = "Tool call failed: " + err.Error()
					} else {
						detail.Status = "ok"
						toolResultText = toolResult.Text
						if toolResult.IsError {
							detail.Status = "error"
							toolResultText = "Tool returned an error: " + toolResultText
						}
					}
				}
			} else if extensionExists {
				detail.Server = "agent chat"
				detail.Tool = toolCall.Name
				if argumentsErr != nil {
					detail.Status = "invalid_arguments"
					toolResultText = "Invalid tool arguments: " + argumentsErr.Error()
				} else {
					toolResult, err := HandleAdvancedChatToolCall(ctx, AdvancedChatToolCallInput{
						UserID:       user.ID,
						Mode:         mode,
						AgentID:      input.AgentID,
						AgentGroupID: input.AgentGroupID,
						SessionID:    persistedSessionID,
						Name:         toolCall.Name,
						Arguments:    arguments,
					})
					if err != nil {
						detail.Status = "error"
						toolResultText = "Tool call failed: " + err.Error()
					} else {
						detail.Status = "ok"
						toolResultText = toolResult
					}
				}
			}
			detail.Result = truncateToolResult(toolResultText)
			toolCallDetails = append(toolCallDetails, detail)
			_ = mergeAdvancedChatMessageToolCall(persistedAssistantMessageID, user.ID, detail)
			if streamWriter != nil {
				if ctx.Err() != nil {
					return
				}
				if err := streamWriter.send("tool_call", detail); err != nil {
					return
				}
			}
			executorMessages = append(executorMessages, ChatExecutorMessage{
				Role:       "tool",
				Content:    truncateToolResult(toolResultText),
				ToolCallID: toolCall.ID,
				Name:       toolCall.Name,
			})
		}
	}

	response := advancedChatCompletionResponse{
		Message:         advancedChatCompletionMessage{ID: persistedAssistantMessageID, Role: "assistant", Content: strings.TrimSpace(lastContent), Parts: normalizeAdvancedChatContentParts(nil, strings.TrimSpace(lastContent))},
		Cost:            totalCost,
		ToolCalls:       totalToolCalls,
		ToolCallDetails: toolCallDetails,
	}
	finishPersistedAdvancedChatCompletionMessage(persistedSessionID, persistedAssistantMessageID, user.ID, response)
	if streamWriter != nil {
		_ = streamWriter.send("done", response)
		return
	}
	c.JSON(http.StatusOK, response)
}

func normalizeAdvancedChatCompletionMessages(input []advancedChatCompletionMessage) []advancedChatCompletionMessage {
	messages := make([]advancedChatCompletionMessage, 0, len(input))
	for _, item := range input {
		role := strings.ToLower(strings.TrimSpace(item.Role))
		if role != "assistant" {
			role = "user"
		}
		content := strings.TrimSpace(item.Content)
		if content == "" && len(item.ToolCalls) == 0 {
			continue
		}
		messages = append(messages, advancedChatCompletionMessage{
			ID:        normalizeAdvancedChatSessionID(item.ID),
			Role:      role,
			Content:   content,
			Parts:     normalizeAdvancedChatContentParts(item.Parts, content),
			ToolCalls: item.ToolCalls,
		})
	}
	if len(messages) > 50 {
		return messages[len(messages)-50:]
	}
	return messages
}

const advancedChatContextCompressionChars = 48000

func compressAdvancedChatMessages(messages []advancedChatCompletionMessage) []advancedChatCompletionMessage {
	total := 0
	for _, message := range messages { total += len([]rune(message.Content)) }
	if total <= advancedChatContextCompressionChars || len(messages) <= 8 { return messages }
	keep := 12
	dropped := messages[:len(messages)-keep]
	parts := make([]string, 0, len(dropped))
	for _, message := range dropped {
		content := strings.Join(strings.Fields(message.Content), " ")
		if len([]rune(content)) > 240 { content = string([]rune(content)[:240]) + "..." }
		if content != "" { parts = append(parts, message.Role+": "+content) }
	}
	summary := "[Earlier conversation compressed for context]\n" + strings.Join(parts, "\n")
	return append([]advancedChatCompletionMessage{{Role: "assistant", Content: summary}}, messages[len(messages)-keep:]...)
}

func loadAdvancedChatAgent(userID uint, rawID string) (*AdvancedChatAgent, error) {
	id := strings.TrimSpace(rawID)
	if id == "" {
		return nil, nil
	}
	if id == advancedChatDefaultAgentID {
		return ensureAdvancedChatDefaultAgent(userID)
	}
	var agent AdvancedChatAgent
	if err := model.DB.Where("id = ? AND user_id = ?", id, userID).First(&agent).Error; err != nil {
		return nil, err
	}
	hydrateAdvancedChatAgentLists(&agent)
	return &agent, nil
}

func loadAdvancedChatSkills(userID uint, rawIDs []string) ([]advancedChatRuntimeSkill, error) {
	ids := uniqueStringsLocal(rawIDs)
	if len(ids) == 0 {
		return []advancedChatRuntimeSkill{}, nil
	}
	var packaged []AdvancedChatPackagedSkill
	if err := model.DB.Where("user_id = ? AND enabled = ? AND id IN ?", userID, true, ids).Find(&packaged).Error; err != nil {
		return nil, err
	}
	byID := map[string]AdvancedChatPackagedSkill{}
	for _, skill := range packaged {
		byID[skill.ID] = skill
	}
	skills := make([]advancedChatRuntimeSkill, 0, len(packaged))
	for _, id := range ids {
		if skill, ok := byID[id]; ok {
			skills = append(skills, advancedChatRuntimeSkill{
				ID:          skill.ID,
				Name:        skill.Name,
				Description: skill.Description,
				Source:      skill.Source,
			})
		}
	}
	return skills, nil
}

func skillMCPIDs(skills []advancedChatRuntimeSkill) []string {
	return []string{}
}

func loadAdvancedChatMCPServersForCall(userID uint, ids []string) ([]AdvancedChatMCPServer, error) {
	if len(ids) == 0 {
		return []AdvancedChatMCPServer{}, nil
	}
	available := map[string]AdvancedChatMCPServer{}
	for _, server := range advancedChatBuiltinMCPServers(true) {
		if server.Enabled {
			available[server.ID] = server
		}
	}
	for _, server := range advancedChatCustomMCPServersWithHeaders(userID) {
		if server.Enabled {
			available[server.ID] = server
		}
	}
	result := make([]AdvancedChatMCPServer, 0, len(ids))
	for _, id := range ids {
		server, ok := available[id]
		if !ok {
			return nil, fmt.Errorf("unknown or disabled MCP server: %s", id)
		}
		result = append(result, server)
	}
	return result, nil
}

func listAdvancedChatMCPTools(ctx context.Context, userID uint, runID string, connectorDevice *AdvancedChatConnectorDevice, servers []AdvancedChatMCPServer) ([]ChatExecutorTool, map[string]mcpToolBinding, error) {
	tools := []ChatExecutorTool{}
	bindings := map[string]mcpToolBinding{}
	for _, server := range servers {
		var client mcpToolClient
		switch normalizeMCPServerType(server.Type) {
		case advancedChatMCPTypeConnector:
			client = newConnectorMCPClient(userID, runID, connectorDevice, server)
		default:
			client = newMCPClient(server.URL, parseMCPHeaders(server.Headers))
		}
		serverTools, err := client.listTools(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", server.Name, err)
		}
		for _, tool := range serverTools {
			name := uniqueOpenAIToolName(bindings, server.ID, tool.Name)
			schema := tool.InputSchema
			if schema == nil {
				schema = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
			}
			tools = append(tools, ChatExecutorTool{Name: name, Description: toolDescription(server, tool), Schema: schema})
			bindings[name] = mcpToolBinding{Server: server, Client: client, Tool: tool}
		}
	}
	return tools, bindings, nil
}

func uniqueOpenAIToolName(existing map[string]mcpToolBinding, serverID, toolName string) string {
	base := sanitizeOpenAIToolName("mcp_" + shortToolNamePart(serverID) + "__" + toolName)
	if base == "" {
		base = "mcp_tool"
	}
	if len(base) > 64 {
		digest := sha1.Sum([]byte(base))
		base = strings.TrimRight(base[:48], "_-") + "_" + hex.EncodeToString(digest[:4])
	}
	name := base
	for i := 2; ; i++ {
		if _, exists := existing[name]; !exists {
			return name
		}
		suffix := "_" + strconv.Itoa(i)
		maxBase := 64 - len(suffix)
		name = strings.TrimRight(base[:minInt(len(base), maxBase)], "_-") + suffix
	}
}

func shortToolNamePart(value string) string {
	value = sanitizeOpenAIToolName(value)
	if len(value) > 16 {
		return value[:16]
	}
	return value
}

func sanitizeOpenAIToolName(value string) string {
	value = toolNameUnsafeChars.ReplaceAllString(strings.TrimSpace(value), "_")
	value = strings.Trim(value, "_-")
	return value
}

func toolDescription(server AdvancedChatMCPServer, tool mcpTool) string {
	description := strings.TrimSpace(tool.Description)
	prefix := "MCP server " + server.Name
	if description == "" {
		return prefix
	}
	return prefix + ": " + description
}

func parseMCPHeaders(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	headers := map[string]string{}
	var object map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &object); err == nil {
		for key, value := range object {
			if text, ok := value.(string); ok && strings.TrimSpace(key) != "" {
				headers[key] = text
			}
		}
		return headers
	}
	for _, line := range strings.Split(raw, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) == "" {
			continue
		}
		headers[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return headers
}

func parseToolArguments(raw string) (map[string]interface{}, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]interface{}{}, nil
	}
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, err
	}
	if args == nil {
		args = map[string]interface{}{}
	}
	return args, nil
}

func truncateToolResult(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "(empty tool result)"
	}
	if len([]rune(text)) <= 20000 {
		return text
	}
	return string([]rune(text)[:20000]) + "\n...(truncated)"
}

func normalizeAssistantToolCalls(message map[string]interface{}) []map[string]interface{} {
	if message == nil {
		return nil
	}
	raw, ok := message["tool_calls"].([]interface{})
	if !ok {
		return nil
	}
	result := make([]map[string]interface{}, 0, len(raw))
	for _, item := range raw {
		if call, ok := item.(map[string]interface{}); ok {
			result = append(result, call)
		}
	}
	return result
}

func buildAdvancedChatCompletionSystemPrompt(agent *AdvancedChatAgent, skills []advancedChatRuntimeSkill, workspaceSkills []advancedChatWorkspaceSkill, mode string) string {
	sections := []string{}
	if agent != nil && strings.TrimSpace(agent.Prompt) != "" {
		sections = append(sections, strings.TrimSpace(agent.Prompt))
	}
	if catalog := advancedChatSkillCatalogPrompt(skills, workspaceSkills); catalog != "" {
		sections = append(sections, catalog)
	}
	if mode == advancedChatModeAssistant || mode == advancedChatModeAgentGroup {
		sections = append(sections, assistantModeSystemPrompt())
	}
	return strings.Join(sections, "\n\n")
}

func normalizeAdvancedChatCompletionMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case advancedChatModeAssistant:
		return advancedChatModeAssistant
	case advancedChatModeAgentGroup:
		return advancedChatModeAgentGroup
	default:
		return advancedChatModeChat
	}
}

func advancedChatCompletionMaxToolRounds(mode string) int {
	if mode == advancedChatModeAssistant || mode == advancedChatModeAgentGroup {
		return assistantMaxToolRounds
	}
	return advancedChatMaxToolRounds
}

func advancedChatCompletionTimeout(mode string) time.Duration {
	if mode == advancedChatModeAgentGroup {
		return time.Duration(advancedChatAgentGroupRunTimeoutSeconds()) * time.Second
	}
	if mode == advancedChatModeAssistant {
		return time.Duration(advancedChatAssistantRunTimeoutSeconds()) * time.Second
	}
	return advancedChatRequestTimeout
}

func assistantModeSystemPrompt() string {
	return strings.TrimSpace(`You are running in assistant mode.
Treat the user's message as a task to complete autonomously, not as a single-turn chat question.
Use available tools whenever they are relevant, including tools that inspect, create, edit, delete, rename, search, or verify workspace resources.
Continue the model/tool/observation loop until the task is completed, blocked by missing information, blocked by tool errors, or blocked by a policy or permission requirement.
Do not stop after planning if tools can make progress. Do not claim that you changed, created, deleted, or verified anything unless a tool result supports it.
Ask the user only when you are genuinely blocked or when a tool or policy requires confirmation.
When finished, give a concise final summary of what was done, what tools or files were involved when known, and any remaining blockers.`)
}

func errorMessageFromAdvancedChatCompletion(err error) string {
	var executorErr *ChatExecutorError
	if errors.As(err, &executorErr) {
		return executorErr.Message
	}
	return err.Error()
}

func writeAdvancedChatCompletionError(c *gin.Context, err error) {
	var executorErr *ChatExecutorError
	if errors.As(err, &executorErr) {
		c.JSON(executorErr.Status, gin.H{"error": executorErr.Message})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
}

func uniqueStringsLocal(values []string) []string {
	seen := map[string]struct{}{}
	result := []string{}
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
