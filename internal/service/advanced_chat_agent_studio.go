package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
)

const (
	advancedChatAgentStudioCommitDeltaToolName = "workspace_commit_delta"
	advancedChatAgentStudioInterruptToolName   = "interrupt_sub_agents"
	advancedChatAgentStudioQueryStatusToolName = "query_sub_agent_status"
	advancedChatAgentStudioResumeToolName      = "resume_sub_agents"
	advancedChatAgentStudioApprovalToolName    = "connector_approval_decision"
	advancedChatAgentStudioSandboxIDArg        = "sandbox_id"
	advancedChatAgentStudioSandboxBackendArg   = "sandbox_backend"
)

type advancedChatAgentStudioMutation struct {
	Action     string `json:"action"`
	Path       string `json:"path"`
	Content    string `json:"content,omitempty"`
	OldText    string `json:"old_text,omitempty"`
	NewText    string `json:"new_text,omitempty"`
	Overwrite  *bool  `json:"overwrite,omitempty"`
	CreateDirs *bool  `json:"create_dirs,omitempty"`
	BaseSHA256 string `json:"base_sha256,omitempty"`
}

type advancedChatAgentStudioDeltaLog struct {
	mu        sync.Mutex
	mutations []advancedChatAgentStudioMutation
}

type advancedChatDelegatedAgentLoopOptions struct {
	RunID              string
	SessionID          string
	ParentToolCallID   string
	Observer           advancedChatCompletionObserver
	OnApprovalRequired func(context.Context, MessageChannelConnectorApproval) error
	AllowSplit         bool
	AllowCommit        bool
	DeltaLog           *advancedChatAgentStudioDeltaLog
	DisplayRound       int
	StatusAgentID      string
	StatusAgentName    string
	StatusAgentType    string
	StatusAgentGroupID string
	SandboxID          string
	Stream             bool
	ApprovalChecker    *advancedChatAgentStudioApprovalChecker
	ChargeBalance      bool
}

var advancedChatAgentStudioLocks sync.Map

type advancedChatAgentStudioApprovalChecker struct {
	Group advancedChatAgentGroup
	Agent advancedChatGroupAgent
}

func advancedChatAgentStudioRole(prepared preparedAdvancedChatAssistantRun) string {
	if prepared.groupAgent != nil {
		return normalizeAdvancedChatAgentType(prepared.groupAgent.Type)
	}
	if prepared.mode == advancedChatModeAgentGroup {
		return "worker"
	}
	return ""
}

func advancedChatAgentStudioCanUseExecutionTools(agentType string) bool {
	switch normalizeAdvancedChatAgentType(agentType) {
	case "chief", "checker":
		return false
	default:
		return true
	}
}

func advancedChatAgentStudioConnectorActionAllowed(agentType string, action string) bool {
	agentType = normalizeAdvancedChatAgentType(agentType)
	if agentType == "checker" {
		return false
	}
	if agentType != "chief" {
		return true
	}
	switch strings.TrimSpace(action) {
	case "list_files", "list_windows_drives", "read_file", "run_command", "web_search", "web_fetch", "list_static_sites":
		return true
	default:
		return false
	}
}

func filterAdvancedChatAgentStudioConnectorTools(agentType string, tools []ChatExecutorTool, bindings map[string]advancedChatConnectorToolBinding) ([]ChatExecutorTool, map[string]advancedChatConnectorToolBinding) {
	agentType = normalizeAdvancedChatAgentType(agentType)
	if agentType == "checker" {
		return nil, map[string]advancedChatConnectorToolBinding{}
	}
	if agentType != "chief" {
		return tools, bindings
	}
	filteredTools := make([]ChatExecutorTool, 0, len(tools))
	filteredBindings := map[string]advancedChatConnectorToolBinding{}
	for _, tool := range tools {
		binding, ok := bindings[tool.Name]
		if !ok || !advancedChatAgentStudioConnectorActionAllowed(agentType, binding.Action) {
			continue
		}
		filteredTools = append(filteredTools, tool)
		filteredBindings[tool.Name] = binding
	}
	return filteredTools, filteredBindings
}

func advancedChatAgentStudioConnectorTaskRequiresApproval(binding advancedChatConnectorToolBinding, arguments map[string]interface{}, delta *advancedChatAgentStudioDeltaLog) bool {
	if delta != nil {
		switch binding.Action {
		case "write_file", "replace_text":
			return false
		}
	}
	return advancedChatConnectorTaskRequiresApproval(binding, arguments)
}

func advancedChatAgentStudioCanSplit(agentType string) bool {
	return advancedChatAgentStudioCanUseExecutionTools(agentType)
}

func advancedChatAgentStudioCanDelegate(agentType string, hasGroupAgent bool) bool {
	if !hasGroupAgent {
		return true
	}
	return normalizeAdvancedChatAgentType(agentType) == "chief"
}

