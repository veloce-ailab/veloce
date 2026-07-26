package premium

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	communityapi "github.com/veloce-ailab/veloce/internal/api"
	communitymiddleware "github.com/veloce-ailab/veloce/internal/middleware"
	"github.com/veloce-ailab/veloce/internal/model"
	communityservice "github.com/veloce-ailab/veloce/internal/service"
	"github.com/gin-gonic/gin"
)

func Register() {
	communityservice.RegisterEditionProvider(func() string {
		return "premium"
	})
	communityapi.EnablePaymentFeature()
	communityservice.RegisterSensitiveFilterHooks(communityservice.SensitiveFilterHooks{
		Enabled:    sensitiveFilterEnabled,
		Scope:      sensitiveFilterScope,
		MatchWords: matchSensitiveWords,
		Words:      sensitiveWords,
	})
	communityservice.RegisterURLGuardHooks(communityservice.URLGuardHooks{
		ValidateConfiguredHTTPURL:    validateConfiguredHTTPURL,
		ValidateConfiguredTCPAddress: validateConfiguredTCPAddress,
		ValidateConfiguredStatus:     validateConfiguredStatusTarget,
		ValidateOutboundHTTPURL:      validateOutboundHTTPURL,
		CurrentOptions:               currentURLGuardOptions,
		Enabled:                      ssrfProtectionEnabled,
	})
	communitymiddleware.RegisterRateLimiterFactory(newRateLimiterMiddleware)
	communityservice.RegisterStartupHook(communityservice.InitSubscriptionFeatures)
	communityservice.RegisterStartupHook(communityservice.InitMetaModelFeatures)
	communityservice.RegisterStartupHook(communityservice.InitMemoryFeatures)
	communityservice.RegisterStartupHook(communityservice.InitAdvancedChatFeatures)
	communityservice.RegisterAdminRouteHook(communityservice.RegisterSubscriptionAdminRoutes)
	communityservice.RegisterAdminRouteHook(communityservice.RegisterMetaModelAdminRoutes)
	communityservice.RegisterAdminRouteHook(communityservice.RegisterAdvancedChatAdminRoutes)
	communityservice.RegisterPublicAPIRouteHook(communityservice.RegisterAdvancedChatPublicRoutes)
	communityservice.RegisterUserRouteHook(communityservice.RegisterSubscriptionUserRoutes)
	communityservice.RegisterUserRouteHook(communityservice.RegisterAdvancedChatUserRoutes)
	communityservice.RegisterUserRouteHook(communityservice.RegisterMemoryUserRoutes)
	communityservice.RegisterGeneratedAssetHook(communityservice.ApplyAdvancedChatGeneratedAssetHook)
	communityservice.RegisterMemoryHooks()
}

// currentPremiumUser resolves the authenticated user for the handlers still
// living in this package. It disappears together with the package once memory
// and meta-model move into internal/service.
func currentPremiumUser(c *gin.Context) (*model.User, bool) {
	value, exists := c.Get("user")
	if !exists {
		return nil, false
	}
	user, ok := value.(*model.User)
	return user, ok && user != nil
}

func sensitiveFilterEnabled() bool {
	return systemSettingBool("sensitive_filter_enabled", false)
}

func sensitiveFilterScope() string {
	scope := strings.ToLower(strings.TrimSpace(model.GetSystemSetting("sensitive_filter_scope", communityservice.SensitiveFilterScopeRequest)))
	if scope == communityservice.SensitiveFilterScopeRequestResponse {
		return scope
	}
	return communityservice.SensitiveFilterScopeRequest
}

func matchSensitiveWords(text string) (communityservice.SensitiveWordMatch, bool) {
	words := sensitiveWords()
	if len(words) == 0 || strings.TrimSpace(text) == "" {
		return communityservice.SensitiveWordMatch{}, false
	}
	foldedText := strings.ToLower(text)
	for _, word := range words {
		if word == "" {
			continue
		}
		if strings.Contains(foldedText, strings.ToLower(word)) {
			return communityservice.SensitiveWordMatch{Word: word}, true
		}
	}
	return communityservice.SensitiveWordMatch{}, false
}

func sensitiveWords() []string {
	return parseDelimitedList(model.GetSystemSetting("sensitive_words", ""))
}

func validateConfiguredHTTPURL(raw string) error {
	if !ssrfProtectionEnabled() {
		return nil
	}
	return validateOutboundHTTPURL(raw, currentURLGuardOptions())
}

func validateConfiguredTCPAddress(raw string) error {
	if !ssrfProtectionEnabled() {
		return nil
	}
	host, _, err := net.SplitHostPort(raw)
	if err != nil {
		return err
	}
	return validateOutboundHost(host, currentURLGuardOptions())
}

func validateConfiguredStatusTarget(target string, checkType string) error {
	if strings.EqualFold(strings.TrimSpace(checkType), communityservice.StatusCheckTCP) {
		address, err := statusTCPGuardAddress(target)
		if err != nil {
			return err
		}
		return validateConfiguredTCPAddress(address)
	}
	return validateConfiguredHTTPURL(target)
}

func statusTCPGuardAddress(target string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", errors.New("tcp target is required")
	}
	defaultPort := ""
	if parsed, err := url.Parse(target); err == nil && parsed.Host != "" {
		target = parsed.Host
		switch parsed.Scheme {
		case "http":
			defaultPort = "80"
		case "https":
			defaultPort = "443"
		}
	}
	if _, _, err := net.SplitHostPort(target); err == nil {
		return target, nil
	}
	if defaultPort == "" {
		return "", errors.New("tcp target must include a port")
	}
	return net.JoinHostPort(target, defaultPort), nil
}

