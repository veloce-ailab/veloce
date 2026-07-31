package api

import (
	"testing"

	"github.com/veloce-ailab/veloce/internal/model"
)

func TestValidateUserChannelRateLimit(t *testing.T) {
	tests := []struct {
		name    string
		channel model.UserChannel
		wantErr bool
	}{
		{name: "disabled is unrestricted", channel: model.UserChannel{}},
		{name: "minimum rate", channel: model.UserChannel{RateLimitEnabled: true, RateLimitRequestsPerMinute: 1}},
		{name: "rate required when enabled", channel: model.UserChannel{RateLimitEnabled: true}, wantErr: true},
		{name: "negative burst rejected", channel: model.UserChannel{RateLimitEnabled: true, RateLimitRequestsPerMinute: 10, RateLimitBurst: -1}, wantErr: true},
		{name: "rate upper bound", channel: model.UserChannel{RateLimitEnabled: true, RateLimitRequestsPerMinute: 1_000_001}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateUserChannelRateLimit(&test.channel)
			if (err != nil) != test.wantErr {
				t.Fatalf("validateUserChannelRateLimit() error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}
