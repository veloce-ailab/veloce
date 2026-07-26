package middleware

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/veloce-ailab/veloce/internal/model"
)

// systemRateLimiter implements the per-identity request ceiling configured under
// rate_limit_* system settings.
type rateLimitEntry struct {
	windowStart time.Time
	count       int
	lastSeen    time.Time
}

type systemRateLimiter struct {
	mu      sync.Mutex
	entries map[string]*rateLimitEntry
}

func newSystemRateLimiterMiddleware() gin.HandlerFunc {
	limiter := &systemRateLimiter{entries: map[string]*rateLimitEntry{}}
	return limiter.middleware()
}

func (limiter *systemRateLimiter) middleware() gin.HandlerFunc {
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

func (limiter *systemRateLimiter) cleanupLocked(now time.Time) {
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
