// Package ratelimit contains rate-limit primitives shared by gateway and
// server-side chat execution. It intentionally has no dependency on the HTTP
// layer so the same user-channel ceiling applies to both request paths.
package ratelimit

import (
	"sync"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
)

const userChannelWindow = time.Minute

// UserChannelDecision describes the result of consuming one user-channel
// request slot.
type UserChannelDecision struct {
	Allowed    bool
	Limit      int
	Remaining  int
	RetryAfter time.Duration
}

type userChannelEntry struct {
	windowStart time.Time
	count       int
	lastSeen    time.Time
}

type userChannelKey struct {
	userID    uint
	channelID uint
}

// UserChannelLimiter maintains independent fixed windows for every
// user-and-logical-channel pair. Rate-limit configuration is read from the
// channel passed to Allow so edits take effect on the following request
// without a restart.
type UserChannelLimiter struct {
	mu      sync.Mutex
	entries map[userChannelKey]*userChannelEntry
	now     func() time.Time
}

func NewUserChannelLimiter() *UserChannelLimiter {
	return &UserChannelLimiter{
		entries: make(map[userChannelKey]*userChannelEntry),
		now:     time.Now,
	}
}

// Allow consumes a request slot for one user through one channel. A disabled
// or invalid ceiling is treated as unlimited, which keeps legacy channels
// backward compatible.
func (limiter *UserChannelLimiter) Allow(userID uint, channel *model.UserChannel) UserChannelDecision {
	if userID == 0 || channel == nil || channel.ID == 0 || !channel.RateLimitEnabled || channel.RateLimitRequestsPerMinute <= 0 {
		return UserChannelDecision{Allowed: true}
	}

	limit := channel.RateLimitRequestsPerMinute
	burst := channel.RateLimitBurst
	if burst < 0 {
		burst = 0
	}
	capacity := limit + burst
	if capacity <= 0 {
		return UserChannelDecision{Allowed: true}
	}

	now := limiter.now()
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	limiter.cleanupLocked(now)
	key := userChannelKey{userID: userID, channelID: channel.ID}
	entry := limiter.entries[key]
	if entry == nil || now.Sub(entry.windowStart) >= userChannelWindow {
		entry = &userChannelEntry{windowStart: now}
		limiter.entries[key] = entry
	}
	entry.lastSeen = now
	if entry.count >= capacity {
		return UserChannelDecision{
			Allowed:    false,
			Limit:      capacity,
			Remaining:  0,
			RetryAfter: remainingWindow(now, entry.windowStart),
		}
	}
	entry.count++
	return UserChannelDecision{
		Allowed:   true,
		Limit:     capacity,
		Remaining: capacity - entry.count,
	}
}

func (limiter *UserChannelLimiter) cleanupLocked(now time.Time) {
	for key, entry := range limiter.entries {
		if now.Sub(entry.lastSeen) > userChannelWindow*2 {
			delete(limiter.entries, key)
		}
	}
}

func remainingWindow(now, windowStart time.Time) time.Duration {
	remaining := userChannelWindow - now.Sub(windowStart)
	if remaining < time.Second {
		return time.Second
	}
	return remaining
}

var defaultUserChannelLimiter = NewUserChannelLimiter()

// AllowUserChannel uses the process-wide limiter used by all request paths.
func AllowUserChannel(userID uint, channel *model.UserChannel) UserChannelDecision {
	return defaultUserChannelLimiter.Allow(userID, channel)
}