func advancedChatAgentStudioPrompt(agentType string, hasConnector bool) string {
	agentType = normalizeAdvancedChatAgentType(agentType)
	if agentType == "chief" {
		return strings.TrimSpace(`Agent Studio role boundary:
- You are the Chief Agent. Keep management authority separate from execution authority.
- Do not edit files or create split sub-agents yourself.
- You may use connector browsing tools and approved commands to inspect context. Commands must be diagnostic/read-only; delegate modifications to employees.
- Use agent_delegate to assign concrete task lists to worker, critic, or reviewer main agents. Do not delegate implementation work to the checker.
- For implementation work, delegate to employees. For final physical commit of merged changes, delegate review and commit to a reviewer.
- When the human asks about running sub-agent progress, use query_sub_agent_status, answer concisely, then call resume_sub_agents.
- When the human sends new information for running sub-agents, use interrupt_sub_agents to deliver that message to them.`)
	}
	if agentType == "checker" {
		return strings.TrimSpace(`Agent Studio checker role boundary:
- You are the Checker Agent. Your only responsibility is connector approval review.
- Do not edit files, run commands, split into sub-agents, delegate, or commit.
- When an approval request is presented, use only the approval decision tool and return yes, no, or a concise approval opinion.`)
	}
	if !hasConnector {
		return ""
	}
	return strings.TrimSpace(`Agent Studio execution model:
- You are an employee main agent. Use the fast path for simple, deterministic local edits.
- Use agent_split for complex work that benefits from parallel temporary sub-agents.
- Split sub-agents work on a deferred virtual filesystem: their file writes become MutationLog entries and are not physically written to disk.
- Workspace commands and file reads in delegated/split work run against your sandbox workspace view when available. Command results may include a SandboxChangeReport containing MutationLog-compatible changes.
- When split results include MutationLog entries, merge and reason about them before reporting. Reviewer agents can use workspace_commit_delta for final physical commit after conflict review.
- Use query_sub_agent_status when the human or caller asks for progress, then call resume_sub_agents after replying.
- Use interrupt_sub_agents when the human or caller sends new information that should be delivered to running sub-agents.`)
}

func advancedChatAgentStudioConnectorSystemPrompt(agentType string, device *AdvancedChatConnectorDevice, workspacePath string) string {
	if normalizeAdvancedChatAgentType(agentType) != "chief" {
		return advancedChatConnectorSystemPrompt(device, workspacePath)
	}
	if device == nil {
		return ""
	}
	workspacePath = strings.TrimSpace(workspacePath)
	osName := strings.TrimSpace(device.OS)
	if osName == "" {
		osName = "unknown"
	}
	archName := strings.TrimSpace(device.Arch)
	if archName == "" {
		archName = "unknown"
	}
	if workspacePath == "" {
		windowsPathHint := ""
		if strings.EqualFold(device.OS, "windows") {
			windowsPathHint = "\nThe connected device is Windows. Use workspace_list_windows_drives to discover available drive roots before selecting absolute paths when the drive is not already known."
		}
		return fmt.Sprintf(`A local device connector is available to the Chief Agent for inspection only.
Device: %s
Environment: OS=%s Arch=%s
Use workspace listing, file reading, web search/fetch, and diagnostic commands to understand the environment.
Do not modify files or run commands that change local state. Delegate any implementation or file-changing work to employee agents.
Absolute paths are allowed. Ask for or infer concrete paths before reading files.%s
Read-only workspace tools, web search, web fetch, and Windows drive listing do not require approval. Commands always require approval unless the command starts with a prefix explicitly allowed in the message channel settings.`, device.Name, osName, archName, windowsPathHint)
	}
	return fmt.Sprintf(`A local workspace connector is available to the Chief Agent for inspection only.
Device: %s
Environment: OS=%s Arch=%s
Workspace: %s
Use workspace listing, file reading, web search/fetch, and diagnostic commands to understand this workspace.
Do not modify files or run commands that change local state. Delegate any implementation or file-changing work to employee agents.
Use only relative paths in workspace tool arguments.
Read-only workspace tools, web search, and web fetch do not require approval. Commands always require approval unless the command starts with a prefix explicitly allowed in the session settings.`, device.Name, osName, archName, workspacePath)
}

func advancedChatAgentStudioCommitDeltaTool() ChatExecutorTool {
	return ChatExecutorTool{
		Name:        advancedChatAgentStudioCommitDeltaToolName,
		Description: "Reviewer-only final commit tool. Apply a reviewed, conflict-free MutationLog to the connected workspace as the physical disk commit.",
		Schema: map[string]interface{}{
			"type":     "object",
			"required": []string{"mutations"},
			"properties": map[string]interface{}{
				"mutations": map[string]interface{}{
					"type":        "array",
					"description": "Ordered MutationLog entries produced by split agents and reviewed for conflicts.",
					"items": map[string]interface{}{
						"type":     "object",
						"required": []string{"action", "path"},
						"properties": map[string]interface{}{
							"action":      map[string]interface{}{"type": "string", "enum": []string{"write_file", "replace_text", "delete_file"}},
							"path":        map[string]interface{}{"type": "string"},
							"content":     map[string]interface{}{"type": "string"},
							"old_text":    map[string]interface{}{"type": "string"},
							"new_text":    map[string]interface{}{"type": "string"},
							"overwrite":   map[string]interface{}{"type": "boolean"},
							"create_dirs": map[string]interface{}{"type": "boolean"},
							"base_sha256": map[string]interface{}{"type": "string"},
						},
					},
				},
				"mutation_log": map[string]interface{}{"type": "string", "description": "Optional JSON encoded MutationLog. Used only when mutations is omitted."},
			},
		},
	}
}

