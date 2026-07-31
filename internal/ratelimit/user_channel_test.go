package ratelimit

import (
	"testing"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
)

func TestUserChannelLimiterLimitsEachUserIndependently(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	limiter := NewUserChannelLimiter()
	limiter.now = func() time.Time { return now }
	channel := &model.UserChannel{ID: 7, RateLimitEnabled: true, RateLimitRequestsPerMinute: 2, RateLimitBurst: 1}

	for wantRemaining := 2; wantRemaining >= 0; wantRemaining-- {
		decision := limiter.Allow(101, channel)
		if !decision.Allowed || decision.Limit != 3 || decision.Remaining != wantRemaining {
			t.Fatalf("decision = %+v, want allowed with remaining %d", decision, wantRemaining)
		}
	}
	decision := limiter.Allow(101, channel)
	if decision.Allowed || decision.Remaining != 0 || decision.RetryAfter != time.Minute {
		t.Fatalf("rejected decision = %+v", decision)
	}

	if decision := limiter.Allow(202, channel); !decision.Allowed || decision.Remaining != 2 {
		t.Fatalf("another user should have an independent limit: %+v", decision)
	}
	otherChannel := &model.UserChannel{ID: 8, RateLimitEnabled: true, RateLimitRequestsPerMinute: 2, RateLimitBurst: 1}
	if decision := limiter.Allow(101, otherChannel); !decision.Allowed || decision.Remaining != 2 {
		t.Fatalf("another channel should have an independent limit: %+v", decision)
	}
}

func TestUserChannelLimiterResetsAndSkipsDisabledChannels(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	limiter := NewUserChannelLimiter()
	limiter.now = func() time.Time { return now }
	channel := &model.UserChannel{ID: 9, RateLimitEnabled: true, RateLimitRequestsPerMinute: 1}

	if !limiter.Allow(101, channel).Allowed || limiter.Allow(101, channel).Allowed {
		t.Fatal("one request should be allowed before the channel is limited")
	}
	now = now.Add(time.Minute)
	if decision := limiter.Allow(101, channel); !decision.Allowed || decision.Remaining != 0 {
		t.Fatalf("window should reset: %+v", decision)
	}
	if decision := limiter.Allow(101, &model.UserChannel{ID: 10, RateLimitEnabled: false}); !decision.Allowed || decision.Limit != 0 {
		t.Fatalf("disabled channel should not be limited: %+v", decision)
	}
}
