package service

import (
	"strings"
	"testing"
	"time"
)

func TestValidateTokenAuthState(t *testing.T) {
	valid := []string{
		strings.Repeat("a", 32),
		strings.Repeat("A", 32),
		"0123456789abcdef0123456789abcdef",
		"abc-DEF_123.~" + strings.Repeat("x", 20),
		strings.Repeat("z", 256),
	}
	for _, state := range valid {
		if err := ValidateTokenAuthState(state); err != nil {
			t.Fatalf("expected %q to be valid: %v", state, err)
		}
	}

	invalid := []string{
		"",
		strings.Repeat("a", 31),
		strings.Repeat("a", 257),
		"0123456789abcdef0123456789abcde!",
		"0123456789abcdef0123456789abcde ",
		"0123456789abcdef0123456789abcde/",
		"你好你好你好你好你好你好你好你好你好你好你好你好你好你好你好你好",
	}
	for _, state := range invalid {
		if err := ValidateTokenAuthState(state); err == nil {
			t.Fatalf("expected %q to be rejected", state)
		}
	}
}

func TestNormalizeTokenAuthRedirectURI(t *testing.T) {
	accepted := map[string]string{
		"https://app.example.com/callback": "https://app.example.com/callback",
		"HTTPS://App.Example.com/cb?x=1":   "https://App.Example.com/cb?x=1",
		"http://localhost:3000/callback":   "http://localhost:3000/callback",
		"http://127.0.0.1/callback":        "http://127.0.0.1/callback",
		"http://[::1]:8080/cb":             "http://[::1]:8080/cb",
		"com.example.app://callback":       "com.example.app://callback",
		"com.example.app://auth/cb?keep=1": "com.example.app://auth/cb?keep=1",
		"com.veloce.aiapp://auth/callback": "com.veloce.aiapp://auth/callback",
	}
	for raw, want := range accepted {
		got, err := NormalizeTokenAuthRedirectURI(raw)
		if err != nil {
			t.Fatalf("expected %q to be accepted: %v", raw, err)
		}
		if got != want {
			t.Fatalf("normalize(%q) = %q, want %q", raw, got, want)
		}
	}

	rejected := []string{
		"",
		"/relative/callback",
		"javascript:alert(1)",
		"data:text/html,hi",
		"file:///etc/passwd",
		"http://app.example.com/callback",
		"https://app.example.com/cb#fragment",
		"https://user:pass@app.example.com/cb",
		"myapp://callback",
		"https://" + strings.Repeat("a", 2100),
	}
	for _, raw := range rejected {
		if _, err := NormalizeTokenAuthRedirectURI(raw); err == nil {
			t.Fatalf("expected %q to be rejected", raw)
		}
	}
}

func TestTokenAuthCallbackURLKeepsExistingQuery(t *testing.T) {
	got, err := TokenAuthCallbackURL("https://app.example.com/cb?keep=1", map[string]string{"state": "s1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "https://app.example.com/cb?keep=1&state=s1" {
		t.Fatalf("unexpected callback url %q", got)
	}
}

func TestSanitizeTokenAuthAppName(t *testing.T) {
	if got := SanitizeTokenAuthAppName("  My\x00App  "); got != "MyApp" {
		t.Fatalf("unexpected sanitized name %q", got)
	}
	if got := SanitizeTokenAuthAppName(strings.Repeat("标", 100)); len([]rune(got)) != 64 {
		t.Fatalf("expected 64 runes, got %d", len([]rune(got)))
	}
}

func TestTokenAuthGrantIsSingleUse(t *testing.T) {
	state := strings.Repeat("s", 32)
	if err := CreateTokenAuthGrant(state, 1, 2, "sk-test", "https://app.example.com/cb"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	grant, err := ConsumeTokenAuthGrant(state)
	if err != nil {
		t.Fatalf("first consume failed: %v", err)
	}
	if grant.Token != "sk-test" || grant.UserID != 1 || grant.APIKeyID != 2 {
		t.Fatalf("unexpected grant %+v", grant)
	}

	if _, err := ConsumeTokenAuthGrant(state); err == nil {
		t.Fatal("second consume should fail")
	}
}

func TestTokenAuthGrantRejectsShortState(t *testing.T) {
	if err := CreateTokenAuthGrant("short", 1, 2, "sk-test", "https://app.example.com/cb"); err == nil {
		t.Fatal("expected short state to be rejected")
	}
	if _, err := ConsumeTokenAuthGrant("unknown-state-value-that-is-long-enough"); err == nil {
		t.Fatal("expected unknown state to be rejected")
	}
}

func TestTokenAuthGrantExpiry(t *testing.T) {
	state := strings.Repeat("e", 32)
	if err := CreateTokenAuthGrant(state, 1, 2, "sk-test", "https://app.example.com/cb"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tokenAuthGrantsMu.Lock()
	grant := tokenAuthGrants[state]
	grant.ExpiresAt = time.Now().Add(-time.Second)
	tokenAuthGrants[state] = grant
	tokenAuthGrantsMu.Unlock()

	if _, err := ConsumeTokenAuthGrant(state); err == nil {
		t.Fatal("expired grant should not be redeemable")
	}
}