func advancedChatAgentStudioApprovalTool() ChatExecutorTool {
	return ChatExecutorTool{
		Name:        advancedChatAgentStudioApprovalToolName,
		Description: "Checker-only approval tool. Return yes to approve, no to reject, or escalate when the owner must decide.",
		Schema: map[string]interface{}{
			"type":     "object",
			"required": []string{"decision"},
			"properties": map[string]interface{}{
				"decision": map[string]interface{}{"type": "string", "enum": []string{"yes", "no", "escalate"}, "description": "yes approves; no rejects; escalate leaves the task for the owner."},
				"opinion":  map[string]interface{}{"type": "string", "description": "Optional concise approval opinion or rejection reason."},
			},
		},
	}
}

func advancedChatAgentStudioInterruptTool() ChatExecutorTool {
	return ChatExecutorTool{
		Name:        advancedChatAgentStudioInterruptToolName,
		Description: "Interrupt split sub-agents and send them a new message or directive. Returns recent sub-agent snapshots so the caller can answer with current context.",
		Schema: map[string]interface{}{
			"type":     "object",
			"required": []string{"message"},
			"properties": map[string]interface{}{
				"agent_id": map[string]interface{}{"type": "string", "description": "Optional agent id or split task label to filter."},
				"message":  map[string]interface{}{"type": "string", "description": "New information, question, or directive to deliver to the selected sub-agents."},
			},
		},
	}
}

func advancedChatAgentStudioQueryStatusTool() ChatExecutorTool {
	return ChatExecutorTool{
		Name:        advancedChatAgentStudioQueryStatusToolName,
		Description: "Query recent split sub-agent progress snapshots without cancelling their background work.",
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"agent_id":    map[string]interface{}{"type": "string", "description": "Optional agent id, split task label, or task id to filter."},
				"max_agents":  map[string]interface{}{"type": "integer", "description": "Maximum sub-agent snapshots to return.", "minimum": 1, "maximum": 20},
				"include_all": map[string]interface{}{"type": "boolean", "description": "When true, include completed and failed sub-agents as well as running ones."},
			},
		},
	}
}

func advancedChatAgentStudioResumeTool() ChatExecutorTool {
	return ChatExecutorTool{
		Name:        advancedChatAgentStudioResumeToolName,
		Description: "Record that sub-agents should continue after a progress query. This does not cancel or restart background work.",
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"agent_id": map[string]interface{}{"type": "string", "description": "Optional agent id, split task label, or task id to resume."},
			},
		},
	}
}

func advancedChatAgentStudioApprovalCheckerForGroup(group *advancedChatAgentGroup) (*advancedChatAgentStudioApprovalChecker, bool) {
	if group == nil {
		return nil, false
	}
	for _, agent := range group.Agents {
		if normalizeAdvancedChatAgentType(agent.Type) == "checker" {
			return &advancedChatAgentStudioApprovalChecker{Group: *group, Agent: agent}, true
		}
	}
	return nil, false
}

func advancedChatAgentStudioApprovalCheckerForGroupValue(group advancedChatAgentGroup) *advancedChatAgentStudioApprovalChecker {
	checker, _ := advancedChatAgentStudioApprovalCheckerForGroup(&group)
	return checker
}

func advancedChatApprovalCheckerForUserAgent(userID uint, agentID string) (*advancedChatAgentStudioApprovalChecker, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return nil, errors.New("approval agent is not configured")
	}
	agent, err := loadAdvancedChatAgent(userID, agentID)
	if err != nil {
		return nil, err
	}
	if agent == nil {
		return nil, errors.New("approval agent is not configured")
	}
	return &advancedChatAgentStudioApprovalChecker{
		Group: advancedChatAgentGroup{ID: "personal-approval", Name: "Personal approval"},
		Agent: advancedChatGroupAgent{
			ID:            "personal-approval-" + agentID,
			Name:          agent.Name,
			Type:          "checker",
			Prompt:        agent.Prompt,
			ChatAgentID:   agentID,
			DefaultModel:  agent.DefaultModel,
			UserChannelID: agent.UserChannelID,
		},
	}, nil
}

