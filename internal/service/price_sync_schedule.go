package service

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/robfig/cron/v3"
)

const DefaultPriceSyncCron = "0 * * * *"

// NormalizePriceSyncCron uses the hourly schedule when a channel has no
// explicit schedule. This preserves the previous automatic-sync behavior.
func NormalizePriceSyncCron(value string) string {
	if value = strings.TrimSpace(value); value == "" {
		return DefaultPriceSyncCron
	}
	return value
}

// ValidatePriceSyncCron accepts standard five-field cron expressions and the
// descriptors supported by robfig/cron, such as @hourly.
func ValidatePriceSyncCron(value string) error {
	if _, err := cron.ParseStandard(NormalizePriceSyncCron(value)); err != nil {
		return fmt.Errorf("invalid price sync cron: %w", err)
	}
	return nil
}

func (s *SyncService) SyncScheduledPrices() []ChannelSyncResult {
	var channels []model.Channel
	if err := model.DB.Where("enabled = ? AND price_sync_enabled = ?", true, true).Find(&channels).Error; err != nil {
		log.Printf("Failed to load scheduled price sync channels: %v", err)
		return []ChannelSyncResult{{Error: err.Error()}}
	}

	now := time.Now()
	results := make([]ChannelSyncResult, 0, len(channels))
	for index := range channels {
		channel := &channels[index]
		if !priceSyncDue(channel, now) {
			continue
		}
		if err := model.DB.Model(channel).Update("price_sync_last_at", now).Error; err != nil {
			log.Printf("Failed to reserve scheduled price sync for %s: %v", channel.Name, err)
			results = append(results, ChannelSyncResult{ChannelID: channel.ID, ChannelName: channel.Name, Error: err.Error()})
			continue
		}
		log.Printf("Running scheduled price sync for channel: %s", channel.Name)
		results = append(results, s.SyncChannelPrices(channel))
	}
	return results
}

func priceSyncDue(channel *model.Channel, now time.Time) bool {
	schedule, err := cron.ParseStandard(NormalizePriceSyncCron(channel.PriceSyncCron))
	if err != nil {
		log.Printf("Skipping scheduled price sync for %s: invalid cron %q: %v", channel.Name, channel.PriceSyncCron, err)
		return false
	}
	if channel.PriceSyncLastAt == nil {
		return !schedule.Next(channel.CreatedAt).After(now)
	}
	return !schedule.Next(*channel.PriceSyncLastAt).After(now)
}
