package service

import (
	"testing"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
)

func TestValidatePriceSyncCron(t *testing.T) {
	if err := ValidatePriceSyncCron("*/15 * * * *"); err != nil {
		t.Fatalf("expected valid cron: %v", err)
	}
	if err := ValidatePriceSyncCron("not-a-cron"); err == nil {
		t.Fatal("expected invalid cron error")
	}
}

func TestPriceSyncDue(t *testing.T) {
	now := time.Date(2026, time.July, 17, 10, 30, 0, 0, time.Local)
	channel := &model.Channel{
		PriceSyncCron: "*/15 * * * *",
		CreatedAt:     time.Date(2026, time.July, 17, 10, 20, 0, 0, time.Local),
	}
	if !priceSyncDue(channel, now) {
		t.Fatal("channel should be due after its first scheduled time")
	}

	lastRun := time.Date(2026, time.July, 17, 10, 20, 0, 0, time.Local)
	channel.PriceSyncLastAt = &lastRun
	if !priceSyncDue(channel, now) {
		t.Fatal("channel should be due at the next scheduled time")
	}

	lastRun = time.Date(2026, time.July, 17, 10, 30, 0, 0, time.Local)
	channel.PriceSyncLastAt = &lastRun
	if priceSyncDue(channel, now) {
		t.Fatal("channel should not run twice for the same scheduled time")
	}
}
