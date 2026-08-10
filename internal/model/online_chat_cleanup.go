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
		"advanced_chat_cloud_sandbox_charges",
		"advanced_chat_cloud_sandboxes",
		"advanced_chat_cloud_sandbox_hosts",
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
		// Retired shared-workspace tables.
		"enterprise_shared_files",
		"enterprise_shared_sessions",
		"enterprise_shared_pools",
		"quota_ledgers",
		"quota_accounts",
		"enterprise_device_assignments",
		"enterprise_devices",
		"enterprise_task_departments",
		"enterprise_task_assignments",
		"enterprise_tasks",
		"department_members",
		"department_role_bindings",
		"role_bindings",
		"role_permissions",
		"roles",
		"permissions",
		"workspace_members",
		"organization_members",
		"workspaces",
		"departments",
		"organizations",
		"company_outbox_events",
		"company_signals",
		"company_audit_events",
		"company_budget_ledgers",
		"company_approval_requests",
		"company_handoff_packages",
		"company_artifacts",
		"company_work_attempts",
		"company_work_items",
		"company_objectives",
		"company_recruitment_plans",
		"company_capability_evidences",
		"company_employee_versions",
		"company_role_templates",
		"personal_company_employees",
		"company_charter_revisions",
		"personal_companies",
	}
	for _, table := range chatTables {
		if db.Migrator().HasTable(table) {
			if err := db.Migrator().DropTable(table); err != nil {
				return err
			}
		}
	}

	return db.Where("key LIKE ? OR key IN ?", "advanced_chat_%", []string{
		"chat_page_mode",
		"community_enabled",
		"sidebar_chat_enabled",
	}).Delete(&SystemSetting{}).Error
}