func approveAdvancedChatConnectorTaskWithChecker(ctx context.Context, user *model.User, runID string, sessionID string, checker *advancedChatAgentStudioApprovalChecker, task AdvancedChatConnectorTask, binding advancedChatConnectorToolBinding, arguments map[string]interface{}, observer advancedChatCompletionObserver, fallbackUserChannelID uint, displayRound int, chargeBalance bool) (string, error) {
	if user == nil {
		return "", errors.New("user is required")
	}
	if checker == nil || strings.TrimSpace(checker.Agent.ID) == "" {
		return "", errors.New("checker agent is required")
	}
	taskID := strings.TrimSpace(task.ID)
	if taskID == "" {
		return "", errors.New("connector task is required")
	}
	eventPayload := gin.H{
		"task_id":             newAdvancedChatID("agt"),
		"parent_id":           taskID,
		"kind":                "approval",
		"status":              "running",
		"group_id":            checker.Group.ID,
		"group_name":          checker.Group.Name,
		"agent_id":            checker.Agent.ID,
		"agent_name":          checker.Agent.Name,
		"agent_type":          "checker",
		"connector_task_id":   taskID,
		"connector_action":    binding.Action,
		"connector_workspace": binding.WorkspacePath,
		"round":               displayRound,
	}
	appendAdvancedChatAgentTaskEvent(runID, sessionID, user.ID, eventPayload)

	chatAgent, err := loadAdvancedChatAgent(user.ID, checker.Agent.ChatAgentID)
	if err != nil {
		appendAdvancedChatAgentTaskEvent(runID, sessionID, user.ID, gin.H{"task_id": eventPayload["task_id"], "status": "error", "error": err.Error()})
		return "", err
	}
	modelName := strings.TrimSpace(checker.Agent.DefaultModel)
	if modelName == "" && chatAgent != nil {
		modelName = strings.TrimSpace(chatAgent.DefaultModel)
	}
	if modelName == "" {
		appendAdvancedChatAgentTaskEvent(runID, sessionID, user.ID, gin.H{"task_id": eventPayload["task_id"], "status": "error", "error": "model is required for checker"})
		return "", errors.New("model is required for checker")
	}
	userChannelID := fallbackUserChannelID
	if checker.Agent.UserChannelID > 0 {
		userChannelID = checker.Agent.UserChannelID
	}
	approvalPayload := gin.H{
		"task_id":                taskID,
		"device":                 binding.DeviceName,
		"action":                 binding.Action,
		"workspace_path":         binding.WorkspacePath,
		"workspace_unrestricted": strings.TrimSpace(binding.WorkspacePath) == "",
		"arguments":              arguments,
	}
	payloadJSON, _ := json.MarshalIndent(approvalPayload, "", "  ")
	system := strings.Join(nonEmptyStrings([]string{
		"You are running as the checker agent for Agent Studio connector approvals.",
		"Agent Studio: " + checker.Group.Name + " (" + checker.Group.ID + ")",
		"Agent: " + checker.Agent.Name + " (" + checker.Agent.ID + "), type: checker",
		advancedChatAgentTypeSystemPrompt("checker"),
		strings.TrimSpace(checker.Agent.Prompt),
		advancedChatAgentStudioPrompt("checker", false),
		"Use the connector_approval_decision tool exactly once. Approve only when the requested local operation is necessary, scoped, and consistent with the user's task. Reject destructive, unrelated, or unsafe operations. Use escalate when the request may be legitimate but you lack enough context to safely decide.",
	}), "\n\n")
	checkCtx, cancel := context.WithTimeout(ctx, advancedChatDelegatedToolWait)
	defer cancel()
	result, err := executeAdvancedChatModelRequestWithRetry(checkCtx, user, ChatExecutorRequest{
		Context:       checkCtx,
		ModelName:     modelName,
		UserChannelID: userChannelID,
		Messages: []ChatExecutorMessage{{
			Role:    "user",
			Content: "Review this connector approval request and decide yes or no:\n\n" + truncateToolResult(string(payloadJSON)),
		}},
		System:        system,
		Tools:         []ChatExecutorTool{advancedChatAgentStudioApprovalTool()},
		Stream:        false,
		ChargeBalance: chargeBalance,
	}, observer, func() bool { return true })
	if err != nil {
		_, _ = decideAdvancedChatConnectorTask(user.ID, taskID, false, "checker", "checker approval failed: "+err.Error())
		appendAdvancedChatAgentTaskEvent(runID, sessionID, user.ID, gin.H{"task_id": eventPayload["task_id"], "status": "error", "error": err.Error()})
		return "", err
	}
	decision, opinion, err := advancedChatAgentStudioCheckerDecision(result)
	if err != nil {
		_, _ = decideAdvancedChatConnectorTask(user.ID, taskID, false, "checker", err.Error())
		appendAdvancedChatAgentTaskEvent(runID, sessionID, user.ID, gin.H{"task_id": eventPayload["task_id"], "status": "error", "error": err.Error(), "result": truncateToolResult(result.Content)})
		return "", err
	}
	if decision == "escalate" {
		appendAdvancedChatAgentTaskEvent(runID, sessionID, user.ID, gin.H{"task_id": eventPayload["task_id"], "status": "approval_required", "decision": "escalate", "opinion": truncateToolResult(opinion)})
		return fmt.Sprintf("Checker needs owner approval. %s", strings.TrimSpace(opinion)), nil
	}
	approved := decision == "yes"
	status, err := decideAdvancedChatConnectorTask(user.ID, taskID, approved, "checker", opinion)
	if err != nil {
		if status, ok := acceptedAdvancedChatConnectorApprovalConflict(err, approved); ok {
			decision := "yes"
			appendAdvancedChatAgentTaskEvent(runID, sessionID, user.ID, gin.H{
				"task_id":  eventPayload["task_id"],
				"status":   "completed",
				"decision": decision,
				"opinion":  truncateToolResult(opinion),
			})
			return fmt.Sprintf("Checker decision: %s. Connector task was already %s. %s", decision, status, strings.TrimSpace(opinion)), nil
		}
		appendAdvancedChatAgentTaskEvent(runID, sessionID, user.ID, gin.H{"task_id": eventPayload["task_id"], "status": "error", "error": err.Error()})
		return "", err
	}
	resolvedDecision := "no"
	if approved {
		resolvedDecision = "yes"
	}
	appendAdvancedChatAgentTaskEvent(runID, sessionID, user.ID, gin.H{
		"task_id":  eventPayload["task_id"],
		"status":   "completed",
		"decision": resolvedDecision,
		"opinion":  truncateToolResult(opinion),
	})
	return fmt.Sprintf("Checker decision: %s. Connector task status: %s. %s", resolvedDecision, status, strings.TrimSpace(opinion)), nil
}

