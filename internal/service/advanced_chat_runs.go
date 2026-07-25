package service

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const (
	advancedChatRunStatusQueued    = "queued"
	advancedChatRunStatusRunning   = "running"
	advancedChatRunStatusCompleted = "completed"
	advancedChatRunStatusFailed    = "failed"
	advancedChatRunStatusCancelled = "cancelled"
)

type advancedChatRunEventState struct {
	mutex               sync.Mutex
	sequenceInitialized bool
	nextSequence        int
}

var advancedChatRunEventStates sync.Map

func advancedChatRunEventStateFor(userID uint, runID string) *advancedChatRunEventState {
	key := fmt.Sprintf("%d:%s", userID, strings.TrimSpace(runID))
	value, _ := advancedChatRunEventStates.LoadOrStore(key, &advancedChatRunEventState{})
	return value.(*advancedChatRunEventState)
}

type AdvancedChatSession struct {
	ID                       string     `gorm:"primaryKey;size:80" json:"id"`
	UserID                   uint       `gorm:"index;not null" json:"user_id"`
	User                     model.User `gorm:"foreignKey:UserID" json:"-"`
	FolderID                 string     `gorm:"index;size:80" json:"folder_id,omitempty"`
	Title                    string     `gorm:"size:200;not null" json:"title"`
	RunMode                  string     `gorm:"size:20;not null" json:"run_mode"`
	AgentID                  string     `gorm:"size:80" json:"agent_id"`
	AgentGroupID             string     `gorm:"size:80" json:"agent_group_id"`
	SkillIDs                 string     `gorm:"type:text;not null" json:"-"`
	MCPServerIDs             string     `gorm:"type:text;not null" json:"-"`
	KnowledgeBaseIDs         string     `gorm:"type:text;not null;default:'[]'" json:"-"`
	ConnectorDeviceID        string     `gorm:"size:80" json:"connector_device_id"`
	ConnectorWorkspacePath   string     `gorm:"type:text" json:"connector_workspace_path"`
	CloudSandboxID           string     `gorm:"size:80;index" json:"cloud_sandbox_id"`
	ConnectorAutoApprove     bool       `gorm:"default:false" json:"connector_auto_approve"`
	ConnectorApprovalMode    string     `gorm:"size:20;not null;default:'manual'" json:"connector_approval_mode"`
	ConnectorCommandPrefixes string     `gorm:"type:text;not null;default:'[]'" json:"-"`
	ModelName                string     `gorm:"size:100" json:"model_name"`
	UserChannelID            uint       `gorm:"index" json:"user_channel_id"`
	MaxTokens                int        `gorm:"default:0" json:"max_tokens"`
	Temperature              *float64   `json:"temperature"`
	ReasoningEffort          string     `gorm:"size:20" json:"reasoning_effort"`
	AutoCompressContext      bool       `gorm:"default:true" json:"auto_compress_context"`
	CreatedAt                time.Time  `json:"created_at"`
	UpdatedAt                time.Time  `json:"updated_at"`
}

type AdvancedChatSessionFolder struct {
	ID        string     `gorm:"primaryKey;size:80" json:"id"`
	UserID    uint       `gorm:"index;not null" json:"user_id"`
	User      model.User `gorm:"foreignKey:UserID" json:"-"`
	Name      string     `gorm:"size:80;not null" json:"name"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type AdvancedChatMessage struct {
	ID           string     `gorm:"primaryKey;size:80" json:"id"`
	SessionID    string     `gorm:"index;not null" json:"session_id"`
	UserID       uint       `gorm:"index;not null" json:"user_id"`
	User         model.User `gorm:"foreignKey:UserID" json:"-"`
	Role         string     `gorm:"size:20;not null" json:"role"`
	Content      string     `gorm:"type:text;not null" json:"content"`
	ContentParts string     `gorm:"type:text;not null;default:'[]'" json:"-"`
	ToolCalls    string     `gorm:"type:text;not null" json:"-"`
	SortOrder    int        `gorm:"index;not null" json:"sort_order"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type AdvancedChatRun struct {
	ID                 string          `gorm:"primaryKey;size:80" json:"id"`
	SessionID          string          `gorm:"index;not null" json:"session_id"`
	UserID             uint            `gorm:"index;not null" json:"user_id"`
	User               model.User      `gorm:"foreignKey:UserID" json:"-"`
	AssistantMessageID string          `gorm:"size:80;not null" json:"assistant_message_id"`
	Mode               string          `gorm:"size:20;not null" json:"mode"`
	Status             string          `gorm:"size:20;index;not null" json:"status"`
	StatusMessage      string          `gorm:"size:80" json:"status_message"`
	CurrentRound       int             `gorm:"default:0" json:"current_round"`
	ErrorMessage       string          `gorm:"type:text;not null" json:"error_message"`
	Cost               decimal.Decimal `gorm:"type:decimal(20,10);not null" json:"cost"`
	ToolCalls          int             `gorm:"default:0" json:"tool_calls"`
	ToolCallDetails    string          `gorm:"type:text;not null" json:"-"`
	StartedAt          *time.Time      `json:"started_at"`
	FinishedAt         *time.Time      `json:"finished_at"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

type AdvancedChatRunEvent struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	RunID     string    `gorm:"uniqueIndex:idx_advanced_chat_run_event_seq;size:80;not null" json:"run_id"`
	SessionID string    `gorm:"index;size:80;not null" json:"session_id"`
	UserID    uint      `gorm:"index;not null" json:"user_id"`
	Seq       int       `gorm:"uniqueIndex:idx_advanced_chat_run_event_seq;not null" json:"seq"`
	Event     string    `gorm:"size:40;not null" json:"event"`
	Payload   string    `gorm:"type:text;not null" json:"-"`
	CreatedAt time.Time `json:"created_at"`
}

type advancedChatMessageResponse struct {
	ID        string                           `json:"id"`
	Role      string                           `json:"role"`
	Content   string                           `json:"content"`
	Parts     []advancedChatContentPart        `json:"content_parts,omitempty"`
	ToolCalls []advancedChatCompletionToolCall `json:"tool_calls,omitempty"`
	CreatedAt time.Time                        `json:"created_at"`
	UpdatedAt time.Time                        `json:"updated_at"`
}

type advancedChatRunResponse struct {
	ID                 string                           `json:"id"`
	SessionID          string                           `json:"session_id"`
	AssistantMessageID string                           `json:"assistant_message_id"`
	Mode               string                           `json:"mode"`
	Status             string                           `json:"status"`
	StatusMessage      string                           `json:"status_message"`
	CurrentRound       int                              `json:"current_round"`
	ErrorMessage       string                           `json:"error_message,omitempty"`
	Cost               decimal.Decimal                  `json:"cost"`
	ToolCalls          int                              `json:"tool_calls"`
	ToolCallDetails    []advancedChatCompletionToolCall `json:"tool_call_details,omitempty"`
	StartedAt          *time.Time                       `json:"started_at,omitempty"`
	FinishedAt         *time.Time                       `json:"finished_at,omitempty"`
	CreatedAt          time.Time                        `json:"created_at"`
	UpdatedAt          time.Time                        `json:"updated_at"`
}

type advancedChatSessionResponse struct {
	ID                       string                        `json:"id"`
	FolderID                 string                        `json:"folder_id,omitempty"`
	Title                    string                        `json:"title"`
	Messages                 []advancedChatMessageResponse `json:"messages"`
	RunMode                  string                        `json:"run_mode"`
	AgentID                  string                        `json:"agent_id,omitempty"`
	AgentGroupID             string                        `json:"agent_group_id,omitempty"`
	SkillIDs                 []string                      `json:"skill_ids"`
	MCPServerIDs             []string                      `json:"mcp_server_ids"`
	KnowledgeBaseIDs         []string                      `json:"knowledge_base_ids"`
	ConnectorDeviceID        string                        `json:"connector_device_id,omitempty"`
	ConnectorWorkspacePath   string                        `json:"connector_workspace_path,omitempty"`
	CloudSandboxID           string                        `json:"cloud_sandbox_id,omitempty"`
	ConnectorAutoApprove     bool                          `json:"connector_auto_approve"`
	ConnectorApprovalMode    string                        `json:"connector_approval_mode"`
	ConnectorCommandPrefixes []string                      `json:"connector_command_prefixes"`
	ModelName                string                        `json:"model_name,omitempty"`
	UserChannelID            uint                          `json:"user_channel_id,omitempty"`
	MaxTokens                int                           `json:"max_tokens,omitempty"`
	Temperature              *float64                      `json:"temperature,omitempty"`
	ReasoningEffort          string                        `json:"reasoning_effort,omitempty"`
	AutoCompressContext      bool                          `json:"auto_compress_context"`
	LatestRun                *advancedChatRunResponse      `json:"latest_run,omitempty"`
	CreatedAt                time.Time                     `json:"created_at"`
	UpdatedAt                time.Time                     `json:"updated_at"`
}

type advancedChatSessionFolderInput struct {
	Name string `json:"name"`
}

type advancedChatSessionFolderMoveInput struct {
	FolderID string `json:"folder_id"`
}

type advancedChatRunEventResponse struct {
	ID        uint                   `json:"id"`
	RunID     string                 `json:"run_id"`
	SessionID string                 `json:"session_id"`
	Seq       int                    `json:"seq"`
	Event     string                 `json:"event"`
	Payload   map[string]interface{} `json:"payload"`
	CreatedAt time.Time              `json:"created_at"`
}

type advancedChatAgentTaskListItem struct {
	RunID         string                         `json:"run_id"`
	SessionID     string                         `json:"session_id"`
	SessionTitle  string                         `json:"session_title"`
	Status        string                         `json:"status"`
	StatusMessage string                         `json:"status_message"`
	StartedAt     *time.Time                     `json:"started_at,omitempty"`
	UpdatedAt     time.Time                      `json:"updated_at"`
	Events        []advancedChatRunEventResponse `json:"events"`
}

type advancedChatAgentWorkResponse struct {
	RunID          string                               `json:"run_id"`
	SessionID      string                               `json:"session_id"`
	GroupID        string                               `json:"group_id"`
	GroupName      string                               `json:"group_name"`
	Agents         []advancedChatAgentWorkStatus        `json:"agents"`
	ConnectorTasks []advancedChatAgentWorkConnectorTask `json:"connector_tasks"`
}

type advancedChatAgentWorkStatus struct {
	AgentID   string                         `json:"agent_id"`
	AgentName string                         `json:"agent_name"`
	AgentType string                         `json:"agent_type"`
	GroupID   string                         `json:"group_id"`
	GroupName string                         `json:"group_name"`
	Status    string                         `json:"status"`
	Working   bool                           `json:"working"`
	UpdatedAt *time.Time                     `json:"updated_at,omitempty"`
	Messages  []advancedChatAgentWorkMessage `json:"messages"`
}

type advancedChatAgentWorkMessage struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	Status    string     `json:"status,omitempty"`
	Tool      string     `json:"tool,omitempty"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
}

type advancedChatAgentWorkConnectorTask struct {
	ID                    string                 `json:"id"`
	DeviceID              string                 `json:"device_id"`
	DeviceName            string                 `json:"device_name"`
	Action                string                 `json:"action"`
	Status                string                 `json:"status"`
	WorkspacePath         string                 `json:"workspace_path"`
	WorkspaceUnrestricted bool                   `json:"workspace_unrestricted"`
	Payload               map[string]interface{} `json:"payload"`
	Result                string                 `json:"result,omitempty"`
	ErrorMessage          string                 `json:"error_message,omitempty"`
	CreatedAt             time.Time              `json:"created_at"`
	UpdatedAt             time.Time              `json:"updated_at"`
	StartedAt             *time.Time             `json:"started_at,omitempty"`
	FinishedAt            *time.Time             `json:"finished_at,omitempty"`
}

type advancedChatSessionInput struct {
	ID                       string                            `json:"id"`
	Title                    string                            `json:"title"`
	RunMode                  string                            `json:"run_mode"`
	AgentID                  string                            `json:"agent_id"`
	AgentGroupID             string                            `json:"agent_group_id"`
	SkillIDs                 []string                          `json:"skill_ids"`
	MCPServerIDs             []string                          `json:"mcp_server_ids"`
	KnowledgeBaseIDs         []string                          `json:"knowledge_base_ids"`
	ConnectorDeviceID        string                            `json:"connector_device_id"`
	ConnectorWorkspacePath   string                            `json:"connector_workspace_path"`
	CloudSandboxID           string                            `json:"cloud_sandbox_id"`
	ConnectorAutoApprove     bool                              `json:"connector_auto_approve"`
	ConnectorApprovalMode    string                            `json:"connector_approval_mode"`
	ConnectorCommandPrefixes []string                          `json:"connector_command_prefixes"`
	ModelName                string                            `json:"model_name"`
	UserChannelID            uint                              `json:"user_channel_id"`
	MaxTokens                int                               `json:"max_tokens"`
	Temperature              *float64                          `json:"temperature"`
	ReasoningEffort          string                            `json:"reasoning_effort"`
	AutoCompressContext      bool                              `json:"auto_compress_context"`
	Messages                 []advancedChatSessionMessageInput `json:"messages"`
}

type advancedChatSessionMessageInput struct {
	ID        string                           `json:"id"`
	Role      string                           `json:"role"`
	Content   string                           `json:"content"`
	Parts     []advancedChatContentPart        `json:"content_parts"`
	ToolCalls []advancedChatCompletionToolCall `json:"tool_calls"`
}

type preparedAdvancedChatAssistantRun struct {
	input                    advancedChatCompletionInput
	messages                 []advancedChatCompletionMessage
	modelName                string
	mode                     string
	runID                    string
	maxToolRounds            int
	agent                    *AdvancedChatAgent
	presetMessages           []AdvancedChatAgentPresetMessage
	skills                   []advancedChatRuntimeSkill
	workspaceSkills          []advancedChatWorkspaceSkill
	agentGroups              []advancedChatAgentGroup
	knowledgeContext         string
	agentGroup               *advancedChatAgentGroup
	groupAgent               *advancedChatGroupAgent
	servers                  []AdvancedChatMCPServer
	connectorDevice          *AdvancedChatConnectorDevice
	connectorWorkspace       string
	connectorAutoApprove     bool
	connectorApprovalMode    string
	approvalChecker          *advancedChatAgentStudioApprovalChecker
	connectorCommandPrefixes []string
	cloudSandboxID           string
	delivery                 *AdvancedChatDelivery
	timeout                  time.Duration
}

type advancedChatCompletionObserver struct {
	OnStatus   func(payload gin.H) error
	OnText     func(delta string, round int) error
	OnToolCall func(detail advancedChatCompletionToolCall) error
}

type advancedChatContentPart struct {
	Round   int    `json:"round,omitempty"`
	Content string `json:"content"`
}

func (api *advancedChatAPI) listSessions(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	sessions, err := listAdvancedChatSessionResponses(user.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list sessions"})
		return
	}
	c.JSON(http.StatusOK, sessions)
}

func (api *advancedChatAPI) listSessionFolders(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var folders []AdvancedChatSessionFolder
	if err := model.DB.Where("user_id = ?", user.ID).Order("created_at ASC").Find(&folders).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list session folders"})
		return
	}
	c.JSON(http.StatusOK, folders)
}

func (api *advancedChatAPI) createSessionFolder(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var input advancedChatSessionFolderInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" || len([]rune(name)) > 80 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Folder name must be between 1 and 80 characters"})
		return
	}
	folder := AdvancedChatSessionFolder{ID: newAdvancedChatID("acf"), UserID: user.ID, Name: name}
	if err := model.DB.Create(&folder).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create session folder"})
		return
	}
	c.JSON(http.StatusCreated, folder)
}

func (api *advancedChatAPI) moveSessionToFolder(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	sessionID := normalizeAdvancedChatSessionID(c.Param("id"))
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid session id"})
		return
	}
	if personalCompanyInternalSession(user.ID, sessionID) {
		c.JSON(http.StatusConflict, gin.H{"error": "Internal Studio sessions are immutable"})
		return
	}
	var input advancedChatSessionFolderMoveInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	folderID := strings.TrimSpace(input.FolderID)
	if folderID != "" {
		if normalizeAdvancedChatSessionID(folderID) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid folder id"})
			return
		}
		var folder AdvancedChatSessionFolder
		if err := model.DB.Where("id = ? AND user_id = ?", folderID, user.ID).First(&folder).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusNotFound, gin.H{"error": "Session folder not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load session folder"})
			return
		}
	}
	result := model.DB.Model(&AdvancedChatSession{}).Where("id = ? AND user_id = ?", sessionID, user.ID).Update("folder_id", folderID)
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to move session"})
		return
	}
	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Session not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": sessionID, "folder_id": folderID})
}

func (api *advancedChatAPI) getSession(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	session, err := advancedChatSessionResponseFor(user.ID, c.Param("id"))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Session not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load session"})
		return
	}
	c.JSON(http.StatusOK, session)
}

func (api *advancedChatAPI) saveSession(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var input advancedChatSessionInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	sessionID := normalizeAdvancedChatSessionID(c.Param("id"))
	if sessionID == "" {
		sessionID = normalizeAdvancedChatSessionID(input.ID)
	}
	if sessionID == "" {
		sessionID = newAdvancedChatID("acs")
	}
	if personalCompanyInternalSession(user.ID, sessionID) {
		c.JSON(http.StatusConflict, gin.H{"error": "Internal Studio sessions are immutable"})
		return
	}
	session, status, message, err := saveAdvancedChatSessionSnapshot(user.ID, sessionID, input, true)
	if err != nil {
		c.JSON(status, gin.H{"error": message})
		return
	}
	c.JSON(http.StatusOK, session)
}

