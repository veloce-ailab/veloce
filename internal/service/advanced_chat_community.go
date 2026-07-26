package service

import (
	"context"
	"net/http"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
)

func InitCommunityAdvancedChatFeatures() error {
	if CurrentEdition() == "premium" {
		return nil
	}
	err := model.DB.AutoMigrate(
		&AdvancedChatAgent{},
		&AdvancedChatAgentStudio{},
		&AdvancedChatUserSettings{},
		&AdvancedChatSkill{},
		&AdvancedChatSkillPackage{},
		&AdvancedChatPackagedSkill{},
		&AdvancedChatSkillPackage{},
		&AdvancedChatPackagedSkill{},
		&AdvancedChatSessionFolder{},
		&AdvancedChatSession{},
		&AdvancedChatSessionTask{},
		&AdvancedChatMessage{},
		&AdvancedChatRun{},
		&AdvancedChatRunEvent{},
		&AdvancedChatKnowledgeBase{},
		&AdvancedChatKnowledgeDocument{},
		&AdvancedChatKnowledgeChunk{},
		&AdvancedChatConnectorDevice{},
		&AdvancedChatConnectorTask{},
		&AdvancedChatCloudSandboxHost{},
		&AdvancedChatCloudSandbox{},
		&AdvancedChatCloudSandboxCharge{},
		&AdvancedChatStaticSite{},
	)
	if err == nil {
		ensureAdvancedChatKnowledgePostgresVectorColumn()
		startAdvancedChatKnowledgeEmbeddingWorker()
	}
	return err
}

func InitAdvancedChatFeatures() error {
	return initAdvancedChatFeatures()
}

func RegisterAdvancedChatAdminRoutes(group *gin.RouterGroup) {
	registerAdvancedChatAdminRoutes(group)
}

func RegisterAdvancedChatUserRoutes(group *gin.RouterGroup) {
	registerAdvancedChatUserRoutes(group)
}

func RegisterAdvancedChatPublicRoutes(group *gin.RouterGroup) {
	registerAdvancedChatConnectorRoutes(group)
}

func ApplyAdvancedChatGeneratedAssetHook(ctx context.Context, input GeneratedAssetInput) {
	autoSaveAdvancedChatGeneratedAsset(ctx, input)
}

func RegisterCommunityAdvancedChatAdminRoutes(group *gin.RouterGroup) {
	if CurrentEdition() == "premium" {
		return
	}
	api := &advancedChatAPI{}
	group.GET("/advanced-chat/settings", api.getAdminSettings)
	group.PUT("/advanced-chat/settings", api.updateAdminSettings)
}

func RegisterCommunityAdvancedChatUserRoutes(group *gin.RouterGroup) {
	if CurrentEdition() == "premium" {
		return
	}
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
	group.GET("/advanced-chat/sessions/:id/tasks", api.listSessionTasks)
	group.PUT("/advanced-chat/sessions/:id/folder", api.moveSessionToFolder)
	group.POST("/advanced-chat/sessions/:id/title/regenerate", api.regenerateSessionTitle)
	group.DELETE("/advanced-chat/sessions/:id", api.deleteSession)
	group.GET("/advanced-chat/runs/:id", api.getRun)
	group.GET("/advanced-chat/runs/:id/events", api.listRunEvents)
	group.GET("/advanced-chat/runs/:id/agent-work", api.getRunAgentWork)
	group.POST("/advanced-chat/runs/:id/stop", api.stopRun)
	group.GET("/advanced-chat/agent-tasks", api.listAgentTasks)
	group.GET("/advanced-chat/knowledge-bases", api.listKnowledgeBases)
	group.POST("/advanced-chat/knowledge-bases", api.createKnowledgeBase)
	group.POST("/advanced-chat/community/knowledge-bases/:id/import", api.importCommunityKnowledgeBase)
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
}

func RegisterCommunityAdvancedChatPublicRoutes(group *gin.RouterGroup) {
	if CurrentEdition() == "premium" {
		return
	}
	registerAdvancedChatConnectorRoutes(group)
}

func currentAdvancedChatUser(c *gin.Context) (*model.User, bool) {
	user, ok := currentUserFromContext(c)
	if !ok {
		return nil, false
	}
	return user, true
}

func writeCommunityAdvancedChatPremiumRequired(c *gin.Context) {
	c.JSON(http.StatusPaymentRequired, gin.H{"error": "Premium edition is required"})
}

func advancedChatPremiumFeaturesAvailable() bool {
	return CurrentEdition() == "premium"
}

func advancedChatPremiumSettingRequested(input advancedChatAdminSettingsInput) bool {
	return boolPtrTrue(input.FileStorageEnabled) ||
		boolPtrTrue(input.FileStorageAutoSaveImagesEnabled) ||
		boolPtrTrue(input.FileStorageAutoSaveVideosEnabled) ||
		boolPtrTrue(input.ScheduledTasksEnabled) ||
		boolPtrTrue(input.MessageChannelEnabled) ||
		boolPtrTrue(input.MessageDeliveryEnabled) ||
		boolPtrTrue(input.DeliverySystemSMTPEnabled)
}

func boolPtrTrue(value *bool) bool {
	return value != nil && *value
}
