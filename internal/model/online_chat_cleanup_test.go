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
		"CREATE TABLE personal_companies (id integer primary key)",
		"CREATE TABLE company_work_attempts (id integer primary key, personal_company_id integer REFERENCES personal_companies(id))",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("prepare schema: %v", err)
		}
	}
	if err := db.AutoMigrate(&SystemSetting{}); err != nil {
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
		"enterprise_shared_pools", "personal_companies", "company_work_attempts",
	} {
		if db.Migrator().HasTable(table) {
			t.Errorf("table %q was not removed", table)
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