func (api *advancedChatAPI) deleteSession(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	sessionID := strings.TrimSpace(c.Param("id"))
	if personalCompanyInternalSession(user.ID, sessionID) {
		c.JSON(http.StatusConflict, gin.H{"error": "Internal Studio sessions are immutable"})
		return
	}
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var runs []AdvancedChatRun
		if err := tx.Where("session_id = ? AND user_id = ?", sessionID, user.ID).Find(&runs).Error; err != nil {
			return err
		}
		for _, run := range runs {
			if err := tx.Where("run_id = ? AND user_id = ?", run.ID, user.ID).Delete(&AdvancedChatRunEvent{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("session_id = ? AND user_id = ?", sessionID, user.ID).Delete(&AdvancedChatRun{}).Error; err != nil {
			return err
		}
		if err := tx.Where("session_id = ? AND user_id = ?", sessionID, user.ID).Delete(&AdvancedChatMessage{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ? AND user_id = ?", sessionID, user.ID).Delete(&AdvancedChatSession{}).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete session"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Session deleted"})
}

func (api *advancedChatAPI) getRun(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var run AdvancedChatRun
	if err := model.DB.Where("id = ? AND user_id = ?", c.Param("id"), user.ID).First(&run).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Run not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load run"})
		return
	}
	c.JSON(http.StatusOK, advancedChatRunResponseFromModel(run))
}

func (api *advancedChatAPI) stopRun(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if personalCompanyChiefRunIsInternal(c.Param("id")) {
		c.JSON(http.StatusConflict, gin.H{"error": "Internal Studio runs are controlled by Studio operations"})
		return
	}
	run, status, message, err := stopAdvancedChatRun(c.Param("id"), user.ID)
	if err != nil {
		c.JSON(status, gin.H{"error": message})
		return
	}
	c.JSON(http.StatusOK, advancedChatRunResponseFromModel(run))
}

func (api *advancedChatAPI) listRunEvents(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	after, _ := strconv.Atoi(c.Query("after"))
	var events []AdvancedChatRunEvent
	if err := model.DB.
		Where("run_id = ? AND user_id = ? AND seq > ?", c.Param("id"), user.ID, after).
		Order("seq ASC").
		Limit(200).
		Find(&events).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load run events"})
		return
	}
	result := make([]advancedChatRunEventResponse, 0, len(events))
	for _, event := range events {
		result = append(result, advancedChatRunEventResponseFromModel(event))
	}
	c.JSON(http.StatusOK, result)
}

func (api *advancedChatAPI) getRunAgentWork(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	response, status, message, err := advancedChatAgentWorkForRun(c.Request.Context(), user.ID, c.Param("id"))
	if err != nil {
		c.JSON(status, gin.H{"error": message})
		return
	}
	c.JSON(http.StatusOK, response)
}

func (api *advancedChatAPI) listAgentTasks(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var runs []AdvancedChatRun
	if err := model.DB.
		Where("user_id = ? AND status IN ?", user.ID, []string{advancedChatRunStatusQueued, advancedChatRunStatusRunning}).
		Order("updated_at DESC").
		Limit(50).
		Find(&runs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load agent tasks"})
		return
	}
	if len(runs) == 0 {
		c.JSON(http.StatusOK, []advancedChatAgentTaskListItem{})
		return
	}
	runIDs := make([]string, 0, len(runs))
	sessionIDs := make([]string, 0, len(runs))
	for _, run := range runs {
		runIDs = append(runIDs, run.ID)
		sessionIDs = append(sessionIDs, run.SessionID)
	}
	var sessions []AdvancedChatSession
	if err := model.DB.
		Select("id", "title").
		Where("user_id = ? AND id IN ?", user.ID, sessionIDs).
		Find(&sessions).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load agent task sessions"})
		return
	}
	sessionTitles := map[string]string{}
	for _, session := range sessions {
		sessionTitles[session.ID] = session.Title
	}
	var events []AdvancedChatRunEvent
	if err := model.DB.
		Where("user_id = ? AND run_id IN ? AND event = ?", user.ID, runIDs, "agent_task").
		Order("run_id ASC, seq ASC").
		Limit(1000).
		Find(&events).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load agent task events"})
		return
	}
	eventsByRun := map[string][]advancedChatRunEventResponse{}
	for _, event := range events {
		eventsByRun[event.RunID] = append(eventsByRun[event.RunID], advancedChatRunEventResponseFromModel(event))
	}
	result := make([]advancedChatAgentTaskListItem, 0, len(runs))
	for _, run := range runs {
		runEvents := eventsByRun[run.ID]
		if runEvents == nil {
			runEvents = []advancedChatRunEventResponse{}
		}
		result = append(result, advancedChatAgentTaskListItem{
			RunID:         run.ID,
			SessionID:     run.SessionID,
			SessionTitle:  sessionTitles[run.SessionID],
			Status:        run.Status,
			StatusMessage: run.StatusMessage,
			StartedAt:     run.StartedAt,
			UpdatedAt:     run.UpdatedAt,
			Events:        runEvents,
		})
	}
	c.JSON(http.StatusOK, result)
}

func advancedChatAgentWorkForRun(ctx context.Context, userID uint, rawRunID string) (advancedChatAgentWorkResponse, int, string, error) {
	runID := strings.TrimSpace(rawRunID)
	var run AdvancedChatRun
	if err := model.DB.WithContext(ctx).Where("id = ? AND user_id = ?", runID, userID).First(&run).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return advancedChatAgentWorkResponse{}, http.StatusNotFound, "Run not found", err
		}
		return advancedChatAgentWorkResponse{}, http.StatusInternalServerError, "Failed to load run", err
	}
	if normalizeAdvancedChatCompletionMode(run.Mode) != advancedChatModeAgentGroup {
		return advancedChatAgentWorkResponse{}, http.StatusBadRequest, "Run is not an Agent Studio run", errors.New("run is not an agent studio run")
	}
	var session AdvancedChatSession
	if err := model.DB.WithContext(ctx).Where("id = ? AND user_id = ?", run.SessionID, userID).First(&session).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return advancedChatAgentWorkResponse{}, http.StatusNotFound, "Session not found", err
		}
		return advancedChatAgentWorkResponse{}, http.StatusInternalServerError, "Failed to load session", err
	}
	group, err := readAdvancedChatAgentGroup(ctx, userID, nil, session.AgentGroupID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return advancedChatAgentWorkResponse{}, http.StatusNotFound, "Studio not found", err
		}
		return advancedChatAgentWorkResponse{}, http.StatusInternalServerError, "Failed to load studio", err
	}
	var messages []AdvancedChatMessage
	if err := model.DB.WithContext(ctx).
		Where("session_id = ? AND user_id = ?", session.ID, userID).
		Order("sort_order ASC, created_at ASC").
		Find(&messages).Error; err != nil {
		return advancedChatAgentWorkResponse{}, http.StatusInternalServerError, "Failed to load messages", err
	}

	response := advancedChatAgentWorkResponse{
		RunID:          run.ID,
		SessionID:      run.SessionID,
		GroupID:        group.ID,
		GroupName:      group.Name,
		Agents:         make([]advancedChatAgentWorkStatus, 0, len(group.Agents)),
		ConnectorTasks: []advancedChatAgentWorkConnectorTask{},
	}
	indexByAgentID := map[string]int{}
	taskAgentIndexByID := map[string]int{}
	taskKindByID := map[string]string{}
	for _, agent := range group.Agents {
		status := advancedChatAgentWorkStatus{
			AgentID:   agent.ID,
			AgentName: agent.Name,
			AgentType: normalizeAdvancedChatAgentType(agent.Type),
			GroupID:   group.ID,
			GroupName: group.Name,
			Status:    "idle",
			Working:   false,
			Messages:  []advancedChatAgentWorkMessage{},
		}
		indexByAgentID[agent.ID] = len(response.Agents)
		response.Agents = append(response.Agents, status)
	}

	if target, ok := advancedChatTopLevelAgentForWork(group, messages); ok {
		if index, exists := indexByAgentID[target.ID]; exists {
			response.Agents[index].Status = strings.TrimSpace(run.Status)
			if response.Agents[index].Status == "" {
				response.Agents[index].Status = "idle"
			}
			response.Agents[index].Working = advancedChatRunIsActive(run.Status)
			response.Agents[index].UpdatedAt = &run.UpdatedAt
			for _, message := range messages {
				if strings.TrimSpace(message.Content) == "" {
					continue
				}
				createdAt := message.CreatedAt
				response.Agents[index].Messages = append(response.Agents[index].Messages, advancedChatAgentWorkMessage{
					Role:      message.Role,
					Content:   truncateToolResult(message.Content),
					CreatedAt: &createdAt,
				})
			}
		}
	}

	var events []AdvancedChatRunEvent
	if err := model.DB.WithContext(ctx).
		Where("run_id = ? AND user_id = ? AND event IN ?", run.ID, userID, []string{"agent_task", "agent_message"}).
		Order("seq ASC").
		Limit(2000).
		Find(&events).Error; err != nil {
		return advancedChatAgentWorkResponse{}, http.StatusInternalServerError, "Failed to load agent work events", err
	}
	for _, event := range events {
		payload := map[string]interface{}{}
		if strings.TrimSpace(event.Payload) != "" {
			_ = json.Unmarshal([]byte(event.Payload), &payload)
		}
		switch event.Event {
		case "agent_task":
			taskID := strings.TrimSpace(stringFromMap(payload, "task_id"))
			kind := strings.ToLower(strings.TrimSpace(stringFromMap(payload, "kind")))
			if kind == "" && taskID != "" {
				kind = taskKindByID[taskID]
			}
			if kind == "split" {
				continue
			}
			agentID := strings.TrimSpace(stringFromMap(payload, "agent_id"))
			index, exists := indexByAgentID[agentID]
			if (!exists || agentID == "") && taskID != "" {
				index, exists = taskAgentIndexByID[taskID]
			}
			if !exists {
				continue
			}
			if taskID != "" {
				taskAgentIndexByID[taskID] = index
				if kind != "" {
					taskKindByID[taskID] = kind
				}
			}
			createdAt := event.CreatedAt
			status := strings.TrimSpace(stringFromMap(payload, "status"))
			if status != "" {
				response.Agents[index].Status = status
				response.Agents[index].Working = status == "running" || status == "approval_required"
				response.Agents[index].UpdatedAt = &createdAt
			}
			if strings.EqualFold(status, "running") {
				goal := strings.TrimSpace(stringFromMap(payload, "goal"))
				if goal != "" {
					contextText := strings.TrimSpace(stringFromMap(payload, "context"))
					content := "Delegated goal:\n" + goal
					if contextText != "" {
						content += "\n\nContext:\n" + contextText
					}
					response.Agents[index].Messages = append(response.Agents[index].Messages, advancedChatAgentWorkMessage{
						Role:      "user",
						Content:   truncateToolResult(content),
						Status:    status,
						CreatedAt: &createdAt,
					})
				}
			}
			if result := strings.TrimSpace(stringFromMap(payload, "result")); result != "" {
				response.Agents[index].Messages = append(response.Agents[index].Messages, advancedChatAgentWorkMessage{
					Role:      "assistant",
					Content:   truncateToolResult(result),
					Status:    status,
					CreatedAt: &createdAt,
				})
			}
			if errorText := strings.TrimSpace(stringFromMap(payload, "error")); errorText != "" {
				response.Agents[index].Messages = append(response.Agents[index].Messages, advancedChatAgentWorkMessage{
					Role:      "system",
					Content:   truncateToolResult(errorText),
					Status:    "error",
					CreatedAt: &createdAt,
				})
			}
		case "agent_message":
			if strings.TrimSpace(stringFromMap(payload, "group_id")) != group.ID {
				continue
			}
			agentID := strings.TrimSpace(stringFromMap(payload, "agent_id"))
			index, exists := indexByAgentID[agentID]
			if agentID == "" || !exists {
				continue
			}
			content := strings.TrimSpace(stringFromMap(payload, "content"))
			if content == "" {
				continue
			}
			createdAt := event.CreatedAt
			status := strings.TrimSpace(stringFromMap(payload, "status"))
			response.Agents[index].Messages = append(response.Agents[index].Messages, advancedChatAgentWorkMessage{
				Role:      normalizedAdvancedChatAgentWorkMessageRole(stringFromMap(payload, "role")),
				Content:   truncateToolResult(content),
				Status:    status,
				Tool:      strings.TrimSpace(stringFromMap(payload, "tool")),
				CreatedAt: &createdAt,
			})
			if status == "running" || status == "approval_required" {
				response.Agents[index].Status = status
				response.Agents[index].Working = true
				response.Agents[index].UpdatedAt = &createdAt
			}
		}
	}
	connectorTasks, err := advancedChatAgentWorkConnectorTasks(ctx, userID, run.ID)
	if err != nil {
		return advancedChatAgentWorkResponse{}, http.StatusInternalServerError, "Failed to load connector tasks", err
	}
	response.ConnectorTasks = connectorTasks
	return response, http.StatusOK, "", nil
}

func advancedChatAgentWorkConnectorTasks(ctx context.Context, userID uint, runID string) ([]advancedChatAgentWorkConnectorTask, error) {
	var tasks []AdvancedChatConnectorTask
	if err := model.DB.WithContext(ctx).
		Where("user_id = ? AND run_id = ?", userID, runID).
		Order("created_at ASC").
		Limit(500).
		Find(&tasks).Error; err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return []advancedChatAgentWorkConnectorTask{}, nil
	}
	deviceIDs := make([]string, 0, len(tasks))
	for _, task := range tasks {
		if strings.TrimSpace(task.DeviceID) != "" {
			deviceIDs = append(deviceIDs, task.DeviceID)
		}
	}
	devices := map[string]AdvancedChatConnectorDevice{}
	if len(deviceIDs) > 0 {
		var rows []AdvancedChatConnectorDevice
		if err := model.DB.WithContext(ctx).Where("user_id = ? AND id IN ?", userID, deviceIDs).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, device := range rows {
			devices[device.ID] = device
		}
	}
	result := make([]advancedChatAgentWorkConnectorTask, 0, len(tasks))
	for _, task := range tasks {
		payload := map[string]interface{}{}
		if strings.TrimSpace(task.Payload) != "" {
			_ = json.Unmarshal([]byte(task.Payload), &payload)
		}
		device := devices[task.DeviceID]
		result = append(result, advancedChatAgentWorkConnectorTask{
			ID:                    task.ID,
			DeviceID:              task.DeviceID,
			DeviceName:            device.Name,
			Action:                task.Action,
			Status:                task.Status,
			WorkspacePath:         task.WorkspacePath,
			WorkspaceUnrestricted: strings.TrimSpace(task.WorkspacePath) == "",
			Payload:               truncateAdvancedChatAgentWorkConnectorPayload(payload),
			Result:                truncateToolResult(task.Result),
			ErrorMessage:          truncateToolResult(task.ErrorMessage),
			CreatedAt:             task.CreatedAt,
			UpdatedAt:             task.UpdatedAt,
			StartedAt:             task.StartedAt,
			FinishedAt:            task.FinishedAt,
		})
	}
	return result, nil
}

func truncateAdvancedChatAgentWorkConnectorPayload(payload map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{}, len(payload))
	for key, value := range payload {
		switch typed := value.(type) {
		case string:
			result[key] = truncateToolResult(typed)
		default:
			result[key] = typed
		}
	}
	return result
}

func advancedChatTopLevelAgentForWork(group advancedChatAgentGroup, messages []AdvancedChatMessage) (advancedChatGroupAgent, bool) {
	completionMessages := make([]advancedChatCompletionMessage, 0, len(messages))
	for _, message := range messages {
		completionMessages = append(completionMessages, advancedChatCompletionMessage{
			Role:    message.Role,
			Content: message.Content,
		})
	}
	if agent, ok := findAdvancedChatMentionedGroupAgentInMessages(group, completionMessages); ok {
		return agent, true
	}
	for _, agent := range group.Agents {
		if normalizeAdvancedChatAgentType(agent.Type) == "chief" {
			return agent, true
		}
	}
	if len(group.Agents) > 0 {
		return group.Agents[0], true
	}
	return advancedChatGroupAgent{}, false
}

func normalizedAdvancedChatAgentWorkMessageRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user", "assistant", "tool", "system":
		return strings.ToLower(strings.TrimSpace(role))
	default:
		return "system"
	}
}