func acceptedAdvancedChatConnectorApprovalConflict(err error, approved bool) (string, bool) {
	if !approved {
		return "", false
	}
	var taskErr advancedChatConnectorTaskDecisionConflict
	if !errors.As(err, &taskErr) {
		return "", false
	}
	status := strings.TrimSpace(taskErr.Status)
	switch status {
	case advancedChatConnectorTaskStatusQueued, advancedChatConnectorTaskStatusRunning, advancedChatConnectorTaskStatusCompleted:
		return status, true
	default:
		return "", false
	}
}

func advancedChatAgentStudioCheckerDecision(result *ChatExecutorResult) (string, string, error) {
	if result == nil {
		return "", "", errors.New("checker returned no result")
	}
	for _, call := range result.ToolCalls {
		if call.Name != advancedChatAgentStudioApprovalToolName {
			continue
		}
		arguments, err := parseToolArguments(call.Arguments)
		if err != nil {
			return "", "", err
		}
		decision := strings.ToLower(strings.TrimSpace(stringFromMap(arguments, "decision")))
		opinion := strings.TrimSpace(stringFromMap(arguments, "opinion"))
		switch decision {
		case "yes":
			return decision, opinion, nil
		case "no":
			return decision, opinion, nil
		case "escalate":
			return decision, opinion, nil
		default:
			return "", opinion, errors.New("checker decision must be yes, no, or escalate")
		}
	}
	return "", strings.TrimSpace(result.Content), errors.New("checker did not call the approval decision tool")
}

func advancedChatAgentStudioLockKey(userID uint, groupID string, agentID string) string {
	return fmt.Sprintf("%d:%s:%s", userID, strings.TrimSpace(groupID), strings.TrimSpace(agentID))
}

func advancedChatAgentStudioSandboxID(parts ...string) string {
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		var builder strings.Builder
		for _, r := range part {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
				builder.WriteRune(r)
			} else {
				builder.WriteByte('-')
			}
			if builder.Len() >= 64 {
				break
			}
		}
		if text := strings.Trim(builder.String(), "-_"); text != "" {
			values = append(values, text)
		}
	}
	if len(values) == 0 {
		return ""
	}
	result := strings.Join(values, "-")
	if len(result) > 160 {
		result = result[:160]
	}
	return result
}

func advancedChatAgentStudioSandboxArguments(arguments map[string]interface{}, sandboxID string) map[string]interface{} {
	sandboxID = strings.TrimSpace(sandboxID)
	if sandboxID == "" {
		return arguments
	}
	next := cloneAdvancedChatConnectorArguments(arguments)
	next[advancedChatAgentStudioSandboxIDArg] = sandboxID
	next[advancedChatAgentStudioSandboxBackendArg] = "appcontainer"
	return next
}

func withAdvancedChatAgentStudioLock(userID uint, groupID string, agentID string, fn func() (string, error)) (string, error) {
	key := advancedChatAgentStudioLockKey(userID, groupID, agentID)
	value, _ := advancedChatAgentStudioLocks.LoadOrStore(key, &sync.Mutex{})
	mutex := value.(*sync.Mutex)
	mutex.Lock()
	defer mutex.Unlock()
	return fn()
}

