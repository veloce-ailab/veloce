package service

import (
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Third-party token authorization ("令牌授权") lets an external application
// obtain one of the signed-in user's API tokens without any client id or
// client secret. The design is deliberately close to PKCE:
//
//  1. The application generates a high-entropy state and opens
//     /authorize?state=...&redirect_uri=...&app_name=...
//  2. The user signs in, then either picks an existing token or creates a new
//     one on the authorization page, and approves the request.
//  3. The authorization page redirects back to redirect_uri carrying only the
//     same state value.
//  4. The application redeems that state through a server-to-server call to
//     POST /api/oauth/token and finally receives the token.
//
// The token never appears in a redirected URL, so it cannot leak through
// browser history, referrer headers or third-party access logs. Grants live in
// memory only: a restart simply invalidates pending authorizations, exactly
// like the desktop login codes.

const (
	// TokenAuthGrantTTL bounds how long an approved authorization can be
	// redeemed for a token.
	TokenAuthGrantTTL        = 5 * time.Minute
	tokenAuthMaxAppNameLen   = 64
	tokenAuthMaxRedirectLen  = 2048
	tokenAuthMaxPendingCount = 4096
)

var (
	// tokenAuthStatePattern follows the PKCE code verifier alphabet and keeps a
	// long minimum length, because the state is both the correlation value and
	// the only thing needed to redeem the token.
	tokenAuthStatePattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{32,256}$`)

	ErrTokenAuthInvalidState     = errors.New("state must be 32-256 characters from [A-Za-z0-9._~-]")
	ErrTokenAuthInvalidRedirect  = errors.New("redirect_uri must be an absolute URL using https, loopback http, or a custom scheme")
	ErrTokenAuthGrantUnavailable = errors.New("authorization is unknown, expired, or already redeemed")
	ErrTokenAuthTooManyPending   = errors.New("too many pending authorizations, please retry later")

	tokenAuthGrantsMu sync.Mutex
	tokenAuthGrants   = map[string]TokenAuthGrant{}
)

// TokenAuthGrant is an approved authorization waiting to be redeemed.
type TokenAuthGrant struct {
	UserID      uint
	APIKeyID    uint
	Token       string
	RedirectURI string
	ApprovedAt  time.Time
	ExpiresAt   time.Time
}

// ValidateTokenAuthState checks the application-provided state value.
func ValidateTokenAuthState(state string) error {
	if !tokenAuthStatePattern.MatchString(state) {
		return ErrTokenAuthInvalidState
	}
	return nil
}

// NormalizeTokenAuthRedirectURI validates and canonicalizes the callback the
// authorization page will send the user back to. Relative URLs, fragments and
// embedded credentials are rejected; plain http is only accepted for loopback
// hosts so local development keeps working.
func NormalizeTokenAuthRedirectURI(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > tokenAuthMaxRedirectLen {
		return "", ErrTokenAuthInvalidRedirect
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", ErrTokenAuthInvalidRedirect
	}
	if parsed.Fragment != "" || parsed.User != nil || parsed.Scheme == "" {
		return "", ErrTokenAuthInvalidRedirect
	}

	scheme := strings.ToLower(parsed.Scheme)
	switch scheme {
	case "":
		return "", ErrTokenAuthInvalidRedirect
	case "https":
		if parsed.Host == "" {
			return "", ErrTokenAuthInvalidRedirect
		}
	case "http":
		if !tokenAuthIsLoopbackHost(parsed.Hostname()) {
			return "", ErrTokenAuthInvalidRedirect
		}
	case "javascript", "data", "file", "vbscript", "blob", "about", "chrome", "chrome-extension":
		return "", ErrTokenAuthInvalidRedirect
	default:
		// Custom reverse-DNS scheme such as myapp://callback. The scheme itself
		// must look like a host-shaped identifier to avoid confusing the user.
		if len(scheme) < 2 || !tokenAuthIsReverseDNSScheme(scheme) {
			return "", ErrTokenAuthInvalidRedirect
		}
		if parsed.Host == "" {
			return "", ErrTokenAuthInvalidRedirect
		}
	}

	parsed.Scheme = scheme
	return parsed.String(), nil
}

// TokenAuthCallbackURL appends the given parameters to the (already
// normalized) redirect URI, preserving query parameters the application put
// there itself.
func TokenAuthCallbackURL(redirectURI string, params map[string]string) (string, error) {
	parsed, err := url.Parse(redirectURI)
	if err != nil {
		return "", ErrTokenAuthInvalidRedirect
	}
	query := parsed.Query()
	for key, value := range params {
		query.Set(key, value)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

// SanitizeTokenAuthAppName keeps the application-provided display name
// printable and short enough for the authorization page.
func SanitizeTokenAuthAppName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	cleaned = strings.TrimSpace(cleaned)
	runes := []rune(cleaned)
	if len(runes) > tokenAuthMaxAppNameLen {
		cleaned = string(runes[:tokenAuthMaxAppNameLen])
	}
	return cleaned
}

// CreateTokenAuthGrant records an approved authorization that the application
// can redeem with the same state it started the flow with.
func CreateTokenAuthGrant(state string, userID, apiKeyID uint, token, redirectURI string) error {
	if err := ValidateTokenAuthState(state); err != nil {
		return err
	}
	if userID == 0 || apiKeyID == 0 || strings.TrimSpace(token) == "" {
		return errors.New("invalid authorization grant")
	}

	now := time.Now()
	tokenAuthGrantsMu.Lock()
	defer tokenAuthGrantsMu.Unlock()
	for existing, grant := range tokenAuthGrants {
		if now.After(grant.ExpiresAt) {
			delete(tokenAuthGrants, existing)
		}
	}
	if len(tokenAuthGrants) >= tokenAuthMaxPendingCount {
		return ErrTokenAuthTooManyPending
	}
	tokenAuthGrants[state] = TokenAuthGrant{
		UserID:      userID,
		APIKeyID:    apiKeyID,
		Token:       token,
		RedirectURI: redirectURI,
		ApprovedAt:  now,
		ExpiresAt:   now.Add(TokenAuthGrantTTL),
	}
	return nil
}

// ConsumeTokenAuthGrant redeems a grant exactly once. Expired or unknown states
// are reported with the same error so callers cannot probe for validity.
func ConsumeTokenAuthGrant(state string) (*TokenAuthGrant, error) {
	if err := ValidateTokenAuthState(state); err != nil {
		return nil, ErrTokenAuthGrantUnavailable
	}

	tokenAuthGrantsMu.Lock()
	grant, ok := tokenAuthGrants[state]
	if ok {
		delete(tokenAuthGrants, state)
	}
	tokenAuthGrantsMu.Unlock()

	if !ok || time.Now().After(grant.ExpiresAt) {
		return nil, ErrTokenAuthGrantUnavailable
	}
	return &grant, nil
}

func tokenAuthIsLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// tokenAuthIsReverseDNSScheme accepts schemes that contain at least one dot, so
// "com.example.app" passes while "app" or "myapp" does not.
func tokenAuthIsReverseDNSScheme(scheme string) bool {
	if !strings.Contains(scheme, ".") {
		return false
	}
	for _, r := range scheme {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '+' || r == '-' {
			continue
		}
		return false
	}
	return !strings.HasPrefix(scheme, ".") && !strings.HasSuffix(scheme, ".")
}