func stopAdvancedChatRun(rawRunID string, userID uint) (AdvancedChatRun, int, string, error) {
	runID := strings.TrimSpace(rawRunID)
	var run AdvancedChatRun
	if err := model.DB.Where("id = ? AND user_id = ?", runID, userID).First(&run).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return run, http.StatusNotFound, "Run not found", err
		}
		return run, http.StatusInternalServerError, "Failed to load run", err
	}
	if !advancedChatRunIsActive(run.Status) {
		return run, http.StatusOK, "", nil
	}

	eventState := advancedChatRunEventStateFor(userID, run.ID)
	eventState.mutex.Lock()
	now := time.Now()
	stopped := false
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var currentRun AdvancedChatRun
		if err := tx.Where("id = ? AND user_id = ?", run.ID, userID).First(&currentRun).Error; err != nil {
			return err
		}
		update := tx.Model(&AdvancedChatRun{}).
			Where("id = ? AND user_id = ? AND status IN ?", run.ID, userID, []string{advancedChatRunStatusQueued, advancedChatRunStatusRunning}).
			Updates(map[string]interface{}{
				"status":         advancedChatRunStatusCancelled,
				"status_message": "cancelled",
				"error_message":  "",
				"finished_at":    &now,
				"updated_at":     now,
			})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected == 0 {
			return nil
		}
		if err := tx.Model(&AdvancedChatMessage{}).
			Where("id = ? AND user_id = ? AND content = ?", run.AssistantMessageID, userID, "").
			Update("content", "Stopped").Error; err != nil {
			return err
		}
		details, changed := cancelActiveAdvancedChatToolCalls(decodeAdvancedChatToolCalls(currentRun.ToolCallDetails), "Cancelled by user.")
		if changed {
			encoded, err := json.Marshal(details)
			if err != nil {
				return err
			}
			if err := tx.Model(&AdvancedChatRun{}).
				Where("id = ? AND user_id = ?", run.ID, userID).
				Updates(map[string]interface{}{"tool_call_details": string(encoded), "tool_calls": len(details)}).Error; err != nil {
				return err
			}
			if err := tx.Model(&AdvancedChatMessage{}).
				Where("id = ? AND user_id = ?", run.AssistantMessageID, userID).
				Update("tool_calls", string(encoded)).Error; err != nil {
				return err
			}
		}
		taskUpdate := tx.Model(&AdvancedChatConnectorTask{}).
			Where("run_id = ? AND user_id = ? AND status IN ?", run.ID, userID, []string{
				advancedChatConnectorTaskStatusPendingApproval,
				advancedChatConnectorTaskStatusQueued,
				advancedChatConnectorTaskStatusRunning,
			}).
			Updates(map[string]interface{}{
				"status":        advancedChatConnectorTaskStatusFailed,
				"error_message": "cancelled by user",
				"finished_at":   &now,
				"updated_at":    now,
			})
		if taskUpdate.Error != nil {
			return taskUpdate.Error
		}
		stopped = true
		return nil
	})
	eventState.mutex.Unlock()
	if err != nil {
		return run, http.StatusInternalServerError, "Failed to stop run", err
	}
	if stopped {
		if cancel, ok := advancedChatRunCancels.Load(run.ID); ok {
			if fn, ok := cancel.(context.CancelFunc); ok {
				fn()
			}
		}
		_ = appendAdvancedChatRunEvent(run.ID, run.SessionID, userID, "status", gin.H{"message": "cancelled"})
		cancelRunningAdvancedChatAgentTasks(run.ID, run.SessionID, userID)
	}
	if err := model.DB.Where("id = ? AND user_id = ?", run.ID, userID).First(&run).Error; err != nil {
		return run, http.StatusInternalServerError, "Failed to load stopped run", err
	}
	return run, http.StatusOK, "", nil
}

func advancedChatRunIsActive(status string) bool {
	return status == advancedChatRunStatusQueued || status == advancedChatRunStatusRunning
}

func ensureAdvancedChatRunNotCancelled(runID string, userID uint) error {
	runID = strings.TrimSpace(runID)
	if strings.HasPrefix(runID, "msgch-") {
		return nil
	}
	var run AdvancedChatRun
	if err := model.DB.Select("status").Where("id = ? AND user_id = ?", runID, userID).First(&run).Error; err != nil {
		return err
	}
	if run.Status == advancedChatRunStatusCancelled {
		return errAdvancedChatRunCancelled
	}
	return nil
}

func interruptActiveAdvancedChatRunsForSession(userID uint, rawSessionID string, reason string) error {
	sessionID := normalizeAdvancedChatSessionID(rawSessionID)
	if sessionID == "" {
		return nil
	}
	var runs []AdvancedChatRun
	if err := model.DB.
		Where("session_id = ? AND user_id = ? AND status IN ?", sessionID, userID, []string{advancedChatRunStatusQueued, advancedChatRunStatusRunning}).
		Find(&runs).Error; err != nil {
		return err
	}
	for _, run := range runs {
		if _, _, _, err := stopAdvancedChatRun(run.ID, userID); err != nil {
			return err
		}
		_ = appendAdvancedChatRunEvent(run.ID, run.SessionID, userID, "status", gin.H{"message": "user_interrupted", "code": strings.TrimSpace(reason)})
	}
	return nil
}

func (api *advancedChatAPI) startAssistantCompletionRun(c *gin.Context, user *model.User, input advancedChatCompletionInput, messages []advancedChatCompletionMessage, modelName string) {
	if personalCompanyInternalSession(user.ID, input.SessionID) {
		c.JSON(http.StatusConflict, gin.H{"error": "Internal Studio sessions are immutable"})
		return
	}
	prepared, status, message, err := prepareAdvancedChatAssistantRun(c.Request.Context(), user.ID, input, messages, modelName)
	if err != nil {
		c.JSON(status, gin.H{"error": message})
		return
	}
	if err := interruptActiveAdvancedChatRunsForSession(user.ID, prepared.input.SessionID, "USER_INTERRUPTED"); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to interrupt active run"})
		return
	}
	session, run, status, message, err := createAdvancedChatAssistantRun(user.ID, prepared)
	if err != nil {
		c.JSON(status, gin.H{"error": message})
		return
	}
	go runAdvancedChatAssistantCompletion(run.ID, user.ID, prepared)
	c.JSON(http.StatusAccepted, gin.H{"session": session, "run": run})
}

func prepareAdvancedChatAssistantRun(ctx context.Context, userID uint, input advancedChatCompletionInput, messages []advancedChatCompletionMessage, modelName string) (preparedAdvancedChatAssistantRun, int, string, error) {
	if !advancedChatAssistantModeEnabled() {
		return preparedAdvancedChatAssistantRun{}, http.StatusForbidden, "Assistant mode is disabled", errors.New("assistant mode disabled")
	}
	mode := normalizeAdvancedChatCompletionMode(input.Mode)
	if mode != advancedChatModeAssistant && mode != advancedChatModeAgentGroup {
		mode = advancedChatModeAssistant
	}
	if mode == advancedChatModeAgentGroup {
		modelName = ""
	}
	if strings.TrimSpace(input.ConnectorApprovalMode) == "" {
		input.ConnectorApprovalMode = legacyConnectorApprovalMode(input.ConnectorAutoApprove)
	} else {
		input.ConnectorApprovalMode = normalizeAdvancedChatConnectorApprovalMode(input.ConnectorApprovalMode)
	}

	var agent *AdvancedChatAgent
	var groupAgent *advancedChatGroupAgent
	var selectedGroup *advancedChatAgentGroup
	skills := []advancedChatRuntimeSkill{}
	servers := []AdvancedChatMCPServer{}
	agentGroups := []advancedChatAgentGroup{}
	var err error

	if mode == advancedChatModeAgentGroup {
		input.ConnectorDeviceID = strings.TrimSpace(input.ConnectorDeviceID)
		input.AgentGroupID = strings.TrimSpace(input.AgentGroupID)
		if input.AgentGroupID == "" {
			return preparedAdvancedChatAssistantRun{}, http.StatusBadRequest, "Studio is required", errors.New("studio required")
		}
	} else {
		if strings.TrimSpace(input.AgentID) == "" {
			input.AgentID = advancedChatDefaultAgentID
		}
		agent, err = loadAdvancedChatAgent(userID, input.AgentID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return preparedAdvancedChatAssistantRun{}, http.StatusBadRequest, "Agent not found", err
			}
			return preparedAdvancedChatAssistantRun{}, http.StatusInternalServerError, "Failed to load agent", err
		}
		skillIDs := input.SkillIDs
		mcpServerIDs := input.MCPServerIDs
		if agent != nil {
			skillIDs = uniqueStringsLocal(append(decodeStringList(agent.SkillIDs), skillIDs...))
			mcpServerIDs = uniqueStringsLocal(append(decodeStringList(agent.MCPServerIDs), mcpServerIDs...))
			if input.UserChannelID == 0 && agent.UserChannelID > 0 {
				input.UserChannelID = agent.UserChannelID
			}
		}
		skills, err = loadAdvancedChatSkills(userID, skillIDs)
		if err != nil {
			return preparedAdvancedChatAssistantRun{}, http.StatusInternalServerError, "Failed to load skills", err
		}
		if len(skills) != len(uniqueStringsLocal(skillIDs)) {
			return preparedAdvancedChatAssistantRun{}, http.StatusBadRequest, "Unknown skill", errors.New("unknown skill")
		}
		serverIDs := uniqueStringsLocal(append(mcpServerIDs, skillMCPIDs(skills)...))
		servers, err = loadAdvancedChatMCPServersForCall(userID, serverIDs)
		if len(serverIDs) > 0 && !advancedChatAssistantMCPToolsEnabled() {
			return preparedAdvancedChatAssistantRun{}, http.StatusBadRequest, "MCP tools are disabled", errors.New("mcp tools disabled")
		}
		if err != nil {
			return preparedAdvancedChatAssistantRun{}, http.StatusBadRequest, err.Error(), err
		}
		if !advancedChatAssistantMCPToolsEnabled() {
			servers = []AdvancedChatMCPServer{}
		}
	}
	input.CloudSandboxID = strings.TrimSpace(input.CloudSandboxID)
	if (strings.TrimSpace(input.ConnectorDeviceID) != "" || strings.TrimSpace(input.ConnectorWorkspacePath) != "" || input.CloudSandboxID != "") && !advancedChatAssistantConnectorToolsEnabled() {
		return preparedAdvancedChatAssistantRun{}, http.StatusBadRequest, "Workspace tools are disabled", errors.New("workspace tools disabled")
	}
	if input.CloudSandboxID != "" {
		if strings.TrimSpace(input.ConnectorDeviceID) != "" || strings.TrimSpace(input.ConnectorWorkspacePath) != "" {
			return preparedAdvancedChatAssistantRun{}, http.StatusBadRequest, "Choose either a cloud sandbox or a local connector", errors.New("conflicting execution target")
		}
		_, _, sandboxDevice, sandboxErr := loadCloudSandboxForUser(userID, input.CloudSandboxID)
		if sandboxErr != nil {
			return preparedAdvancedChatAssistantRun{}, http.StatusBadRequest, "Cloud sandbox is unavailable", sandboxErr
		}
		input.ConnectorDeviceID = sandboxDevice.ID
		input.ConnectorWorkspacePath = ""
		// Cloud sandboxes are administrator-governed execution targets. The
		// host policy replaces per-user connector approval prompts.
		input.ConnectorApprovalMode = advancedChatConnectorApprovalFullAccess
	}
	connectorDevice, connectorWorkspace, err := loadAdvancedChatConnectorForRun(userID, input.ConnectorDeviceID, input.ConnectorWorkspacePath)
	if input.CloudSandboxID != "" {
		_, _, sandboxDevice, sandboxErr := loadCloudSandboxForUser(userID, input.CloudSandboxID)
		if sandboxErr != nil {
			return preparedAdvancedChatAssistantRun{}, http.StatusBadRequest, "Cloud sandbox is unavailable", sandboxErr
		}
		connectorDevice, connectorWorkspace, err = &sandboxDevice, "", nil
	}
	if err != nil {
		return preparedAdvancedChatAssistantRun{}, http.StatusBadRequest, err.Error(), err
	}
	workspaceSkills := []advancedChatWorkspaceSkill{}
	if connectorDevice != nil && input.CloudSandboxID == "" {
		workspaceSkills, err = loadAdvancedChatWorkspaceSkillsForRun(ctx, userID, connectorDevice, connectorWorkspace)
		if err != nil {
			return preparedAdvancedChatAssistantRun{}, http.StatusBadRequest, err.Error(), err
		}
	}
	var personalApprovalChecker *advancedChatAgentStudioApprovalChecker
	if connectorDevice != nil && input.ConnectorApprovalMode == advancedChatConnectorApprovalAssistant {
		settings := ensureAdvancedChatUserSettings(userID)
		personalApprovalChecker, err = advancedChatApprovalCheckerForUserAgent(userID, settings.ConnectorApprovalAgentID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return preparedAdvancedChatAssistantRun{}, http.StatusBadRequest, "Approval agent not found", err
			}
			return preparedAdvancedChatAssistantRun{}, http.StatusBadRequest, err.Error(), err
		}
	}
	if mode == advancedChatModeAgentGroup {
		if loaded, loadErr := loadAdvancedChatAgentGroupsForRun(ctx, userID, connectorDevice); loadErr == nil {
			agentGroups = loaded
		}
		group, target, status, message, groupErr := prepareAdvancedChatAgentGroupTarget(input.AgentGroupID, agentGroups)
		if groupErr != nil {
			return preparedAdvancedChatAssistantRun{}, status, message, groupErr
		}
		selectedGroup = &group
		groupAgent = &target
		agentGroups = []advancedChatAgentGroup{group}
		agent, err = loadAdvancedChatAgent(userID, target.ChatAgentID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return preparedAdvancedChatAssistantRun{}, http.StatusBadRequest, "Referenced chat agent was not found", err
			}
			return preparedAdvancedChatAssistantRun{}, http.StatusInternalServerError, "Failed to load group agent", err
		}
		skillIDs := []string{}
		mcpServerIDs := []string{}
		if agent != nil {
			skillIDs = uniqueStringsLocal(decodeStringList(agent.SkillIDs))
			mcpServerIDs = uniqueStringsLocal(decodeStringList(agent.MCPServerIDs))
		}
		skillIDs = uniqueStringsLocal(append(skillIDs, target.SkillIDs...))
		mcpServerIDs = uniqueStringsLocal(append(mcpServerIDs, target.MCPServerIDs...))
		skills, err = loadAdvancedChatSkills(userID, skillIDs)
		if err != nil {
			return preparedAdvancedChatAssistantRun{}, http.StatusInternalServerError, "Failed to load group agent skills", err
		}
		if len(skills) != len(uniqueStringsLocal(skillIDs)) {
			return preparedAdvancedChatAssistantRun{}, http.StatusBadRequest, "Unknown group agent skill", errors.New("unknown group agent skill")
		}
		serverIDs := uniqueStringsLocal(append([]string{}, mcpServerIDs...))
		serverIDs = uniqueStringsLocal(append(serverIDs, skillMCPIDs(skills)...))
		if len(serverIDs) > 0 && !advancedChatAssistantMCPToolsEnabled() {
			return preparedAdvancedChatAssistantRun{}, http.StatusBadRequest, "MCP tools are disabled", errors.New("mcp tools disabled")
		}
		servers, err = loadAdvancedChatMCPServersForCall(userID, serverIDs)
		if err != nil {
			return preparedAdvancedChatAssistantRun{}, http.StatusBadRequest, err.Error(), err
		}
		if !advancedChatAssistantMCPToolsEnabled() {
			servers = []AdvancedChatMCPServer{}
		}
		if strings.TrimSpace(target.DefaultModel) != "" {
			modelName = strings.TrimSpace(target.DefaultModel)
		} else if agent != nil && strings.TrimSpace(agent.DefaultModel) != "" {
			modelName = strings.TrimSpace(agent.DefaultModel)
		}
		if agent != nil && agent.UserChannelID > 0 {
			input.UserChannelID = agent.UserChannelID
		} else if target.UserChannelID > 0 {
			input.UserChannelID = target.UserChannelID
		}
	}
	if strings.TrimSpace(modelName) == "" {
		if mode == advancedChatModeAgentGroup {
			return preparedAdvancedChatAssistantRun{}, http.StatusBadRequest, "Model is required for Studio member", errors.New("studio member model required")
		}
		return preparedAdvancedChatAssistantRun{}, http.StatusBadRequest, "Model is required", errors.New("model required")
	}
	input.ConnectorDeviceID = strings.TrimSpace(input.ConnectorDeviceID)
	input.ConnectorWorkspacePath = connectorWorkspace
	input.ConnectorCommandPrefixes = normalizeConnectorCommandPrefixes(input.ConnectorCommandPrefixes)
	if mode == advancedChatModeAgentGroup {
		input.AgentID = ""
	}
	if mode == advancedChatModeChat {
		input.ConnectorApprovalMode = advancedChatConnectorApprovalManual
	}
	sessionKnowledgeBaseIDs, valid := normalizeAdvancedChatKnowledgeBaseIDs(nil, userID, input.KnowledgeBaseIDs)
	if !valid {
		return preparedAdvancedChatAssistantRun{}, http.StatusBadRequest, "Knowledge base is not vectorized", errors.New("knowledge base is not vectorized")
	}
	input.KnowledgeBaseIDs = sessionKnowledgeBaseIDs
	knowledgeBaseIDs := sessionKnowledgeBaseIDs
	if agent != nil {
		knowledgeBaseIDs = uniqueStringsLocal(append(decodeStringList(agent.KnowledgeBaseIDs), knowledgeBaseIDs...))
	}
	if len(knowledgeBaseIDs) > 0 {
		knowledgeBaseIDs, valid = normalizeAdvancedChatKnowledgeBaseIDs(nil, userID, knowledgeBaseIDs)
		if !valid {
			return preparedAdvancedChatAssistantRun{}, http.StatusBadRequest, "Knowledge base is not vectorized", errors.New("knowledge base is not vectorized")
		}
	}
	knowledgeContext, err := advancedChatKnowledgeContext(ctx, nil, &model.User{ID: userID}, knowledgeBaseIDs, messages)
	if err != nil {
		return preparedAdvancedChatAssistantRun{}, http.StatusBadGateway, err.Error(), err
	}
	input.ConnectorAutoApprove = input.ConnectorApprovalMode == advancedChatConnectorApprovalFullAccess
	input.Mode = mode
	presetMessages := []AdvancedChatAgentPresetMessage{}
	if agent != nil {
		presetMessages = agent.Presets
	}
	return preparedAdvancedChatAssistantRun{
		input:                    input,
		messages:                 messages,
		modelName:                modelName,
		mode:                     mode,
		maxToolRounds:            advancedChatCompletionMaxToolRounds(mode),
		agent:                    agent,
		presetMessages:           presetMessages,
		skills:                   skills,
		workspaceSkills:          workspaceSkills,
		agentGroups:              agentGroups,
		knowledgeContext:         knowledgeContext,
		agentGroup:               selectedGroup,
		groupAgent:               groupAgent,
		servers:                  servers,
		connectorDevice:          connectorDevice,
		connectorWorkspace:       connectorWorkspace,
		connectorAutoApprove:     input.ConnectorApprovalMode == advancedChatConnectorApprovalFullAccess,
		connectorApprovalMode:    input.ConnectorApprovalMode,
		approvalChecker:          personalApprovalChecker,
		connectorCommandPrefixes: input.ConnectorCommandPrefixes,
		cloudSandboxID:           input.CloudSandboxID,
	}, http.StatusOK, "", nil
}

