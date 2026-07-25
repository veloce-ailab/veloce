package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	advancedChatAgentDelegateToolName = "agent_delegate"
	advancedChatAgentSplitToolName    = "agent_split"
	advancedChatDelegatedToolWait     = 45 * time.Second
)

var advancedChatAgentGroupIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

type advancedChatAgentGroupInput struct {
	ID          string                   `json:"id"`
	Name        string                   `json:"name"`
	Description string                   `json:"description"`
	Agents      []advancedChatGroupAgent `json:"agents"`
}

type advancedChatAgentGroup struct {
	ID          string                   `json:"id"`
	Name        string                   `json:"name"`
	Description string                   `json:"description,omitempty"`
	Agents      []advancedChatGroupAgent `json:"agents"`
	UpdatedAt   string                   `json:"updated_at,omitempty"`
}

type advancedChatGroupAgent struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Type          string   `json:"type"`
	Prompt        string   `json:"prompt"`
	ChatAgentID   string   `json:"chat_agent_id,omitempty"`
	DefaultModel  string   `json:"default_model,omitempty"`
	UserChannelID uint     `json:"user_channel_id,omitempty"`
	SkillIDs      []string `json:"skill_ids,omitempty"`
	MCPServerIDs  []string `json:"mcp_server_ids,omitempty"`
}

func (api *advancedChatAPI) listAgentGroups(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	groups, err := loadAdvancedChatAgentGroupsForRun(c.Request.Context(), user.ID, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"groups": groups})
}

func (api *advancedChatAPI) getAgentGroup(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	group, err := readAdvancedChatAgentGroup(c.Request.Context(), user.ID, nil, c.Param("id"))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Studio not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, group)
}

func (api *advancedChatAPI) saveAgentGroup(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var input advancedChatAgentGroupInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	pathID := strings.TrimSpace(c.Param("id"))
	bodyID := strings.TrimSpace(input.ID)
	if pathID != "" {
		if bodyID != "" && bodyID != pathID {
			c.JSON(http.StatusBadRequest, gin.H{"error": "studio id does not match path"})
			return
		}
		input.ID = pathID
	} else if bodyID != "" {
		input.ID = bodyID
	}
	group, err := normalizeAdvancedChatAgentGroup(input)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	organizationID, workspaceID := advancedChatEnterpriseScope(c)
	if err := writeAdvancedChatAgentGroup(c.Request.Context(), user.ID, nil, group, organizationID, workspaceID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, group)
}

func (api *advancedChatAPI) deleteAgentGroup(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if err := deleteAdvancedChatAgentGroup(c.Request.Context(), user.ID, nil, c.Param("id")); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Studio deleted"})
}

func loadAdvancedChatAgentGroupsForRun(ctx context.Context, userID uint, device *AdvancedChatConnectorDevice) ([]advancedChatAgentGroup, error) {
	var records []AdvancedChatAgentStudio
	err := model.DB.WithContext(ctx).Where("user_id = ?", userID).Order("name ASC").Find(&records).Error
	if err != nil {
		return nil, fmt.Errorf("failed to load studios: %w", err)
	}
	groups := make([]advancedChatAgentGroup, 0, len(records))
	for _, record := range records {
		group, err := advancedChatAgentGroupFromRecord(record)
		if err == nil && group.ID != "" {
			groups = append(groups, group)
		}
	}
	return groups, nil
}

func readAdvancedChatAgentGroup(ctx context.Context, userID uint, device *AdvancedChatConnectorDevice, id string) (advancedChatAgentGroup, error) {
	var record AdvancedChatAgentStudio
	if err := model.DB.WithContext(ctx).Where("user_id = ? AND studio_id = ?", userID, strings.TrimSpace(id)).First(&record).Error; err != nil {
		return advancedChatAgentGroup{}, err
	}
	return advancedChatAgentGroupFromRecord(record)
}

func writeAdvancedChatAgentGroup(ctx context.Context, userID uint, device *AdvancedChatConnectorDevice, group advancedChatAgentGroup, scope ...uint) error {
	agents, err := json.Marshal(group.Agents)
	if err != nil {
		return err
	}
	var record AdvancedChatAgentStudio
	err = model.DB.WithContext(ctx).Where("user_id = ? AND studio_id = ?", userID, group.ID).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		organizationID, workspaceID := uint(0), uint(0)
		if len(scope) > 0 {
			organizationID = scope[0]
		}
		if len(scope) > 1 {
			workspaceID = scope[1]
		}
		record = AdvancedChatAgentStudio{
			UserID:         userID,
			OrganizationID: organizationID,
			WorkspaceID:    workspaceID,
			OwnerUserID:    userID,
			Visibility:     model.ResourceVisibilityPersonal,
			StudioID:       group.ID,
			Name:           group.Name,
			Description:    group.Description,
			Agents:         string(agents),
		}
		return model.DB.WithContext(ctx).Create(&record).Error
	}
	if err != nil {
		return err
	}
	return model.DB.WithContext(ctx).Model(&record).Updates(map[string]interface{}{
		"name":        group.Name,
		"description": group.Description,
		"agents":      string(agents),
	}).Error
}

func deleteAdvancedChatAgentGroup(ctx context.Context, userID uint, device *AdvancedChatConnectorDevice, id string) error {
	return model.DB.WithContext(ctx).Where("user_id = ? AND studio_id = ?", userID, strings.TrimSpace(id)).Delete(&AdvancedChatAgentStudio{}).Error
}

func advancedChatAgentGroupFromRecord(record AdvancedChatAgentStudio) (advancedChatAgentGroup, error) {
	agents := []advancedChatGroupAgent{}
	if strings.TrimSpace(record.Agents) != "" {
		if err := json.Unmarshal([]byte(record.Agents), &agents); err != nil {
			return advancedChatAgentGroup{}, err
		}
	}
	group, err := normalizeAdvancedChatAgentGroup(advancedChatAgentGroupInput{
		ID:          record.StudioID,
		Name:        record.Name,
		Description: record.Description,
		Agents:      agents,
	})
	if err != nil {
		return advancedChatAgentGroup{}, err
	}
	group.UpdatedAt = record.UpdatedAt.Format(time.RFC3339)
	return group, nil
}

func normalizeAdvancedChatAgentGroup(input advancedChatAgentGroupInput) (advancedChatAgentGroup, error) {
	id := strings.TrimSpace(input.ID)
	if id == "" {
		id = newAdvancedChatID("agp")
	}
	if !advancedChatAgentGroupIDPattern.MatchString(id) {
		return advancedChatAgentGroup{}, errors.New("studio id must be 1-80 characters of letters, numbers, underscore, or dash")
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return advancedChatAgentGroup{}, errors.New("studio name is required")
	}
	if len([]rune(name)) > 120 {
		name = string([]rune(name)[:120])
	}
	description := strings.TrimSpace(input.Description)
	if len([]rune(description)) > 2000 {
		description = string([]rune(description)[:2000])
	}
	agents := normalizeAdvancedChatGroupAgents(input.Agents)
	if len(agents) == 0 {
		return advancedChatAgentGroup{}, errors.New("studio requires at least one member")
	}
	chiefCount := 0
	checkerCount := 0
	for _, agent := range agents {
		switch normalizeAdvancedChatAgentType(agent.Type) {
		case "chief":
			chiefCount++
		case "checker":
			checkerCount++
		}
	}
	if chiefCount != 1 {
		return advancedChatAgentGroup{}, errors.New("studio must contain exactly one chief")
	}
	if checkerCount != 1 {
		return advancedChatAgentGroup{}, errors.New("studio must contain exactly one checker")
	}
	return advancedChatAgentGroup{
		ID:          id,
		Name:        name,
		Description: description,
		Agents:      agents,
	}, nil
}

