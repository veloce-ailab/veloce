package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"gorm.io/gorm"
)

type MessageChannelAssistantRequest struct {
	Context                        context.Context
	RunID                          string
	ModelName                      string
	UserChannelID                  uint
	Messages                       []ChatExecutorMessage
	System                         string
	ConnectorDeviceID              string
	ConnectorWorkspacePath         string
	ConnectorWorkspaceUnrestricted bool
	ConnectorAutoApprove           bool
	ConnectorCommandPrefixes       []string
	AgentGroupID                   string
	MaxTokens                      int
	Temperature                    *float64
	ReasoningEffort                string
	MaxToolRounds                  int
	OnApprovalRequired             func(context.Context, MessageChannelConnectorApproval) error
}

type MessageChannelAssistantResult struct {
	Content   string
	ToolCalls int
}

type MessageChannelConnectorApproval struct {
	TaskID        string
	DeviceName    string
	Action        string
	WorkspacePath string
	Unrestricted  bool
	Arguments     map[string]interface{}
}

func ExecuteMessageChannelAssistantCompletion(user *model.User, req MessageChannelAssistantRequest) (*MessageChannelAssistantResult, error) {
	if user == nil {
		return nil, newChatExecutorError(401, "Unauthorized")
	}
	if !advancedChatAssistantModeEnabled() {
		return nil, errors.New("assistant mode is disabled")
	}
	agentGroupID := strings.TrimSpace(req.AgentGroupID)
	wantsConnector := strings.TrimSpace(req.ConnectorDeviceID) != "" || strings.TrimSpace(req.ConnectorWorkspacePath) != "" || req.ConnectorWorkspaceUnrestricted
	if wantsConnector && !advancedChatAssistantConnectorToolsEnabled() {
		return nil, errors.New("workspace tools are disabled")
	}
	ctx := req.Context
	if ctx == nil {
		ctx = context.Background()
	}
	device, workspacePath, err := loadMessageChannelConnectorForRun(user.ID, req.ConnectorDeviceID, req.ConnectorWorkspacePath, req.ConnectorWorkspaceUnrestricted)
	if err != nil {
		return nil, err
	}
	workspaceSkills := []advancedChatWorkspaceSkill{}
	if device != nil {
		workspaceSkills, err = loadAdvancedChatWorkspaceSkillsForRun(ctx, user.ID, device, workspacePath)
		if err != nil {
			return nil, err
		}
	}
	agentGroups := []advancedChatAgentGroup{}
	if agentGroupID != "" {
		if loaded, loadErr := readAdvancedChatAgentGroup(ctx, user.ID, nil, agentGroupID); loadErr == nil && loaded.ID != "" {
			agentGroups = []advancedChatAgentGroup{loaded}
		}
	}
	allConnectorTools, allConnectorBindings := advancedChatConnectorTools(device, workspacePath, req.ConnectorAutoApprove, req.ConnectorCommandPrefixes)
	tools := allConnectorTools
	connectorBindings := allConnectorBindings
	if len(agentGroups) > 0 {
		tools, connectorBindings = filterAdvancedChatAgentStudioConnectorTools("chief", allConnectorTools, allConnectorBindings)
	}
	connectorTools := append([]ChatExecutorTool{}, allConnectorTools...)
	if len(agentGroups) > 0 {
		tools = append(tools, advancedChatAgentDelegateTool(agentGroups))
	}
	if len(tools) == 0 {
		return nil, errors.New("no workspace tools are available for this message channel")
	}

	systemParts := []string{}
	if system := strings.TrimSpace(req.System); system != "" {
		systemParts = append(systemParts, system)
	}
	if prompt := buildAdvancedChatCompletionSystemPrompt(nil, nil, workspaceSkills, advancedChatModeAssistant); strings.TrimSpace(prompt) != "" {
		systemParts = append(systemParts, prompt)
	}
	if prompt := advancedChatAgentGroupSystemPrompt(agentGroups); strings.TrimSpace(prompt) != "" {
		systemParts = append(systemParts, prompt)
	}
	if len(agentGroups) > 0 {
		if prompt := advancedChatAgentStudioPrompt("chief", device != nil); strings.TrimSpace(prompt) != "" {
			systemParts = append(systemParts, prompt)
		}
	}
	if prompt := advancedChatConnectorSystemPrompt(device, workspacePath); strings.TrimSpace(prompt) != "" {
		systemParts = append(systemParts, prompt)
	}
	systemPrompt := strings.Join(systemParts, "\n\n")

	maxToolRounds := req.MaxToolRounds
	if maxToolRounds <= 0 {
		maxToolRounds = assistantMaxToolRounds
	}
	executorMessages := append([]ChatExecutorMessage{}, req.Messages...)
	totalToolCalls := 0
	lastContent := ""

	for round := 0; round < maxToolRounds; round++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result, err := executeAdvancedChatModelRequestWithRetry(ctx, user, ChatExecutorRequest{
			Context:         ctx,
			ModelName:       req.ModelName,
			UserChannelID:   req.UserChannelID,
			Messages:        executorMessages,
			System:          systemPrompt,
			Tools:           tools,
			MaxTokens:       normalizeAdvancedChatMaxTokens(req.MaxTokens),
			Temperature:     normalizeAdvancedChatTemperature(req.Temperature),
			ReasoningEffort: normalizeAdvancedChatReasoningEffort(req.ReasoningEffort),
		}, advancedChatCompletionObserver{}, func() bool { return true })
		if err != nil {
			return nil, err
		}
		lastContent = result.Content
		if len(result.ToolCalls) == 0 {
			return &MessageChannelAssistantResult{Content: result.Content, ToolCalls: totalToolCalls}, nil
		}

		totalToolCalls += len(result.ToolCalls)
		executorMessages = append(executorMessages, ChatExecutorMessage{
			Role:      "assistant",
			Content:   result.Content,
			ToolCalls: normalizeAssistantToolCalls(result.AssistantMessage),
		})
		for _, toolCall := range result.ToolCalls {
			toolResultText := "Tool not found: " + toolCall.Name
			binding, exists := connectorBindings[toolCall.Name]
			agentDelegateExists := toolCall.Name == advancedChatAgentDelegateToolName && len(agentGroups) > 0
			arguments, argumentsErr := parseToolArguments(toolCall.Arguments)
			if exists {
				if argumentsErr != nil {
					toolResultText = "Invalid tool arguments: " + argumentsErr.Error()
				} else if advancedChatConnectorTaskRequiresApproval(binding, arguments) {
					task, err := createAdvancedChatConnectorTask(user.ID, req.RunID, binding, arguments)
					if err != nil {
						toolResultText = "Failed to create connector approval task: " + err.Error()
					} else {
						approval := MessageChannelConnectorApproval{
							TaskID:        task.ID,
							DeviceName:    binding.DeviceName,
							Action:        binding.Action,
							WorkspacePath: binding.WorkspacePath,
							Unrestricted:  strings.TrimSpace(binding.WorkspacePath) == "",
							Arguments:     arguments,
						}
						if req.OnApprovalRequired != nil {
							if err := req.OnApprovalRequired(ctx, approval); err != nil {
								return nil, err
							}
						}
						toolResult, err := waitAdvancedChatConnectorTask(ctx, task.ID, user.ID)
						if err != nil {
							toolResultText = "Connector tool failed: " + err.Error()
							if strings.TrimSpace(toolResult) != "" {
								toolResultText = strings.TrimSpace(toolResult) + "\n\n" + toolResultText
							}
						} else {
							toolResultText = toolResult
						}
					}
				} else {
					toolResult, err := callAdvancedChatConnectorToolExpanded(ctx, user.ID, req.RunID, binding, arguments)
					if err != nil {
						toolResultText = "Connector tool failed: " + err.Error()
						if strings.TrimSpace(toolResult) != "" {
							toolResultText = strings.TrimSpace(toolResult) + "\n\n" + toolResultText
						}
					} else {
						toolResultText = toolResult
					}
				}
			} else if agentDelegateExists {
				if argumentsErr != nil {
					toolResultText = "Invalid delegation arguments: " + argumentsErr.Error()
				} else {
					toolResult, err := executeAdvancedChatAgentDelegate(ctx, user, advancedChatAgentDelegateInput{
						UserID:             user.ID,
						RunID:              req.RunID,
						ModelName:          req.ModelName,
						UserChannelID:      req.UserChannelID,
						Messages:           executorMessages,
						WorkspaceSkills:    workspaceSkills,
						ConnectorDevice:    device,
						ConnectorWorkspace: workspacePath,
						ConnectorBindings:  allConnectorBindings,
						ConnectorTools:     connectorTools,
						Groups:             agentGroups,
						OnApprovalRequired: req.OnApprovalRequired,
						Arguments:          arguments,
					})
					if err != nil {
						toolResultText = "Delegated agent failed: " + err.Error()
					} else {
						toolResultText = toolResult
					}
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

	return &MessageChannelAssistantResult{Content: strings.TrimSpace(lastContent), ToolCalls: totalToolCalls}, nil
}

func DecideMessageChannelConnectorApproval(userID uint, runID string, approved bool) (string, bool, error) {
	runID = strings.TrimSpace(runID)
	if userID == 0 || runID == "" {
		return "", false, nil
	}
	var task AdvancedChatConnectorTask
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ? AND run_id = ? AND status = ?", userID, runID, advancedChatConnectorTaskStatusPendingApproval).
			Order("created_at ASC").
			Limit(1).
			Find(&task).Error; err != nil {
			return err
		}
		if task.ID == "" {
			return nil
		}
		now := time.Now()
		updates := map[string]interface{}{
			"status":     advancedChatConnectorTaskStatusQueued,
			"updated_at": now,
		}
		if !approved {
			updates = map[string]interface{}{
				"status":        advancedChatConnectorTaskStatusFailed,
				"error_message": "denied by message channel user",
				"finished_at":   &now,
				"updated_at":    now,
			}
		}
		update := tx.Model(&AdvancedChatConnectorTask{}).
			Where("id = ? AND user_id = ? AND status = ?", task.ID, userID, advancedChatConnectorTaskStatusPendingApproval).
			Updates(updates)
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected == 0 {
			task = AdvancedChatConnectorTask{}
		}
		return nil
	})
	if err != nil {
		return "", false, err
	}
	if task.ID == "" {
		return "", false, nil
	}
	return task.ID, true, nil
}

func RunMessageChannelConnectorAction(ctx context.Context, userID uint, runID string, deviceID string, workspacePath string, unrestricted bool, action string, arguments map[string]interface{}, autoApprove bool, commandPrefixes []string) (string, error) {
	if userID == 0 {
		return "", newChatExecutorError(401, "Unauthorized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	device, resolvedWorkspacePath, err := loadMessageChannelConnectorForRun(userID, deviceID, workspacePath, unrestricted)
	if err != nil {
		return "", err
	}
	if device == nil {
		return "", errors.New("connector device is required")
	}
	binding := advancedChatConnectorToolBinding{
		DeviceID:        device.ID,
		DeviceName:      device.Name,
		WorkspacePath:   resolvedWorkspacePath,
		Action:          strings.TrimSpace(action),
		AutoApprove:     autoApprove,
		CommandPrefixes: normalizeConnectorCommandPrefixes(commandPrefixes),
	}
	return callAdvancedChatConnectorToolExpanded(ctx, userID, runID, binding, arguments)
}

func loadMessageChannelConnectorForRun(userID uint, deviceID string, workspacePath string, unrestricted bool) (*AdvancedChatConnectorDevice, string, error) {
	if !unrestricted {
		return loadAdvancedChatConnectorForRun(userID, deviceID, workspacePath)
	}
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return nil, "", errors.New("connector device is required")
	}
	var device AdvancedChatConnectorDevice
	if err := model.DB.Where("id = ? AND user_id = ?", deviceID, userID).First(&device).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", errors.New("connector device not found")
		}
		return nil, "", err
	}
	if !advancedChatConnectorDeviceOnline(device) {
		return nil, "", errors.New("connector device is offline")
	}
	return &device, "", nil
}

func MessageChannelApprovalArgumentsPreview(arguments map[string]interface{}) string {
	if len(arguments) == 0 {
		return "{}"
	}
	data, err := json.MarshalIndent(arguments, "", "  ")
	if err != nil {
		return "{}"
	}
	text := string(data)
	if len([]rune(text)) > 1200 {
		return string([]rune(text)[:1200]) + "\n...(truncated)"
	}
	return text
}