func prepareAdvancedChatAgentGroupTarget(groupID string, groups []advancedChatAgentGroup) (advancedChatAgentGroup, advancedChatGroupAgent, int, string, error) {
	groupID = strings.TrimSpace(groupID)
	var group advancedChatAgentGroup
	found := false
	for _, item := range groups {
		if item.ID == groupID {
			group = item
			found = true
			break
		}
	}
	if !found {
		return group, advancedChatGroupAgent{}, http.StatusBadRequest, "Studio not found", errors.New("studio not found")
	}
	chiefs := []advancedChatGroupAgent{}
	for _, agent := range group.Agents {
		if normalizeAdvancedChatAgentType(agent.Type) == "chief" {
			chiefs = append(chiefs, agent)
		}
	}
	if len(chiefs) != 1 {
		return group, advancedChatGroupAgent{}, http.StatusBadRequest, "Studio must contain exactly one chief", errors.New("studio chief count invalid")
	}
	target := chiefs[0]
	if strings.TrimSpace(target.ChatAgentID) == "" {
		return group, target, http.StatusBadRequest, "Studio member must select an agent", errors.New("studio member agent required")
	}
	return group, target, http.StatusOK, "", nil
}

func advancedChatLatestUserMessageHasMention(messages []advancedChatCompletionMessage) bool {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role != "user" {
			continue
		}
		for _, field := range strings.Fields(messages[index].Content) {
			if strings.HasPrefix(strings.TrimSpace(field), "@") {
				return true
			}
		}
		return false
	}
	return false
}

func findAdvancedChatMentionedGroupAgentInMessages(group advancedChatAgentGroup, messages []advancedChatCompletionMessage) (advancedChatGroupAgent, bool) {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role != "user" {
			continue
		}
		return findAdvancedChatMentionedGroupAgent(group, messages[index].Content)
	}
	return advancedChatGroupAgent{}, false
}

func findAdvancedChatMentionedGroupAgent(group advancedChatAgentGroup, content string) (advancedChatGroupAgent, bool) {
	content = strings.ToLower(strings.TrimSpace(content))
	if content == "" || !strings.Contains(content, "@") {
		return advancedChatGroupAgent{}, false
	}
	for _, agent := range group.Agents {
		id := strings.ToLower(strings.TrimSpace(agent.ID))
		name := strings.ToLower(strings.TrimSpace(agent.Name))
		if (id != "" && strings.Contains(content, "@"+id)) || (name != "" && strings.Contains(content, "@"+name)) {
			return agent, true
		}
	}
	return advancedChatGroupAgent{}, false
}

func saveAdvancedChatSessionSnapshot(userID uint, sessionID string, input advancedChatSessionInput, replaceMessages bool) (advancedChatSessionResponse, int, string, error) {
	sessionID = normalizeAdvancedChatSessionID(sessionID)
	if sessionID == "" {
		return advancedChatSessionResponse{}, http.StatusBadRequest, "Invalid session id", errors.New("invalid session id")
	}
	runMode := normalizeAdvancedChatCompletionMode(input.RunMode)
	if (runMode == advancedChatModeAssistant || runMode == advancedChatModeAgentGroup) && !advancedChatAssistantModeEnabled() {
		return advancedChatSessionResponse{}, http.StatusForbidden, "Assistant mode is disabled", errors.New("assistant mode disabled")
	}
	modelName := strings.TrimSpace(input.ModelName)
	if len([]rune(modelName)) > 100 {
		return advancedChatSessionResponse{}, http.StatusBadRequest, "Model name is too long", errors.New("model name too long")
	}
	if runMode == advancedChatModeAgentGroup {
		modelName = ""
	}
	maxTokens := normalizeAdvancedChatMaxTokens(input.MaxTokens)
	temperature := normalizeAdvancedChatTemperature(input.Temperature)
	reasoningEffort := normalizeAdvancedChatReasoningEffort(input.ReasoningEffort)
	agentID := strings.TrimSpace(input.AgentID)
	agentGroupID := strings.TrimSpace(input.AgentGroupID)
	if runMode == advancedChatModeChat || runMode == advancedChatModeAssistant {
		if agentID == "" {
			agentID = advancedChatDefaultAgentID
		}
		agentGroupID = ""
	}
	if runMode == advancedChatModeAgentGroup {
		agentID = ""
	}
	var agent *AdvancedChatAgent
	if agentID != "" {
		loadedAgent, err := loadAdvancedChatAgent(userID, agentID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return advancedChatSessionResponse{}, http.StatusBadRequest, "Agent not found", err
			}
			return advancedChatSessionResponse{}, http.StatusInternalServerError, "Failed to load agent", err
		}
		agent = loadedAgent
		if input.UserChannelID == 0 && agent.UserChannelID > 0 {
			input.UserChannelID = agent.UserChannelID
		}
	}
	skillIDs := uniqueStringsLocal(input.SkillIDs)
	if agent != nil {
		skillIDs = uniqueStringsLocal(append(decodeStringList(agent.SkillIDs), skillIDs...))
	}
	skills := []advancedChatRuntimeSkill{}
	if len(skillIDs) > 0 {
		var err error
		skills, err = loadAdvancedChatSkills(userID, skillIDs)
		if err != nil {
			return advancedChatSessionResponse{}, http.StatusInternalServerError, "Failed to load skills", err
		}
		if len(skills) != len(skillIDs) {
			return advancedChatSessionResponse{}, http.StatusBadRequest, "Unknown skill", errors.New("unknown skill")
		}
	}
	mcpServerIDs := uniqueStringsLocal(input.MCPServerIDs)
	if agent != nil {
		mcpServerIDs = uniqueStringsLocal(append(decodeStringList(agent.MCPServerIDs), mcpServerIDs...))
	}
	knowledgeBaseIDs, valid := normalizeAdvancedChatKnowledgeBaseIDs(nil, userID, input.KnowledgeBaseIDs)
	if !valid {
		return advancedChatSessionResponse{}, http.StatusBadRequest, "Knowledge base is not vectorized", errors.New("knowledge base is not vectorized")
	}
	if (runMode == advancedChatModeAssistant || runMode == advancedChatModeAgentGroup) && !advancedChatAssistantMCPToolsEnabled() && (len(mcpServerIDs) > 0 || len(skillMCPIDs(skills)) > 0) {
		return advancedChatSessionResponse{}, http.StatusBadRequest, "MCP tools are disabled", errors.New("mcp tools disabled")
	}
	if len(mcpServerIDs) > 0 {
		if _, err := loadAdvancedChatMCPServersForCall(userID, mcpServerIDs); err != nil {
			return advancedChatSessionResponse{}, http.StatusBadRequest, err.Error(), err
		}
	}
	commandPrefixes := normalizeConnectorCommandPrefixes(input.ConnectorCommandPrefixes)
	commandPrefixesJSON, _ := json.Marshal(commandPrefixes)
	connectorDeviceID := strings.TrimSpace(input.ConnectorDeviceID)
	connectorWorkspacePath := strings.TrimSpace(input.ConnectorWorkspacePath)
	cloudSandboxID := strings.TrimSpace(input.CloudSandboxID)
	connectorApprovalMode := normalizeAdvancedChatConnectorApprovalMode(input.ConnectorApprovalMode)
	if strings.TrimSpace(input.ConnectorApprovalMode) == "" {
		connectorApprovalMode = legacyConnectorApprovalMode(input.ConnectorAutoApprove)
	}
	connectorAutoApprove := connectorApprovalMode == advancedChatConnectorApprovalFullAccess
	if runMode == advancedChatModeChat {
		connectorDeviceID = ""
		connectorWorkspacePath = ""
		cloudSandboxID = ""
		connectorAutoApprove = false
		connectorApprovalMode = advancedChatConnectorApprovalManual
		commandPrefixes = []string{}
		commandPrefixesJSON, _ = json.Marshal(commandPrefixes)
	}
	if runMode == advancedChatModeAgentGroup && agentGroupID == "" {
		return advancedChatSessionResponse{}, http.StatusBadRequest, "Studio is required", errors.New("studio required")
	}
	if (connectorDeviceID != "" || connectorWorkspacePath != "" || cloudSandboxID != "") && (runMode == advancedChatModeAssistant || runMode == advancedChatModeAgentGroup) && !advancedChatAssistantConnectorToolsEnabled() {
		return advancedChatSessionResponse{}, http.StatusBadRequest, "Workspace tools are disabled", errors.New("workspace tools disabled")
	}
	if cloudSandboxID != "" {
		if connectorWorkspacePath != "" {
			return advancedChatSessionResponse{}, http.StatusBadRequest, "Cloud sandboxes do not accept a workspace path", errors.New("cloud sandbox workspace path")
		}
		_, _, device, err := loadCloudSandboxForUser(userID, cloudSandboxID)
		if err != nil {
			return advancedChatSessionResponse{}, http.StatusBadRequest, "Cloud sandbox is unavailable", err
		}
		connectorDeviceID = device.ID
		connectorAutoApprove = true
		connectorApprovalMode = advancedChatConnectorApprovalFullAccess
	} else if connectorDeviceID != "" || connectorWorkspacePath != "" {
		if _, workspacePath, err := loadAdvancedChatConnectorForSession(userID, connectorDeviceID, connectorWorkspacePath); err != nil {
			return advancedChatSessionResponse{}, http.StatusBadRequest, err.Error(), err
		} else {
			connectorWorkspacePath = workspacePath
		}
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = advancedChatTitleFromSessionMessages(input.Messages)
	}
	if title == "" {
		title = "New session"
	}
	if len([]rune(title)) > 200 {
		title = string([]rune(title)[:200])
	}
	skillIDsJSON, _ := json.Marshal(skillIDs)
	mcpServerIDsJSON, _ := json.Marshal(mcpServerIDs)
	knowledgeBaseIDsJSON, _ := json.Marshal(knowledgeBaseIDs)

	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var existingByID AdvancedChatSession
		if err := tx.Where("id = ?", sessionID).Limit(1).Find(&existingByID).Error; err != nil {
			return err
		}
		if existingByID.ID != "" && existingByID.UserID != userID {
			return errAdvancedChatSessionConflict
		}
		if replaceMessages {
			var activeRuns int64
			if err := tx.Model(&AdvancedChatRun{}).
				Where("session_id = ? AND user_id = ? AND status IN ?", sessionID, userID, []string{advancedChatRunStatusQueued, advancedChatRunStatusRunning}).
				Count(&activeRuns).Error; err != nil {
				return err
			}
			if activeRuns > 0 {
				return errAdvancedChatRunActive
			}
		}
		session := AdvancedChatSession{
			ID:                       sessionID,
			UserID:                   userID,
			Title:                    title,
			RunMode:                  runMode,
			AgentID:                  agentID,
			AgentGroupID:             agentGroupID,
			SkillIDs:                 string(skillIDsJSON),
			MCPServerIDs:             string(mcpServerIDsJSON),
			KnowledgeBaseIDs:         string(knowledgeBaseIDsJSON),
			ConnectorDeviceID:        connectorDeviceID,
			ConnectorWorkspacePath:   connectorWorkspacePath,
			CloudSandboxID:           cloudSandboxID,
			ConnectorAutoApprove:     connectorAutoApprove,
			ConnectorApprovalMode:    connectorApprovalMode,
			ConnectorCommandPrefixes: string(commandPrefixesJSON),
			ModelName:                modelName,
			UserChannelID:            input.UserChannelID,
			MaxTokens:                maxTokens,
			Temperature:              temperature,
			ReasoningEffort:          reasoningEffort,
			AutoCompressContext:      input.AutoCompressContext,
		}
		var existing AdvancedChatSession
		if err := tx.Where("id = ? AND user_id = ?", sessionID, userID).Limit(1).Find(&existing).Error; err != nil {
			return err
		}
		if existing.ID != "" {
			if err := tx.Model(&existing).Updates(map[string]interface{}{
				"title":                      session.Title,
				"run_mode":                   session.RunMode,
				"agent_id":                   session.AgentID,
				"agent_group_id":             session.AgentGroupID,
				"skill_ids":                  session.SkillIDs,
				"mcp_server_ids":             session.MCPServerIDs,
				"knowledge_base_ids":         session.KnowledgeBaseIDs,
				"connector_device_id":        session.ConnectorDeviceID,
				"connector_workspace_path":   session.ConnectorWorkspacePath,
				"cloud_sandbox_id":           session.CloudSandboxID,
				"connector_auto_approve":     session.ConnectorAutoApprove,
				"connector_approval_mode":    session.ConnectorApprovalMode,
				"connector_command_prefixes": session.ConnectorCommandPrefixes,
				"model_name":                 session.ModelName,
				"user_channel_id":            session.UserChannelID,
				"max_tokens":                 session.MaxTokens,
				"temperature":                session.Temperature,
				"reasoning_effort":           session.ReasoningEffort,
				"auto_compress_context":      session.AutoCompressContext,
			}).Error; err != nil {
				return err
			}
		} else {
			if err := tx.Create(&session).Error; err != nil {
				return err
			}
		}
		if !replaceMessages {
			return nil
		}
		if err := tx.Where("session_id = ? AND user_id = ?", sessionID, userID).Delete(&AdvancedChatMessage{}).Error; err != nil {
			return err
		}
		now := time.Now()
		for index, message := range input.Messages {
			role := strings.ToLower(strings.TrimSpace(message.Role))
			if role != "assistant" {
				role = "user"
			}
			toolCalls, err := json.Marshal(message.ToolCalls)
			if err != nil {
				return err
			}
			contentParts, err := json.Marshal(normalizeAdvancedChatContentParts(message.Parts, message.Content))
			if err != nil {
				return err
			}
			id := normalizeAdvancedChatSessionID(message.ID)
			if id == "" {
				id = newAdvancedChatID("acm")
			}
			row := AdvancedChatMessage{
				ID:           id,
				SessionID:    sessionID,
				UserID:       userID,
				Role:         role,
				Content:      message.Content,
				ContentParts: string(contentParts),
				ToolCalls:    string(toolCalls),
				SortOrder:    index,
				CreatedAt:    now.Add(time.Duration(index) * time.Millisecond),
				UpdatedAt:    now.Add(time.Duration(index) * time.Millisecond),
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		return tx.Model(&AdvancedChatSession{}).Where("id = ? AND user_id = ?", sessionID, userID).Update("updated_at", time.Now()).Error
	})
	if err != nil {
		switch {
		case errors.Is(err, errAdvancedChatRunActive):
			return advancedChatSessionResponse{}, http.StatusConflict, "This session already has a running assistant run", err
		case errors.Is(err, errAdvancedChatSessionConflict):
			return advancedChatSessionResponse{}, http.StatusConflict, "Session id is already used", err
		default:
			return advancedChatSessionResponse{}, http.StatusInternalServerError, "Failed to save session", err
		}
	}
	session, err := advancedChatSessionResponseFor(userID, sessionID)
	if err != nil {
		return advancedChatSessionResponse{}, http.StatusInternalServerError, "Failed to load session", err
	}
	return session, http.StatusOK, "", nil
}