func normalizeAdvancedChatGroupAgents(input []advancedChatGroupAgent) []advancedChatGroupAgent {
	result := []advancedChatGroupAgent{}
	seen := map[string]struct{}{}
	for index, agent := range input {
		chatAgentID := truncateAdvancedChatAgentField(agent.ChatAgentID, 80)
		if chatAgentID == "" {
			continue
		}
		id := strings.TrimSpace(agent.ID)
		if id == "" {
			id = "agent-" + chatAgentID
		}
		id = uniqueAdvancedChatAgentGroupPart(seen, id, fmt.Sprintf("agent-%d", index+1))
		name := strings.TrimSpace(agent.Name)
		if name == "" {
			name = id
		}
		if len([]rune(name)) > 120 {
			name = string([]rune(name)[:120])
		}
		prompt := strings.TrimSpace(agent.Prompt)
		if len([]rune(prompt)) > 20000 {
			prompt = string([]rune(prompt)[:20000])
		}
		result = append(result, advancedChatGroupAgent{
			ID:            id,
			Name:          name,
			Type:          normalizeAdvancedChatAgentType(agent.Type),
			Prompt:        prompt,
			ChatAgentID:   chatAgentID,
			DefaultModel:  truncateAdvancedChatAgentField(agent.DefaultModel, 100),
			UserChannelID: agent.UserChannelID,
			SkillIDs:      uniqueStringsLocal(agent.SkillIDs),
			MCPServerIDs:  uniqueStringsLocal(agent.MCPServerIDs),
		})
		seen[id] = struct{}{}
		if len(result) >= 40 {
			break
		}
	}
	return result
}

func uniqueAdvancedChatAgentGroupPart(seen map[string]struct{}, value string, fallback string) string {
	base := sanitizeAdvancedChatAgentGroupPart(value, fallback)
	id := base
	for i := 2; ; i++ {
		if _, exists := seen[id]; !exists {
			return id
		}
		suffix := fmt.Sprintf("-%d", i)
		maxBase := 80 - len(suffix)
		if maxBase < 1 {
			maxBase = 1
		}
		truncated := base
		if len(truncated) > maxBase {
			truncated = truncated[:maxBase]
		}
		id = truncated + suffix
	}
}

