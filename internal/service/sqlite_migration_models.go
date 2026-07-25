package service

import "github.com/veloce-ailab/veloce/internal/model"

func init() {
	model.RegisterSQLiteMigrationModels(
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
}
