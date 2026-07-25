package service

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/veloce-ailab/veloce/internal/config"
	"github.com/veloce-ailab/veloce/internal/model"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPluginListResponseUsesFrontendFromManifest(t *testing.T) {
	manifest := PluginManifest{Frontend: json.RawMessage(`{"routes":[{"path":"status","page":{"type":"page","children":[{"type":"text","text":"ready"}]}}]}`)}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	response := pluginListResponse(model.Plugin{ID: "test-plugin", ManifestJSON: string(raw), FrontendJSON: `{"routes":[{"path":"status"}]}`}, true)
	var frontend struct {
		Routes []struct {
			Page json.RawMessage `json:"page"`
		} `json:"routes"`
	}
	if err := json.Unmarshal(response.Frontend, &frontend); err != nil {
		t.Fatal(err)
	}
	if len(frontend.Routes) != 1 || len(frontend.Routes[0].Page) == 0 {
		t.Fatalf("frontend response lost route page: %s", response.Frontend)
	}
}

func TestPluginFrontendAccessFiltersRestrictedPages(t *testing.T) {
	raw := json.RawMessage(`{
		"sidebar":[
			{"label":"公开页面","path":"guide"},
			{"label":"管理页面","path":"status"}
		],
		"routes":[
			{"path":"guide","access":"public","page":{"type":"page"}},
			{"path":"status","access":"admin","page":{"type":"page"}}
		]
	}`)

	filtered := filterPluginFrontendForUser(raw, false)
	var frontend struct {
		Sidebar []struct {
			Path string `json:"path"`
		} `json:"sidebar"`
		Routes []struct {
			Path string `json:"path"`
		} `json:"routes"`
	}
	if err := json.Unmarshal(filtered, &frontend); err != nil {
		t.Fatal(err)
	}
	if len(frontend.Routes) != 1 || frontend.Routes[0].Path != "guide" {
		t.Fatalf("visible routes = %#v, want only guide", frontend.Routes)
	}
	if len(frontend.Sidebar) != 1 || frontend.Sidebar[0].Path != "guide" {
		t.Fatalf("visible sidebar = %#v, want only guide", frontend.Sidebar)
	}

	if err := validatePluginFrontendAccess(json.RawMessage(`{"routes":[{"path":"status","access":"owner"}]}`)); err == nil {
		t.Fatal("invalid frontend access should be rejected")
	}
}

// Opt-in diagnostic for a running local instance. It only reads plugin JSON
// from the configured database and never modifies the schema or data.
func TestLiveCodexPluginFrontendPages(t *testing.T) {
	if os.Getenv("VELOCE_PLUGIN_LIVE_INSPECT") != "1" {
		t.Skip("VELOCE_PLUGIN_LIVE_INSPECT is not set")
	}
	config.Init()
	db, err := gorm.Open(postgres.Open(config.DBDSN), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	var plugin model.Plugin
	if err := db.Select("id", "manifest_json", "frontend_json").Where("id = ?", "codex-subscription").First(&plugin).Error; err != nil {
		t.Fatal(err)
	}
	var manifest PluginManifest
	if err := json.Unmarshal([]byte(plugin.ManifestJSON), &manifest); err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Routes []struct {
			Page json.RawMessage `json:"page"`
		} `json:"routes"`
	}
	if err := json.Unmarshal([]byte(plugin.FrontendJSON), &stored); err != nil {
		t.Fatal(err)
	}
	var declared struct {
		Routes []struct {
			Page json.RawMessage `json:"page"`
		} `json:"routes"`
	}
	if err := json.Unmarshal(manifest.Frontend, &declared); err != nil {
		t.Fatal(err)
	}
	t.Logf("stored routes=%d pages=%t; manifest routes=%d pages=%t", len(stored.Routes), allRoutesHavePages(stored.Routes), len(declared.Routes), allRoutesHavePages(declared.Routes))
}

func allRoutesHavePages(routes []struct {
	Page json.RawMessage `json:"page"`
}) bool {
	return len(routes) > 0 && allRoutePagesPresent(routes)
}

func allRoutePagesPresent(routes []struct {
	Page json.RawMessage `json:"page"`
}) bool {
	for _, route := range routes {
		if len(route.Page) == 0 || string(route.Page) == "null" {
			return false
		}
	}
	return true
}