func createAdvancedChatAssistantRun(userID uint, prepared preparedAdvancedChatAssistantRun) (advancedChatSessionResponse, advancedChatRunResponse, int, string, error) {
	sessionID := normalizeAdvancedChatSessionID(prepared.input.SessionID)
	if sessionID == "" {
		sessionID = newAdvancedChatID("acs")
	}
	runID := newAdvancedChatID("acr")
	assistantMessageID := newAdvancedChatID("acm")
	title := strings.TrimSpace(prepared.input.Title)
	if title == "" {
		title = advancedChatTitleFromMessages(prepared.messages)
	}
	if title == "" {
		title = "Assistant session"
	}
	if len([]rune(title)) > 200 {
		title = string([]rune(title)[:200])
	}
	skillIDs, _ := json.Marshal(uniqueStringsLocal(prepared.input.SkillIDs))
	mcpServerIDs, _ := json.Marshal(uniqueStringsLocal(prepared.input.MCPServerIDs))
	knowledgeBaseIDs, _ := json.Marshal(uniqueStringsLocal(prepared.input.KnowledgeBaseIDs))
	commandPrefixes, _ := json.Marshal(normalizeConnectorCommandPrefixes(prepared.input.ConnectorCommandPrefixes))
	emptyToolCalls := "[]"
	now := time.Now()

	var sessionResp advancedChatSessionResponse
	var runResp advancedChatRunResponse
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var existingByID AdvancedChatSession
		if err := tx.Where("id = ?", sessionID).Limit(1).Find(&existingByID).Error; err != nil {
			return err
		}
		if existingByID.ID != "" && existingByID.UserID != userID {
			return errAdvancedChatSessionConflict
		}

		var activeRuns int64
		if err := tx.Model(&AdvancedChatRun{}).
			Where("session_id = ? AND user_id = ? AND status IN ?", sessionID, userID, []string{advancedChatRunStatusQueued, advancedChatRunStatusRunning}).
			Count(&activeRuns).Error; err != nil {
			return err
		}
		if activeRuns > 0 {
			return errAdvancedChatRunActive
		}

		session := AdvancedChatSession{
			ID:                       sessionID,
			UserID:                   userID,
			Title:                    title,
			RunMode:                  prepared.mode,
			AgentID:                  strings.TrimSpace(prepared.input.AgentID),
			AgentGroupID:             strings.TrimSpace(prepared.input.AgentGroupID),
			SkillIDs:                 string(skillIDs),
			MCPServerIDs:             string(mcpServerIDs),
			KnowledgeBaseIDs:         string(knowledgeBaseIDs),
			ConnectorDeviceID:        strings.TrimSpace(prepared.input.ConnectorDeviceID),
			ConnectorWorkspacePath:   prepared.connectorWorkspace,
			CloudSandboxID:           prepared.input.CloudSandboxID,
			ConnectorAutoApprove:     prepared.connectorApprovalMode == advancedChatConnectorApprovalFullAccess,
			ConnectorApprovalMode:    prepared.connectorApprovalMode,
			ConnectorCommandPrefixes: string(commandPrefixes),
			ModelName:                prepared.modelName,
			UserChannelID:            prepared.input.UserChannelID,
			MaxTokens:                normalizeAdvancedChatMaxTokens(prepared.input.MaxTokens),
			Temperature:              normalizeAdvancedChatTemperature(prepared.input.Temperature),
			ReasoningEffort:          normalizeAdvancedChatReasoningEffort(prepared.input.ReasoningEffort),
			AutoCompressContext:      prepared.input.AutoCompressContext,
		}
		var existing AdvancedChatSession
		if err := tx.Where("id = ? AND user_id = ?", sessionID, userID).Limit(1).Find(&existing).Error; err != nil {
			return err
		}
		if existing.ID != "" {
			if err := tx.Model(&existing).Updates(map[string]interface{}{
				"title":                      session.Title,
				"run_mode":                   session.RunMode,
				"agent_id":                   session.AgentID,
				"agent_group_id":             session.AgentGroupID,
				"skill_ids":                  session.SkillIDs,
				"mcp_server_ids":             session.MCPServerIDs,
				"knowledge_base_ids":         session.KnowledgeBaseIDs,
				"connector_device_id":        session.ConnectorDeviceID,
				"connector_workspace_path":   session.ConnectorWorkspacePath,
				"cloud_sandbox_id":           session.CloudSandboxID,
				"connector_auto_approve":     session.ConnectorAutoApprove,
				"connector_approval_mode":    session.ConnectorApprovalMode,
				"connector_command_prefixes": session.ConnectorCommandPrefixes,
				"model_name":                 session.ModelName,
				"user_channel_id":            session.UserChannelID,
				"max_tokens":                 session.MaxTokens,
				"temperature":                session.Temperature,
				"reasoning_effort":           session.ReasoningEffort,
				"auto_compress_context":      session.AutoCompressContext,
			}).Error; err != nil {
				return err
			}
		} else {
			if err := tx.Create(&session).Error; err != nil {
				return err
			}
		}

		if err := tx.Where("session_id = ? AND user_id = ?", sessionID, userID).Delete(&AdvancedChatMessage{}).Error; err != nil {
			return err
		}
		for index, message := range prepared.messages {
			messageID := normalizeAdvancedChatSessionID(message.ID)
			if messageID == "" {
				messageID = newAdvancedChatID("acm")
			}
			toolCalls, err := json.Marshal(message.ToolCalls)
			if err != nil {
				return err
			}
			contentParts, err := json.Marshal(normalizeAdvancedChatContentParts(message.Parts, message.Content))
			if err != nil {
				return err
			}
			row := AdvancedChatMessage{
				ID:           messageID,
				SessionID:    sessionID,
				UserID:       userID,
				Role:         message.Role,
				Content:      message.Content,
				ContentParts: string(contentParts),
				ToolCalls:    string(toolCalls),
				SortOrder:    index,
				CreatedAt:    now.Add(time.Duration(index) * time.Millisecond),
				UpdatedAt:    now.Add(time.Duration(index) * time.Millisecond),
			}
			if err := tx.Create(&row).Error; err != nil {
				return err
			}
		}
		assistantMessage := AdvancedChatMessage{
			ID:           assistantMessageID,
			SessionID:    sessionID,
			UserID:       userID,
			Role:         "assistant",
			Content:      "",
			ContentParts: "[]",
			ToolCalls:    emptyToolCalls,
			SortOrder:    len(prepared.messages),
			CreatedAt:    now.Add(time.Duration(len(prepared.messages)) * time.Millisecond),
			UpdatedAt:    now.Add(time.Duration(len(prepared.messages)) * time.Millisecond),
		}
		if err := tx.Create(&assistantMessage).Error; err != nil {
			return err
		}
		run := AdvancedChatRun{
			ID:                 runID,
			SessionID:          sessionID,
			UserID:             userID,
			AssistantMessageID: assistantMessageID,
			Mode:               prepared.mode,
			Status:             advancedChatRunStatusQueued,
			StatusMessage:      "assistant_started",
			ErrorMessage:       "",
			Cost:               decimal.Zero,
			ToolCallDetails:    emptyToolCalls,
		}
		if err := tx.Create(&run).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		switch {
		case errors.Is(err, errAdvancedChatRunActive):
			return sessionResp, runResp, http.StatusConflict, "This session already has a running assistant run", err
		case errors.Is(err, errAdvancedChatSessionConflict):
			return sessionResp, runResp, http.StatusConflict, "Session id is already used", err
		default:
			return sessionResp, runResp, http.StatusInternalServerError, "Failed to create assistant run", err
		}
	}
	maybeStartAdvancedChatTitleGeneration(userID, sessionID, prepared.messages)
	sessionResp, err = advancedChatSessionResponseFor(userID, sessionID)
	if err != nil {
		return sessionResp, runResp, http.StatusInternalServerError, "Failed to load assistant session", err
	}
	var run AdvancedChatRun
	if err := model.DB.Where("id = ? AND user_id = ?", runID, userID).First(&run).Error; err != nil {
		return sessionResp, runResp, http.StatusInternalServerError, "Failed to load assistant run", err
	}
	runResp = advancedChatRunResponseFromModel(run)
	return sessionResp, runResp, http.StatusAccepted, "", nil
}

var (
	errAdvancedChatRunActive       = errors.New("agent chat run active")
	errAdvancedChatSessionConflict = errors.New("agent chat session conflict")
	errAdvancedChatRunCancelled    = errors.New("agent chat run cancelled")
	advancedChatRunCancels         sync.Map
)

func runAdvancedChatAssistantCompletion(runID string, userID uint, prepared preparedAdvancedChatAssistantRun) {
	timeout := advancedChatCompletionTimeout(prepared.mode)
	if prepared.timeout > 0 {
		timeout = prepared.timeout
	}
	timeoutCtx, timeoutCancel := context.WithTimeout(context.Background(), timeout)
	defer timeoutCancel()
	ctx, cancel := context.WithCancel(timeoutCtx)
	defer cancel()
	advancedChatRunCancels.Store(runID, cancel)
	defer advancedChatRunCancels.Delete(runID)
	prepared.runID = runID

	var run AdvancedChatRun
	if err := model.DB.Where("id = ? AND user_id = ?", runID, userID).First(&run).Error; err != nil {
		return
	}
	prepared.input.SessionID = run.SessionID
	now := time.Now()
	startUpdate := model.DB.Model(&run).
		Where("status = ?", advancedChatRunStatusQueued).
		Updates(map[string]interface{}{
			"status":         advancedChatRunStatusRunning,
			"status_message": "assistant_started",
			"started_at":     &now,
		})
	if startUpdate.Error != nil || startUpdate.RowsAffected == 0 {
		return
	}
	appendAdvancedChatRunEvent(run.ID, run.SessionID, userID, "status", gin.H{"message": "assistant_started"})

	var user model.User
	if err := model.DB.First(&user, userID).Error; err != nil {
		failAdvancedChatRun(run.ID, run.SessionID, userID, run.AssistantMessageID, "Failed to load user: "+err.Error())
		return
	}
	observer := advancedChatCompletionObserver{
		OnStatus: func(payload gin.H) error {
			if err := ensureAdvancedChatRunNotCancelled(run.ID, userID); err != nil {
				return err
			}
			message, _ := payload["message"].(string)
			round := 0
			if value, ok := payload["round"].(int); ok {
				round = value
			}
			statusMessage := message
			if message == "retrying" {
				attempt, _ := payload["attempt"].(int)
				maxAttempts, _ := payload["max"].(int)
				if attempt > 0 && maxAttempts > 0 {
					statusMessage = fmt.Sprintf("retrying:%d/%d", attempt, maxAttempts)
				}
			}
			updates := map[string]interface{}{"status_message": statusMessage}
			if round > 0 {
				updates["current_round"] = round
			}
			if err := model.DB.Model(&AdvancedChatRun{}).Where("id = ? AND user_id = ?", run.ID, userID).Updates(updates).Error; err != nil {
				return err
			}
			return appendAdvancedChatRunEvent(run.ID, run.SessionID, userID, "status", payload)
		},
		OnText: func(delta string, round int) error {
			if err := ensureAdvancedChatRunNotCancelled(run.ID, userID); err != nil {
				return err
			}
			if err := appendAdvancedChatRunEvent(run.ID, run.SessionID, userID, "text", gin.H{"delta": delta, "round": round}); err != nil {
				return err
			}
			return nil
		},
		OnToolCall: func(detail advancedChatCompletionToolCall) error {
			if err := ensureAdvancedChatRunNotCancelled(run.ID, userID); err != nil {
				return err
			}
			if err := mergeAdvancedChatRunToolCall(run.ID, userID, run.AssistantMessageID, detail); err != nil {
				return err
			}
			return appendAdvancedChatRunEvent(run.ID, run.SessionID, userID, "tool_call", detail)
		},
	}

	var response *advancedChatCompletionResponse
	var err error
	if prepared.groupAgent != nil && advancedChatAgentStudioCanUseExecutionTools(prepared.groupAgent.Type) {
		_, err = withAdvancedChatAgentStudioLock(user.ID, prepared.input.AgentGroupID, prepared.groupAgent.ID, func() (string, error) {
			var runErr error
			response, runErr = executePreparedAdvancedChatCompletion(ctx, &user, prepared, observer, advancedChatPreparedAgentStream(prepared.agent))
			return "", runErr
		})
	} else {
		response, err = executePreparedAdvancedChatCompletion(ctx, &user, prepared, observer, advancedChatPreparedAgentStream(prepared.agent))
	}
	if err != nil {
		if errors.Is(err, errAdvancedChatRunCancelled) || errors.Is(err, context.Canceled) {
			return
		}
		failAdvancedChatRun(run.ID, run.SessionID, userID, run.AssistantMessageID, errorMessageFromAdvancedChatCompletion(err))
		return
	}
	response = advancedChatAgentGroupNamedResponse(response, prepared.groupAgent)
	finishAdvancedChatRun(run.ID, run.SessionID, userID, run.AssistantMessageID, response)
}

