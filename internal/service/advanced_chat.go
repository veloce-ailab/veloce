package service

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type AdvancedChatAgent struct {
	ID               uint                             `gorm:"primaryKey" json:"id"`
	UserID           uint                             `gorm:"uniqueIndex:idx_advanced_chat_agent_user_name;uniqueIndex:idx_advanced_chat_agent_user_stable_id;not null" json:"user_id"`
	User             model.User                       `gorm:"foreignKey:UserID" json:"-"`
	OrganizationID   uint                             `gorm:"index" json:"organization_id,omitempty"`
	WorkspaceID      uint                             `gorm:"index" json:"workspace_id,omitempty"`
	OwnerUserID      uint                             `gorm:"index" json:"owner_user_id,omitempty"`
	Visibility       string                           `gorm:"size:20;not null;default:'personal';index" json:"visibility"`
	StableID         *string                          `gorm:"uniqueIndex:idx_advanced_chat_agent_user_stable_id;size:80" json:"-"`
	Name             string                           `gorm:"uniqueIndex:idx_advanced_chat_agent_user_name;size:100;not null" json:"name"`
	Prompt           string                           `gorm:"type:text;not null" json:"prompt"`
	DefaultModel     string                           `gorm:"size:100;not null" json:"default_model"`
	UserChannelID    uint                             `gorm:"index" json:"user_channel_id"`
	Stream           bool                             `gorm:"not null;default:false" json:"stream"`
	SkillIDs         string                           `gorm:"type:text;not null;default:'[]'" json:"-"`
	Skills           []string                         `gorm:"-" json:"skill_ids"`
	MCPServerIDs     string                           `gorm:"type:text;not null;default:'[]'" json:"-"`
	MCPServers       []string                         `gorm:"-" json:"mcp_server_ids"`
	KnowledgeBaseIDs string                           `gorm:"type:text;not null;default:'[]'" json:"-"`
	KnowledgeBases   []string                         `gorm:"-" json:"knowledge_base_ids"`
	PresetMessages   string                           `gorm:"type:text;not null;default:'[]'" json:"-"`
	Presets          []AdvancedChatAgentPresetMessage `gorm:"-" json:"preset_messages"`
	CreatedAt        time.Time                        `json:"created_at"`
	UpdatedAt        time.Time                        `json:"updated_at"`
}

const (
	advancedChatDefaultAgentID   = "default"
	advancedChatDefaultAgentName = "Default"
)

type advancedChatAgentResponse struct {
	ID               string                           `json:"id"`
	Name             string                           `json:"name"`
	OrganizationID   uint                             `json:"organization_id,omitempty"`
	WorkspaceID      uint                             `json:"workspace_id,omitempty"`
	OwnerUserID      uint                             `json:"owner_user_id,omitempty"`
	Visibility       string                           `json:"visibility"`
	Prompt           string                           `json:"prompt"`
	DefaultModel     string                           `json:"default_model"`
	UserChannelID    uint                             `json:"user_channel_id,omitempty"`
	Stream           bool                             `json:"stream"`
	SkillIDs         []string                         `json:"skill_ids"`
	MCPServerIDs     []string                         `json:"mcp_server_ids"`
	KnowledgeBaseIDs []string                         `json:"knowledge_base_ids"`
	PresetMessages   []AdvancedChatAgentPresetMessage `json:"preset_messages"`
	CreatedAt        time.Time                        `json:"created_at"`
	UpdatedAt        time.Time                        `json:"updated_at"`
}

type AdvancedChatAgentPresetMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type AdvancedChatAgentStudio struct {
	ID             uint       `gorm:"primaryKey" json:"-"`
	UserID         uint       `gorm:"uniqueIndex:idx_advanced_chat_agent_studio_user_studio;not null" json:"user_id"`
	User           model.User `gorm:"foreignKey:UserID" json:"-"`
	OrganizationID uint       `gorm:"index" json:"organization_id,omitempty"`
	WorkspaceID    uint       `gorm:"index" json:"workspace_id,omitempty"`
	OwnerUserID    uint       `gorm:"index" json:"owner_user_id,omitempty"`
	Visibility     string     `gorm:"size:20;not null;default:'personal';index" json:"visibility"`
	StudioID       string     `gorm:"uniqueIndex:idx_advanced_chat_agent_studio_user_studio;size:80;not null" json:"id"`
	Name           string     `gorm:"size:120;not null" json:"name"`
	Description    string     `gorm:"type:text;not null" json:"description"`
	Agents         string     `gorm:"type:text;not null" json:"-"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type AdvancedChatUserSettings struct {
	ID                       uint       `gorm:"primaryKey" json:"id"`
	UserID                   uint       `gorm:"uniqueIndex;not null" json:"user_id"`
	User                     model.User `gorm:"foreignKey:UserID" json:"-"`
	CustomMCPServers         string     `gorm:"type:text;not null" json:"custom_mcp_servers"`
	TitleModelName           string     `gorm:"size:100;not null;default:''" json:"title_model_name"`
	TitleUserChannelID       uint       `gorm:"index" json:"title_user_channel_id"`
	TitleGenerationScope     string     `gorm:"size:20;not null;default:'recent'" json:"title_generation_scope"`
	ConnectorApprovalAgentID string     `gorm:"size:80;not null;default:''" json:"connector_approval_agent_id"`
	CreatedAt                time.Time  `json:"created_at"`
	UpdatedAt                time.Time  `json:"updated_at"`
}

type AdvancedChatSkill struct {
	ID             uint       `gorm:"primaryKey" json:"id"`
	UserID         uint       `gorm:"uniqueIndex:idx_advanced_chat_skill_user_name;not null" json:"user_id"`
	User           model.User `gorm:"foreignKey:UserID" json:"-"`
	OrganizationID uint       `gorm:"index" json:"organization_id,omitempty"`
	WorkspaceID    uint       `gorm:"index" json:"workspace_id,omitempty"`
	OwnerUserID    uint       `gorm:"index" json:"owner_user_id,omitempty"`
	Visibility     string     `gorm:"size:20;not null;default:'personal';index" json:"visibility"`
	Name           string     `gorm:"uniqueIndex:idx_advanced_chat_skill_user_name;size:100;not null" json:"name"`
	Description    string     `gorm:"type:text;not null" json:"description"`
	Prompt         string     `gorm:"type:text;not null" json:"prompt"`
	MCPServerIDs   string     `gorm:"type:text;not null" json:"-"`
	MCPServers     []string   `gorm:"-" json:"mcp_server_ids"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type AdvancedChatMCPServer struct {
	ID             string            `json:"id"`
	OrganizationID uint              `json:"organization_id,omitempty"`
	WorkspaceID    uint              `json:"workspace_id,omitempty"`
	OwnerUserID    uint              `json:"owner_user_id,omitempty"`
	Visibility     string            `json:"visibility,omitempty"`
	Name           string            `json:"name"`
	Type           string            `json:"type,omitempty"`
	URL            string            `json:"url,omitempty"`
	Headers        string            `json:"headers,omitempty"`
	Command        string            `json:"command,omitempty"`
	Args           []string          `json:"args,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Cwd            string            `json:"cwd,omitempty"`
	Enabled        bool              `json:"enabled"`
	RequestMode    string            `json:"request_mode"`
}

type advancedChatAPI struct{}

type advancedChatAgentInput struct {
	Name             string                           `json:"name"`
	Prompt           string                           `json:"prompt"`
	DefaultModel     string                           `json:"default_model"`
	UserChannelID    uint                             `json:"user_channel_id"`
	Stream           bool                             `json:"stream"`
	SkillIDs         []string                         `json:"skill_ids"`
	MCPServerIDs     []string                         `json:"mcp_server_ids"`
	KnowledgeBaseIDs []string                         `json:"knowledge_base_ids"`
	PresetMessages   []AdvancedChatAgentPresetMessage `json:"preset_messages"`
	Visibility       string                           `json:"visibility"`
}

type advancedChatAdminSettingsResponse struct {
	AttachmentMaxMB                      int                     `json:"attachment_max_mb"`
	AttachmentAllowedTypes               []string                `json:"attachment_allowed_types"`
	FileStorageEnabled                   bool                    `json:"file_storage_enabled"`
	FileStorageTotalMB                   int                     `json:"file_storage_total_mb"`
	FileStorageAutoSaveImagesEnabled     bool                    `json:"file_storage_auto_save_images_enabled"`
	FileStorageAutoSaveVideosEnabled     bool                    `json:"file_storage_auto_save_videos_enabled"`
	BuiltinMCPServers                    []AdvancedChatMCPServer `json:"builtin_mcp_servers"`
	AssistantModeEnabled                 bool                    `json:"assistant_mode_enabled"`
	AssistantRunTimeoutSeconds           int                     `json:"assistant_run_timeout_seconds"`
	AgentGroupRunTimeoutSeconds          int                     `json:"agent_group_run_timeout_seconds"`
	AssistantMCPToolsEnabled             bool                    `json:"assistant_mcp_tools_enabled"`
	AssistantConnectorListFilesEnabled   bool                    `json:"assistant_connector_list_files_enabled"`
	AssistantConnectorReadFileEnabled    bool                    `json:"assistant_connector_read_file_enabled"`
	AssistantConnectorWriteFileEnabled   bool                    `json:"assistant_connector_write_file_enabled"`
	AssistantConnectorReplaceTextEnabled bool                    `json:"assistant_connector_replace_text_enabled"`
	AssistantConnectorRunCommandEnabled  bool                    `json:"assistant_connector_run_command_enabled"`
	AssistantConnectorWebSearchEnabled   bool                    `json:"assistant_connector_web_search_enabled"`
	AssistantConnectorStaticSiteEnabled  bool                    `json:"assistant_connector_static_site_enabled"`
	ScheduledTasksEnabled                bool                    `json:"scheduled_tasks_enabled"`
	MessageChannelEnabled                bool                    `json:"message_channel_enabled"`
	MessageDeliveryEnabled               bool                    `json:"message_delivery_enabled"`
	DeliverySystemSMTPEnabled            bool                    `json:"delivery_system_smtp_enabled"`
}

type advancedChatUserSettingsResponse struct {
	AttachmentMaxMB                      int                     `json:"attachment_max_mb"`
	AttachmentAllowedTypes               []string                `json:"attachment_allowed_types"`
	FileStorageEnabled                   bool                    `json:"file_storage_enabled"`
	FileStorageTotalMB                   int                     `json:"file_storage_total_mb"`
	FileStorageUsedBytes                 int64                   `json:"file_storage_used_bytes"`
	FileStorageAutoSaveImagesEnabled     bool                    `json:"file_storage_auto_save_images_enabled"`
	FileStorageAutoSaveVideosEnabled     bool                    `json:"file_storage_auto_save_videos_enabled"`
	MCPServers                           []AdvancedChatMCPServer `json:"mcp_servers"`
	BuiltinMCPServers                    []AdvancedChatMCPServer `json:"builtin_mcp_servers"`
	CustomMCPServers                     []AdvancedChatMCPServer `json:"custom_mcp_servers"`
	AssistantModeEnabled                 bool                    `json:"assistant_mode_enabled"`
	AssistantMCPToolsEnabled             bool                    `json:"assistant_mcp_tools_enabled"`
	AssistantConnectorListFilesEnabled   bool                    `json:"assistant_connector_list_files_enabled"`
	AssistantConnectorReadFileEnabled    bool                    `json:"assistant_connector_read_file_enabled"`
	AssistantConnectorWriteFileEnabled   bool                    `json:"assistant_connector_write_file_enabled"`
	AssistantConnectorReplaceTextEnabled bool                    `json:"assistant_connector_replace_text_enabled"`
	AssistantConnectorRunCommandEnabled  bool                    `json:"assistant_connector_run_command_enabled"`
	AssistantConnectorWebSearchEnabled   bool                    `json:"assistant_connector_web_search_enabled"`
	AssistantConnectorStaticSiteEnabled  bool                    `json:"assistant_connector_static_site_enabled"`
	ScheduledTasksEnabled                bool                    `json:"scheduled_tasks_enabled"`
	MessageDeliveryEnabled               bool                    `json:"message_delivery_enabled"`
	DeliverySystemSMTPEnabled            bool                    `json:"delivery_system_smtp_enabled"`
	TitleModelName                       string                  `json:"title_model_name"`
	TitleUserChannelID                   uint                    `json:"title_user_channel_id,omitempty"`
	TitleGenerationScope                 string                  `json:"title_generation_scope"`
	ConnectorApprovalAgentID             string                  `json:"connector_approval_agent_id"`
}

type advancedChatAdminSettingsInput struct {
	AttachmentMaxMB                      *int                    `json:"attachment_max_mb"`
	AttachmentAllowedTypes               []string                `json:"attachment_allowed_types"`
	FileStorageEnabled                   *bool                   `json:"file_storage_enabled"`
	FileStorageTotalMB                   *int                    `json:"file_storage_total_mb"`
	FileStorageAutoSaveImagesEnabled     *bool                   `json:"file_storage_auto_save_images_enabled"`
	FileStorageAutoSaveVideosEnabled     *bool                   `json:"file_storage_auto_save_videos_enabled"`
	BuiltinMCPServers                    []AdvancedChatMCPServer `json:"builtin_mcp_servers"`
	AssistantModeEnabled                 *bool                   `json:"assistant_mode_enabled"`
	AssistantRunTimeoutSeconds           *int                    `json:"assistant_run_timeout_seconds"`
	AgentGroupRunTimeoutSeconds          *int                    `json:"agent_group_run_timeout_seconds"`
	AssistantMCPToolsEnabled             *bool                   `json:"assistant_mcp_tools_enabled"`
	AssistantConnectorListFilesEnabled   *bool                   `json:"assistant_connector_list_files_enabled"`
	AssistantConnectorReadFileEnabled    *bool                   `json:"assistant_connector_read_file_enabled"`
	AssistantConnectorWriteFileEnabled   *bool                   `json:"assistant_connector_write_file_enabled"`
	AssistantConnectorReplaceTextEnabled *bool                   `json:"assistant_connector_replace_text_enabled"`
	AssistantConnectorRunCommandEnabled  *bool                   `json:"assistant_connector_run_command_enabled"`
	AssistantConnectorWebSearchEnabled   *bool                   `json:"assistant_connector_web_search_enabled"`
	AssistantConnectorStaticSiteEnabled  *bool                   `json:"assistant_connector_static_site_enabled"`
	ScheduledTasksEnabled                *bool                   `json:"scheduled_tasks_enabled"`
	MessageChannelEnabled                *bool                   `json:"message_channel_enabled"`
	MessageDeliveryEnabled               *bool                   `json:"message_delivery_enabled"`
	DeliverySystemSMTPEnabled            *bool                   `json:"delivery_system_smtp_enabled"`
}

type advancedChatUserMCPInput struct {
	CustomMCPServers []AdvancedChatMCPServer `json:"custom_mcp_servers"`
}

type advancedChatUserSettingsInput struct {
	TitleModelName           string `json:"title_model_name"`
	TitleUserChannelID       uint   `json:"title_user_channel_id"`
	TitleGenerationScope     string `json:"title_generation_scope"`
	ConnectorApprovalAgentID string `json:"connector_approval_agent_id"`
}

const (
	advancedChatAttachmentMaxMBKey                      = "advanced_chat_attachment_max_mb"
	advancedChatAttachmentAllowedTypesKey               = "advanced_chat_attachment_allowed_types"
	advancedChatFileStorageEnabledKey                   = "advanced_chat_file_storage_enabled"
	advancedChatFileStorageTotalMBKey                   = "advanced_chat_file_storage_total_mb"
	advancedChatFileStorageAutoSaveImagesEnabledKey     = "advanced_chat_file_storage_auto_save_images_enabled"
	advancedChatFileStorageAutoSaveVideosEnabledKey     = "advanced_chat_file_storage_auto_save_videos_enabled"
	advancedChatBuiltinMCPServersKey                    = "advanced_chat_builtin_mcp_servers"
	advancedChatAssistantModeEnabledKey                 = "advanced_chat_assistant_mode_enabled"
	advancedChatAssistantRunTimeoutSecondsKey           = "advanced_chat_assistant_run_timeout_seconds"
	advancedChatAgentGroupRunTimeoutSecondsKey          = "advanced_chat_agent_group_run_timeout_seconds"
	advancedChatAssistantMCPToolsEnabledKey             = "advanced_chat_assistant_mcp_tools_enabled"
	advancedChatAssistantConnectorListFilesEnabledKey   = "advanced_chat_assistant_connector_list_files_enabled"
	advancedChatAssistantConnectorReadFileEnabledKey    = "advanced_chat_assistant_connector_read_file_enabled"
	advancedChatAssistantConnectorWriteFileEnabledKey   = "advanced_chat_assistant_connector_write_file_enabled"
	advancedChatAssistantConnectorReplaceTextEnabledKey = "advanced_chat_assistant_connector_replace_text_enabled"
	advancedChatAssistantConnectorRunCommandEnabledKey  = "advanced_chat_assistant_connector_run_command_enabled"
	advancedChatAssistantConnectorWebSearchEnabledKey   = "advanced_chat_assistant_connector_web_search_enabled"
	advancedChatAssistantConnectorStaticSiteEnabledKey  = "advanced_chat_assistant_connector_static_site_enabled"
	advancedChatScheduledTasksEnabledKey                = "advanced_chat_scheduled_tasks_enabled"
	advancedChatMessageChannelEnabledKey                = "message_channel_enabled"
	advancedChatMessageDeliveryEnabledKey               = "advanced_chat_message_delivery_enabled"
	advancedChatDeliverySystemSMTPEnabledKey            = "advanced_chat_delivery_system_smtp_enabled"
	advancedChatDefaultAttachmentMaxMB                  = 10
	advancedChatDefaultFileStorageTotalMB               = 100
	advancedChatDefaultAttachmentTypes                  = "text/plain,text/markdown,application/json,text/csv,image/png,image/jpeg,application/pdf"
	advancedChatDefaultAssistantRunTimeoutSeconds       = 1800
	advancedChatDefaultAgentGroupRunTimeoutSeconds      = 3600
	advancedChatMinAssistantRunTimeoutSeconds           = 300
	advancedChatMaxAssistantRunTimeoutSeconds           = 86400
	advancedChatMCPModeBackend                          = "backend"
	advancedChatMCPModeFrontend                         = "frontend"
	advancedChatMCPTypeHTTP                             = "http"
	advancedChatMCPTypeConnector                        = "connector"
)

func initAdvancedChatFeatures() error {
	err := model.DB.AutoMigrate(
		&AdvancedChatAgent{},
		&AdvancedChatAgentStudio{},
		&AdvancedChatUserSettings{},
		&AdvancedChatSkill{},
		&AdvancedChatSkillPackage{},
		&AdvancedChatPackagedSkill{},
		&AdvancedChatSessionFolder{},
		&AdvancedChatSession{},
		&AdvancedChatMessage{},
		&AdvancedChatRun{},
		&AdvancedChatRunEvent{},
		&AdvancedChatFile{},
		&AdvancedChatKnowledgeBase{},
		&AdvancedChatKnowledgeDocument{},
		&AdvancedChatKnowledgeChunk{},
		&AdvancedChatConnectorDevice{},
		&AdvancedChatConnectorTask{},
		&AdvancedChatCloudSandboxHost{},
		&AdvancedChatCloudSandbox{},
		&AdvancedChatCloudSandboxCharge{},
		&AdvancedChatStaticSite{},
		&AdvancedChatDelivery{},
		&AdvancedChatScheduledTask{},
	)
	if err == nil {
		ensureAdvancedChatKnowledgePostgresVectorColumn()
		startAdvancedChatScheduledTaskScheduler()
		startAdvancedChatKnowledgeEmbeddingWorker()
	}
	return err
}

func registerAdvancedChatAdminRoutes(group *gin.RouterGroup) {
	api := &advancedChatAPI{}
	group.GET("/advanced-chat/settings", api.getAdminSettings)
	group.PUT("/advanced-chat/settings", api.updateAdminSettings)
}

func registerAdvancedChatUserRoutes(group *gin.RouterGroup) {
	api := &advancedChatAPI{}
	group.GET("/advanced-chat/settings", api.getUserSettings)
	group.PUT("/advanced-chat/settings", api.updateUserSettings)
	group.POST("/advanced-chat/completions", api.completeChat)
	group.GET("/advanced-chat/sessions/folders", api.listSessionFolders)
	group.POST("/advanced-chat/sessions/folders", api.createSessionFolder)
	group.GET("/advanced-chat/sessions", api.listSessions)
	group.POST("/advanced-chat/sessions", api.saveSession)
	group.GET("/advanced-chat/sessions/:id", api.getSession)
	group.PUT("/advanced-chat/sessions/:id", api.saveSession)
	group.PUT("/advanced-chat/sessions/:id/folder", api.moveSessionToFolder)
	group.POST("/advanced-chat/sessions/:id/title/regenerate", api.regenerateSessionTitle)
	group.DELETE("/advanced-chat/sessions/:id", api.deleteSession)
	group.GET("/advanced-chat/runs/:id", api.getRun)
	group.GET("/advanced-chat/runs/:id/events", api.listRunEvents)
	group.GET("/advanced-chat/runs/:id/agent-work", api.getRunAgentWork)
	group.POST("/advanced-chat/runs/:id/stop", api.stopRun)
	group.GET("/advanced-chat/agent-tasks", api.listAgentTasks)
	group.GET("/advanced-chat/files", api.listFiles)
	group.POST("/advanced-chat/files", api.uploadFile)
	group.GET("/advanced-chat/files/:id/content", api.getFileContent)
	group.GET("/advanced-chat/files/:id/download", api.downloadFile)
	group.DELETE("/advanced-chat/files/:id", api.deleteFile)
	group.GET("/advanced-chat/knowledge-bases", api.listKnowledgeBases)
	group.POST("/advanced-chat/knowledge-bases", api.createKnowledgeBase)
	group.PUT("/advanced-chat/knowledge-bases/:id", api.updateKnowledgeBase)
	group.DELETE("/advanced-chat/knowledge-bases/:id", api.deleteKnowledgeBase)
	group.GET("/advanced-chat/knowledge-bases/:id/documents", api.listKnowledgeDocuments)
	group.POST("/advanced-chat/knowledge-bases/:id/documents", api.uploadKnowledgeDocument)
	group.DELETE("/advanced-chat/knowledge-bases/:id/documents/:document_id", api.deleteKnowledgeDocument)
	group.POST("/advanced-chat/knowledge-bases/:id/vectorize", api.vectorizeKnowledgeBase)
	group.POST("/advanced-chat/knowledge-bases/:id/search", api.searchKnowledgeBase)
	group.GET("/advanced-chat/runs/:id/connector-tasks/pending", api.listPendingConnectorTasks)
	group.GET("/advanced-chat/connector-tasks/:id", api.getConnectorTask)
	group.POST("/advanced-chat/connector-tasks/:id/decision", api.decideConnectorTask)
	group.GET("/advanced-chat/workspace/directories", api.getWorkspaceDirectories)
	group.GET("/advanced-chat/workspace/git/status", api.getWorkspaceGitStatus)
	group.POST("/advanced-chat/workspace/git/action", api.runWorkspaceGitAction)
	group.GET("/advanced-chat/devices", api.listConnectorDevices)
	group.GET("/advanced-chat/devices/:id", api.getConnectorDevice)
	group.GET("/advanced-chat/devices/:id/tasks", api.listConnectorDeviceTasks)
	group.POST("/advanced-chat/devices/:id/tasks/:task_id/cancel", api.cancelConnectorDeviceTask)
	group.GET("/advanced-chat/devices/:id/mcp-processes", api.listConnectorDeviceMCPProcesses)
	group.POST("/advanced-chat/devices/:id/mcp-processes/stop", api.stopConnectorDeviceMCPProcess)
	group.POST("/advanced-chat/devices/desktop/ensure", api.ensureDesktopConnector)
	group.POST("/advanced-chat/devices/token", api.createConnectorToken)
	group.POST("/advanced-chat/devices/:id/token", api.rotateConnectorDeviceToken)
	group.PUT("/advanced-chat/devices/:id", api.updateConnectorDevice)
	group.DELETE("/advanced-chat/devices/:id", api.deleteConnectorDevice)
	group.GET("/advanced-chat/static-sites", api.listStaticSites)
	group.PUT("/advanced-chat/static-sites/:id", api.updateStaticSite)
	group.DELETE("/advanced-chat/static-sites/:id", api.deleteStaticSite)
	group.GET("/advanced-chat/agent-groups", api.listAgentGroups)
	group.GET("/advanced-chat/agent-groups/:id", api.getAgentGroup)
	group.POST("/advanced-chat/agent-groups", api.saveAgentGroup)
	group.PUT("/advanced-chat/agent-groups/:id", api.saveAgentGroup)
	group.DELETE("/advanced-chat/agent-groups/:id", api.deleteAgentGroup)
	group.POST("/advanced-chat/workspace-skills/refresh", api.refreshWorkspaceSkills)
	group.PUT("/advanced-chat/mcp-servers", api.updateUserMCPServers)
	group.GET("/advanced-chat/agents", api.listAgents)
	group.POST("/advanced-chat/agents", api.createAgent)
	group.POST("/advanced-chat/agents/generate", api.generateAgent)
	group.PUT("/advanced-chat/agents/:id", api.updateAgent)
	group.DELETE("/advanced-chat/agents/:id", api.deleteAgent)
	group.GET("/advanced-chat/skills", api.listSkills)
	group.GET("/advanced-chat/skills/:id", api.getSkill)
	group.GET("/advanced-chat/skills/:id/files", api.readSkillFile)
	group.POST("/advanced-chat/skills", api.createSkill)
	group.PUT("/advanced-chat/skills/:id", api.updateSkill)
	group.DELETE("/advanced-chat/skills/:id", api.deleteSkill)
	group.GET("/advanced-chat/skill-packages", api.listSkillPackages)
	group.GET("/advanced-chat/skill-packages/:id", api.getSkillPackage)
	group.GET("/advanced-chat/skill-packages/:id/files", api.readSkillPackageFile)
	group.POST("/advanced-chat/skill-packages", api.uploadSkillPackage)
	group.DELETE("/advanced-chat/skill-packages/:id", api.deleteSkillPackage)
	group.GET("/advanced-chat/deliveries", api.listDeliveries)
	group.POST("/advanced-chat/deliveries", api.createDelivery)
	group.PUT("/advanced-chat/deliveries/:id", api.updateDelivery)
	group.DELETE("/advanced-chat/deliveries/:id", api.deleteDelivery)
	group.GET("/advanced-chat/scheduled-tasks", api.listScheduledTasks)
	group.POST("/advanced-chat/scheduled-tasks", api.createScheduledTask)
	group.PUT("/advanced-chat/scheduled-tasks/:id", api.updateScheduledTask)
	group.DELETE("/advanced-chat/scheduled-tasks/:id", api.deleteScheduledTask)
	group.POST("/advanced-chat/scheduled-tasks/:id/run", api.runScheduledTask)
}

func (api *advancedChatAPI) getAdminSettings(c *gin.Context) {
	c.JSON(http.StatusOK, currentAdvancedChatAdminSettings())
}

func (api *advancedChatAPI) updateAdminSettings(c *gin.Context) {
	var input advancedChatAdminSettingsInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if input.AttachmentMaxMB != nil {
		if *input.AttachmentMaxMB < 1 || *input.AttachmentMaxMB > 100 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Attachment size must be between 1 and 100 MB"})
			return
		}
		if err := model.SetSystemSetting(advancedChatAttachmentMaxMBKey, strconv.Itoa(*input.AttachmentMaxMB)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update agent chat settings"})
			return
		}
	}

	if input.AttachmentAllowedTypes != nil {
		types := normalizeAttachmentTypes(input.AttachmentAllowedTypes)
		if len(types) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "At least one attachment type is required"})
			return
		}
		if err := model.SetSystemSetting(advancedChatAttachmentAllowedTypesKey, strings.Join(types, ",")); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update agent chat settings"})
			return
		}
	}

	if input.FileStorageTotalMB != nil {
		if *input.FileStorageTotalMB < 1 || *input.FileStorageTotalMB > 102400 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "File storage quota must be between 1 and 102400 MB"})
			return
		}
		if err := model.SetSystemSetting(advancedChatFileStorageTotalMBKey, strconv.Itoa(*input.FileStorageTotalMB)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update agent chat settings"})
			return
		}
	}

	if input.AssistantRunTimeoutSeconds != nil {
		if !validAdvancedChatRunTimeoutSeconds(*input.AssistantRunTimeoutSeconds) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Assistant run timeout must be between 300 and 86400 seconds"})
			return
		}
		if err := model.SetSystemSetting(advancedChatAssistantRunTimeoutSecondsKey, strconv.Itoa(*input.AssistantRunTimeoutSeconds)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update agent chat settings"})
			return
		}
	}
	if input.AgentGroupRunTimeoutSeconds != nil {
		if !validAdvancedChatRunTimeoutSeconds(*input.AgentGroupRunTimeoutSeconds) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Studio run timeout must be between 300 and 86400 seconds"})
			return
		}
		if err := model.SetSystemSetting(advancedChatAgentGroupRunTimeoutSecondsKey, strconv.Itoa(*input.AgentGroupRunTimeoutSeconds)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update agent chat settings"})
			return
		}
	}

	if !advancedChatPremiumFeaturesAvailable() && advancedChatPremiumSettingRequested(input) {
		writeCommunityAdvancedChatPremiumRequired(c)
		return
	}

	if input.BuiltinMCPServers != nil {
		servers, ok := normalizeMCPServers(c, input.BuiltinMCPServers, advancedChatMCPModeBackend, true)
		if !ok {
			return
		}
		data, err := json.Marshal(servers)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to encode MCP servers"})
			return
		}
		if err := model.SetSystemSetting(advancedChatBuiltinMCPServersKey, string(data)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update agent chat settings"})
			return
		}
	}

	boolSettings := map[string]*bool{
		advancedChatFileStorageEnabledKey:                   input.FileStorageEnabled,
		advancedChatFileStorageAutoSaveImagesEnabledKey:     input.FileStorageAutoSaveImagesEnabled,
		advancedChatFileStorageAutoSaveVideosEnabledKey:     input.FileStorageAutoSaveVideosEnabled,
		advancedChatAssistantModeEnabledKey:                 input.AssistantModeEnabled,
		advancedChatAssistantMCPToolsEnabledKey:             input.AssistantMCPToolsEnabled,
		advancedChatAssistantConnectorListFilesEnabledKey:   input.AssistantConnectorListFilesEnabled,
		advancedChatAssistantConnectorReadFileEnabledKey:    input.AssistantConnectorReadFileEnabled,
		advancedChatAssistantConnectorWriteFileEnabledKey:   input.AssistantConnectorWriteFileEnabled,
		advancedChatAssistantConnectorReplaceTextEnabledKey: input.AssistantConnectorReplaceTextEnabled,
		advancedChatAssistantConnectorRunCommandEnabledKey:  input.AssistantConnectorRunCommandEnabled,
		advancedChatAssistantConnectorWebSearchEnabledKey:   input.AssistantConnectorWebSearchEnabled,
		advancedChatAssistantConnectorStaticSiteEnabledKey:  input.AssistantConnectorStaticSiteEnabled,
		advancedChatScheduledTasksEnabledKey:                input.ScheduledTasksEnabled,
		advancedChatMessageChannelEnabledKey:                input.MessageChannelEnabled,
		advancedChatMessageDeliveryEnabledKey:               input.MessageDeliveryEnabled,
		advancedChatDeliverySystemSMTPEnabledKey:            input.DeliverySystemSMTPEnabled,
	}
	for key, value := range boolSettings {
		if value == nil {
			continue
		}
		if err := model.SetSystemSetting(key, strconv.FormatBool(*value)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update agent chat settings"})
			return
		}
	}

	c.JSON(http.StatusOK, currentAdvancedChatAdminSettings())
}

func (api *advancedChatAPI) getUserSettings(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	c.JSON(http.StatusOK, currentAdvancedChatUserSettings(user.ID))
}

func (api *advancedChatAPI) updateUserSettings(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var input advancedChatUserSettingsInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	titleModel := strings.TrimSpace(input.TitleModelName)
	if len([]rune(titleModel)) > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Title model is too long"})
		return
	}
	titleScope := normalizeAdvancedChatTitleGenerationScope(input.TitleGenerationScope)
	approvalAgentID := strings.TrimSpace(input.ConnectorApprovalAgentID)
	if approvalAgentID != "" {
		if _, err := loadAdvancedChatAgent(user.ID, approvalAgentID); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "Approval agent not found"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load approval agent"})
			return
		}
	}
	settings := ensureAdvancedChatUserSettings(user.ID)
	if err := model.DB.Model(&settings).Updates(map[string]interface{}{
		"title_model_name":            titleModel,
		"title_user_channel_id":       input.TitleUserChannelID,
		"title_generation_scope":      titleScope,
		"connector_approval_agent_id": approvalAgentID,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save settings"})
		return
	}
	c.JSON(http.StatusOK, currentAdvancedChatUserSettings(user.ID))
}

func (api *advancedChatAPI) updateUserMCPServers(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	var input advancedChatUserMCPInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	servers, ok := normalizeMCPServers(c, input.CustomMCPServers, advancedChatMCPModeBackend, true)
	if !ok {
		return
	}
	organizationID, workspaceID := advancedChatEnterpriseScope(c)
	if organizationID != 0 {
		for index := range servers {
			visibility := model.NormalizeResourceVisibility(servers[index].Visibility)
			if visibility == model.ResourceVisibilityWorkspace && workspaceID == 0 {
				c.JSON(http.StatusBadRequest, gin.H{"error": "A workspace is required for workspace visibility"})
				return
			}
			servers[index].OrganizationID = organizationID
			servers[index].WorkspaceID = workspaceID
			servers[index].OwnerUserID = user.ID
			servers[index].Visibility = visibility
		}
	}
	data, err := json.Marshal(servers)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to encode MCP servers"})
		return
	}
	settings := ensureAdvancedChatUserSettings(user.ID)
	if err := model.DB.Model(&settings).Update("custom_mcp_servers", string(data)).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save MCP servers"})
		return
	}
	c.JSON(http.StatusOK, currentAdvancedChatUserSettings(user.ID))
}

func (api *advancedChatAPI) listAgents(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	if _, err := ensureAdvancedChatDefaultAgent(user.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to ensure default agent"})
		return
	}
	var agents []AdvancedChatAgent
	if err := model.DB.Where("user_id = ?", user.ID).Order("created_at ASC").Find(&agents).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list agents"})
		return
	}
	responses := make([]advancedChatAgentResponse, 0, len(agents))
	for i := range agents {
		responses = append(responses, advancedChatAgentResponseFromModel(&agents[i]))
	}
	c.JSON(http.StatusOK, responses)
}

func (api *advancedChatAPI) createAgent(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	var input advancedChatAgentInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	agent, ok := advancedChatAgentFromInput(c, user.ID, input)
	if !ok {
		return
	}
	if err := model.DB.Create(&agent).Error; err != nil {
		if isAdvancedChatUniqueConstraintError(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "Agent name already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create agent"})
		return
	}
	hydrateAdvancedChatAgentLists(&agent)
	c.JSON(http.StatusOK, advancedChatAgentResponseFromModel(&agent))
}

func (api *advancedChatAPI) updateAgent(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	agent, err := loadAdvancedChatAgent(user.ID, c.Param("id"))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Agent not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load agent"})
		return
	}

	var input advancedChatAgentInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if isAdvancedChatDefaultAgent(agent) {
		input.Name = agent.Name
	}
	next, ok := advancedChatAgentFromInput(c, user.ID, input)
	if !ok {
		return
	}
	if err := model.DB.Model(agent).Updates(map[string]interface{}{
		"name":               next.Name,
		"prompt":             next.Prompt,
		"default_model":      next.DefaultModel,
		"user_channel_id":    next.UserChannelID,
		"stream":             next.Stream,
		"skill_ids":          next.SkillIDs,
		"mcp_server_ids":     next.MCPServerIDs,
		"knowledge_base_ids": next.KnowledgeBaseIDs,
		"preset_messages":    next.PresetMessages,
	}).Error; err != nil {
		if isAdvancedChatUniqueConstraintError(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "Agent name already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update agent"})
		return
	}
	model.DB.First(agent, agent.ID)
	hydrateAdvancedChatAgentLists(agent)
	c.JSON(http.StatusOK, advancedChatAgentResponseFromModel(agent))
}

func (api *advancedChatAPI) deleteAgent(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	agent, err := loadAdvancedChatAgent(user.ID, c.Param("id"))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Agent not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load agent"})
		return
	}
	if isAdvancedChatDefaultAgent(agent) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Default agent cannot be deleted"})
		return
	}
	if err := model.DB.Where("id = ? AND user_id = ?", agent.ID, user.ID).Delete(&AdvancedChatAgent{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete agent"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Agent deleted"})
}

func (api *advancedChatAPI) listSkills(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	var skills []AdvancedChatPackagedSkill
	if err := model.DB.Where("user_id = ? AND enabled = ?", user.ID, true).Order("created_at ASC").Find(&skills).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list skills"})
		return
	}
	result := make([]advancedChatPackagedSkillBrief, 0, len(skills))
	for _, skill := range skills {
		result = append(result, advancedChatPackagedSkillBriefFromModel(skill))
	}
	c.JSON(http.StatusOK, result)
}

func (api *advancedChatAPI) createSkill(c *gin.Context) {
	c.JSON(http.StatusGone, gin.H{"error": "Manual skills are no longer supported. Upload a skill package instead."})
}

func (api *advancedChatAPI) updateSkill(c *gin.Context) {
	c.JSON(http.StatusGone, gin.H{"error": "Manual skills are no longer supported. Upload a skill package instead."})
}

func (api *advancedChatAPI) deleteSkill(c *gin.Context) {
	c.JSON(http.StatusGone, gin.H{"error": "Manual skills are no longer supported. Delete the containing skill package instead."})
}

// normalizeAdvancedChatMCPServerIDs validates referenced MCP server ids against
// the set of servers available to the user (admin builtin + user custom) and
// returns a deduplicated, order-preserving list.
func normalizeAdvancedChatMCPServerIDs(c *gin.Context, userID uint, ids []string) ([]string, bool) {
	if len(ids) > 20 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Too many MCP servers"})
		return nil, false
	}
	available := map[string]struct{}{}
	for _, server := range advancedChatBuiltinMCPServers(false) {
		available[server.ID] = struct{}{}
	}
	for _, server := range advancedChatCustomMCPServers(userID) {
		available[server.ID] = struct{}{}
	}
	result := make([]string, 0, len(ids))
	seen := map[string]struct{}{}
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		if _, exists := available[id]; !exists {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Unknown MCP server: " + id})
			return nil, false
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result, true
}

func decodeMCPServerIDs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{}
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return []string{}
	}
	if ids == nil {
		return []string{}
	}
	return ids
}

func advancedChatAgentFromInput(c *gin.Context, userID uint, input advancedChatAgentInput) (AdvancedChatAgent, bool) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Agent name is required"})
		return AdvancedChatAgent{}, false
	}
	if len([]rune(name)) > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Agent name is too long"})
		return AdvancedChatAgent{}, false
	}
	defaultModel := strings.TrimSpace(input.DefaultModel)
	if len([]rune(defaultModel)) > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Default model is too long"})
		return AdvancedChatAgent{}, false
	}
	userChannelID := input.UserChannelID
	prompt := strings.TrimSpace(input.Prompt)
	if len([]rune(prompt)) > 20000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Agent prompt is too long"})
		return AdvancedChatAgent{}, false
	}
	skillIDs := uniqueStringsLocal(input.SkillIDs)
	if len(skillIDs) > 0 {
		skills, err := loadAdvancedChatSkills(userID, skillIDs)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load skills"})
			return AdvancedChatAgent{}, false
		}
		if len(skills) != len(skillIDs) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Unknown skill"})
			return AdvancedChatAgent{}, false
		}
	}
	mcpServerIDs, ok := normalizeAdvancedChatMCPServerIDs(c, userID, input.MCPServerIDs)
	if !ok {
		return AdvancedChatAgent{}, false
	}
	knowledgeBaseIDs, ok := normalizeAdvancedChatKnowledgeBaseIDs(c, userID, input.KnowledgeBaseIDs)
	if !ok {
		return AdvancedChatAgent{}, false
	}
	skillIDsJSON, _ := json.Marshal(skillIDs)
	mcpServerIDsJSON, _ := json.Marshal(mcpServerIDs)
	knowledgeBaseIDsJSON, _ := json.Marshal(knowledgeBaseIDs)
	presetMessages, ok := normalizeAdvancedChatAgentPresetMessages(c, input.PresetMessages)
	if !ok {
		return AdvancedChatAgent{}, false
	}
	presetMessagesJSON, _ := json.Marshal(presetMessages)
	organizationID, workspaceID := advancedChatEnterpriseScope(c)
	visibility := model.NormalizeResourceVisibility(input.Visibility)
	if organizationID == 0 {
		visibility = model.ResourceVisibilityPersonal
	}
	if visibility == model.ResourceVisibilityWorkspace && workspaceID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "A workspace is required for workspace visibility"})
		return AdvancedChatAgent{}, false
	}
	return AdvancedChatAgent{
		UserID:           userID,
		OwnerUserID:      userID,
		OrganizationID:   organizationID,
		WorkspaceID:      workspaceID,
		Visibility:       visibility,
		Name:             name,
		Prompt:           prompt,
		DefaultModel:     defaultModel,
		UserChannelID:    userChannelID,
		Stream:           input.Stream,
		SkillIDs:         string(skillIDsJSON),
		MCPServerIDs:     string(mcpServerIDsJSON),
		KnowledgeBaseIDs: string(knowledgeBaseIDsJSON),
		PresetMessages:   string(presetMessagesJSON),
	}, true
}

func hydrateAdvancedChatAgentLists(agent *AdvancedChatAgent) {
	if agent == nil {
		return
	}
	agent.Skills = decodeStringList(agent.SkillIDs)
	agent.MCPServers = decodeStringList(agent.MCPServerIDs)
	agent.KnowledgeBases = decodeStringList(agent.KnowledgeBaseIDs)
	agent.Presets = decodeAdvancedChatAgentPresetMessages(agent.PresetMessages)
}

func normalizeAdvancedChatAgentPresetMessages(c *gin.Context, input []AdvancedChatAgentPresetMessage) ([]AdvancedChatAgentPresetMessage, bool) {
	if len(input) > 24 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Too many preset messages"})
		return nil, false
	}
	result := make([]AdvancedChatAgentPresetMessage, 0, len(input))
	totalRunes := 0
	for _, item := range input {
		role := strings.ToLower(strings.TrimSpace(item.Role))
		if role != "system" && role != "user" && role != "assistant" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Preset message role must be system, user, or assistant"})
			return nil, false
		}
		content := strings.TrimSpace(item.Content)
		if content == "" {
			continue
		}
		if len([]rune(content)) > 8000 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Preset message is too long"})
			return nil, false
		}
		totalRunes += len([]rune(content))
		if totalRunes > 30000 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Preset messages are too long"})
			return nil, false
		}
		result = append(result, AdvancedChatAgentPresetMessage{Role: role, Content: content})
	}
	return result, true
}

func decodeAdvancedChatAgentPresetMessages(raw string) []AdvancedChatAgentPresetMessage {
	var values []AdvancedChatAgentPresetMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &values); err != nil {
		return []AdvancedChatAgentPresetMessage{}
	}
	result := make([]AdvancedChatAgentPresetMessage, 0, len(values))
	for _, item := range values {
		role := strings.ToLower(strings.TrimSpace(item.Role))
		content := strings.TrimSpace(item.Content)
		if content != "" && (role == "system" || role == "user" || role == "assistant") {
			result = append(result, AdvancedChatAgentPresetMessage{Role: role, Content: content})
		}
	}
	return result
}

func advancedChatAgentResponseFromModel(agent *AdvancedChatAgent) advancedChatAgentResponse {
	if agent == nil {
		return advancedChatAgentResponse{}
	}
	hydrateAdvancedChatAgentLists(agent)
	id := strconv.FormatUint(uint64(agent.ID), 10)
	if agent.StableID != nil && strings.TrimSpace(*agent.StableID) != "" {
		id = strings.TrimSpace(*agent.StableID)
	}
	return advancedChatAgentResponse{
		ID:               id,
		Name:             agent.Name,
		OrganizationID:   agent.OrganizationID,
		WorkspaceID:      agent.WorkspaceID,
		OwnerUserID:      agent.OwnerUserID,
		Visibility:       agent.Visibility,
		Prompt:           agent.Prompt,
		DefaultModel:     agent.DefaultModel,
		UserChannelID:    agent.UserChannelID,
		Stream:           agent.Stream,
		SkillIDs:         agent.Skills,
		MCPServerIDs:     agent.MCPServers,
		KnowledgeBaseIDs: agent.KnowledgeBases,
		PresetMessages:   agent.Presets,
		CreatedAt:        agent.CreatedAt,
		UpdatedAt:        agent.UpdatedAt,
	}
}

func advancedChatEnterpriseScope(c *gin.Context) (uint, uint) {
	if !serviceEnterpriseFeaturesEnabled() || c == nil {
		return 0, 0
	}
	organizationID, _ := c.Get("enterprise_organization_id")
	workspaceID, _ := c.Get("enterprise_workspace_id")
	organization, _ := organizationID.(uint)
	workspace, _ := workspaceID.(uint)
	return organization, workspace
}

func serviceEnterpriseFeaturesEnabled() bool { return model.EnterpriseModeEnabledWithDB(model.DB) }

func isAdvancedChatDefaultAgent(agent *AdvancedChatAgent) bool {
	return agent != nil && agent.StableID != nil && strings.TrimSpace(*agent.StableID) == advancedChatDefaultAgentID
}

func ensureAdvancedChatDefaultAgent(userID uint) (*AdvancedChatAgent, error) {
	stableID := advancedChatDefaultAgentID
	var agent AdvancedChatAgent
	err := model.DB.Where("user_id = ? AND stable_id = ?", userID, stableID).First(&agent).Error
	if err == nil {
		hydrateAdvancedChatAgentLists(&agent)
		return &agent, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	if err := model.DB.Where("user_id = ? AND name = ?", userID, advancedChatDefaultAgentName).First(&agent).Error; err == nil {
		if err := model.DB.Model(&agent).Update("stable_id", stableID).Error; err != nil {
			return nil, err
		}
		agent.StableID = &stableID
		hydrateAdvancedChatAgentLists(&agent)
		return &agent, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	skillIDsJSON, _ := json.Marshal([]string{})
	agent = AdvancedChatAgent{
		UserID:           userID,
		StableID:         &stableID,
		Name:             advancedChatDefaultAgentName,
		Prompt:           "",
		DefaultModel:     "",
		UserChannelID:    0,
		Stream:           false,
		SkillIDs:         string(skillIDsJSON),
		MCPServerIDs:     string(skillIDsJSON),
		KnowledgeBaseIDs: string(skillIDsJSON),
		PresetMessages:   string(skillIDsJSON),
	}
	if err := model.DB.Create(&agent).Error; err != nil {
		return nil, err
	}
	hydrateAdvancedChatAgentLists(&agent)
	return &agent, nil
}

func isAdvancedChatUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint") || strings.Contains(message, "duplicate")
}

func currentAdvancedChatAdminSettings() advancedChatAdminSettingsResponse {
	settings := advancedChatAdminSettingsResponse{
		AttachmentMaxMB:                      advancedChatAttachmentMaxMB(),
		AttachmentAllowedTypes:               advancedChatAttachmentAllowedTypes(),
		FileStorageEnabled:                   advancedChatFileStorageEnabled(),
		FileStorageTotalMB:                   advancedChatFileStorageTotalMB(),
		FileStorageAutoSaveImagesEnabled:     advancedChatFileStorageAutoSaveImagesEnabled(),
		FileStorageAutoSaveVideosEnabled:     advancedChatFileStorageAutoSaveVideosEnabled(),
		BuiltinMCPServers:                    advancedChatBuiltinMCPServers(true),
		AssistantModeEnabled:                 advancedChatAssistantModeEnabled(),
		AssistantRunTimeoutSeconds:           advancedChatAssistantRunTimeoutSeconds(),
		AgentGroupRunTimeoutSeconds:          advancedChatAgentGroupRunTimeoutSeconds(),
		AssistantMCPToolsEnabled:             advancedChatAssistantMCPToolsEnabled(),
		AssistantConnectorListFilesEnabled:   advancedChatAssistantConnectorListFilesEnabled(),
		AssistantConnectorReadFileEnabled:    advancedChatAssistantConnectorReadFileEnabled(),
		AssistantConnectorWriteFileEnabled:   advancedChatAssistantConnectorWriteFileEnabled(),
		AssistantConnectorReplaceTextEnabled: advancedChatAssistantConnectorReplaceTextEnabled(),
		AssistantConnectorRunCommandEnabled:  advancedChatAssistantConnectorRunCommandEnabled(),
		AssistantConnectorWebSearchEnabled:   advancedChatAssistantConnectorWebSearchEnabled(),
		AssistantConnectorStaticSiteEnabled:  advancedChatAssistantConnectorStaticSiteEnabled(),
		ScheduledTasksEnabled:                advancedChatScheduledTasksEnabled(),
		MessageChannelEnabled:                advancedChatMessageChannelEnabled(),
		MessageDeliveryEnabled:               advancedChatMessageDeliveryEnabled(),
		DeliverySystemSMTPEnabled:            advancedChatDeliverySystemSMTPEnabled(),
	}
	if !advancedChatPremiumFeaturesAvailable() {
		settings.FileStorageEnabled = false
		settings.FileStorageAutoSaveImagesEnabled = false
		settings.FileStorageAutoSaveVideosEnabled = false
		settings.ScheduledTasksEnabled = false
		settings.MessageChannelEnabled = false
		settings.MessageDeliveryEnabled = false
		settings.DeliverySystemSMTPEnabled = false
	}
	return settings
}

func currentAdvancedChatUserSettings(userID uint) advancedChatUserSettingsResponse {
	builtinServers := advancedChatBuiltinMCPServers(false)
	customServers := advancedChatCustomMCPServers(userID)
	customServersWithHeaders := advancedChatCustomMCPServersWithHeaders(userID)
	userSettings := ensureAdvancedChatUserSettings(userID)
	settings := advancedChatUserSettingsResponse{
		AttachmentMaxMB:                      advancedChatAttachmentMaxMB(),
		AttachmentAllowedTypes:               advancedChatAttachmentAllowedTypes(),
		FileStorageEnabled:                   advancedChatFileStorageEnabled(),
		FileStorageTotalMB:                   advancedChatFileStorageTotalMB(),
		FileStorageUsedBytes:                 advancedChatFileStorageUsedBytes(userID),
		FileStorageAutoSaveImagesEnabled:     advancedChatFileStorageAutoSaveImagesEnabled(),
		FileStorageAutoSaveVideosEnabled:     advancedChatFileStorageAutoSaveVideosEnabled(),
		MCPServers:                           mergeAdvancedChatMCPServers(builtinServers, customServers),
		BuiltinMCPServers:                    builtinServers,
		CustomMCPServers:                     customServersWithHeaders,
		AssistantModeEnabled:                 advancedChatAssistantModeEnabled(),
		AssistantMCPToolsEnabled:             advancedChatAssistantMCPToolsEnabled(),
		AssistantConnectorListFilesEnabled:   advancedChatAssistantConnectorListFilesEnabled(),
		AssistantConnectorReadFileEnabled:    advancedChatAssistantConnectorReadFileEnabled(),
		AssistantConnectorWriteFileEnabled:   advancedChatAssistantConnectorWriteFileEnabled(),
		AssistantConnectorReplaceTextEnabled: advancedChatAssistantConnectorReplaceTextEnabled(),
		AssistantConnectorRunCommandEnabled:  advancedChatAssistantConnectorRunCommandEnabled(),
		AssistantConnectorWebSearchEnabled:   advancedChatAssistantConnectorWebSearchEnabled(),
		AssistantConnectorStaticSiteEnabled:  advancedChatAssistantConnectorStaticSiteEnabled(),
		ScheduledTasksEnabled:                advancedChatScheduledTasksEnabled(),
		MessageDeliveryEnabled:               advancedChatMessageDeliveryEnabled(),
		DeliverySystemSMTPEnabled:            advancedChatDeliverySystemSMTPEnabled(),
		TitleModelName:                       strings.TrimSpace(userSettings.TitleModelName),
		TitleUserChannelID:                   userSettings.TitleUserChannelID,
		TitleGenerationScope:                 normalizeAdvancedChatTitleGenerationScope(userSettings.TitleGenerationScope),
		ConnectorApprovalAgentID:             strings.TrimSpace(userSettings.ConnectorApprovalAgentID),
	}
	if !advancedChatPremiumFeaturesAvailable() {
		settings.FileStorageEnabled = false
		settings.FileStorageUsedBytes = 0
		settings.FileStorageAutoSaveImagesEnabled = false
		settings.FileStorageAutoSaveVideosEnabled = false
		settings.ScheduledTasksEnabled = false
		settings.MessageDeliveryEnabled = false
		settings.DeliverySystemSMTPEnabled = false
	}
	return settings
}

func ensureAdvancedChatUserSettings(userID uint) AdvancedChatUserSettings {
	var settings AdvancedChatUserSettings
	err := model.DB.Where("user_id = ?", userID).First(&settings).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		settings = AdvancedChatUserSettings{
			UserID:               userID,
			CustomMCPServers:     "[]",
			TitleGenerationScope: "recent",
		}
		if createErr := model.DB.Create(&settings).Error; createErr != nil {
			_ = model.DB.Where("user_id = ?", userID).First(&settings).Error
		}
	} else if err != nil {
		settings = AdvancedChatUserSettings{
			UserID:               userID,
			CustomMCPServers:     "[]",
			TitleGenerationScope: "recent",
		}
	}
	if strings.TrimSpace(settings.CustomMCPServers) == "" {
		settings.CustomMCPServers = "[]"
		_ = model.DB.Model(&settings).Update("custom_mcp_servers", settings.CustomMCPServers).Error
	}
	settings.TitleGenerationScope = normalizeAdvancedChatTitleGenerationScope(settings.TitleGenerationScope)
	return settings
}

func normalizeAdvancedChatTitleGenerationScope(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "all":
		return "all"
	default:
		return "recent"
	}
}

func mergeAdvancedChatMCPServers(groups ...[]AdvancedChatMCPServer) []AdvancedChatMCPServer {
	servers := []AdvancedChatMCPServer{}
	seen := map[string]struct{}{}
	for _, group := range groups {
		for _, server := range group {
			if _, exists := seen[server.ID]; exists {
				continue
			}
			seen[server.ID] = struct{}{}
			servers = append(servers, server)
		}
	}
	return servers
}

func advancedChatAttachmentMaxMB() int {
	value, err := strconv.Atoi(strings.TrimSpace(model.GetSystemSetting(advancedChatAttachmentMaxMBKey, strconv.Itoa(advancedChatDefaultAttachmentMaxMB))))
	if err != nil || value < 1 {
		return advancedChatDefaultAttachmentMaxMB
	}
	if value > 100 {
		return 100
	}
	return value
}

func advancedChatAttachmentAllowedTypes() []string {
	raw := model.GetSystemSetting(advancedChatAttachmentAllowedTypesKey, advancedChatDefaultAttachmentTypes)
	return normalizeAttachmentTypes(strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r'
	}))
}

func advancedChatAssistantModeEnabled() bool {
	return advancedChatSettingBool(advancedChatAssistantModeEnabledKey, true)
}

func advancedChatAssistantRunTimeoutSeconds() int {
	return advancedChatSettingInt(
		advancedChatAssistantRunTimeoutSecondsKey,
		advancedChatDefaultAssistantRunTimeoutSeconds,
		advancedChatMinAssistantRunTimeoutSeconds,
		advancedChatMaxAssistantRunTimeoutSeconds,
	)
}

func advancedChatAgentGroupRunTimeoutSeconds() int {
	return advancedChatSettingInt(
		advancedChatAgentGroupRunTimeoutSecondsKey,
		advancedChatDefaultAgentGroupRunTimeoutSeconds,
		advancedChatMinAssistantRunTimeoutSeconds,
		advancedChatMaxAssistantRunTimeoutSeconds,
	)
}

func validAdvancedChatRunTimeoutSeconds(value int) bool {
	return value >= advancedChatMinAssistantRunTimeoutSeconds && value <= advancedChatMaxAssistantRunTimeoutSeconds
}

func advancedChatAssistantMCPToolsEnabled() bool {
	return advancedChatSettingBool(advancedChatAssistantMCPToolsEnabledKey, true)
}

func advancedChatAssistantConnectorListFilesEnabled() bool {
	return advancedChatSettingBool(advancedChatAssistantConnectorListFilesEnabledKey, true)
}

func advancedChatAssistantConnectorReadFileEnabled() bool {
	return advancedChatSettingBool(advancedChatAssistantConnectorReadFileEnabledKey, true)
}

func advancedChatAssistantConnectorWriteFileEnabled() bool {
	return advancedChatSettingBool(advancedChatAssistantConnectorWriteFileEnabledKey, true)
}

func advancedChatAssistantConnectorReplaceTextEnabled() bool {
	return advancedChatSettingBool(advancedChatAssistantConnectorReplaceTextEnabledKey, true)
}

func advancedChatAssistantConnectorRunCommandEnabled() bool {
	return advancedChatSettingBool(advancedChatAssistantConnectorRunCommandEnabledKey, true)
}

func advancedChatAssistantConnectorWebSearchEnabled() bool {
	return advancedChatSettingBool(advancedChatAssistantConnectorWebSearchEnabledKey, true)
}

func advancedChatAssistantConnectorStaticSiteEnabled() bool {
	return advancedChatSettingBool(advancedChatAssistantConnectorStaticSiteEnabledKey, true)
}

func advancedChatScheduledTasksEnabled() bool {
	if !advancedChatPremiumFeaturesAvailable() {
		return false
	}
	return advancedChatSettingBool(advancedChatScheduledTasksEnabledKey, true)
}

func advancedChatMessageChannelEnabled() bool {
	if !advancedChatPremiumFeaturesAvailable() {
		return false
	}
	return advancedChatSettingBool(advancedChatMessageChannelEnabledKey, false)
}

func advancedChatMessageDeliveryEnabled() bool {
	if !advancedChatPremiumFeaturesAvailable() {
		return false
	}
	return advancedChatSettingBool(advancedChatMessageDeliveryEnabledKey, true)
}

func advancedChatDeliverySystemSMTPEnabled() bool {
	if !advancedChatPremiumFeaturesAvailable() {
		return false
	}
	return advancedChatSettingBool(advancedChatDeliverySystemSMTPEnabledKey, true)
}

func advancedChatAssistantConnectorToolsEnabled() bool {
	return advancedChatAssistantConnectorListFilesEnabled() ||
		advancedChatAssistantConnectorReadFileEnabled() ||
		advancedChatAssistantConnectorWriteFileEnabled() ||
		advancedChatAssistantConnectorReplaceTextEnabled() ||
		advancedChatAssistantConnectorRunCommandEnabled() ||
		advancedChatAssistantConnectorWebSearchEnabled() ||
		advancedChatAssistantConnectorStaticSiteEnabled()
}

func advancedChatAssistantConnectorActionEnabled(action string) bool {
	switch action {
	case "list_files":
		return advancedChatAssistantConnectorListFilesEnabled()
	case "list_windows_drives":
		return advancedChatAssistantConnectorListFilesEnabled() ||
			advancedChatAssistantConnectorReadFileEnabled() ||
			advancedChatAssistantConnectorWriteFileEnabled() ||
			advancedChatAssistantConnectorReplaceTextEnabled()
	case "read_file":
		return advancedChatAssistantConnectorReadFileEnabled()
	case "write_file":
		return advancedChatAssistantConnectorWriteFileEnabled()
	case "replace_text":
		return advancedChatAssistantConnectorReplaceTextEnabled()
	case "run_command":
		return advancedChatAssistantConnectorRunCommandEnabled()
	case "web_search":
		return advancedChatAssistantConnectorWebSearchEnabled()
	case "web_fetch":
		return advancedChatAssistantConnectorWebSearchEnabled()
	case "list_static_sites", "deploy_static_site", "set_static_site_enabled", "delete_static_site":
		return advancedChatAssistantConnectorStaticSiteEnabled()
	default:
		return false
	}
}

func advancedChatSettingBool(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(model.GetSystemSetting(key, strconv.FormatBool(fallback))))
	switch value {
	case "1", "true", "yes", "on", "enabled":
		return true
	case "0", "false", "no", "off", "disabled":
		return false
	default:
		return fallback
	}
}

func advancedChatSettingInt(key string, fallback, min, max int) int {
	value, err := strconv.Atoi(strings.TrimSpace(model.GetSystemSetting(key, strconv.Itoa(fallback))))
	if err != nil {
		return fallback
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func advancedChatBuiltinMCPServers(includeHeaders bool) []AdvancedChatMCPServer {
	raw := strings.TrimSpace(model.GetSystemSetting(advancedChatBuiltinMCPServersKey, "[]"))
	var servers []AdvancedChatMCPServer
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &servers)
	}
	normalized, ok := normalizeMCPServerList(servers, advancedChatMCPModeBackend, includeHeaders)
	if !ok {
		return []AdvancedChatMCPServer{}
	}
	return normalized
}

func advancedChatCustomMCPServers(userID uint) []AdvancedChatMCPServer {
	return advancedChatCustomMCPServersForResponse(userID, false)
}

func advancedChatCustomMCPServersWithHeaders(userID uint) []AdvancedChatMCPServer {
	return advancedChatCustomMCPServersForResponse(userID, true)
}

func advancedChatCustomMCPServersForResponse(userID uint, includeHeaders bool) []AdvancedChatMCPServer {
	var settings AdvancedChatUserSettings
	if err := model.DB.Where("user_id = ?", userID).First(&settings).Error; err != nil {
		return []AdvancedChatMCPServer{}
	}
	var servers []AdvancedChatMCPServer
	if strings.TrimSpace(settings.CustomMCPServers) != "" {
		_ = json.Unmarshal([]byte(settings.CustomMCPServers), &servers)
	}
	normalized, ok := normalizeMCPServerList(servers, advancedChatMCPModeBackend, includeHeaders)
	if !ok {
		return []AdvancedChatMCPServer{}
	}
	return normalized
}

func normalizeAttachmentTypes(values []string) []string {
	types := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		item := strings.ToLower(strings.TrimSpace(value))
		if item == "" {
			continue
		}
		if len(item) > 100 {
			continue
		}
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		types = append(types, item)
		if len(types) >= 50 {
			break
		}
	}
	return types
}

func normalizeMCPServers(c *gin.Context, input []AdvancedChatMCPServer, requestMode string, includeHeaders bool) ([]AdvancedChatMCPServer, bool) {
	servers, ok := normalizeMCPServerList(input, requestMode, includeHeaders)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid MCP server configuration"})
		return nil, false
	}
	return servers, true
}

func normalizeMCPServerList(input []AdvancedChatMCPServer, requestMode string, includeHeaders bool) ([]AdvancedChatMCPServer, bool) {
	if len(input) > 20 {
		return nil, false
	}
	servers := make([]AdvancedChatMCPServer, 0, len(input))
	seenIDs := map[string]struct{}{}
	now := time.Now().UnixNano()
	for index, item := range input {
		name := strings.TrimSpace(item.Name)
		if name == "" || len([]rune(name)) > 100 {
			return nil, false
		}
		id := strings.TrimSpace(item.ID)
		if id == "" {
			id = "mcp-" + strconv.FormatInt(now+int64(index), 36)
		}
		if len(id) > 80 {
			return nil, false
		}
		if _, exists := seenIDs[id]; exists {
			return nil, false
		}
		seenIDs[id] = struct{}{}
		serverType := normalizeMCPServerType(item.Type)
		mode := requestMode
		if serverType == advancedChatMCPTypeConnector {
			mode = advancedChatMCPTypeConnector
		}
		endpoint := ""
		headers := ""
		command := ""
		args := []string(nil)
		env := map[string]string(nil)
		cwd := ""
		if serverType == advancedChatMCPTypeHTTP {
			endpoint = strings.TrimSpace(item.URL)
			if endpoint == "" || len(endpoint) > 2000 || !validMCPServerURL(endpoint) {
				return nil, false
			}
		}
		if serverType == advancedChatMCPTypeConnector {
			var ok bool
			command, args, env, cwd, ok = normalizeConnectorMCPCommand(item)
			if !ok {
				return nil, false
			}
		}
		if includeHeaders && serverType == advancedChatMCPTypeHTTP {
			headers = strings.TrimSpace(item.Headers)
			if len(headers) > 4000 {
				return nil, false
			}
		}
		servers = append(servers, AdvancedChatMCPServer{
			ID:          id,
			Name:        name,
			Type:        serverType,
			URL:         endpoint,
			Headers:     headers,
			Command:     command,
			Args:        args,
			Env:         env,
			Cwd:         cwd,
			Enabled:     item.Enabled,
			RequestMode: mode,
		})
	}
	return servers, true
}

func normalizeMCPServerType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case advancedChatMCPTypeConnector:
		return advancedChatMCPTypeConnector
	default:
		return advancedChatMCPTypeHTTP
	}
}

func normalizeConnectorMCPCommand(item AdvancedChatMCPServer) (string, []string, map[string]string, string, bool) {
	command := strings.TrimSpace(item.Command)
	if command == "" || len(command) > 300 {
		return "", nil, nil, "", false
	}
	if len(item.Args) > 64 {
		return "", nil, nil, "", false
	}
	args := make([]string, 0, len(item.Args))
	for _, arg := range item.Args {
		arg = strings.TrimSpace(arg)
		if len(arg) > 1000 {
			return "", nil, nil, "", false
		}
		args = append(args, arg)
	}
	env := map[string]string(nil)
	if len(item.Env) > 50 {
		return "", nil, nil, "", false
	}
	if len(item.Env) > 0 {
		env = make(map[string]string, len(item.Env))
		for key, value := range item.Env {
			key = strings.TrimSpace(key)
			if key == "" || len(key) > 120 || len(value) > 4000 || strings.ContainsAny(key, "=\x00") || strings.Contains(value, "\x00") {
				return "", nil, nil, "", false
			}
			env[key] = value
		}
	}
	cwd := strings.TrimSpace(item.Cwd)
	if len(cwd) > 1000 || strings.Contains(cwd, "\x00") {
		return "", nil, nil, "", false
	}
	return command, args, env, cwd, true
}

func validMCPServerURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https"
}
