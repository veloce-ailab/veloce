package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/veloce-ailab/veloce/internal/service"
)

var rateLimiterFactory func() gin.HandlerFunc

type RateLimiter struct {
	handler gin.HandlerFunc
}

func RegisterRateLimiterFactory(factory func() gin.HandlerFunc) {
	rateLimiterFactory = factory
}

func NewRateLimiter() *RateLimiter {
	if rateLimiterFactory != nil {
		return &RateLimiter{handler: rateLimiterFactory()}
	}
	return &RateLimiter{}
}

func (limiter *RateLimiter) Middleware() gin.HandlerFunc {
	if limiter.handler != nil {
		return limiter.handler
	}
	return func(c *gin.Context) {
		c.Next()
	}
}

// SensitiveRateLimitConfig configures an in-process rate limit for a public,
// security-sensitive endpoint. Requests are limited by both source IP and the
// supplied identity field (for example, email or login identifier) so changing
// IP addresses cannot bypass an account-targeted limit.
type SensitiveRateLimitConfig struct {
	// Name separates counters used by different endpoint classes.
	Name string
	// Requests allowed per key in Window. Values less than one disable the limiter.
	Limit int
	// Window is the fixed accounting window. It defaults to one minute.
	Window time.Duration
	// IdentityFields are JSON string fields used as an additional rate-limit key.
	IdentityFields []string
}

type sensitiveRateLimitEntry struct {
	windowStart time.Time
	count       int
	lastSeen    time.Time
}

type sensitiveRateLimiter struct {
	config  SensitiveRateLimitConfig
	mu      sync.Mutex
	entries map[string]*sensitiveRateLimitEntry
}

// NewSensitiveRateLimiter creates a rate limiter intended for login,
// registration, verification-code, and setup endpoints. It deliberately does
// not depend on the configurable gateway rate limiter: these endpoints must be
// protected even when gateway request limiting is turned off.
func NewSensitiveRateLimiter(config SensitiveRateLimitConfig) gin.HandlerFunc {
	if config.Limit < 1 {
		return func(c *gin.Context) { c.Next() }
	}
	if config.Window <= 0 {
		config.Window = time.Minute
	}
	config.Name = strings.TrimSpace(config.Name)
	if config.Name == "" {
		config.Name = "sensitive"
	}
	return (&sensitiveRateLimiter{
		config:  config,
		entries: make(map[string]*sensitiveRateLimitEntry),
	}).middleware()
}

func (limiter *sensitiveRateLimiter) middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		keys := []string{limiter.config.Name + ":ip:" + c.ClientIP()}
		if identity := requestRateLimitIdentity(c, limiter.config.IdentityFields); identity != "" {
			keys = append(keys, limiter.config.Name+":identity:"+identity)
		}

		now := time.Now()
		limiter.mu.Lock()
		limiter.cleanupLocked(now)
		allowed := true
		var retryAfter time.Duration
		for _, key := range keys {
			entry := limiter.entries[key]
			if entry == nil || now.Sub(entry.windowStart) >= limiter.config.Window {
				entry = &sensitiveRateLimitEntry{windowStart: now}
				limiter.entries[key] = entry
			}
			if entry.count >= limiter.config.Limit {
				allowed = false
				remaining := limiter.config.Window - now.Sub(entry.windowStart)
				if remaining > retryAfter {
					retryAfter = remaining
				}
			}
		}
		if allowed {
			for _, key := range keys {
				entry := limiter.entries[key]
				entry.count++
				entry.lastSeen = now
			}
		}
		limiter.mu.Unlock()

		if !allowed {
			seconds := int(retryAfter.Seconds())
			if retryAfter%time.Second != 0 {
				seconds++
			}
			if seconds < 1 {
				seconds = 1
			}
			c.Header("Retry-After", strconvItoa(seconds))
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "Too many requests. Please try again later.", "retry_after": seconds})
			c.Abort()
			return
		}
		c.Next()
	}
}

func (limiter *sensitiveRateLimiter) cleanupLocked(now time.Time) {
	for key, entry := range limiter.entries {
		if now.Sub(entry.lastSeen) > limiter.config.Window*2 {
			delete(limiter.entries, key)
		}
	}
}

func requestRateLimitIdentity(c *gin.Context, fields []string) string {
	if len(fields) == 0 || c.Request.Body == nil {
		return ""
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return ""
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	if len(body) == 0 {
		return ""
	}
	var input map[string]any
	if err := json.Unmarshal(body, &input); err != nil {
		return ""
	}
	for _, field := range fields {
		value, ok := input[field].(string)
		if !ok {
			continue
		}
		value = normalizeRateLimitIdentity(value)
		if value == "" {
			continue
		}
		// Do not retain arbitrary large request values as in-memory map keys.
		if len(value) > 254 {
			value = value[:254]
		}
		return value
	}
	return ""
}

// normalizeRateLimitIdentity collapses the identity to the same canonical form
// the handlers resolve it to. Phone numbers matter here: the handlers strip
// spaces, dashes and a +86/86 prefix, so without the same treatment
// "13800138000", "+8613800138000" and "138-0013-8000" each get their own
// counter and the per-identity limit on SMS codes and phone login is bypassed.
func normalizeRateLimitIdentity(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	if phone, err := service.NormalizePhone(value); err == nil {
		return "phone:" + phone
	}
	return value
}

// Kept local to avoid making integer conversion part of the public API.
func strconvItoa(value int) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	buf := [20]byte{}
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = digits[value%10]
		value /= 10
	}
	return string(buf[i:])
}