func executePreparedAdvancedChatCompletion(ctx context.Context, user *model.User, prepared preparedAdvancedChatAssistantRun, observer advancedChatCompletionObserver, stream bool) (*advancedChatCompletionResponse, error) {
	if observer.OnStatus != nil {
		if err := observer.OnStatus(gin.H{"message": "loading_tools"}); err != nil {
			return nil, err
		}
	}
	tools := []ChatExecutorTool{}
	mcpTools := []ChatExecutorTool{}
	bindings := map[string]mcpToolBinding{}
	studioRole := advancedChatAgentStudioRole(prepared)
	studioRoleActive := prepared.mode == advancedChatModeAgentGroup && prepared.groupAgent != nil
	studioCanExecute := !studioRoleActive || advancedChatAgentStudioCanUseExecutionTools(studioRole)
	studioCanSplit := studioRoleActive && advancedChatAgentStudioCanSplit(studioRole)
	studioCanDelegate := studioRoleActive && advancedChatAgentStudioCanDelegate(studioRole, true)
	studioCanCommit := studioRoleActive && normalizeAdvancedChatAgentType(studioRole) == "reviewer"
	externalChiefScheduler := studioRoleActive && normalizeAdvancedChatAgentType(studioRole) == "chief" && personalCompanyExternalChiefSchedulingRun(user.ID, prepared.input.AgentGroupID, prepared.runID)
	if externalChiefScheduler {
		studioCanDelegate = false
	}
	approvalChecker := prepared.approvalChecker
	if prepared.agentGroup != nil {
		if groupChecker, ok := advancedChatAgentStudioApprovalCheckerForGroup(prepared.agentGroup); ok {
			approvalChecker = groupChecker
		}
	}
	if advancedChatAssistantMCPToolsEnabled() {
		var err error
		mcpTools, bindings, err = listAdvancedChatMCPTools(ctx, user.ID, prepared.runID, prepared.connectorDevice, prepared.servers)
		if err != nil {
			return nil, fmt.Errorf("Failed to load MCP tools: %w", err)
		}
		if studioCanExecute {
			tools = append(tools, mcpTools...)
		} else {
			mcpTools = nil
			bindings = map[string]mcpToolBinding{}
		}
	}
	allConnectorTools, allConnectorBindings := advancedChatConnectorToolsWithApprovalModeAndSandbox(prepared.connectorDevice, prepared.connectorWorkspace, prepared.connectorApprovalMode, prepared.connectorCommandPrefixes, prepared.cloudSandboxID)
	connectorTools := allConnectorTools
	connectorBindings := allConnectorBindings
	if studioRoleActive {
		connectorTools, connectorBindings = filterAdvancedChatAgentStudioConnectorTools(studioRole, allConnectorTools, allConnectorBindings)
		if externalChiefScheduler {
			connectorTools = nil
			connectorBindings = map[string]advancedChatConnectorToolBinding{}
		}
	}
	if len(connectorTools) > 0 {
		tools = append(tools, connectorTools...)
	}
	hasSkillCatalog := len(prepared.skills) > 0 || len(prepared.workspaceSkills) > 0
	if hasSkillCatalog {
		tools = append(tools, advancedChatSkillTools(true)...)
	}
	if len(prepared.agentGroups) > 0 && studioCanDelegate {
		tools = append(tools, advancedChatAgentDelegateTool(prepared.agentGroups))
	}
	if studioCanSplit {
		tools = append(tools, advancedChatAgentSplitTool())
	}
	if studioRoleActive && !externalChiefScheduler {
		tools = append(tools, advancedChatAgentStudioInterruptTool(), advancedChatAgentStudioQueryStatusTool(), advancedChatAgentStudioResumeTool())
	}
	if studioCanCommit && len(connectorTools) > 0 {
		tools = append(tools, advancedChatAgentStudioCommitDeltaTool())
	}
	deliveryToolName := ""
	if prepared.delivery != nil {
		deliveryToolName = "deliver_result"
		tools = append(tools, advancedChatDeliveryTool(deliveryToolName))
	}
	systemPrompt := buildAdvancedChatCompletionSystemPrompt(prepared.agent, prepared.skills, prepared.workspaceSkills, prepared.mode)
	extension, err := BuildAdvancedChatRuntimeExtension(ctx, AdvancedChatRuntimeContext{
		UserID:       user.ID,
		Mode:         prepared.mode,
		AgentID:      prepared.input.AgentID,
		AgentGroupID: prepared.input.AgentGroupID,
		SessionID:    prepared.input.SessionID,
		RunID:        prepared.runID,
	})
	if err != nil {
		return nil, fmt.Errorf("Failed to load assistant extensions: %w", err)
	}
	extensionToolNames := map[string]bool{}
	if len(extension.Tools) > 0 {
		if externalChiefScheduler {
			for _, tool := range extension.Tools {
				if tool.Name == personalCompanyChiefCreateWorkToolName || tool.Name == personalCompanyChiefListWorkToolName {
					tools = append(tools, tool)
					extensionToolNames[tool.Name] = true
				}
			}
		} else {
			tools = append(tools, extension.Tools...)
			for _, tool := range extension.Tools {
				extensionToolNames[tool.Name] = true
			}
		}
	}
	if groupPrompt := advancedChatAgentGroupChatSystemPrompt(prepared.agentGroup, prepared.groupAgent); groupPrompt != "" {
		if strings.TrimSpace(systemPrompt) == "" {
			systemPrompt = groupPrompt
		} else {
			systemPrompt = strings.Join([]string{systemPrompt, groupPrompt}, "\n\n")
		}
	}
	if studioRoleActive {
		if agentGroupPrompt := advancedChatAgentGroupSystemPrompt(prepared.agentGroups); agentGroupPrompt != "" {
			if strings.TrimSpace(systemPrompt) == "" {
				systemPrompt = agentGroupPrompt
			} else {
				systemPrompt = strings.Join([]string{systemPrompt, agentGroupPrompt}, "\n\n")
			}
		}
	}
	if studioRoleActive {
		if studioPrompt := advancedChatAgentStudioPrompt(studioRole, prepared.connectorDevice != nil); studioPrompt != "" {
			if strings.TrimSpace(systemPrompt) == "" {
				systemPrompt = studioPrompt
			} else {
				systemPrompt = strings.Join([]string{systemPrompt, studioPrompt}, "\n\n")
			}
		}
	}
	if studioCanSplit {
		if splitPrompt := advancedChatAgentSplitSystemPrompt(); splitPrompt != "" {
			if strings.TrimSpace(systemPrompt) == "" {
				systemPrompt = splitPrompt
			} else {
				systemPrompt = strings.Join([]string{systemPrompt, splitPrompt}, "\n\n")
			}
		}
	}
	if connectorPrompt := advancedChatAgentStudioConnectorSystemPrompt(studioRole, prepared.connectorDevice, prepared.connectorWorkspace); connectorPrompt != "" && len(connectorTools) > 0 {
		if strings.TrimSpace(systemPrompt) == "" {
			systemPrompt = connectorPrompt
		} else {
			systemPrompt = strings.Join([]string{systemPrompt, connectorPrompt}, "\n\n")
		}
	}
	if prepared.delivery != nil {
		deliveryPrompt := "When the scheduled task is complete, call the deliver_result tool with a concise title and the final result body. Do this after you have produced the final result."
		if strings.TrimSpace(systemPrompt) == "" {
			systemPrompt = deliveryPrompt
		} else {
			systemPrompt = strings.Join([]string{systemPrompt, deliveryPrompt}, "\n\n")
		}
	}
	if strings.TrimSpace(extension.SystemPrompt) != "" {
		if strings.TrimSpace(systemPrompt) == "" {
			systemPrompt = extension.SystemPrompt
		} else {
			systemPrompt = strings.Join([]string{systemPrompt, extension.SystemPrompt}, "\n\n")
		}
	}
	if strings.TrimSpace(prepared.knowledgeContext) != "" {
		if strings.TrimSpace(systemPrompt) == "" {
			systemPrompt = prepared.knowledgeContext
		} else {
			systemPrompt = strings.Join([]string{systemPrompt, prepared.knowledgeContext}, "\n\n")
		}
	}
	executorMessages := make([]ChatExecutorMessage, 0, len(prepared.presetMessages)+len(prepared.messages)+prepared.maxToolRounds*2)
	executorMessages = append(executorMessages, advancedChatPresetExecutorMessages(prepared.presetMessages)...)
	modelMessages := prepared.messages
	if prepared.input.AutoCompressContext { modelMessages = compressAdvancedChatMessages(modelMessages) }
	for _, message := range modelMessages {
		executorMessages = append(executorMessages, advancedChatExecutorMessageForPreparedRun(user.ID, message, prepared))
	}

	totalCost := decimal.Zero
	totalToolCalls := 0
	toolCallDetails := []advancedChatCompletionToolCall{}
	contentParts := []advancedChatContentPart{}
	var lastContent string
	for round := 0; round < prepared.maxToolRounds; round++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if observer.OnStatus != nil {
			if err := observer.OnStatus(gin.H{"message": "model_round", "round": round + 1, "mode": prepared.mode}); err != nil {
				return nil, err
			}
		}
		streamedText := false
		request := ChatExecutorRequest{
			Context:         ctx,
			ModelName:       prepared.modelName,
			UserChannelID:   prepared.input.UserChannelID,
			Messages:        executorMessages,
			System:          systemPrompt,
			Tools:           tools,
			MaxTokens:       prepared.input.MaxTokens,
			Temperature:     prepared.input.Temperature,
			ReasoningEffort: normalizeAdvancedChatReasoningEffort(prepared.input.ReasoningEffort),
			Stream:          stream,
			OnTextDelta: func(delta string) error {
				if !stream || delta == "" || observer.OnText == nil {
					return nil
				}
				streamedText = true
				return observer.OnText(delta, round+1)
			},
			ChargeBalance: prepared.input.ChargeBalance,
		}
		result, err := executeAdvancedChatModelRequestWithRetry(ctx, user, request, observer, func() bool {
			return !streamedText
		})
		if err != nil {
			return nil, err
		}
		totalCost = totalCost.Add(result.Cost)
		lastContent = result.Content
		contentParts = appendAdvancedChatContentPart(contentParts, round+1, result.Content)
		if stream && !streamedText && strings.TrimSpace(result.Content) != "" && observer.OnText != nil {
			if err := observer.OnText(result.Content, round+1); err != nil {
				return nil, err
			}
		}
		if len(result.ToolCalls) == 0 {
			return &advancedChatCompletionResponse{
				Message:         advancedChatCompletionMessage{Role: "assistant", Content: result.Content, Parts: contentParts},
				Cost:            totalCost,
				ToolCalls:       totalToolCalls,
				ToolCallDetails: toolCallDetails,
			}, nil
		}

		totalToolCalls += len(result.ToolCalls)
		executorMessages = append(executorMessages, ChatExecutorMessage{
			Role:      "assistant",
			Content:   result.Content,
			ToolCalls: normalizeAssistantToolCalls(result.AssistantMessage),
		})
		for _, toolCall := range result.ToolCalls {
			binding, exists := bindings[toolCall.Name]
			connectorBinding, connectorExists := connectorBindings[toolCall.Name]
			deliveryExists := prepared.delivery != nil && toolCall.Name == deliveryToolName
			agentDelegateExists := toolCall.Name == advancedChatAgentDelegateToolName && len(prepared.agentGroups) > 0 && studioCanDelegate
			agentSplitExists := toolCall.Name == advancedChatAgentSplitToolName && studioCanSplit
			commitDeltaExists := toolCall.Name == advancedChatAgentStudioCommitDeltaToolName && studioCanCommit
			interruptExists := toolCall.Name == advancedChatAgentStudioInterruptToolName && studioRoleActive
			queryStatusExists := toolCall.Name == advancedChatAgentStudioQueryStatusToolName && studioRoleActive
			resumeExists := toolCall.Name == advancedChatAgentStudioResumeToolName && studioRoleActive
			activateSkillExists := toolCall.Name == advancedChatActivateSkillToolName && hasSkillCatalog
			readSkillResourceExists := toolCall.Name == advancedChatReadSkillResourceToolName && hasSkillCatalog
			extensionExists := extensionToolNames[toolCall.Name] && AdvancedChatToolHandlerExists(toolCall.Name)
			detail := advancedChatCompletionToolCall{ID: toolCall.ID, Round: round + 1, Name: toolCall.Name, Status: "running"}
			precreatedConnectorTaskID := ""
			var precreateConnectorTaskErr error
			toolResultText := "Tool not found: " + toolCall.Name
			if exists {
				detail.Server = binding.Server.Name
				detail.Tool = binding.Tool.Name
			} else if connectorExists {
				detail.Server = connectorBinding.DeviceName
				detail.Tool = connectorBinding.Action
			} else if deliveryExists {
				detail.Server = "result delivery"
				detail.Tool = "deliver_result"
			} else if agentDelegateExists {
				detail.Server = "Agent Studio"
				detail.Tool = "agent_delegate"
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
			} else if activateSkillExists {
				detail.Server = "skills"
				detail.Tool = advancedChatActivateSkillToolName
			} else if readSkillResourceExists {
				detail.Server = "skills"
				detail.Tool = advancedChatReadSkillResourceToolName
			} else if extensionExists {
				detail.Server = "agent chat"
				detail.Tool = toolCall.Name
			}
			arguments, argumentsErr := parseToolArguments(toolCall.Arguments)
			if argumentsErr == nil {
				if connectorExists {
					arguments = advancedChatConnectorToolPreviewArguments(ctx, user.ID, prepared.runID, connectorBinding, arguments)
					arguments = advancedChatConnectorArgumentsWithToolCallID(arguments, toolCall.ID)
				}
				detail.Arguments = arguments
			}
			if connectorExists && argumentsErr == nil && advancedChatConnectorTaskRequiresApproval(connectorBinding, arguments) {
				task, err := createAdvancedChatConnectorTask(user.ID, prepared.runID, connectorBinding, arguments)
				if err != nil {
					precreateConnectorTaskErr = err
					detail.Status = "error"
				} else {
					precreatedConnectorTaskID = task.ID
					arguments = advancedChatConnectorArgumentsWithTaskID(arguments, task.ID)
					detail.Arguments = arguments
					detail.Status = "approval_required"
					if approvalChecker != nil {
						if value, err := approveAdvancedChatConnectorTaskWithChecker(ctx, user, prepared.runID, prepared.input.SessionID, approvalChecker, task, connectorBinding, arguments, observer, prepared.input.UserChannelID, round+1, prepared.input.ChargeBalance); err != nil {
							precreateConnectorTaskErr = err
							toolResultText = "Checker approval failed: " + err.Error()
						} else if strings.TrimSpace(value) != "" {
							toolResultText = value
						}
					}
				}
			}
			if observer.OnToolCall != nil {
				if err := observer.OnToolCall(detail); err != nil {
					return nil, err
				}
			}
			detail.Status = "missing"
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
			} else if connectorExists {
				detail.Server = connectorBinding.DeviceName
				detail.Tool = connectorBinding.Action
				if argumentsErr != nil {
					detail.Status = "invalid_arguments"
					toolResultText = "Invalid tool arguments: " + argumentsErr.Error()
				} else if precreateConnectorTaskErr != nil {
					detail.Status = "error"
					toolResultText = "Connector task unavailable: " + precreateConnectorTaskErr.Error()
				} else {
					var toolResult string
					var err error
					if precreatedConnectorTaskID != "" {
						toolResult, err = waitAdvancedChatConnectorTask(ctx, precreatedConnectorTaskID, user.ID)
					} else {
						toolResult, err = callAdvancedChatConnectorToolExpanded(ctx, user.ID, prepared.runID, connectorBinding, arguments)
					}
					if err != nil {
						detail.Status = "error"
						toolResultText = "Connector tool failed: " + err.Error()
						if strings.TrimSpace(toolResult) != "" {
							toolResultText = strings.TrimSpace(toolResult) + "\n\n" + toolResultText
						}
					} else {
						detail.Status = "ok"
						toolResultText = toolResult
					}
				}
			} else if extensionExists {
				detail.Server = "agent chat"
				detail.Tool = toolCall.Name
				if argumentsErr != nil {
					detail.Status = "invalid_arguments"
					toolResultText = "Invalid tool arguments: " + argumentsErr.Error()
				} else {
					toolResultText, err = HandleAdvancedChatToolCall(ctx, AdvancedChatToolCallInput{
						UserID:       user.ID,
						Mode:         prepared.mode,
						AgentID:      prepared.input.AgentID,
						AgentGroupID: prepared.input.AgentGroupID,
						SessionID:    prepared.input.SessionID,
						RunID:        prepared.runID,
						Name:         toolCall.Name,
						Arguments:    arguments,
					})
					if err != nil {
						detail.Status = "error"
						toolResultText = "Tool call failed: " + err.Error()
					} else {
						detail.Status = "ok"
					}
				}
			} else if deliveryExists {
				detail.Server = "result delivery"
				detail.Tool = "deliver_result"
				if argumentsErr != nil {
					detail.Status = "invalid_arguments"
					toolResultText = "Invalid delivery arguments: " + argumentsErr.Error()
				} else {
					toolResultText, err = deliverAdvancedChatResult(ctx, user.ID, prepared.delivery, arguments)
					if err != nil {
						detail.Status = "error"
						toolResultText = "Delivery failed: " + err.Error()
					} else {
						detail.Status = "ok"
					}
				}
			} else if agentDelegateExists {
				detail.Server = "Agent Studio"
				detail.Tool = "agent_delegate"
				if argumentsErr != nil {
					detail.Status = "invalid_arguments"
					toolResultText = "Invalid delegation arguments: " + argumentsErr.Error()
				} else {
					toolResultText, err = executeAdvancedChatAgentDelegate(ctx, user, advancedChatAgentDelegateInput{
						UserID:             user.ID,
						RunID:              prepared.runID,
						SessionID:          prepared.input.SessionID,
						ToolCallID:         toolCall.ID,
						ModelName:          prepared.modelName,
						UserChannelID:      prepared.input.UserChannelID,
						Messages:           executorMessages,
						WorkspaceSkills:    prepared.workspaceSkills,
						ConnectorDevice:    prepared.connectorDevice,
						ConnectorWorkspace: prepared.connectorWorkspace,
						ConnectorBindings:  allConnectorBindings,
						ConnectorTools:     allConnectorTools,
						Groups:             prepared.agentGroups,
						CallerAgentName:    preparedGroupAgentName(prepared.groupAgent),
						Observer:           observer,
						Arguments:          arguments,
						DisplayRound:       round + 1,
						ChargeBalance:      prepared.input.ChargeBalance,
					})
					if err != nil {
						detail.Status = "error"
						toolResultText = "Delegated agent failed: " + err.Error()
					} else {
						detail.Status = "ok"
					}
				}
			} else if agentSplitExists {
				detail.Server = "agent split"
				detail.Tool = "agent_split"
				if argumentsErr != nil {
					detail.Status = "invalid_arguments"
					toolResultText = "Invalid split arguments: " + argumentsErr.Error()
				} else {
					splitTools := append([]ChatExecutorTool{}, mcpTools...)
					splitTools = append(splitTools, connectorTools...)
					toolResultText, err = executeAdvancedChatAgentSplit(ctx, user, advancedChatAgentSplitInput{
						RunID:             prepared.runID,
						SessionID:         prepared.input.SessionID,
						ToolCallID:        toolCall.ID,
						ModelName:         prepared.modelName,
						UserChannelID:     prepared.input.UserChannelID,
						SystemPrompt:      systemPrompt,
						Messages:          executorMessages,
						Tools:             splitTools,
						MCPBindings:       bindings,
						ConnectorBindings: connectorBindings,
						Observer:          observer,
						Stream:            advancedChatPreparedAgentStream(prepared.agent),
						Arguments:         arguments,
						ApprovalChecker:   approvalChecker,
						DisplayRound:      round + 1,
						ChargeBalance:     prepared.input.ChargeBalance,
					})
					if err != nil {
						detail.Status = "error"
						toolResultText = "Split agent failed: " + err.Error()
					} else {
						detail.Status = "ok"
					}
				}
			} else if commitDeltaExists {
				detail.Server = "agent studio"
				detail.Tool = "workspace_commit_delta"
				if argumentsErr != nil {
					detail.Status = "invalid_arguments"
					toolResultText = "Invalid delta commit arguments: " + argumentsErr.Error()
				} else if len(connectorBindings) == 0 {
					detail.Status = "error"
					toolResultText = "No connector workspace is available for commit."
				} else {
					var commitBinding advancedChatConnectorToolBinding
					for _, binding := range connectorBindings {
						commitBinding = binding
						break
					}
					toolResultText, err = commitAdvancedChatAgentStudioDelta(ctx, user, prepared.runID, prepared.input.SessionID, commitBinding, arguments, approvalChecker, observer, prepared.input.UserChannelID, round+1, prepared.input.ChargeBalance)
					if err != nil {
						detail.Status = "error"
						if strings.TrimSpace(toolResultText) != "" {
							toolResultText = strings.TrimSpace(toolResultText) + "\n\nDelta commit failed: " + err.Error()
						} else {
							toolResultText = "Delta commit failed: " + err.Error()
						}
					} else {
						detail.Status = "ok"
					}
				}
			} else if interruptExists {
				detail.Server = "agent studio"
				detail.Tool = "interrupt_sub_agents"
				if argumentsErr != nil {
					detail.Status = "invalid_arguments"
					toolResultText = "Invalid sub-agent interrupt arguments: " + argumentsErr.Error()
				} else {
					toolResultText, err = interruptAdvancedChatAgentStudioSubAgents(prepared.runID, prepared.input.SessionID, user.ID, arguments)
					if err != nil {
						detail.Status = "error"
						toolResultText = "Sub-agent interrupt failed: " + err.Error()
					} else {
						detail.Status = "ok"
					}
				}
			} else if queryStatusExists {
				detail.Server = "agent studio"
				detail.Tool = "query_sub_agent_status"
				if argumentsErr != nil {
					detail.Status = "invalid_arguments"
					toolResultText = "Invalid sub-agent status query arguments: " + argumentsErr.Error()
				} else {
					toolResultText, err = queryAdvancedChatAgentStudioSubAgentStatus(prepared.runID, user.ID, arguments)
					if err != nil {
						detail.Status = "error"
						toolResultText = "Sub-agent status query failed: " + err.Error()
					} else {
						detail.Status = "ok"
					}
				}
			} else if resumeExists {
				detail.Server = "agent studio"
				detail.Tool = "resume_sub_agents"
				if argumentsErr != nil {
					detail.Status = "invalid_arguments"
					toolResultText = "Invalid sub-agent resume arguments: " + argumentsErr.Error()
				} else {
					toolResultText, err = resumeAdvancedChatAgentStudioSubAgents(prepared.runID, prepared.input.SessionID, user.ID, arguments)
					if err != nil {
						detail.Status = "error"
						toolResultText = "Sub-agent resume failed: " + err.Error()
					} else {
						detail.Status = "ok"
					}
				}
			} else if activateSkillExists {
				detail.Server = "skills"
				detail.Tool = advancedChatActivateSkillToolName
				if argumentsErr != nil {
					detail.Status = "invalid_arguments"
					toolResultText = "Invalid skill activation arguments: " + argumentsErr.Error()
				} else {
					toolResultText, err = activateAdvancedChatSkill(ctx, user.ID, prepared.connectorDevice, prepared.connectorWorkspace, prepared.workspaceSkills, arguments)
					if err != nil {
						detail.Status = "error"
						toolResultText = "Skill activation failed: " + err.Error()
					} else {
						detail.Status = "ok"
					}
				}
			} else if readSkillResourceExists {
				detail.Server = "skills"
				detail.Tool = advancedChatReadSkillResourceToolName
				if argumentsErr != nil {
					detail.Status = "invalid_arguments"
					toolResultText = "Invalid skill resource arguments: " + argumentsErr.Error()
				} else {
					toolResultText, err = readAdvancedChatSkillResource(ctx, user.ID, prepared.connectorDevice, prepared.connectorWorkspace, prepared.workspaceSkills, arguments)
					if err != nil {
						detail.Status = "error"
						toolResultText = "Skill resource read failed: " + err.Error()
					} else {
						detail.Status = "ok"
					}
				}
			}
			detail.Result = truncateToolResult(toolResultText)
			toolCallDetails = append(toolCallDetails, detail)
			if observer.OnToolCall != nil {
				if err := observer.OnToolCall(detail); err != nil {
					return nil, err
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
	return &advancedChatCompletionResponse{
		Message:         advancedChatCompletionMessage{Role: "assistant", Content: strings.TrimSpace(lastContent), Parts: contentParts},
		Cost:            totalCost,
		ToolCalls:       totalToolCalls,
		ToolCallDetails: toolCallDetails,
	}, nil
}

func advancedChatPresetExecutorMessages(values []AdvancedChatAgentPresetMessage) []ChatExecutorMessage {
	messages := make([]ChatExecutorMessage, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value.Content) == "" {
			continue
		}
		messages = append(messages, ChatExecutorMessage{Role: value.Role, Content: value.Content})
	}
	return messages
}

func executeAdvancedChatModelRequestWithRetry(
	ctx context.Context,
	user *model.User,
	request ChatExecutorRequest,
	observer advancedChatCompletionObserver,
	canRetry func() bool,
) (*ChatExecutorResult, error) {
	var lastErr error
	for attempt := 0; attempt <= assistantModelMaxRetries; attempt++ {
		result, err := ExecuteServerChatCompletion(nil, user, request)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if attempt == assistantModelMaxRetries || !retryableAdvancedChatModelRequestError(err) || (canRetry != nil && !canRetry()) {
			return nil, err
		}
		if observer.OnStatus != nil {
			if err := observer.OnStatus(advancedChatModelRetryStatusPayload(err, attempt+1)); err != nil {
				return nil, err
			}
		}
		if err := sleepAdvancedChatModelRetry(ctx, attempt); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func advancedChatModelRetryStatusPayload(err error, attempt int) gin.H {
	payload := gin.H{"message": "retrying", "attempt": attempt, "max": assistantModelMaxRetries}
	if err == nil {
		return payload
	}
	payload["error"] = err.Error()
	var executorErr *ChatExecutorError
	if errors.As(err, &executorErr) {
		payload["status"] = executorErr.Status
		if executorErr.ChannelID > 0 {
			payload["channel_id"] = executorErr.ChannelID
		}
		if executorErr.UserChannelID > 0 {
			payload["user_channel_id"] = executorErr.UserChannelID
		}
		if strings.TrimSpace(executorErr.ModelName) != "" {
			payload["model"] = strings.TrimSpace(executorErr.ModelName)
		}
		if strings.TrimSpace(executorErr.UpstreamModelName) != "" {
			payload["upstream_model"] = strings.TrimSpace(executorErr.UpstreamModelName)
		}
	}
	return payload
}

func retryableAdvancedChatModelRequestError(err error) bool {
	var executorErr *ChatExecutorError
	if !errors.As(err, &executorErr) {
		return false
	}
	if executorErr.Status == http.StatusRequestTimeout || executorErr.Status == http.StatusTooManyRequests {
		return true
	}
	if executorErr.Status < http.StatusInternalServerError {
		return false
	}
	message := strings.TrimSpace(executorErr.Message)
	return message == "Upstream request failed" ||
		strings.HasPrefix(message, "Failed to read upstream") ||
		strings.HasPrefix(message, "Failed to parse upstream")
}

func sleepAdvancedChatModelRetry(ctx context.Context, attempt int) error {
	delay := advancedChatModelRetryDelay(attempt)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func advancedChatModelRetryDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	delay := assistantModelRetryDelay
	for index := 0; index < attempt; index++ {
		delay *= 2
		if delay >= assistantModelRetryMaxDelay {
			return assistantModelRetryMaxDelay
		}
	}
	return delay
}

func finishAdvancedChatRun(runID string, sessionID string, userID uint, assistantMessageID string, response *advancedChatCompletionResponse) {
	if response == nil {
		return
	}
	content := strings.TrimSpace(response.Message.Content)
	contentParts, _ := json.Marshal(normalizeAdvancedChatContentParts(response.Message.Parts, content))
	now := time.Now()
	finished := false
	eventState := advancedChatRunEventStateFor(userID, runID)
	eventState.mutex.Lock()
	_ = model.DB.Transaction(func(tx *gorm.DB) error {
		var currentRun AdvancedChatRun
		if err := tx.Where("id = ? AND user_id = ?", runID, userID).First(&currentRun).Error; err != nil {
			return err
		}
		toolCallDetails := mergeAdvancedChatToolCallDetailList(decodeAdvancedChatToolCalls(currentRun.ToolCallDetails), response.ToolCallDetails)
		toolCallCount := len(toolCallDetails)
		if toolCallCount == 0 {
			toolCallCount = response.ToolCalls
		}
		toolDetailsJSON, err := json.Marshal(toolCallDetails)
		if err != nil {
			return err
		}
		update := tx.Model(&AdvancedChatRun{}).
			Where("id = ? AND user_id = ? AND status IN ?", runID, userID, []string{advancedChatRunStatusQueued, advancedChatRunStatusRunning}).
			Updates(map[string]interface{}{
				"status":            advancedChatRunStatusCompleted,
				"status_message":    "completed",
				"error_message":     "",
				"cost":              response.Cost,
				"tool_calls":        toolCallCount,
				"tool_call_details": string(toolDetailsJSON),
				"finished_at":       &now,
			})
		if update.Error != nil || update.RowsAffected == 0 {
			return update.Error
		}
		if err := tx.Model(&AdvancedChatMessage{}).
			Where("id = ? AND user_id = ?", assistantMessageID, userID).
			Updates(map[string]interface{}{"content": content, "content_parts": string(contentParts), "tool_calls": string(toolDetailsJSON)}).Error; err != nil {
			return err
		}
		response.ToolCallDetails = toolCallDetails
		response.ToolCalls = toolCallCount
		finished = true
		return nil
	})
	eventState.mutex.Unlock()
	if finished {
		_ = appendAdvancedChatRunEvent(runID, sessionID, userID, "done", response)
	}
}

func advancedChatExecutorMessageForPreparedRun(userID uint, message advancedChatCompletionMessage, prepared preparedAdvancedChatAssistantRun) ChatExecutorMessage {
	executorMessage := advancedChatExecutorMessage(userID, message)
	if prepared.mode != advancedChatModeAgentGroup || strings.TrimSpace(executorMessage.Content) == "" {
		return executorMessage
	}
	switch message.Role {
	case "assistant":
		executorMessage.Content = "From " + advancedChatAgentGroupAssistantSpeaker(message.Content) + ":\n" + strings.TrimSpace(message.Content)
	default:
		executorMessage.Content = "From user:\n" + strings.TrimSpace(message.Content)
	}
	return executorMessage
}

func advancedChatAgentGroupAssistantSpeaker(content string) string {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "[") {
		if end := strings.Index(content, "]"); end > 1 && end <= 120 {
			name := strings.TrimSpace(content[1:end])
			if name != "" {
				return "agent " + name
			}
		}
	}
	return "an agent"
}

func advancedChatAgentGroupNamedResponse(response *advancedChatCompletionResponse, agent *advancedChatGroupAgent) *advancedChatCompletionResponse {
	if response == nil || agent == nil {
		return response
	}
	name := strings.TrimSpace(agent.Name)
	if name == "" {
		return response
	}
	prefix := "[" + name + "]"
	content := strings.TrimSpace(response.Message.Content)
	if !strings.HasPrefix(content, prefix) {
		content = strings.TrimSpace(prefix + " " + content)
		response.Message.Content = content
		response.Message.Parts = normalizeAdvancedChatContentParts(nil, content)
	}
	return response
}

func preparedGroupAgentName(agent *advancedChatGroupAgent) string {
	if agent == nil {
		return ""
	}
	return strings.TrimSpace(agent.Name)
}

func advancedChatPreparedAgentStream(agent *AdvancedChatAgent) bool {
	return agent != nil && agent.Stream
}

func failAdvancedChatRun(runID string, sessionID string, userID uint, assistantMessageID string, message string) {
	if strings.TrimSpace(message) == "" {
		message = "Assistant run failed"
	}
	now := time.Now()
	failed := false
	_ = model.DB.Transaction(func(tx *gorm.DB) error {
		update := tx.Model(&AdvancedChatRun{}).
			Where("id = ? AND user_id = ? AND status IN ?", runID, userID, []string{advancedChatRunStatusQueued, advancedChatRunStatusRunning}).
			Updates(map[string]interface{}{
				"status":         advancedChatRunStatusFailed,
				"status_message": "failed",
				"error_message":  message,
				"finished_at":    &now,
			})
		if update.Error != nil || update.RowsAffected == 0 {
			return update.Error
		}
		if err := tx.Model(&AdvancedChatMessage{}).
			Where("id = ? AND user_id = ? AND content = ?", assistantMessageID, userID, "").
			Update("content", message).Error; err != nil {
			return err
		}
		failed = true
		return nil
	})
	if failed {
		_ = appendAdvancedChatRunEvent(runID, sessionID, userID, "error", gin.H{"error": message})
	}
}

// appendAdvancedChatAssistantContent is only used when an upstream completes
// without emitting deltas. Streaming runs persist their final response once.
func appendAdvancedChatAssistantContent(messageID string, userID uint, delta string, round int) error {
	if delta == "" {
		return nil
	}
	if round <= 0 {
		round = 1
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var message AdvancedChatMessage
		if err := tx.Where("id = ? AND user_id = ?", messageID, userID).First(&message).Error; err != nil {
			return err
		}
		message.Content += delta
		parts := appendAdvancedChatContentPart(decodeAdvancedChatContentParts(message.ContentParts), round, delta)
		encoded, err := json.Marshal(parts)
		if err != nil {
			return err
		}
		return tx.Model(&message).Updates(map[string]interface{}{"content": message.Content, "content_parts": string(encoded)}).Error
	})
}

func createPersistedAdvancedChatCompletionSession(userID uint, input advancedChatCompletionInput, messages []advancedChatCompletionMessage, mode string, modelName string) (string, string, int, string, error) {
	sessionID := normalizeAdvancedChatSessionID(input.SessionID)
	if sessionID == "" {
		return "", "", http.StatusOK, "", nil
	}
	sessionMessages := make([]advancedChatSessionMessageInput, 0, len(messages))
	for _, message := range messages {
		sessionMessages = append(sessionMessages, advancedChatSessionMessageInput{
			ID:        message.ID,
			Role:      message.Role,
			Content:   message.Content,
			ToolCalls: message.ToolCalls,
		})
	}
	snapshot := advancedChatSessionInput{
		ID:                       sessionID,
		Title:                    input.Title,
		RunMode:                  mode,
		AgentID:                  input.AgentID,
		AgentGroupID:             input.AgentGroupID,
		SkillIDs:                 input.SkillIDs,
		MCPServerIDs:             input.MCPServerIDs,
		KnowledgeBaseIDs:         input.KnowledgeBaseIDs,
		ConnectorDeviceID:        input.ConnectorDeviceID,
		ConnectorWorkspacePath:   input.ConnectorWorkspacePath,
		CloudSandboxID:           input.CloudSandboxID,
		ConnectorAutoApprove:     input.ConnectorAutoApprove,
		ConnectorApprovalMode:    normalizeAdvancedChatConnectorApprovalMode(input.ConnectorApprovalMode),
		ConnectorCommandPrefixes: input.ConnectorCommandPrefixes,
		ModelName:                modelName,
		UserChannelID:            input.UserChannelID,
		MaxTokens:                normalizeAdvancedChatMaxTokens(input.MaxTokens),
		Temperature:              normalizeAdvancedChatTemperature(input.Temperature),
		ReasoningEffort:          normalizeAdvancedChatReasoningEffort(input.ReasoningEffort),
		AutoCompressContext:      input.AutoCompressContext,
		Messages:                 sessionMessages,
	}
	if _, status, message, err := saveAdvancedChatSessionSnapshot(userID, sessionID, snapshot, true); err != nil {
		return "", "", status, message, err
	}
	maybeStartAdvancedChatTitleGeneration(userID, sessionID, messages)
	assistantMessageID := newAdvancedChatID("acm")
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		row := AdvancedChatMessage{
			ID:           assistantMessageID,
			SessionID:    sessionID,
			UserID:       userID,
			Role:         "assistant",
			Content:      "",
			ContentParts: "[]",
			ToolCalls:    "[]",
			SortOrder:    len(messages),
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		return tx.Model(&AdvancedChatSession{}).Where("id = ? AND user_id = ?", sessionID, userID).Update("updated_at", time.Now()).Error
	})
	if err != nil {
		return "", "", http.StatusInternalServerError, "Failed to save assistant message", err
	}
	return sessionID, assistantMessageID, http.StatusOK, "", nil
}