func executeAdvancedChatConnectorToolForAgent(ctx context.Context, userID uint, runID string, binding advancedChatConnectorToolBinding, arguments map[string]interface{}, delta *advancedChatAgentStudioDeltaLog) (string, error) {
	if delta == nil {
		return callAdvancedChatConnectorToolExpanded(ctx, userID, runID, binding, arguments)
	}
	switch binding.Action {
	case "read_file":
		content, err := callAdvancedChatConnectorToolExpanded(ctx, userID, runID, binding, arguments)
		if err != nil {
			return content, err
		}
		path, _ := arguments["path"].(string)
		virtual := delta.applyToPath(path, content)
		if maxBytes, ok := intFromConnectorArgument(arguments["max_bytes"]); ok && maxBytes > 0 {
			virtual = truncateBytes(virtual, maxBytes)
		}
		return virtual, nil
	case "write_file":
		baseSHA256 := baseSHA256ForPath(ctx, userID, runID, binding, stringFromMap(arguments, "path"))
		mutation := advancedChatAgentStudioMutation{
			Action:     "write_file",
			Path:       stringFromMap(arguments, "path"),
			Content:    stringFromMap(arguments, "content"),
			BaseSHA256: baseSHA256,
		}
		if value, ok := boolFromConnectorArgument(arguments["overwrite"]); ok {
			mutation.Overwrite = &value
		}
		if value, ok := boolFromConnectorArgument(arguments["create_dirs"]); ok {
			mutation.CreateDirs = &value
		}
		delta.append(mutation)
		return "Deferred write captured in MutationLog. No physical file was changed.", nil
	case "replace_text":
		baseSHA256ByPath := map[string]string{}
		baseForPath := func(path string) string {
			path = strings.TrimSpace(path)
			if value, ok := baseSHA256ByPath[path]; ok {
				return value
			}
			value := baseSHA256ForPath(ctx, userID, runID, binding, path)
			baseSHA256ByPath[path] = value
			return value
		}
		mutations := advancedChatAgentStudioMutationsFromReplaceArguments(arguments, baseForPath)
		for _, mutation := range mutations {
			delta.append(mutation)
		}
		return fmt.Sprintf("Deferred %d text replacement(s) captured in MutationLog. No physical file was changed.", len(mutations)), nil
	case "run_command":
		result, err := callAdvancedChatConnectorToolExpanded(ctx, userID, runID, binding, arguments)
		if err == nil {
			appendAdvancedChatAgentStudioDeltaMutations(delta, advancedChatAgentStudioSandboxMutations(result))
		}
		return result, err
	default:
		return callAdvancedChatConnectorToolExpanded(ctx, userID, runID, binding, arguments)
	}
}

func baseSHA256ForPath(ctx context.Context, userID uint, runID string, binding advancedChatConnectorToolBinding, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return sha256Hex("")
	}
	hashBinding := binding
	hashBinding.Action = "file_sha256"
	hashCtx, cancel := context.WithTimeout(ctx, advancedChatDelegatedToolWait)
	defer cancel()
	result, err := callAdvancedChatConnectorTool(hashCtx, userID, runID, hashBinding, map[string]interface{}{"path": path})
	if err != nil {
		return sha256Hex("")
	}
	result = strings.ToLower(strings.TrimSpace(result))
	if len(result) != 64 {
		return sha256Hex("")
	}
	for _, char := range result {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return sha256Hex("")
		}
	}
	return result
}

func commitAdvancedChatAgentStudioDelta(ctx context.Context, user *model.User, runID string, sessionID string, binding advancedChatConnectorToolBinding, arguments map[string]interface{}, checker *advancedChatAgentStudioApprovalChecker, observer advancedChatCompletionObserver, fallbackUserChannelID uint, displayRound int, chargeBalance bool) (string, error) {
	if user == nil {
		return "", errors.New("user is required")
	}
	mutations, err := parseAdvancedChatAgentStudioMutations(arguments)
	if err != nil {
		return "", err
	}
	if len(mutations) == 0 {
		return "", errors.New("mutations are required")
	}
	if err := validateAdvancedChatAgentStudioMutations(mutations); err != nil {
		return "", err
	}

	callBinding := binding
	callBinding.Action = "commit_delta"
	callArguments := map[string]interface{}{"mutations": mutations}
	if advancedChatConnectorTaskRequiresApproval(callBinding, callArguments) && checker != nil {
		task, err := createAdvancedChatConnectorTask(user.ID, runID, callBinding, callArguments)
		if err != nil {
			return "", err
		}
		checkerResult, err := approveAdvancedChatConnectorTaskWithChecker(ctx, user, runID, sessionID, checker, task, callBinding, callArguments, observer, fallbackUserChannelID, displayRound, chargeBalance)
		if err != nil {
			return checkerResult, err
		}
		result, err := waitAdvancedChatConnectorTask(ctx, task.ID, user.ID)
		if err != nil {
			if strings.TrimSpace(checkerResult) != "" {
				return checkerResult, err
			}
			return result, err
		}
		return result, nil
	}
	return callAdvancedChatConnectorTool(ctx, user.ID, runID, callBinding, callArguments)
}

func validateAdvancedChatAgentStudioMutations(mutations []advancedChatAgentStudioMutation) error {
	writeByPath := map[string]string{}
	replaceByPathAndAnchor := map[string]string{}
	for index, mutation := range mutations {
		action := strings.TrimSpace(mutation.Action)
		path := strings.TrimSpace(mutation.Path)
		if path == "" {
			return fmt.Errorf("mutation %d is missing path", index+1)
		}
		switch action {
		case "write_file":
			if previous, exists := writeByPath[path]; exists && previous != mutation.Content {
				return fmt.Errorf("conflicting write_file mutations for %s", path)
			}
			writeByPath[path] = mutation.Content
		case "replace_text":
			if strings.TrimSpace(mutation.OldText) == "" {
				return fmt.Errorf("mutation %d old_text is required", index+1)
			}
			key := path + "\x00" + mutation.OldText
			if previous, exists := replaceByPathAndAnchor[key]; exists && previous != mutation.NewText {
				return fmt.Errorf("conflicting replace_text mutations for %s", path)
			}
			if _, exists := replaceByPathAndAnchor[key]; exists {
				return fmt.Errorf("duplicate replace_text mutation for %s", path)
			}
			replaceByPathAndAnchor[key] = mutation.NewText
		case "delete_file":
			if _, exists := writeByPath[path]; exists {
				return fmt.Errorf("conflicting delete_file mutation for %s", path)
			}
			writeByPath[path] = "\x00deleted"
		default:
			return fmt.Errorf("unsupported mutation action %q at index %d", mutation.Action, index+1)
		}
	}
	return nil
}

