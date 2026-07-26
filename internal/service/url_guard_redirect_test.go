package service

import (
	"errors"
	"net/http"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/veloce-ailab/veloce/internal/model"
	"gorm.io/gorm"
)

// useURLGuardSettings points the guard at an in-memory settings table so tests
// drive the real validator instead of a stub. Hosts are allow-listed by name to
// keep assertions independent of DNS.
func useURLGuardSettings(t *testing.T, values map[string]string) {
	t.Helper()
	database, err := gorm.Open(sqlite.Open("file:url-guard-"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	for key, value := range values {
		if err := database.Create(&model.SystemSetting{Key: key, Value: value}).Error; err != nil {
			t.Fatal(err)
		}
	}
	previous := model.DB
	model.DB = database
	t.Cleanup(func() { model.DB = previous })
}

func withBlockedHostGuard(t *testing.T, blockedHost string) {
	t.Helper()
	if blockedHost != "127.0.0.1" {
		t.Fatalf("helper only models the loopback case, got %q", blockedHost)
	}
	// Loopback is rejected by the guard itself; the example hosts are allow-listed
	// so the redirect assertions do not depend on name resolution.
	useURLGuardSettings(t, map[string]string{
		"ssrf_protection_enabled": "true",
		"ssrf_allowed_hosts":      "mcp.example.com,other.example.com",
	})
}

func newRedirectHop(t *testing.T, target string, headers map[string]string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	return req
}

// A host that passes the guard can still answer with a redirect to an internal
// address; each hop must be re-validated rather than followed blindly.
func TestGuardedRedirectPolicyRejectsBlockedHop(t *testing.T) {
	withBlockedHostGuard(t, "127.0.0.1")
	policy := GuardedRedirectPolicy()

	via := []*http.Request{newRedirectHop(t, "https://mcp.example.com/rpc", nil)}
	hop := newRedirectHop(t, "http://127.0.0.1:3000/api/admin/settings", nil)
	if err := policy(hop, via); !errors.Is(err, ErrUnsafeURL) {
		t.Fatalf("policy error = %v, want ErrUnsafeURL", err)
	}
}

func TestGuardedRedirectPolicyStripsCredentialsAcrossHosts(t *testing.T) {
	withBlockedHostGuard(t, "127.0.0.1")
	policy := GuardedRedirectPolicy()

	via := []*http.Request{newRedirectHop(t, "https://mcp.example.com/rpc", nil)}
	hop := newRedirectHop(t, "https://other.example.com/rpc", map[string]string{
		"Authorization": "Bearer secret",
		"Cookie":        "session=secret",
		"X-Api-Key":     "secret",
		"X-Trace":       "keep-me",
	})
	if err := policy(hop, via); err != nil {
		t.Fatalf("policy error = %v, want nil", err)
	}
	for _, header := range []string{"Authorization", "Cookie", "X-Api-Key"} {
		if value := hop.Header.Get(header); value != "" {
			t.Errorf("%s = %q, want it stripped when the redirect leaves the original host", header, value)
		}
	}
	if hop.Header.Get("X-Trace") != "keep-me" {
		t.Error("non-credential headers must survive the redirect")
	}
}

func TestGuardedRedirectPolicyKeepsCredentialsOnSameHost(t *testing.T) {
	withBlockedHostGuard(t, "127.0.0.1")
	policy := GuardedRedirectPolicy()

	via := []*http.Request{newRedirectHop(t, "https://mcp.example.com/rpc", nil)}
	hop := newRedirectHop(t, "https://mcp.example.com/rpc/v2", map[string]string{"Authorization": "Bearer secret"})
	if err := policy(hop, via); err != nil {
		t.Fatalf("policy error = %v, want nil", err)
	}
	if hop.Header.Get("Authorization") != "Bearer secret" {
		t.Error("credentials must be preserved for a same-host redirect")
	}
}