func (api *advancedChatAPI) regenerateSessionTitle(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	sessionID := normalizeAdvancedChatSessionID(c.Param("id"))
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid session id"})
		return
	}
	if personalCompanyInternalSession(user.ID, sessionID) {
		c.JSON(http.StatusConflict, gin.H{"error": "Internal Studio sessions are immutable"})
		return
	}
	settings := ensureAdvancedChatUserSettings(user.ID)
	modelName := strings.TrimSpace(settings.TitleModelName)
	if modelName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Title model is not configured"})
		return
	}
	content, err := advancedChatSessionTitleSource(user.ID, sessionID, settings.TitleGenerationScope)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Session not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load session"})
		return
	}
	if strings.TrimSpace(content) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Session has no content"})
		return
	}
	title, err := generateAdvancedChatTitle(user.ID, content, modelName, settings.TitleUserChannelID)
	if err != nil {
		writeAdvancedChatCompletionError(c, err)
		return
	}
	if title == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to generate title"})
		return
	}
	if err := model.DB.Model(&AdvancedChatSession{}).Where("id = ? AND user_id = ?", sessionID, user.ID).Update("title", title).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save title"})
		return
	}
	session, err := advancedChatSessionResponseFor(user.ID, sessionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load session"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"title": title, "session": session})
}

func maybeStartAdvancedChatTitleGeneration(userID uint, sessionID string, messages []advancedChatCompletionMessage) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || len(messages) != 1 {
		return
	}
	content := strings.TrimSpace(messages[0].Content)
	if content == "" {
		return
	}
	settings := ensureAdvancedChatUserSettings(userID)
	modelName := strings.TrimSpace(settings.TitleModelName)
	if modelName == "" {
		return
	}
	go generateAndSaveAdvancedChatSessionTitle(userID, sessionID, content, modelName, settings.TitleUserChannelID)
}

func generateAndSaveAdvancedChatSessionTitle(userID uint, sessionID string, content string, modelName string, userChannelID uint) {
	title, err := generateAdvancedChatTitle(userID, content, modelName, userChannelID)
	if err != nil || title == "" {
		return
	}
	_ = model.DB.Model(&AdvancedChatSession{}).
		Where("id = ? AND user_id = ?", sessionID, userID).
		Update("title", title).Error
}

func generateAdvancedChatTitle(userID uint, content string, modelName string, userChannelID uint) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var user model.User
	if err := model.DB.First(&user, userID).Error; err != nil {
		return "", err
	}
	system := "Generate a concise title for this conversation. Return only the title, no quotes, no markdown. Use the same language as the user when possible. Keep it under 16 words."
	result, err := ExecuteServerChatCompletion(nil, &user, ChatExecutorRequest{
		ModelName:     modelName,
		UserChannelID: userChannelID,
		Messages: []ChatExecutorMessage{{
			Role:    "user",
			Content: content,
		}},
		System:      system,
		MaxTokens:   64,
		Temperature: advancedChatFloatPtr(0.2),
		Context:     ctx,
	})
	if err != nil {
		return "", err
	}
	return normalizeAdvancedChatGeneratedTitle(result.Content), nil
}