func interruptAdvancedChatAgentStudioSubAgents(runID string, sessionID string, userID uint, arguments map[string]interface{}) (string, error) {
	agentID := strings.TrimSpace(stringFromMap(arguments, "agent_id"))
	message := strings.TrimSpace(stringFromMap(arguments, "message"))
	if message == "" {
		return "", errors.New("message is required")
	}
	var events []AdvancedChatRunEvent
	if err := model.DB.Where("run_id = ? AND user_id = ? AND event = ?", runID, userID, "agent_task").
		Order("seq DESC").
		Limit(20).
		Find(&events).Error; err != nil {
		return "", err
	}
	items := make([]map[string]interface{}, 0, len(events))
	for _, event := range events {
		payload := map[string]interface{}{}
		_ = json.Unmarshal([]byte(event.Payload), &payload)
		if agentID != "" {
			id, _ := payload["agent_id"].(string)
			name, _ := payload["agent_name"].(string)
			if id != agentID && name != agentID {
				continue
			}
		}
		items = append(items, payload)
	}
	_ = appendAdvancedChatRunEvent(runID, sessionID, userID, "agent_task", gin.H{
		"status":   "interrupted",
		"agent_id": agentID,
		"message":  message,
	})
	data, err := json.Marshal(gin.H{"interrupted": true, "message": message, "sub_agents": items})
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func queryAdvancedChatAgentStudioSubAgentStatus(runID string, userID uint, arguments map[string]interface{}) (string, error) {
	agentID := strings.TrimSpace(stringFromMap(arguments, "agent_id"))
	maxAgents := 10
	if value, ok := intFromConnectorArgument(arguments["max_agents"]); ok && value > 0 && value <= 20 {
		maxAgents = value
	}
	includeAll := false
	if value, ok := boolFromConnectorArgument(arguments["include_all"]); ok {
		includeAll = value
	}
	var events []AdvancedChatRunEvent
	if err := model.DB.Where("run_id = ? AND user_id = ? AND event = ?", strings.TrimSpace(runID), userID, "agent_task").
		Order("seq ASC").
		Limit(200).
		Find(&events).Error; err != nil {
		return "", err
	}
	states := map[string]map[string]interface{}{}
	order := []string{}
	for _, event := range events {
		payload := map[string]interface{}{}
		if err := json.Unmarshal([]byte(event.Payload), &payload); err != nil {
			continue
		}
		if !advancedChatAgentStudioSubAgentMatches(payload, agentID) {
			continue
		}
		key := advancedChatAgentStudioSubAgentKey(payload)
		if key == "" {
			continue
		}
		current := states[key]
		if current == nil {
			current = map[string]interface{}{}
			states[key] = current
			order = append(order, key)
		}
		for name, value := range payload {
			current[name] = value
		}
	}
	items := make([]map[string]interface{}, 0, len(order))
	runningCount := 0
	for _, key := range order {
		item := states[key]
		status := strings.ToLower(strings.TrimSpace(fmt.Sprint(item["status"])))
		if status == "running" || status == "approval_required" {
			runningCount++
		}
		if !includeAll && status != "running" && status != "approval_required" {
			continue
		}
		items = append(items, item)
		if len(items) >= maxAgents {
			break
		}
	}
	data, err := json.Marshal(gin.H{
		"queried":       true,
		"running_count": runningCount,
		"sub_agents":    items,
	})
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func resumeAdvancedChatAgentStudioSubAgents(runID string, sessionID string, userID uint, arguments map[string]interface{}) (string, error) {
	agentID := strings.TrimSpace(stringFromMap(arguments, "agent_id"))
	_ = appendAdvancedChatRunEvent(runID, sessionID, userID, "agent_task", gin.H{
		"status":   "resumed",
		"agent_id": agentID,
	})
	data, err := json.Marshal(gin.H{"resumed": true, "agent_id": agentID})
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func advancedChatAgentStudioSubAgentMatches(payload map[string]interface{}, agentID string) bool {
	if agentID == "" {
		return true
	}
	taskID := strings.TrimSpace(fmt.Sprint(payload["task_id"]))
	id := strings.TrimSpace(fmt.Sprint(payload["agent_id"]))
	name := strings.TrimSpace(fmt.Sprint(payload["agent_name"]))
	return taskID == agentID || id == agentID || name == agentID
}

func advancedChatAgentStudioSubAgentKey(payload map[string]interface{}) string {
	for _, name := range []string{"task_id", "agent_id", "agent_name"} {
		value := strings.TrimSpace(fmt.Sprint(payload[name]))
		if value != "" && value != "<nil>" {
			return value
		}
	}
	return ""
}

func (d *advancedChatAgentStudioDeltaLog) append(mutation advancedChatAgentStudioMutation) {
	if d == nil {
		return
	}
	mutation.Action = strings.TrimSpace(mutation.Action)
	mutation.Path = strings.TrimSpace(mutation.Path)
	if mutation.Action == "" || mutation.Path == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.mutations = append(d.mutations, mutation)
}

func appendAdvancedChatAgentStudioDeltaMutations(delta *advancedChatAgentStudioDeltaLog, mutations []advancedChatAgentStudioMutation) {
	for _, mutation := range mutations {
		delta.append(mutation)
	}
}

func (d *advancedChatAgentStudioDeltaLog) snapshot() []advancedChatAgentStudioMutation {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	result := make([]advancedChatAgentStudioMutation, len(d.mutations))
	copy(result, d.mutations)
	return result
}

func advancedChatAgentStudioSandboxMutations(content string) []advancedChatAgentStudioMutation {
	const marker = "SandboxChangeReport:"
	mutations := []advancedChatAgentStudioMutation{}
	remaining := content
	for {
		index := strings.Index(remaining, marker)
		if index < 0 {
			break
		}
		after := remaining[index+len(marker):]
		jsonStart := strings.Index(after, "{")
		if jsonStart < 0 {
			break
		}
		after = after[jsonStart:]
		jsonEnd := strings.Index(after, "\n```")
		if jsonEnd < 0 {
			jsonEnd = len(after)
		}
		var report struct {
			Mutations []advancedChatAgentStudioMutation `json:"mutations"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(after[:jsonEnd])), &report); err == nil {
			mutations = append(mutations, normalizeAdvancedChatAgentStudioMutations(report.Mutations)...)
		}
		remaining = after[jsonEnd:]
	}
	return mutations
}

func (d *advancedChatAgentStudioDeltaLog) applyToPath(path string, base string) string {
	path = strings.TrimSpace(path)
	result := base
	for _, mutation := range d.snapshot() {
		if strings.TrimSpace(mutation.Path) != path {
			continue
		}
		switch mutation.Action {
		case "write_file":
			result = mutation.Content
		case "replace_text":
			result = strings.Replace(result, mutation.OldText, mutation.NewText, 1)
		}
	}
	return result
}

func advancedChatAgentStudioMutationsFromReplaceArguments(arguments map[string]interface{}, baseSHA256ForPath func(string) string) []advancedChatAgentStudioMutation {
	calls := expandAdvancedChatConnectorToolArguments(advancedChatConnectorToolBinding{Action: "replace_text"}, arguments)
	mutations := make([]advancedChatAgentStudioMutation, 0, len(calls))
	for _, call := range calls {
		path := stringFromMap(call, "path")
		baseSHA256 := sha256Hex("")
		if baseSHA256ForPath != nil {
			baseSHA256 = baseSHA256ForPath(path)
		}
		mutations = append(mutations, advancedChatAgentStudioMutation{
			Action:     "replace_text",
			Path:       path,
			OldText:    stringFromMap(call, "old_text"),
			NewText:    stringFromMap(call, "new_text"),
			BaseSHA256: baseSHA256,
		})
	}
	return mutations
}

func parseAdvancedChatAgentStudioMutations(arguments map[string]interface{}) ([]advancedChatAgentStudioMutation, error) {
	if raw, ok := arguments["mutations"]; ok {
		data, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		var mutations []advancedChatAgentStudioMutation
		if err := json.Unmarshal(data, &mutations); err != nil {
			return nil, err
		}
		return normalizeAdvancedChatAgentStudioMutations(mutations), nil
	}
	if raw := strings.TrimSpace(stringFromMap(arguments, "mutation_log")); raw != "" {
		var payload struct {
			Mutations []advancedChatAgentStudioMutation `json:"mutations"`
		}
		if err := json.Unmarshal([]byte(raw), &payload); err == nil && len(payload.Mutations) > 0 {
			return normalizeAdvancedChatAgentStudioMutations(payload.Mutations), nil
		}
		var mutations []advancedChatAgentStudioMutation
		if err := json.Unmarshal([]byte(raw), &mutations); err != nil {
			return nil, err
		}
		return normalizeAdvancedChatAgentStudioMutations(mutations), nil
	}
	return nil, nil
}

func normalizeAdvancedChatAgentStudioMutations(input []advancedChatAgentStudioMutation) []advancedChatAgentStudioMutation {
	result := make([]advancedChatAgentStudioMutation, 0, len(input))
	for _, mutation := range input {
		mutation.Action = strings.TrimSpace(mutation.Action)
		mutation.Path = strings.TrimSpace(mutation.Path)
		if mutation.Action == "" || mutation.Path == "" {
			continue
		}
		result = append(result, mutation)
		if len(result) >= 200 {
			break
		}
	}
	return result
}

func intFromConnectorArgument(value interface{}) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		number, err := typed.Int64()
		return int(number), err == nil
	default:
		return 0, false
	}
}

func boolFromConnectorArgument(value interface{}) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	default:
		return false, false
	}
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func filterAdvancedChatToolsByName(tools []ChatExecutorTool, excluded map[string]bool) []ChatExecutorTool {
	result := make([]ChatExecutorTool, 0, len(tools))
	for _, tool := range tools {
		if excluded[tool.Name] {
			continue
		}
		result = append(result, tool)
	}
	return result
}
