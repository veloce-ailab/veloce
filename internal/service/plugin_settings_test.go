package service

import (
	"testing"

	"github.com/veloce-ailab/veloce/internal/model"
)

func TestPluginGlobalSettingsAreSharedByUsers(t *testing.T) {
	db := walletTestDB(t, "global-plugin-settings")
	previous := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previous })
	plugin := model.Plugin{
		ID: "lottery-settings", Name: "Lottery", Version: "1", ManifestJSON: "{}", Path: "plugins",
		PermissionsJSON: mustJSON([]string{"plugin.settings.global"}), GlobalConfigJSON: `{"entry_fee":"2"}`,
	}
	if err := db.Create(&plugin).Error; err != nil {
		t.Fatal(err)
	}
	first := pluginConfigForUser(101, plugin.ID)
	second := pluginConfigForUser(202, plugin.ID)
	if first["entry_fee"] != "2" || second["entry_fee"] != "2" {
		t.Fatalf("global configs = %#v and %#v", first, second)
	}
}
