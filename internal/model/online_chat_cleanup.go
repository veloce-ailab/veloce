package model

import "gorm.io/gorm"

// RemoveOnlineChatSchema permanently removes the retired browser-chat feature
// data. It is deliberately idempotent so it also handles installations that
// have never enabled the feature.
func RemoveOnlineChatSchema(db *gorm.DB) error {
	if db == nil {
		return nil
	}

	chatTables := []string{
		// Drop dependent tables before their referenced parents so this works
		// with PostgreSQL and MySQL foreign-key enforcement as well as SQLite.
		"advanced_chat_static_sites",
		"advanced_chat_packaged_skills",
		"advanced_chat_agent_studios",
		"advanced_chat_agents",
		"advanced_chat_connector_devices",
		"advanced_chat_connector_tasks",
		"advanced_chat_deliveries",
		"advanced_chat_files",
		"advanced_chat_knowledge_bases",
		"advanced_chat_knowledge_chunks",
		"advanced_chat_knowledge_documents",
		"advanced_chat_memory_documents",
		"advanced_chat_messages",
		"advanced_chat_run_events",
		"advanced_chat_runs",
		"advanced_chat_scheduled_tasks",
		"advanced_chat_session_folders",
		"advanced_chat_sessions",
		"advanced_chat_skill_packages",
		"advanced_chat_skills",
		"advanced_chat_user_settings",
		// These enterprise tables only existed to share browser-chat sessions,
		// attachments, and Connector devices.
		"enterprise_shared_files",
		"enterprise_shared_sessions",
		"enterprise_shared_pools",
	}
	for _, table := range chatTables {
		if db.Migrator().HasTable(table) {
			if err := db.Migrator().DropTable(table); err != nil {
				return err
			}
		}
	}

	for _, column := range []struct {
		model interface{}
		table string
		name  string
	}{
		{model: &PersonalCompanyEmployee{}, table: "personal_company_employees", name: "advanced_chat_agent_id"},
		{model: &CompanyWorkAttempt{}, table: "company_work_attempts", name: "advanced_chat_run_id"},
	} {
		if db.Migrator().HasTable(column.table) && db.Migrator().HasColumn(column.table, column.name) {
			if err := db.Migrator().DropColumn(column.model, column.name); err != nil {
				return err
			}
		}
	}

	return db.Where("key LIKE ? OR key IN ?", "advanced_chat_%", []string{
		"chat_page_mode",
		"sidebar_chat_enabled",
	}).Delete(&SystemSetting{}).Error
}
