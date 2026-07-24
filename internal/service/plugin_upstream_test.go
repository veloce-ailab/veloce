package service

import "testing"

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
