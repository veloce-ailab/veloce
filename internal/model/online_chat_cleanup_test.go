package model

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestRemoveOnlineChatSchema(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:online_chat_cleanup?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	for _, statement := range []string{
		"CREATE TABLE advanced_chat_sessions (id text primary key)",
		"CREATE TABLE advanced_chat_messages (id text primary key)",
		"CREATE TABLE advanced_chat_connector_devices (id text primary key)",
		"CREATE TABLE advanced_chat_static_sites (id text primary key, device_id text REFERENCES advanced_chat_connector_devices(id))",
		"CREATE TABLE advanced_chat_skill_packages (id text primary key)",
		"CREATE TABLE advanced_chat_packaged_skills (id text primary key, package_id text REFERENCES advanced_chat_skill_packages(id))",
		"CREATE TABLE enterprise_shared_pools (id integer primary key)",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("prepare schema: %v", err)
		}
	}
	if err := db.AutoMigrate(&SystemSetting{}, &PersonalCompanyEmployee{}, &CompanyWorkAttempt{}); err != nil {
		t.Fatalf("migrate models: %v", err)
	}
	if err := db.Create(&[]SystemSetting{
		{Key: "advanced_chat_file_storage_enabled", Value: "true"},
		{Key: "chat_page_mode", Value: "advanced"},
		{Key: "sidebar_chat_enabled", Value: "true"},
		{Key: "site_name", Value: "kept"},
	}).Error; err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	if err := RemoveOnlineChatSchema(db); err != nil {
		t.Fatalf("remove online chat schema: %v", err)
	}

	for _, table := range []string{
		"advanced_chat_sessions", "advanced_chat_messages",
		"advanced_chat_connector_devices", "advanced_chat_static_sites",
		"advanced_chat_skill_packages", "advanced_chat_packaged_skills",
		"enterprise_shared_pools",
	} {
		if db.Migrator().HasTable(table) {
			t.Errorf("table %q was not removed", table)
		}
	}
	for _, column := range []struct{ table, name string }{
		{table: "personal_company_employees", name: "advanced_chat_agent_id"},
		{table: "company_work_attempts", name: "advanced_chat_run_id"},
	} {
		if db.Migrator().HasColumn(column.table, column.name) {
			t.Errorf("column %s.%s was not removed", column.table, column.name)
		}
	}
	var remaining []SystemSetting
	if err := db.Find(&remaining).Error; err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if len(remaining) != 1 || remaining[0].Key != "site_name" {
		t.Errorf("unexpected retained settings: %#v", remaining)
	}
}