func validateOutboundHTTPURL(raw string, options communityservice.URLGuardOptions) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("invalid URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("URL must use http or https")
	}
	return validateOutboundHost(parsed.Hostname(), options)
}

func currentURLGuardOptions() communityservice.URLGuardOptions {
	return communityservice.URLGuardOptions{
		AllowPrivateNetworks: systemSettingBool("ssrf_allow_private_networks", false),
		AllowedHosts:         parseDelimitedList(model.GetSystemSetting("ssrf_allowed_hosts", "")),
		Resolve:              true,
	}
}

func ssrfProtectionEnabled() bool {
	return systemSettingBool("ssrf_protection_enabled", true)
}

func validateOutboundHost(host string, options communityservice.URLGuardOptions) error {
	host = normalizeGuardHost(host)
	if host == "" {
		return errors.New("host is required")
	}
	if options.AllowPrivateNetworks || hostAllowed(host, options.AllowedHosts) {
		return nil
	}
	if blockedHostname(host) {
		return communityservice.ErrUnsafeURL
	}
	if ip := net.ParseIP(host); ip != nil {
		if unsafeIP(ip) {
			return communityservice.ErrUnsafeURL
		}
		return nil
	}
	if !options.Resolve {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return err
	}
	if len(ips) == 0 {
		return errors.New("host did not resolve")
	}
	for _, resolved := range ips {
		if unsafeIP(resolved.IP) {
			return communityservice.ErrUnsafeURL
		}
	}
	return nil
}

func normalizeGuardHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	host = strings.TrimSuffix(host, ".")
	if unescaped, err := url.QueryUnescape(host); err == nil {
		host = unescaped
	}
	return strings.Trim(host, "[]")
}

func hostAllowed(host string, allowedHosts []string) bool {
	host = normalizeGuardHost(host)
	for _, allowed := range allowedHosts {
		allowed = normalizeGuardHost(allowed)
		if allowed == "" {
			continue
		}
		if host == allowed {
			return true
		}
		if strings.HasPrefix(allowed, "*.") && strings.HasSuffix(host, strings.TrimPrefix(allowed, "*")) {
			return true
		}
	}
	return false
}

func blockedHostname(host string) bool {
	return host == "localhost" || strings.HasSuffix(host, ".localhost")
}

func unsafeIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	return ip.IsLoopback() || ip.IsUnspecified() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast()
}

type rateLimitEntry struct {
	windowStart time.Time
	count       int
	lastSeen    time.Time
}

type premiumRateLimiter struct {
	mu      sync.Mutex
	entries map[string]*rateLimitEntry
}

func newRateLimiterMiddleware() gin.HandlerFunc {
	limiter := &premiumRateLimiter{entries: map[string]*rateLimitEntry{}}
	return limiter.middleware()
}

func (limiter *premiumRateLimiter) middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !systemSettingBool("rate_limit_enabled", true) {
			c.Next()
			return
		}

		limit := systemSettingInt("rate_limit_requests_per_minute", 60)
		burst := systemSettingInt("rate_limit_burst", 10)
		if limit <= 0 {
			c.Next()
			return
		}
		if burst < 0 {
			burst = 0
		}
		maxRequests := limit + burst
		key := rateLimitKey(c)
		now := time.Now()

		limiter.mu.Lock()
		limiter.cleanupLocked(now)
		entry := limiter.entries[key]
		if entry == nil || now.Sub(entry.windowStart) >= time.Minute {
			entry = &rateLimitEntry{windowStart: now, lastSeen: now}
			limiter.entries[key] = entry
		}
		entry.count++
		entry.lastSeen = now
		allowed := entry.count <= maxRequests
		retryAfter := int(time.Until(entry.windowStart.Add(time.Minute)).Seconds())
		limiter.mu.Unlock()

		if !allowed {
			if retryAfter < 1 {
				retryAfter = 1
			}
			c.Header("Retry-After", strconv.Itoa(retryAfter))
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "Rate limit exceeded", "retry_after": retryAfter})
			c.Abort()
			return
		}

		c.Next()
	}
}

func (limiter *premiumRateLimiter) cleanupLocked(now time.Time) {
	for key, entry := range limiter.entries {
		if now.Sub(entry.lastSeen) > 5*time.Minute {
			delete(limiter.entries, key)
		}
	}
}

func rateLimitKey(c *gin.Context) string {
	if value, exists := c.Get("api_key"); exists {
		if apiKey, ok := value.(*model.APIKey); ok && apiKey != nil && apiKey.ID != 0 {
			return "api_key:" + strconv.FormatUint(uint64(apiKey.ID), 10)
		}
	}
	if value, exists := c.Get("user"); exists {
		if user, ok := value.(*model.User); ok && user != nil && user.ID != 0 {
			return "user:" + strconv.FormatUint(uint64(user.ID), 10)
		}
	}
	return "ip:" + c.ClientIP()
}

func systemSettingInt(key string, fallback int) int {
	value, err := strconv.Atoi(model.GetSystemSetting(key, strconv.Itoa(fallback)))
	if err != nil {
		return fallback
	}
	return value
}

func systemSettingBool(key string, fallback bool) bool {
	value, err := strconv.ParseBool(model.GetSystemSetting(key, strconv.FormatBool(fallback)))
	if err != nil {
		return fallback
	}
	return value
}

func parseDelimitedList(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '，' || r == '\n' || r == '\r' || r == ';' || r == '；' || unicode.IsSpace(r) && r != ' '
	})
	items := make([]string, 0, len(fields))
	seen := map[string]struct{}{}
	for _, field := range fields {
		item := strings.TrimSpace(field)
		if item == "" {
			continue
		}
		key := strings.ToLower(item)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		items = append(items, item)
	}
	return items
}
