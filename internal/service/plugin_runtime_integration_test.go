package service

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/WindyPear-Team/veloce/internal/model"
	"github.com/shopspring/decimal"
)

// This opt-in check verifies that the runtime keeps declarative route pages
// intact when it reads a plugin manifest from a WASM module.
func TestPluginManifestWASMIncludesRoutePages(t *testing.T) {
	wasmPath := os.Getenv("VELOCE_PLUGIN_MANIFEST_TEST_WASM")
	if wasmPath == "" {
		t.Skip("VELOCE_PLUGIN_MANIFEST_TEST_WASM is not set")
	}
	manifest, _, err := ReadPluginManifestFromWASM(context.Background(), wasmPath)
	if err != nil {
		t.Fatal(err)
	}
	var frontend struct {
		Routes []struct {
			Path string          `json:"path"`
			Page json.RawMessage `json:"page"`
		} `json:"routes"`
	}
	if err := json.Unmarshal(manifest.Frontend, &frontend); err != nil {
		t.Fatal(err)
	}
	if len(frontend.Routes) == 0 {
		t.Fatal("manifest has no frontend routes")
	}
	for _, route := range frontend.Routes {
		if len(route.Page) == 0 || string(route.Page) == "null" {
			t.Fatalf("route %q has no declarative page: %s", route.Path, manifest.Frontend)
		}
		var page struct {
			Type     string          `json:"type"`
			Children json.RawMessage `json:"children"`
		}
		if err := json.Unmarshal(route.Page, &page); err != nil {
			t.Fatalf("route %q has invalid page: %v", route.Path, err)
		}
		if page.Type != "page" || len(page.Children) == 0 || string(page.Children) == "null" || string(page.Children) == "[]" {
			t.Fatalf("route %q has an empty page: %s", route.Path, route.Page)
		}
	}
}

// This test is opt-in because building the fixture requires TinyGo. CI can set
// VELOCE_PLUGIN_TEST_WASM to any helper-based plugin with a draw action.
func TestPluginRuntimeHelperWASM(t *testing.T) {
	wasmPath := os.Getenv("VELOCE_PLUGIN_TEST_WASM")
	if wasmPath == "" {
		t.Skip("VELOCE_PLUGIN_TEST_WASM is not set")
	}
	db := walletTestDB(t, "runtime-helper")
	previous := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previous })
	user := model.User{Username: "runtime-helper", Email: "runtime-helper@example.com", APIKey: "runtime-helper-key", Balance: decimal.NewFromInt(10)}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	plugin := model.Plugin{
		ID: "balance-lottery-example", WASMPath: wasmPath,
		PermissionsJSON: mustJSON([]string{"wallet.balance.read", "wallet.balance.debit", "wallet.balance.credit"}),
	}
	result, err := InvokePluginAction(context.Background(), plugin, user.ID, "integration-draw-1", "draw", map[string]interface{}{"values": map[string]interface{}{}})
	if err != nil {
		t.Fatal(err)
	}
	if result["message"] == "" || result["balance"] == "" {
		t.Fatalf("unexpected helper plugin result: %#v", result)
	}
	replay, err := InvokePluginAction(context.Background(), plugin, user.ID, "integration-draw-1", "draw", map[string]interface{}{"values": map[string]interface{}{}})
	if err != nil {
		t.Fatal(err)
	}
	if replay["replay"] != true || replay["prize"] != result["prize"] {
		t.Fatalf("unexpected replay: first=%#v replay=%#v", result, replay)
	}
}