func parseAdvancedChatAgentGroups(raw string) ([]advancedChatAgentGroup, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []advancedChatAgentGroup{}, nil
	}
	var payload struct {
		Groups []json.RawMessage `json:"groups"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, err
	}
	groups := make([]advancedChatAgentGroup, 0, len(payload.Groups))
	for _, item := range payload.Groups {
		group, err := parseAdvancedChatAgentGroup(string(item))
		if err != nil || group.ID == "" {
			continue
		}
		groups = append(groups, group)
		if len(groups) >= 100 {
			break
		}
	}
	sort.Slice(groups, func(i, j int) bool {
		return strings.ToLower(groups[i].Name) < strings.ToLower(groups[j].Name)
	})
	return groups, nil
}

func parseAdvancedChatAgentGroup(raw string) (advancedChatAgentGroup, error) {
	var group advancedChatAgentGroup
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &group); err != nil {
		return advancedChatAgentGroup{}, err
	}
	input := advancedChatAgentGroupInput{
		ID:          group.ID,
		Name:        group.Name,
		Description: group.Description,
		Agents:      group.Agents,
	}
	normalized, err := normalizeAdvancedChatAgentGroup(input)
	if err != nil {
		return advancedChatAgentGroup{}, err
	}
	normalized.UpdatedAt = strings.TrimSpace(group.UpdatedAt)
	return normalized, nil
}

func sanitizeAdvancedChatAgentGroupPart(value string, fallback string) string {
	value = strings.TrimSpace(value)
	var builder strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			builder.WriteRune(r)
		}
	}
	result := builder.String()
	if result == "" {
		result = fallback
	}
	if len(result) > 80 {
		result = result[:80]
	}
	return result
}

func normalizeAdvancedChatAgentType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "chief", "worker", "critic", "reviewer", "checker":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "worker"
	}
}

func advancedChatAgentTypeSystemPrompt(agentType string) string {
	switch normalizeAdvancedChatAgentType(agentType) {
	case "chief":
		return "Role: chief agent. Coordinate the work, decompose goals, delegate to suitable employee agents, integrate results, and keep final decisions coherent. You do not directly edit files, run local commands, or split yourself into sub-agents."
	case "critic":
		return "Role: critic agent. Stress-test assumptions, identify flaws, missing evidence, unsafe steps, and weak reasoning. You are an employee agent and may use execution tools when the delegated critique requires local evidence."
	case "reviewer":
		return "Role: reviewer agent. Review completed work for correctness, regressions, quality, maintainability, and test gaps. You are the employee agent responsible for final conflict review and physical commit when MutationLogs are provided."
	case "checker":
		return "Role: checker agent. Review connector approval requests only. You do not execute tasks, edit files, run local commands, split into sub-agents, delegate, or commit. Use only the approval decision tool when it is provided."
	default:
		return "Role: worker agent. Execute the assigned goal directly, choose fast direct execution for simple work, split into temporary sub-agents for complex work, report concrete results, and avoid taking ownership of unrelated work."
	}
}

func truncateAdvancedChatAgentField(value string, max int) string {
	value = strings.TrimSpace(value)
	if len([]rune(value)) <= max {
		return value
	}
	return string([]rune(value)[:max])
}

func advancedChatAgentDelegateTool(groups []advancedChatAgentGroup) ChatExecutorTool {
	return ChatExecutorTool{
		Name:        advancedChatAgentDelegateToolName,
		Description: "Delegate a focused goal to an existing member from the loaded Agent Studio. This is CPS-style delegation: the current agent waits until the selected member returns a result. You may call this tool multiple times in the same assistant turn when several existing members should work on separate goals.",
		Schema: map[string]interface{}{
			"type":     "object",
			"required": []string{"group_id", "agent_id", "goal"},
			"properties": map[string]interface{}{
				"group_id": map[string]interface{}{"type": "string", "description": "Agent Studio id."},
				"agent_id": map[string]interface{}{"type": "string", "description": "Member id inside the studio. Use an existing member, not a newly split agent."},
				"goal":     map[string]interface{}{"type": "string", "description": "Specific task goal for the delegated agent."},
				"context":  map[string]interface{}{"type": "string", "description": "Optional extra context or constraints for this delegated task."},
			},
		},
	}
}

func advancedChatAgentSplitTool() ChatExecutorTool {
	return ChatExecutorTool{
		Name:        advancedChatAgentSplitToolName,
		Description: "Split the current assistant into one or more temporary sibling agents that share the same conversation history but each receive a different focused goal. This is not CPS delegation to an Agent Studio member; use agent_delegate when you need a defined studio member.",
		Schema: map[string]interface{}{
			"type":     "object",
			"required": []string{"tasks"},
			"properties": map[string]interface{}{
				"tasks": map[string]interface{}{
					"type":        "array",
					"description": "Temporary split-agent tasks. Keep each task independent and focused.",
					"minItems":    1,
					"maxItems":    8,
					"items": map[string]interface{}{
						"type":     "object",
						"required": []string{"goal"},
						"properties": map[string]interface{}{
							"id":      map[string]interface{}{"type": "string", "description": "Optional caller-defined task id."},
							"goal":    map[string]interface{}{"type": "string", "description": "Focused goal for this split agent."},
							"context": map[string]interface{}{"type": "string", "description": "Optional extra context or constraints for this task."},
						},
					},
				},
			},
		},
	}
}

func advancedChatAgentGroupSystemPrompt(groups []advancedChatAgentGroup) string {
	if len(groups) == 0 {
		return ""
	}
	lines := []string{
		"Agent Studios are available for CPS-style delegation.",
		"Use agent_delegate when a task should be handled by an existing studio member. Do not treat CPS delegation as agent splitting; choose one of the defined members by group_id and agent_id.",
		"Available Agent Studios:",
	}
	for _, group := range groups {
		lines = append(lines, "- group_id: "+group.ID+"; name: "+group.Name)
		for _, agent := range group.Agents {
			lines = append(lines, "  - agent_id: "+agent.ID+"; type: "+agent.Type+"; name: "+agent.Name)
		}
	}
	return strings.Join(lines, "\n")
}

func advancedChatAgentSplitSystemPrompt() string {
	return strings.TrimSpace(`Use agent_split when the current task can be divided into independent temporary sibling-agent tasks that share this conversation history.
Use agent_delegate instead when you need a specific existing member from an Agent Studio.
You may call agent_split with several tasks at once. Each split agent returns a result to you; you remain responsible for combining the results and producing the final answer.`)
}

func advancedChatAgentGroupChatSystemPrompt(group *advancedChatAgentGroup, agent *advancedChatGroupAgent) string {
	if group == nil || agent == nil {
		return ""
	}
	return strings.TrimSpace("You are participating in an Agent Studio chat.\n" +
		"Studio: " + group.Name + " (" + group.ID + ").\n" +
		"Your agent identity: " + agent.Name + " (" + agent.ID + "), type: " + normalizeAdvancedChatAgentType(agent.Type) + ".\n" +
		advancedChatAgentTypeSystemPrompt(agent.Type) + "\n" +
		"Messages are annotated as coming from the user or from another named agent. Use those annotations to understand who is speaking.\n" +
		"Reply as this agent only. Start every visible reply with [" + agent.Name + "].\n" +
		"All user-facing Studio messages are received by the Chief. Coordinate members through delegation rather than asking the user to address members directly.")
}

type advancedChatAgentDelegateInput struct {
	UserID             uint
	RunID              string
	SessionID          string
	ToolCallID         string
	ModelName          string
	UserChannelID      uint
	Messages           []ChatExecutorMessage
	WorkspaceSkills    []advancedChatWorkspaceSkill
	ConnectorDevice    *AdvancedChatConnectorDevice
	ConnectorWorkspace string
	ConnectorBindings  map[string]advancedChatConnectorToolBinding
	ConnectorTools     []ChatExecutorTool
	Groups             []advancedChatAgentGroup
	CallerAgentName    string
	Observer           advancedChatCompletionObserver
	OnApprovalRequired func(context.Context, MessageChannelConnectorApproval) error
	Arguments          map[string]interface{}
	DisplayRound       int
	ChargeBalance      bool
}

func executeAdvancedChatAgentDelegate(ctx context.Context, user *model.User, input advancedChatAgentDelegateInput) (string, error) {
	if user == nil {
		return "", errors.New("user is required")
	}
	groupID := strings.TrimSpace(stringFromMap(input.Arguments, "group_id"))
	agentID := strings.TrimSpace(stringFromMap(input.Arguments, "agent_id"))
	goal := strings.TrimSpace(stringFromMap(input.Arguments, "goal"))
	extraContext := strings.TrimSpace(stringFromMap(input.Arguments, "context"))
	if groupID == "" || agentID == "" || goal == "" {
		return "", errors.New("group_id, agent_id, and goal are required")
	}
	group, agent, ok := findAdvancedChatGroupAgent(input.Groups, groupID, agentID)
	if !ok {
		return "", errors.New("member was not found in Agent Studio")
	}
	if normalizeAdvancedChatAgentType(agent.Type) == "chief" {
		return "", errors.New("chief agents cannot be delegated execution tasks")
	}
	if normalizeAdvancedChatAgentType(agent.Type) == "checker" {
		return "", errors.New("checker agents can only be used for connector approval checks")
	}
	taskID := newAdvancedChatID("agt")
	emitTaskEvent := func(payload gin.H) {
		if strings.TrimSpace(stringFromMap(payload, "task_id")) == "" {
			payload["task_id"] = taskID
		}
		if strings.TrimSpace(stringFromMap(payload, "parent_id")) == "" {
			payload["parent_id"] = strings.TrimSpace(input.ToolCallID)
		}
		if strings.TrimSpace(stringFromMap(payload, "kind")) == "" {
			payload["kind"] = "cps"
		}
		if strings.TrimSpace(stringFromMap(payload, "group_id")) == "" {
			payload["group_id"] = group.ID
		}
		if strings.TrimSpace(stringFromMap(payload, "group_name")) == "" {
			payload["group_name"] = group.Name
		}
		if strings.TrimSpace(stringFromMap(payload, "agent_id")) == "" {
			payload["agent_id"] = agent.ID
		}
		if strings.TrimSpace(stringFromMap(payload, "agent_name")) == "" {
			payload["agent_name"] = agent.Name
		}
		if strings.TrimSpace(stringFromMap(payload, "agent_type")) == "" {
			payload["agent_type"] = normalizeAdvancedChatAgentType(agent.Type)
		}
		appendAdvancedChatAgentTaskEvent(input.RunID, input.SessionID, user.ID, payload)
	}
	emitTaskEvent(gin.H{
		"task_id":       taskID,
		"parent_id":     strings.TrimSpace(input.ToolCallID),
		"kind":          "cps",
		"status":        "running",
		"group_id":      group.ID,
		"group_name":    group.Name,
		"agent_id":      agent.ID,
		"agent_name":    agent.Name,
		"agent_type":    normalizeAdvancedChatAgentType(agent.Type),
		"goal":          goal,
		"context":       extraContext,
		"chat_agent_id": strings.TrimSpace(agent.ChatAgentID),
	})
	chatAgent, err := loadAdvancedChatAgent(user.ID, agent.ChatAgentID)
	if err != nil {
		emitTaskEvent(gin.H{"status": "error", "error": err.Error()})
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", errors.New("referenced chat agent was not found")
		}
		return "", err
	}
	skillIDs := []string{}
	mcpServerIDs := []string{}
	if chatAgent != nil {
		skillIDs = uniqueStringsLocal(decodeStringList(chatAgent.SkillIDs))
		mcpServerIDs = uniqueStringsLocal(decodeStringList(chatAgent.MCPServerIDs))
	}
	skillIDs = uniqueStringsLocal(append(skillIDs, agent.SkillIDs...))
	mcpServerIDs = uniqueStringsLocal(append(mcpServerIDs, agent.MCPServerIDs...))
	skills, err := loadAdvancedChatSkills(user.ID, skillIDs)
	if err != nil {
		emitTaskEvent(gin.H{"status": "error", "error": err.Error()})
		return "", err
	}
	if len(skills) != len(uniqueStringsLocal(skillIDs)) {
		emitTaskEvent(gin.H{"status": "error", "error": "referenced skill was not found"})
		return "", errors.New("referenced skill was not found")
	}
	serverIDs := uniqueStringsLocal(append(mcpServerIDs, skillMCPIDs(skills)...))
	servers := []AdvancedChatMCPServer{}
	if len(serverIDs) > 0 {
		if !advancedChatAssistantMCPToolsEnabled() {
			emitTaskEvent(gin.H{"status": "error", "error": "mcp tools are disabled"})
			return "", errors.New("mcp tools are disabled")
		}
		servers, err = loadAdvancedChatMCPServersForCall(user.ID, serverIDs)
		if err != nil {
			emitTaskEvent(gin.H{"status": "error", "error": err.Error()})
			return "", err
		}
	}
	modelName := strings.TrimSpace(agent.DefaultModel)
	if modelName == "" && chatAgent != nil {
		modelName = strings.TrimSpace(chatAgent.DefaultModel)
	}
	if modelName == "" {
		modelName = strings.TrimSpace(input.ModelName)
	}
	if modelName == "" {
		emitTaskEvent(gin.H{"status": "error", "error": "model is required for delegated agent"})
		return "", errors.New("model is required for delegated agent")
	}
	userChannelID := input.UserChannelID
	if chatAgent != nil && chatAgent.UserChannelID > 0 {
		userChannelID = chatAgent.UserChannelID
	} else if agent.UserChannelID > 0 {
		userChannelID = agent.UserChannelID
	}
	var delegatedDelta *advancedChatAgentStudioDeltaLog
	result, err := withAdvancedChatAgentStudioLock(user.ID, group.ID, agent.ID, func() (string, error) {
		delegatedCtx, delegatedCancel := newAdvancedChatDelegatedAgentContext(input.RunID, user.ID)
		defer delegatedCancel()
		agentType := normalizeAdvancedChatAgentType(agent.Type)
		if advancedChatAgentStudioCanUseExecutionTools(agentType) && agentType != "reviewer" {
			delegatedDelta = &advancedChatAgentStudioDeltaLog{}
		}
		systemParts := []string{
			"You are running as an employee main agent in Agent Studio. Complete only the delegated goal and return a concise result to the caller agent.",
			"Agent Studio: " + group.Name + " (" + group.ID + ")",
			"Agent: " + agent.Name + " (" + agent.ID + "), type: " + agent.Type,
			advancedChatAgentTypeSystemPrompt(agent.Type),
		}
		if prompt := buildAdvancedChatCompletionSystemPrompt(chatAgent, skills, input.WorkspaceSkills, advancedChatModeAssistant); strings.TrimSpace(prompt) != "" {
			systemParts = append(systemParts, prompt)
		}
		if prompt := strings.TrimSpace(agent.Prompt); prompt != "" {
			systemParts = append(systemParts, prompt)
		}
		if prompt := advancedChatConnectorSystemPrompt(input.ConnectorDevice, input.ConnectorWorkspace); strings.TrimSpace(prompt) != "" {
			systemParts = append(systemParts, prompt)
		}
		if prompt := advancedChatAgentStudioPrompt(agent.Type, input.ConnectorDevice != nil); prompt != "" {
			systemParts = append(systemParts, prompt)
		}
		messages := advancedChatMessagesForDelegatedAgent(input.Messages)
		taskText := "Delegated goal:\n" + goal
		if caller := strings.TrimSpace(input.CallerAgentName); caller != "" {
			taskText = "Message source: agent " + caller + " via CPS delegation.\n\n" + taskText
		}
		if extraContext != "" {
			taskText += "\n\nAdditional context:\n" + extraContext
		}
		messages = append(messages, ChatExecutorMessage{Role: "user", Content: taskText})
		tools := append([]ChatExecutorTool{}, input.ConnectorTools...)
		mcpBindings := map[string]mcpToolBinding{}
		if len(servers) > 0 {
			mcpTools, bindings, err := listAdvancedChatMCPTools(delegatedCtx, user.ID, input.RunID, input.ConnectorDevice, servers)
			if err != nil {
				return "", fmt.Errorf("failed to load delegated MCP tools: %w", err)
			}
			tools = append(mcpTools, tools...)
			mcpBindings = bindings
		}
		if advancedChatAgentStudioCanSplit(agent.Type) {
			tools = append(tools, advancedChatAgentSplitTool(), advancedChatAgentStudioInterruptTool(), advancedChatAgentStudioQueryStatusTool(), advancedChatAgentStudioResumeTool())
		}
		if normalizeAdvancedChatAgentType(agent.Type) == "reviewer" && input.ConnectorDevice != nil {
			tools = append(tools, advancedChatAgentStudioCommitDeltaTool())
		}
		return runAdvancedChatDelegatedAgentLoop(delegatedCtx, user, modelName, userChannelID, strings.Join(nonEmptyStrings(systemParts), "\n\n"), messages, tools, mcpBindings, input.ConnectorBindings, advancedChatDelegatedAgentLoopOptions{
			RunID:              input.RunID,
			SessionID:          input.SessionID,
			ParentToolCallID:   input.ToolCallID,
			Observer:           input.Observer,
			OnApprovalRequired: input.OnApprovalRequired,
			AllowSplit:         advancedChatAgentStudioCanSplit(agent.Type),
			AllowCommit:        normalizeAdvancedChatAgentType(agent.Type) == "reviewer",
			DeltaLog:           delegatedDelta,
			StatusAgentID:      agent.ID,
			StatusAgentName:    agent.Name,
			StatusAgentType:    normalizeAdvancedChatAgentType(agent.Type),
			StatusAgentGroupID: group.ID,
			SandboxID:          advancedChatAgentStudioSandboxID(input.RunID, group.ID, agent.ID),
			Stream:             chatAgent != nil && chatAgent.Stream,
			ApprovalChecker:    advancedChatAgentStudioApprovalCheckerForGroupValue(group),
			DisplayRound:       input.DisplayRound,
			ChargeBalance:      input.ChargeBalance,
		})
	})
	if err != nil {
		emitTaskEvent(gin.H{"status": "error", "error": err.Error(), "result": truncateToolResult(result)})
		return result, err
	}
	mutations := []advancedChatAgentStudioMutation{}
	if delegatedDelta != nil {
		mutations = delegatedDelta.snapshot()
		if len(mutations) > 0 {
			result = appendAdvancedChatAgentStudioMutationLog(result, mutations)
			commitResult, commitErr := commitDelegatedAdvancedChatAgentDelta(ctx, user, input, group, agent, mutations, userChannelID, input.DisplayRound)
			if commitErr != nil {
				result = strings.TrimSpace(result) + "\n\nAutoCommit failed: " + commitErr.Error()
				emitTaskEvent(gin.H{"status": "error", "error": commitErr.Error(), "result": truncateToolResult(result), "mutation_count": len(mutations)})
				return result, commitErr
			}
			result = appendAdvancedChatAgentStudioCommitResult(result, commitResult)
		}
	}
	result = advancedChatAgentNamePrefix(result, agent.Name)
	emitTaskEvent(gin.H{"status": "completed", "result": truncateToolResult(result), "mutation_count": len(mutations)})
	return result, nil
}

func commitDelegatedAdvancedChatAgentDelta(ctx context.Context, user *model.User, input advancedChatAgentDelegateInput, group advancedChatAgentGroup, agent advancedChatGroupAgent, mutations []advancedChatAgentStudioMutation, userChannelID uint, displayRound int) (string, error) {
	binding, ok := advancedChatAgentStudioCommitBinding(input.ConnectorBindings)
	if !ok {
		return "", errors.New("no connector workspace is available for final delta commit")
	}
	taskID := newAdvancedChatID("agt")
	reviewer := advancedChatAgentStudioReviewer(group)
	agentID := "auto-commit"
	agentName := "AutoCommit"
	agentType := "reviewer"
	if reviewer.ID != "" {
		agentID = reviewer.ID
		agentName = reviewer.Name
	}
	appendAdvancedChatAgentTaskEvent(input.RunID, input.SessionID, user.ID, gin.H{
		"task_id":         taskID,
		"parent_id":       strings.TrimSpace(input.ToolCallID),
		"kind":            "commit",
		"status":          "running",
		"group_id":        group.ID,
		"group_name":      group.Name,
		"agent_id":        agentID,
		"agent_name":      agentName,
		"agent_type":      agentType,
		"source_agent_id": agent.ID,
		"mutation_count":  len(mutations),
	})
	result, err := commitAdvancedChatAgentStudioDelta(ctx, user, input.RunID, input.SessionID, binding, map[string]interface{}{"mutations": mutations}, advancedChatAgentStudioApprovalCheckerForGroupValue(group), input.Observer, userChannelID, displayRound, input.ChargeBalance)
	if err != nil {
		appendAdvancedChatAgentTaskEvent(input.RunID, input.SessionID, user.ID, gin.H{
			"task_id":         taskID,
			"kind":            "commit",
			"status":          "error",
			"group_id":        group.ID,
			"agent_id":        agentID,
			"agent_name":      agentName,
			"agent_type":      agentType,
			"source_agent_id": agent.ID,
			"mutation_count":  len(mutations),
			"error":           err.Error(),
			"result":          truncateToolResult(result),
		})
		return result, err
	}
	appendAdvancedChatAgentTaskEvent(input.RunID, input.SessionID, user.ID, gin.H{
		"task_id":         taskID,
		"kind":            "commit",
		"status":          "completed",
		"group_id":        group.ID,
		"agent_id":        agentID,
		"agent_name":      agentName,
		"agent_type":      agentType,
		"source_agent_id": agent.ID,
		"mutation_count":  len(mutations),
		"result":          truncateToolResult(result),
	})
	return result, nil
}

func appendAdvancedChatAgentStudioCommitResult(content string, commitResult string) string {
	content = strings.TrimSpace(content)
	commitResult = strings.TrimSpace(commitResult)
	if commitResult == "" {
		commitResult = "MutationLog physically committed."
	}
	if content == "" {
		return "AutoCommit:\n" + commitResult
	}
	return content + "\n\nAutoCommit:\n" + commitResult
}

func advancedChatAgentStudioCommitBinding(bindings map[string]advancedChatConnectorToolBinding) (advancedChatConnectorToolBinding, bool) {
	for _, preferred := range []string{advancedChatConnectorToolWriteFile, advancedChatConnectorToolReplaceText, advancedChatConnectorToolReadFile, advancedChatConnectorToolListFiles, advancedChatConnectorToolRunCommand} {
		if binding, ok := bindings[preferred]; ok {
			return binding, true
		}
	}
	for _, binding := range bindings {
		return binding, true
	}
	return advancedChatConnectorToolBinding{}, false
}

func advancedChatAgentStudioReviewer(group advancedChatAgentGroup) advancedChatGroupAgent {
	for _, agent := range group.Agents {
		if normalizeAdvancedChatAgentType(agent.Type) == "reviewer" {
			return agent
		}
	}
	return advancedChatGroupAgent{}
}

func advancedChatAgentNamePrefix(content string, name string) string {
	content = strings.TrimSpace(content)
	name = strings.TrimSpace(name)
	if content == "" || name == "" {
		return content
	}
	prefix := "[" + name + "]"
	if strings.HasPrefix(content, prefix) {
		return content
	}
	return prefix + " " + content
}

func appendAdvancedChatAgentStudioMutationLog(content string, mutations []advancedChatAgentStudioMutation) string {
	if len(mutations) == 0 {
		return strings.TrimSpace(content)
	}
	data, err := json.MarshalIndent(gin.H{"mutations": mutations}, "", "  ")
	if err != nil {
		return strings.TrimSpace(content)
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return "MutationLog:\n```json\n" + string(data) + "\n```"
	}
	return content + "\n\nMutationLog:\n```json\n" + string(data) + "\n```"
}

func newAdvancedChatDelegatedAgentContext(runID string, userID uint) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(advancedChatAgentGroupRunTimeoutSeconds())*time.Second)
	runID = strings.TrimSpace(runID)
	if runID == "" || userID == 0 {
		return ctx, cancel
	}
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := ensureAdvancedChatRunNotCancelled(runID, userID); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel
}

func runAdvancedChatDelegatedAgentLoop(ctx context.Context, user *model.User, modelName string, userChannelID uint, system string, messages []ChatExecutorMessage, tools []ChatExecutorTool, mcpBindings map[string]mcpToolBinding, connectorBindings map[string]advancedChatConnectorToolBinding, options advancedChatDelegatedAgentLoopOptions) (string, error) {
	executorMessages := append([]ChatExecutorMessage{}, messages...)
	lastContent := ""
	lastToolResult := ""
	lastToolStatus := ""
	observer := advancedChatDelegatedModelObserver(options, user.ID)
	for round := 0; round < 6; round++ {
		var onTextDelta func(string) error
		if options.Stream {
			onTextDelta = func(delta string) error {
				return nil
			}
		}
		result, err := executeAdvancedChatModelRequestWithRetry(ctx, user, ChatExecutorRequest{
			Context:       ctx,
			ModelName:     modelName,
			UserChannelID: userChannelID,
			Messages:      executorMessages,
			System:        system,
			Tools:         tools,
			MaxTokens:     0,
			Stream:        options.Stream,
			OnTextDelta:   onTextDelta,
			ChargeBalance: options.ChargeBalance,
		}, observer, func() bool { return true })
		if err != nil {
			return strings.TrimSpace(lastContent), err
		}
		lastContent = result.Content
		appendAdvancedChatAgentMessageEvent(options, user.ID, "assistant", result.Content, "", "running")
		if len(result.ToolCalls) == 0 {
			return strings.TrimSpace(result.Content), nil
		}
		executorMessages = append(executorMessages, ChatExecutorMessage{
			Role:      "assistant",
			Content:   result.Content,
			ToolCalls: normalizeAssistantToolCalls(result.AssistantMessage),
		})
		for _, call := range result.ToolCalls {
			mcpBinding, mcpExists := mcpBindings[call.Name]
			connectorBinding, connectorExists := connectorBindings[call.Name]
			agentSplitExists := call.Name == advancedChatAgentSplitToolName && options.AllowSplit
			commitDeltaExists := call.Name == advancedChatAgentStudioCommitDeltaToolName && options.AllowCommit
			interruptExists := call.Name == advancedChatAgentStudioInterruptToolName
			queryStatusExists := call.Name == advancedChatAgentStudioQueryStatusToolName
			resumeExists := call.Name == advancedChatAgentStudioResumeToolName
			detail := advancedChatCompletionToolCall{ID: call.ID, Round: advancedChatDelegatedDisplayRound(options, round+1), Name: call.Name, Status: "running"}
			precreatedConnectorTaskID := ""
			var precreateConnectorTaskErr error
			if mcpExists {
				detail.Server = mcpBinding.Server.Name
				detail.Tool = mcpBinding.Tool.Name
			} else if connectorExists {
				detail.Server = connectorBinding.DeviceName
				detail.Tool = connectorBinding.Action
			} else if agentSplitExists {
				detail.Server = "agent split"
				detail.Tool = "agent_split"
			} else if commitDeltaExists {
				detail.Server = "agent studio"
				detail.Tool = "workspace_commit_delta"
			} else if interruptExists {
				detail.Server = "agent studio"
				detail.Tool = "interrupt_sub_agents"
			} else if queryStatusExists {
				detail.Server = "agent studio"
				detail.Tool = "query_sub_agent_status"
			} else if resumeExists {
				detail.Server = "agent studio"
				detail.Tool = "resume_sub_agents"
			}
			toolResult := "Tool not found for delegated agent: " + call.Name
			arguments, parseErr := parseToolArguments(call.Arguments)
			if parseErr == nil {
				if connectorExists {
					arguments = advancedChatAgentStudioSandboxArguments(arguments, options.SandboxID)
					arguments = advancedChatConnectorToolPreviewArguments(ctx, user.ID, options.RunID, connectorBinding, arguments)
					arguments = advancedChatConnectorArgumentsWithToolCallID(arguments, call.ID)
				}
				detail.Arguments = arguments
			}
			if connectorExists && parseErr == nil && advancedChatAgentStudioConnectorTaskRequiresApproval(connectorBinding, arguments, options.DeltaLog) {
				task, err := createAdvancedChatConnectorTask(user.ID, options.RunID, connectorBinding, arguments)
				if err != nil {
					precreateConnectorTaskErr = err
					detail.Status = "error"
				} else {
					precreatedConnectorTaskID = task.ID
					arguments = advancedChatConnectorArgumentsWithTaskID(arguments, task.ID)
					detail.Arguments = arguments
					detail.Status = "approval_required"
					if options.OnApprovalRequired != nil {
						approval := MessageChannelConnectorApproval{
							TaskID:        task.ID,
							DeviceName:    connectorBinding.DeviceName,
							Action:        connectorBinding.Action,
							WorkspacePath: connectorBinding.WorkspacePath,
							Unrestricted:  strings.TrimSpace(connectorBinding.WorkspacePath) == "",
							Arguments:     arguments,
						}
						if err := options.OnApprovalRequired(ctx, approval); err != nil {
							return strings.TrimSpace(lastContent), err
						}
					}
					if options.ApprovalChecker != nil {
						if value, err := approveAdvancedChatConnectorTaskWithChecker(ctx, user, options.RunID, options.SessionID, options.ApprovalChecker, task, connectorBinding, arguments, options.Observer, userChannelID, advancedChatDelegatedDisplayRound(options, round+1), options.ChargeBalance); err != nil {
							precreateConnectorTaskErr = err
							toolResult = "Checker approval failed: " + err.Error()
						} else if strings.TrimSpace(value) != "" {
							toolResult = value
						}
					}
				}
			}
			if options.Observer.OnToolCall != nil {
				if err := options.Observer.OnToolCall(detail); err != nil {
					return strings.TrimSpace(lastContent), err
				}
			}
			detail.Status = "missing"
			if !mcpExists && !connectorExists && !agentSplitExists && !commitDeltaExists && !interruptExists && !queryStatusExists && !resumeExists {
				// Delegated agents deliberately do not get agent_delegate again.
			} else if parseErr != nil {
				detail.Status = "invalid_arguments"
				toolResult = "Invalid tool arguments: " + parseErr.Error()
			} else if mcpExists {
				value, err := mcpBinding.Client.callTool(ctx, mcpBinding.Tool.Name, arguments)
				if err != nil {
					detail.Status = "error"
					toolResult = "MCP tool failed: " + err.Error()
				} else {
					detail.Status = "ok"
					toolResult = value.Text
					if value.IsError {
						detail.Status = "error"
						toolResult = "MCP tool returned an error: " + toolResult
					}
				}
			} else if connectorExists {
				var value string
				var err error
				if precreateConnectorTaskErr != nil {
					err = precreateConnectorTaskErr
					toolResult = "Connector task unavailable: " + precreateConnectorTaskErr.Error()
				} else if precreatedConnectorTaskID != "" {
					value, err = waitAdvancedChatConnectorTask(ctx, precreatedConnectorTaskID, user.ID)
				} else {
					toolCtx, cancel := context.WithTimeout(ctx, advancedChatDelegatedToolWait)
					value, err = executeAdvancedChatConnectorToolForAgent(toolCtx, user.ID, options.RunID, connectorBinding, arguments, options.DeltaLog)
					cancel()
				}
				if err != nil {
					detail.Status = "error"
					if precreateConnectorTaskErr == nil {
						toolResult = "Connector tool failed: " + err.Error()
					}
					if strings.TrimSpace(value) != "" {
						toolResult = strings.TrimSpace(value) + "\n\n" + toolResult
					}
				} else {
					detail.Status = "ok"
					toolResult = value
					if connectorBinding.Action == "run_command" && precreatedConnectorTaskID != "" {
						appendAdvancedChatAgentStudioDeltaMutations(options.DeltaLog, advancedChatAgentStudioSandboxMutations(value))
					}
				}
			} else if agentSplitExists {
				splitTools := filterAdvancedChatToolsByName(tools, map[string]bool{
					advancedChatAgentSplitToolName:             true,
					advancedChatAgentStudioCommitDeltaToolName: true,
					advancedChatAgentStudioInterruptToolName:   true,
					advancedChatAgentStudioQueryStatusToolName: true,
					advancedChatAgentStudioResumeToolName:      true,
				})
				value, err := executeAdvancedChatAgentSplit(ctx, user, advancedChatAgentSplitInput{
					RunID:              options.RunID,
					SessionID:          options.SessionID,
					ToolCallID:         call.ID,
					ModelName:          modelName,
					UserChannelID:      userChannelID,
					SystemPrompt:       system,
					Messages:           executorMessages,
					Tools:              splitTools,
					MCPBindings:        mcpBindings,
					ConnectorBindings:  connectorBindings,
					Observer:           options.Observer,
					OnApprovalRequired: options.OnApprovalRequired,
					Stream:             options.Stream,
					DeltaLog:           options.DeltaLog,
					Arguments:          arguments,
					ApprovalChecker:    options.ApprovalChecker,
					DisplayRound:       advancedChatDelegatedDisplayRound(options, round+1),
					ChargeBalance:      options.ChargeBalance,
				})
				if err != nil {
					detail.Status = "error"
					toolResult = "Split agent failed: " + err.Error()
				} else {
					detail.Status = "ok"
					toolResult = value
				}
			} else if commitDeltaExists {
				if len(connectorBindings) == 0 {
					detail.Status = "error"
					toolResult = "No connector workspace is available for commit."
				} else {
					var commitBinding advancedChatConnectorToolBinding
					for _, binding := range connectorBindings {
						commitBinding = binding
						break
					}
					value, err := commitAdvancedChatAgentStudioDelta(ctx, user, options.RunID, options.SessionID, commitBinding, arguments, options.ApprovalChecker, options.Observer, userChannelID, advancedChatDelegatedDisplayRound(options, round+1), options.ChargeBalance)
					if err != nil {
						detail.Status = "error"
						toolResult = "Delta commit failed: " + err.Error()
						if strings.TrimSpace(value) != "" {
							toolResult = strings.TrimSpace(value) + "\n\n" + toolResult
						}
					} else {
						detail.Status = "ok"
						toolResult = value
					}
				}
			} else if interruptExists {
				value, err := interruptAdvancedChatAgentStudioSubAgents(options.RunID, options.SessionID, user.ID, arguments)
				if err != nil {
					detail.Status = "error"
					toolResult = "Sub-agent interrupt failed: " + err.Error()
				} else {
					detail.Status = "ok"
					toolResult = value
				}
			} else if queryStatusExists {
				value, err := queryAdvancedChatAgentStudioSubAgentStatus(options.RunID, user.ID, arguments)
				if err != nil {
					detail.Status = "error"
					toolResult = "Sub-agent status query failed: " + err.Error()
				} else {
					detail.Status = "ok"
					toolResult = value
				}
			} else if resumeExists {
				value, err := resumeAdvancedChatAgentStudioSubAgents(options.RunID, options.SessionID, user.ID, arguments)
				if err != nil {
					detail.Status = "error"
					toolResult = "Sub-agent resume failed: " + err.Error()
				} else {
					detail.Status = "ok"
					toolResult = value
				}
			}
			if trimmed := strings.TrimSpace(toolResult); trimmed != "" {
				lastToolResult = trimmed
				lastToolStatus = detail.Status
			}
			appendAdvancedChatAgentMessageEvent(options, user.ID, "tool", toolResult, detail.Tool, detail.Status)
			detail.Result = truncateToolResult(toolResult)
			if options.Observer.OnToolCall != nil {
				if err := options.Observer.OnToolCall(detail); err != nil {
					return strings.TrimSpace(lastContent), err
				}
			}
			executorMessages = append(executorMessages, ChatExecutorMessage{
				Role:       "tool",
				Content:    truncateToolResult(toolResult),
				ToolCallID: call.ID,
				Name:       call.Name,
			})
		}
	}
	if content := strings.TrimSpace(lastContent); content != "" {
		return content, nil
	}
	if strings.TrimSpace(lastToolResult) != "" && strings.EqualFold(strings.TrimSpace(lastToolStatus), "ok") {
		return "Completed delegated work. Last tool result:\n" + truncateToolResult(lastToolResult), nil
	}
	return "", errors.New("delegated agent reached the tool round limit without a final result")
}

func advancedChatDelegatedModelObserver(options advancedChatDelegatedAgentLoopOptions, userID uint) advancedChatCompletionObserver {
	return advancedChatCompletionObserver{
		OnStatus: func(payload gin.H) error {
			message := strings.TrimSpace(stringFromMap(payload, "message"))
			if message == "retrying" {
				attempt := intFromStatusPayload(payload, "attempt")
				maxAttempts := intFromStatusPayload(payload, "max")
				status := intFromStatusPayload(payload, "status")
				channelID := intFromStatusPayload(payload, "channel_id")
				errorText := strings.TrimSpace(stringFromMap(payload, "error"))
				parts := []string{}
				if attempt > 0 && maxAttempts > 0 {
					parts = append(parts, fmt.Sprintf("retrying:%d/%d", attempt, maxAttempts))
				} else {
					parts = append(parts, "retrying")
				}
				if status > 0 {
					parts = append(parts, fmt.Sprintf("status=%d", status))
				}
				if channelID > 0 {
					parts = append(parts, fmt.Sprintf("channel=%d", channelID))
				}
				if errorText != "" {
					parts = append(parts, errorText)
				}
				appendAdvancedChatAgentMessageEvent(options, userID, "system", strings.Join(parts, " "), "", "running")
			}
			if options.Observer.OnStatus != nil {
				return options.Observer.OnStatus(payload)
			}
			return nil
		},
	}
}

func intFromStatusPayload(payload gin.H, key string) int {
	value, exists := payload[key]
	if !exists {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case uint:
		return int(typed)
	case uint64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		result, _ := typed.Int64()
		return int(result)
	default:
		return 0
	}
}

func advancedChatDelegatedDisplayRound(options advancedChatDelegatedAgentLoopOptions, fallback int) int {
	if options.DisplayRound > 0 {
		return options.DisplayRound
	}
	return fallback
}

type advancedChatAgentSplitInput struct {
	RunID              string
	SessionID          string
	ToolCallID         string
	ModelName          string
	UserChannelID      uint
	SystemPrompt       string
	Messages           []ChatExecutorMessage
	Tools              []ChatExecutorTool
	MCPBindings        map[string]mcpToolBinding
	ConnectorBindings  map[string]advancedChatConnectorToolBinding
	Observer           advancedChatCompletionObserver
	OnApprovalRequired func(context.Context, MessageChannelConnectorApproval) error
	Stream             bool
	DeltaLog           *advancedChatAgentStudioDeltaLog
	Arguments          map[string]interface{}
	ApprovalChecker    *advancedChatAgentStudioApprovalChecker
	DisplayRound       int
	ChargeBalance      bool
}

func executeAdvancedChatAgentSplit(ctx context.Context, user *model.User, input advancedChatAgentSplitInput) (string, error) {
	if user == nil {
		return "", errors.New("user is required")
	}
	tasks := parseAdvancedChatSplitTasks(input.Arguments)
	if len(tasks) == 0 {
		return "", errors.New("tasks are required")
	}
	results := make([]map[string]interface{}, len(tasks))
	var wg sync.WaitGroup
	for index, task := range tasks {
		wg.Add(1)
		go func(index int, task advancedChatSplitTask) {
			defer wg.Done()
			taskID := newAdvancedChatID("agt")
			label := strings.TrimSpace(task.ID)
			if label == "" {
				label = fmt.Sprintf("split-%d", index+1)
			}
			appendAdvancedChatAgentTaskEvent(input.RunID, input.SessionID, user.ID, gin.H{
				"task_id":    taskID,
				"parent_id":  strings.TrimSpace(input.ToolCallID),
				"kind":       "split",
				"status":     "running",
				"agent_id":   label,
				"agent_name": label,
				"agent_type": "worker",
				"goal":       task.Goal,
			})
			messages := advancedChatMessagesForDelegatedAgent(input.Messages)
			taskText := "Split agent goal:\n" + task.Goal
			if strings.TrimSpace(task.Context) != "" {
				taskText += "\n\nAdditional context:\n" + strings.TrimSpace(task.Context)
			}
			messages = append(messages, ChatExecutorMessage{Role: "user", Content: taskText})
			system := strings.Join(nonEmptyStrings([]string{
				input.SystemPrompt,
				"You are a temporary split worker agent. Work only on the split goal and return a concise result to the caller agent. File writes are deferred into your MutationLog and are not physically written to disk.",
				advancedChatAgentTypeSystemPrompt("worker"),
			}), "\n\n")
			delta := &advancedChatAgentStudioDeltaLog{}
			result, err := runAdvancedChatDelegatedAgentLoop(ctx, user, input.ModelName, input.UserChannelID, system, messages, input.Tools, input.MCPBindings, input.ConnectorBindings, advancedChatDelegatedAgentLoopOptions{
				RunID:              input.RunID,
				SessionID:          input.SessionID,
				ParentToolCallID:   input.ToolCallID,
				Observer:           input.Observer,
				OnApprovalRequired: input.OnApprovalRequired,
				AllowSplit:         false,
				DeltaLog:           delta,
				StatusAgentID:      label,
				StatusAgentName:    label,
				StatusAgentType:    "worker",
				SandboxID:          advancedChatAgentStudioSandboxID(input.RunID, strings.TrimSpace(input.ToolCallID), label),
				Stream:             input.Stream,
				ApprovalChecker:    input.ApprovalChecker,
				DisplayRound:       input.DisplayRound,
				ChargeBalance:      input.ChargeBalance,
			})
			mutations := delta.snapshot()
			appendAdvancedChatAgentStudioDeltaMutations(input.DeltaLog, mutations)
			if err != nil {
				appendAdvancedChatAgentTaskEvent(input.RunID, input.SessionID, user.ID, gin.H{"task_id": taskID, "status": "error", "error": err.Error(), "result": truncateToolResult(result), "mutation_count": len(mutations)})
				results[index] = map[string]interface{}{"id": label, "status": "error", "error": err.Error(), "result": strings.TrimSpace(result), "mutations": mutations}
				return
			}
			appendAdvancedChatAgentTaskEvent(input.RunID, input.SessionID, user.ID, gin.H{"task_id": taskID, "status": "completed", "result": truncateToolResult(result), "mutation_count": len(mutations)})
			results[index] = map[string]interface{}{"id": label, "status": "completed", "result": strings.TrimSpace(result), "mutations": mutations}
		}(index, task)
	}
	wg.Wait()
	data, err := json.Marshal(results)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func advancedChatMessagesForDelegatedAgent(messages []ChatExecutorMessage) []ChatExecutorMessage {
	result := append([]ChatExecutorMessage{}, messages...)
	if len(result) == 0 {
		return result
	}
	last := &result[len(result)-1]
	if strings.ToLower(strings.TrimSpace(last.Role)) != "assistant" || len(last.ToolCalls) == 0 {
		return result
	}
	if strings.TrimSpace(last.Content) == "" && len(last.Parts) == 0 {
		return result[:len(result)-1]
	}
	last.ToolCalls = nil
	return result
}

type advancedChatSplitTask struct {
	ID      string
	Goal    string
	Context string
}

func parseAdvancedChatSplitTasks(arguments map[string]interface{}) []advancedChatSplitTask {
	raw, ok := arguments["tasks"].([]interface{})
	if !ok {
		return nil
	}
	tasks := []advancedChatSplitTask{}
	for _, item := range raw {
		row, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		goal := strings.TrimSpace(stringFromMap(row, "goal"))
		if goal == "" {
			continue
		}
		tasks = append(tasks, advancedChatSplitTask{
			ID:      truncateAdvancedChatAgentField(stringFromMap(row, "id"), 80),
			Goal:    truncateAdvancedChatAgentField(goal, 4000),
			Context: truncateAdvancedChatAgentField(stringFromMap(row, "context"), 4000),
		})
		if len(tasks) >= 8 {
			break
		}
	}
	return tasks
}

func appendAdvancedChatAgentTaskEvent(runID string, sessionID string, userID uint, payload gin.H) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return
	}
	if strings.HasPrefix(runID, "msgch-") {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		var run AdvancedChatRun
		if err := model.DB.Select("session_id").Where("id = ? AND user_id = ?", runID, userID).First(&run).Error; err != nil {
			return
		}
		sessionID = run.SessionID
	}
	_ = appendAdvancedChatRunEvent(runID, sessionID, userID, "agent_task", payload)
}

func appendAdvancedChatAgentMessageEvent(options advancedChatDelegatedAgentLoopOptions, userID uint, role string, content string, tool string, status string) {
	runID := strings.TrimSpace(options.RunID)
	groupID := strings.TrimSpace(options.StatusAgentGroupID)
	agentID := strings.TrimSpace(options.StatusAgentID)
	content = strings.TrimSpace(content)
	if strings.HasPrefix(runID, "msgch-") {
		return
	}
	if runID == "" || groupID == "" || agentID == "" || content == "" {
		return
	}
	_ = appendAdvancedChatRunEvent(runID, strings.TrimSpace(options.SessionID), userID, "agent_message", gin.H{
		"group_id":   groupID,
		"agent_id":   agentID,
		"agent_name": strings.TrimSpace(options.StatusAgentName),
		"agent_type": strings.TrimSpace(options.StatusAgentType),
		"role":       normalizedAdvancedChatAgentWorkMessageRole(role),
		"content":    truncateToolResult(content),
		"tool":       strings.TrimSpace(tool),
		"status":     strings.TrimSpace(status),
	})
}

func findAdvancedChatGroupAgent(groups []advancedChatAgentGroup, groupID string, agentID string) (advancedChatAgentGroup, advancedChatGroupAgent, bool) {
	for _, group := range groups {
		if group.ID != groupID {
			continue
		}
		for _, agent := range group.Agents {
			if agent.ID == agentID {
				return group, agent, true
			}
		}
	}
	return advancedChatAgentGroup{}, advancedChatGroupAgent{}, false
}

func nonEmptyStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text := strings.TrimSpace(value); text != "" {
			result = append(result, text)
		}
	}
	return result
}
