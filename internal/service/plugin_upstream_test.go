package service

import (
	"encoding/json"
	"testing"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestPluginManifestValidatesResponsesUpstream(t *testing.T) {
	manifest := PluginManifest{
		ID: "codex-plugin", Name: "Codex", Version: "1.0.0",
		Upstreams: []PluginUpstreamType{{ID: "codex", Name: "Codex", Protocol: "responses", PrepareAction: "upstream.prepare"}},
	}
	if err := validatePluginManifest(manifest); err != nil {
		t.Fatalf("valid upstream manifest: %v", err)
	}
	manifest.Upstreams[0].Protocol = "openai"
	if err := validatePluginManifest(manifest); err == nil {
		t.Fatal("expected non-Responses upstream to be rejected")
	}
}

func TestPluginUpstreamValidatesRequiredChannelConfig(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:plugin-upstream-config?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Plugin{}, &model.Channel{}); err != nil {
		t.Fatal(err)
	}
	previous := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previous })
	manifest := PluginManifest{ID: "codex-plugin", Name: "Codex", Version: "1.0.0", Upstreams: []PluginUpstreamType{{ID: "codex", Name: "Codex", Protocol: "responses", PrepareAction: "upstream.prepare", Config: json.RawMessage(`{"fields":[{"name":"pool_id","required":true}]}`)}}}
	raw, _ := json.Marshal(manifest)
	if err := db.Create(&model.Plugin{ID: manifest.ID, Name: manifest.Name, Version: manifest.Version, Enabled: true, ManifestJSON: string(raw), Path: "plugins"}).Error; err != nil {
		t.Fatal(err)
	}
	channel := model.Channel{Type: "plugin--codex-plugin--codex", PluginConfigJSON: `{}`}
	if err := ValidatePluginUpstreamChannel(channel); err == nil {
		t.Fatal("expected required plugin setting to be rejected")
	}
	channel.PluginConfigJSON = `{"pool_id":"shared"}`
	if err := ValidatePluginUpstreamChannel(channel); err != nil {
		t.Fatalf("valid plugin channel config: %v", err)
	}
}
