package service

import (
	"net/http"
	"net/url"
	"testing"
)

func TestAllowedCommunityPluginRedirect(t *testing.T) {
	for _, raw := range []string{
		"https://github.com/example/plugin/releases/download/v1/plugin.wasm",
		"https://release-assets.githubusercontent.com/assets/1/plugin.wasm",
		"https://objects.githubusercontent.com/github-production-release-asset-2e65be/plugin.wasm",
	} {
		target, err := url.Parse(raw)
		if err != nil || !allowedCommunityPluginRedirect(target) {
			t.Fatalf("allowedCommunityPluginRedirect(%q) = false", raw)
		}
	}
	for _, raw := range []string{
		"http://github.com/example/plugin.wasm",
		"https://example.com/plugin.wasm",
		"https://github.com.evil.example/plugin.wasm",
		"https://user@github.com/example/plugin.wasm",
	} {
		target, _ := url.Parse(raw)
		if allowedCommunityPluginRedirect(target) {
			t.Fatalf("allowedCommunityPluginRedirect(%q) = true", raw)
		}
	}
}

func TestFilenameFromPluginRelease(t *testing.T) {
	response := &http.Response{Header: http.Header{"Content-Disposition": []string{`attachment; filename="market-plugin.wasm"`}}}
	if got := filenameFromPluginRelease(response); got != "market-plugin.wasm" {
		t.Fatalf("filenameFromPluginRelease() = %q", got)
	}
}