func advancedChatSessionTitleSource(userID uint, sessionID string, scope string) (string, error) {
	var session AdvancedChatSession
	if err := model.DB.Where("id = ? AND user_id = ?", sessionID, userID).First(&session).Error; err != nil {
		return "", err
	}
	var messages []AdvancedChatMessage
	if err := model.DB.Where("session_id = ? AND user_id = ?", sessionID, userID).Order("sort_order ASC, created_at ASC").Find(&messages).Error; err != nil {
		return "", err
	}
	if normalizeAdvancedChatTitleGenerationScope(scope) == "recent" && len(messages) > 12 {
		messages = messages[len(messages)-12:]
	}
	limit := 12000
	if normalizeAdvancedChatTitleGenerationScope(scope) == "recent" {
		limit = 6000
	}
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		role := "Assistant"
		if message.Role == "user" {
			role = "User"
		}
		parts = append(parts, role+": "+content)
	}
	source := strings.Join(parts, "\n\n")
	runes := []rune(source)
	if len(runes) > limit {
		runes = runes[len(runes)-limit:]
	}
	return strings.TrimSpace(string(runes)), nil
}

func normalizeAdvancedChatGeneratedTitle(raw string) string {
	title := strings.TrimSpace(raw)
	title = strings.Trim(title, "\"'` \t\r\n")
	title = strings.ReplaceAll(title, "\r", " ")
	title = strings.ReplaceAll(title, "\n", " ")
	title = strings.Join(strings.Fields(title), " ")
	if len([]rune(title)) > 80 {
		title = string([]rune(title)[:80])
	}
	return strings.TrimSpace(title)
}

func advancedChatFloatPtr(value float64) *float64 {
	return &value
}

func finishPersistedAdvancedChatCompletionMessage(sessionID string, messageID string, userID uint, response advancedChatCompletionResponse) {
	if messageID == "" {
		return
	}
	toolCalls, _ := json.Marshal(response.ToolCallDetails)
	contentParts, _ := json.Marshal(normalizeAdvancedChatContentParts(response.Message.Parts, response.Message.Content))
	_ = model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&AdvancedChatMessage{}).
			Where("id = ? AND user_id = ?", messageID, userID).
			Updates(map[string]interface{}{"content": response.Message.Content, "content_parts": string(contentParts), "tool_calls": string(toolCalls)}).Error; err != nil {
			return err
		}
		if sessionID == "" {
			return nil
		}
		return tx.Model(&AdvancedChatSession{}).Where("id = ? AND user_id = ?", sessionID, userID).Update("updated_at", time.Now()).Error
	})
}

func failPersistedAdvancedChatCompletionMessage(sessionID string, messageID string, userID uint, message string) {
	if messageID == "" || strings.TrimSpace(message) == "" {
		return
	}
	_ = model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&AdvancedChatMessage{}).
			Where("id = ? AND user_id = ? AND content = ?", messageID, userID, "").
			Update("content", message).Error; err != nil {
			return err
		}
		if sessionID == "" {
			return nil
		}
		return tx.Model(&AdvancedChatSession{}).Where("id = ? AND user_id = ?", sessionID, userID).Update("updated_at", time.Now()).Error
	})
}

func mergeAdvancedChatMessageToolCall(messageID string, userID uint, detail advancedChatCompletionToolCall) error {
	if messageID == "" {
		return nil
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var message AdvancedChatMessage
		if err := tx.Where("id = ? AND user_id = ?", messageID, userID).First(&message).Error; err != nil {
			return err
		}
		details := decodeAdvancedChatToolCalls(message.ToolCalls)
		details = mergeAdvancedChatToolCallDetails(details, detail)
		encoded, err := json.Marshal(details)
		if err != nil {
			return err
		}
		return tx.Model(&message).Update("tool_calls", string(encoded)).Error
	})
}

func mergeAdvancedChatRunToolCall(runID string, userID uint, assistantMessageID string, detail advancedChatCompletionToolCall) error {
	eventState := advancedChatRunEventStateFor(userID, runID)
	eventState.mutex.Lock()
	defer eventState.mutex.Unlock()
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var run AdvancedChatRun
		if err := tx.Where("id = ? AND user_id = ?", runID, userID).First(&run).Error; err != nil {
			return err
		}
		details := decodeAdvancedChatToolCalls(run.ToolCallDetails)
		details = mergeAdvancedChatToolCallDetails(details, detail)
		encoded, err := json.Marshal(details)
		if err != nil {
			return err
		}
		if err := tx.Model(&run).Updates(map[string]interface{}{
			"tool_call_details": string(encoded),
			"tool_calls":        len(details),
		}).Error; err != nil {
			return err
		}
		return tx.Model(&AdvancedChatMessage{}).
			Where("id = ? AND user_id = ?", assistantMessageID, userID).
			Update("tool_calls", string(encoded)).Error
	})
}

func mergeAdvancedChatToolCallDetails(current []advancedChatCompletionToolCall, next advancedChatCompletionToolCall) []advancedChatCompletionToolCall {
	for index, item := range current {
		if item.ID != "" && item.ID == next.ID {
			current[index] = mergeAdvancedChatToolCallDetail(item, next)
			return current
		}
		if item.ID == "" && next.ID == "" && item.Round == next.Round && item.Name == next.Name && item.Server == next.Server && item.Tool == next.Tool {
			current[index] = mergeAdvancedChatToolCallDetail(item, next)
			return current
		}
	}
	return append(current, next)
}

func mergeAdvancedChatToolCallDetail(current advancedChatCompletionToolCall, next advancedChatCompletionToolCall) advancedChatCompletionToolCall {
	merged := next
	if strings.TrimSpace(merged.Result) == "" {
		merged.Result = current.Result
	}
	if len(merged.Arguments) == 0 {
		merged.Arguments = current.Arguments
	}
	return merged
}

func mergeAdvancedChatToolCallDetailList(current []advancedChatCompletionToolCall, next []advancedChatCompletionToolCall) []advancedChatCompletionToolCall {
	result := append([]advancedChatCompletionToolCall{}, current...)
	for _, detail := range next {
		result = mergeAdvancedChatToolCallDetails(result, detail)
	}
	return result
}

func cancelActiveAdvancedChatToolCalls(details []advancedChatCompletionToolCall, result string) ([]advancedChatCompletionToolCall, bool) {
	changed := false
	result = truncateToolResult(strings.TrimSpace(result))
	for index := range details {
		switch strings.ToLower(strings.TrimSpace(details[index].Status)) {
		case "running", "approval_required":
			details[index].Status = "error"
			if result != "" {
				details[index].Result = result
			}
			changed = true
		}
	}
	return details, changed
}

func cancelRunningAdvancedChatAgentTasks(runID string, sessionID string, userID uint) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return
	}
	type taskState struct {
		status string
	}
	var events []AdvancedChatRunEvent
	if err := model.DB.
		Where("run_id = ? AND user_id = ? AND event = ?", runID, userID, "agent_task").
		Order("seq ASC").
		Find(&events).Error; err != nil {
		return
	}
	states := map[string]taskState{}
	order := []string{}
	for _, event := range events {
		payload := map[string]interface{}{}
		if strings.TrimSpace(event.Payload) != "" {
			_ = json.Unmarshal([]byte(event.Payload), &payload)
		}
		taskID := strings.TrimSpace(stringFromMap(payload, "task_id"))
		if taskID == "" {
			continue
		}
		if _, exists := states[taskID]; !exists {
			order = append(order, taskID)
		}
		states[taskID] = taskState{status: strings.ToLower(strings.TrimSpace(stringFromMap(payload, "status")))}
	}
	for _, taskID := range order {
		state := states[taskID]
		if state.status != "" && state.status != "running" && state.status != "approval_required" {
			continue
		}
		appendAdvancedChatAgentTaskEvent(runID, sessionID, userID, gin.H{
			"task_id": taskID,
			"status":  "error",
			"error":   "cancelled by user",
		})
	}
}

func appendAdvancedChatRunEvent(runID string, sessionID string, userID uint, event string, payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	eventState := advancedChatRunEventStateFor(userID, runID)
	eventState.mutex.Lock()
	defer eventState.mutex.Unlock()
	if !eventState.sequenceInitialized {
		var maxSeq int
		if err := model.DB.Model(&AdvancedChatRunEvent{}).
			Where("run_id = ? AND user_id = ?", runID, userID).
			Select("COALESCE(MAX(seq), 0)").
			Scan(&maxSeq).Error; err != nil {
			return err
		}
		eventState.nextSequence = maxSeq + 1
		eventState.sequenceInitialized = true
	}
	row := AdvancedChatRunEvent{
		RunID:     runID,
		SessionID: sessionID,
		UserID:    userID,
		Seq:       eventState.nextSequence,
		Event:     event,
		Payload:   string(data),
	}
	if err := model.DB.Create(&row).Error; err != nil {
		return err
	}
	eventState.nextSequence++
	return nil
}

func listAdvancedChatSessionResponses(userID uint) ([]advancedChatSessionResponse, error) {
	var sessions []AdvancedChatSession
	if err := model.DB.Where("user_id = ?", userID).Order("updated_at DESC").Limit(100).Find(&sessions).Error; err != nil {
		return nil, err
	}
	result := make([]advancedChatSessionResponse, 0, len(sessions))
	for _, session := range sessions {
		item, err := advancedChatSessionResponseFromModel(session)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func advancedChatSessionResponseFor(userID uint, sessionID string) (advancedChatSessionResponse, error) {
	var session AdvancedChatSession
	if err := model.DB.Where("id = ? AND user_id = ?", strings.TrimSpace(sessionID), userID).First(&session).Error; err != nil {
		return advancedChatSessionResponse{}, err
	}
	return advancedChatSessionResponseFromModel(session)
}

func advancedChatSessionResponseFromModel(session AdvancedChatSession) (advancedChatSessionResponse, error) {
	var messages []AdvancedChatMessage
	if err := model.DB.Where("session_id = ? AND user_id = ?", session.ID, session.UserID).Order("sort_order ASC, created_at ASC").Find(&messages).Error; err != nil {
		return advancedChatSessionResponse{}, err
	}
	messageResponses := make([]advancedChatMessageResponse, 0, len(messages))
	for _, message := range messages {
		messageResponses = append(messageResponses, advancedChatMessageResponseFromModel(message))
	}
	var latestRun *advancedChatRunResponse
	var run AdvancedChatRun
	if err := model.DB.Where("session_id = ? AND user_id = ?", session.ID, session.UserID).Order("created_at DESC").Limit(1).Find(&run).Error; err != nil {
		return advancedChatSessionResponse{}, err
	}
	if run.ID != "" {
		item := advancedChatRunResponseFromModel(run)
		latestRun = &item
	}
	return advancedChatSessionResponse{
		ID:                       session.ID,
		FolderID:                 session.FolderID,
		Title:                    session.Title,
		Messages:                 messageResponses,
		RunMode:                  normalizeAdvancedChatCompletionMode(session.RunMode),
		AgentID:                  session.AgentID,
		AgentGroupID:             session.AgentGroupID,
		SkillIDs:                 decodeStringList(session.SkillIDs),
		MCPServerIDs:             decodeStringList(session.MCPServerIDs),
		KnowledgeBaseIDs:         decodeStringList(session.KnowledgeBaseIDs),
		ConnectorDeviceID:        session.ConnectorDeviceID,
		ConnectorWorkspacePath:   session.ConnectorWorkspacePath,
		CloudSandboxID:           session.CloudSandboxID,
		ConnectorAutoApprove:     session.ConnectorAutoApprove,
		ConnectorApprovalMode:    normalizeAdvancedChatConnectorApprovalMode(session.ConnectorApprovalMode),
		ConnectorCommandPrefixes: decodeStringList(session.ConnectorCommandPrefixes),
		ModelName:                session.ModelName,
		UserChannelID:            session.UserChannelID,
		MaxTokens:                session.MaxTokens,
		Temperature:              session.Temperature,
		ReasoningEffort:          normalizeAdvancedChatReasoningEffort(session.ReasoningEffort),
			AutoCompressContext:      session.AutoCompressContext,
		LatestRun:                latestRun,
		CreatedAt:                session.CreatedAt,
		UpdatedAt:                session.UpdatedAt,
	}, nil
}

func advancedChatMessageResponseFromModel(message AdvancedChatMessage) advancedChatMessageResponse {
	return advancedChatMessageResponse{
		ID:        message.ID,
		Role:      message.Role,
		Content:   message.Content,
		Parts:     decodeAdvancedChatContentPartsWithFallback(message.ContentParts, message.Content),
		ToolCalls: decodeAdvancedChatToolCalls(message.ToolCalls),
		CreatedAt: message.CreatedAt,
		UpdatedAt: message.UpdatedAt,
	}
}

func advancedChatRunResponseFromModel(run AdvancedChatRun) advancedChatRunResponse {
	return advancedChatRunResponse{
		ID:                 run.ID,
		SessionID:          run.SessionID,
		AssistantMessageID: run.AssistantMessageID,
		Mode:               run.Mode,
		Status:             run.Status,
		StatusMessage:      run.StatusMessage,
		CurrentRound:       run.CurrentRound,
		ErrorMessage:       run.ErrorMessage,
		Cost:               run.Cost,
		ToolCalls:          run.ToolCalls,
		ToolCallDetails:    decodeAdvancedChatToolCalls(run.ToolCallDetails),
		StartedAt:          run.StartedAt,
		FinishedAt:         run.FinishedAt,
		CreatedAt:          run.CreatedAt,
		UpdatedAt:          run.UpdatedAt,
	}
}

func advancedChatRunEventResponseFromModel(event AdvancedChatRunEvent) advancedChatRunEventResponse {
	payload := map[string]interface{}{}
	if strings.TrimSpace(event.Payload) != "" {
		_ = json.Unmarshal([]byte(event.Payload), &payload)
	}
	return advancedChatRunEventResponse{
		ID:        event.ID,
		RunID:     event.RunID,
		SessionID: event.SessionID,
		Seq:       event.Seq,
		Event:     event.Event,
		Payload:   payload,
		CreatedAt: event.CreatedAt,
	}
}

func decodeAdvancedChatToolCalls(raw string) []advancedChatCompletionToolCall {
	if strings.TrimSpace(raw) == "" {
		return []advancedChatCompletionToolCall{}
	}
	var calls []advancedChatCompletionToolCall
	if err := json.Unmarshal([]byte(raw), &calls); err != nil || calls == nil {
		return []advancedChatCompletionToolCall{}
	}
	return calls
}

func decodeAdvancedChatContentPartsWithFallback(raw string, fallback string) []advancedChatContentPart {
	parts := decodeAdvancedChatContentParts(raw)
	if len(parts) > 0 {
		return parts
	}
	return normalizeAdvancedChatContentParts(nil, fallback)
}

func decodeAdvancedChatContentParts(raw string) []advancedChatContentPart {
	if strings.TrimSpace(raw) == "" {
		return []advancedChatContentPart{}
	}
	var parts []advancedChatContentPart
	if err := json.Unmarshal([]byte(raw), &parts); err != nil || parts == nil {
		return []advancedChatContentPart{}
	}
	return normalizeAdvancedChatContentParts(parts, "")
}

func normalizeAdvancedChatContentParts(parts []advancedChatContentPart, fallback string) []advancedChatContentPart {
	result := make([]advancedChatContentPart, 0, len(parts))
	for _, part := range parts {
		if part.Round <= 0 {
			part.Round = len(result) + 1
		}
		if strings.TrimSpace(part.Content) == "" {
			continue
		}
		result = append(result, part)
	}
	if len(result) == 0 && strings.TrimSpace(fallback) != "" {
		result = append(result, advancedChatContentPart{Round: 1, Content: fallback})
	}
	return result
}

func appendAdvancedChatContentPart(parts []advancedChatContentPart, round int, delta string) []advancedChatContentPart {
	if strings.TrimSpace(delta) == "" {
		return parts
	}
	if round <= 0 {
		round = 1
	}
	if len(parts) > 0 && parts[len(parts)-1].Round == round {
		parts[len(parts)-1].Content += delta
		return parts
	}
	return append(parts, advancedChatContentPart{Round: round, Content: delta})
}

func decodeStringList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{}
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil || values == nil {
		return []string{}
	}
	return values
}

func normalizeAdvancedChatSessionID(raw string) string {
	id := strings.TrimSpace(raw)
	if id == "" || len(id) > 80 || strings.ContainsAny(id, `/\?#`) {
		return ""
	}
	return id
}

func advancedChatTitleFromMessages(messages []advancedChatCompletionMessage) string {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role != "user" {
			continue
		}
		title := strings.Join(strings.Fields(messages[index].Content), " ")
		if len([]rune(title)) > 28 {
			return string([]rune(title)[:28])
		}
		return title
	}
	return ""
}

func advancedChatTitleFromSessionMessages(messages []advancedChatSessionMessageInput) string {
	for index := len(messages) - 1; index >= 0; index-- {
		if strings.ToLower(strings.TrimSpace(messages[index].Role)) != "user" {
			continue
		}
		title := strings.Join(strings.Fields(messages[index].Content), " ")
		if len([]rune(title)) > 28 {
			return string([]rune(title)[:28])
		}
		return title
	}
	return ""
}

func newAdvancedChatID(prefix string) string {
	var raw [10]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return prefix + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return prefix + "-" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:]))
}

func normalizeAdvancedChatMaxTokens(value int) int {
	if value < 0 {
		return 0
	}
	if value > 200000 {
		return 200000
	}
	return value
}

func normalizeAdvancedChatTemperature(value *float64) *float64 {
	if value == nil {
		return nil
	}
	next := *value
	if next < 0 {
		next = 0
	}
	if next > 2 {
		next = 2
	}
	return &next
}

func normalizeAdvancedChatReasoningEffort(value string) string {
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
