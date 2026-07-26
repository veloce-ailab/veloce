package service

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"gorm.io/gorm"
)

const (
	reliabilityDefaultDisableAfterFailures = 3
	reliabilityDefaultDetectInterval       = 300
	reliabilityDefaultDetectTimeout        = 10
	reliabilityDefaultRecoveryAfter        = 1800
)

type ReliabilityService struct {
	client *http.Client
}

func NewReliabilityService() *ReliabilityService {
	return &ReliabilityService{
		client: &http.Client{},
	}
}

func (s *ReliabilityService) Start() {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			s.RunDueChecks(context.Background())
			<-ticker.C
		}
	}()
}

func (s *ReliabilityService) RunDueChecks(ctx context.Context) {
	// Only the primary probes upstreams and flips channel availability.
	if !IsPrimaryNode() {
		return
	}
	recoverAutoDisabledChannels()

	if !ReliabilityAutoDetectUpstreamEnabled() {
		return
	}

	interval := time.Duration(reliabilityDetectIntervalSeconds()) * time.Second
	cutoff := time.Now().Add(-interval)

	var channels []model.Channel
	if err := model.DB.
		Where("enabled = ? AND (last_health_checked_at IS NULL OR last_health_checked_at <= ?)", true, cutoff).
		Order("id ASC").
		Find(&channels).Error; err != nil {
		log.Printf("reliability upstream detection scan failed: %v", err)
		return
	}

	for index := range channels {
		channel := &channels[index]
		checkCtx, cancel := context.WithTimeout(ctx, time.Duration(reliabilityDetectTimeoutSeconds())*time.Second)
		s.checkChannel(checkCtx, channel)
		cancel()
	}
}

func (s *ReliabilityService) checkChannel(ctx context.Context, channel *model.Channel) {
	if channel == nil || channel.ID == 0 {
		return
	}
	healthURL := reliabilityHealthURL(channel)
	if err := ValidateConfiguredHTTPURL(healthURL); err != nil {
		RecordChannelFailure(channel.ID, "health check URL blocked: "+err.Error())
		updateChannelHealthStatus(channel.ID, "down")
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		RecordChannelFailure(channel.ID, "health check request failed: "+err.Error())
		updateChannelHealthStatus(channel.ID, "down")
		return
	}
	req.Header.Set("User-Agent", "WindyPear-Reliability/1.0")
	for key, values := range providerHeadersFromOriginal(channel, channelProtocol(channel.Type), nil) {
		req.Header[key] = values
	}

	resp, err := s.client.Do(req)
	if err != nil {
		RecordChannelFailure(channel.ID, "health check failed: "+err.Error())
		updateChannelHealthStatus(channel.ID, "down")
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))

	if shouldCountUpstreamFailure(resp.StatusCode) {
		RecordChannelFailure(channel.ID, fmt.Sprintf("health check returned HTTP %d", resp.StatusCode))
		updateChannelHealthStatus(channel.ID, "down")
		return
	}
	RecordChannelSuccess(channel.ID)
	updateChannelHealthStatus(channel.ID, "up")
}

func ReliabilityAutoDisableEnabled() bool {
	return settingBool("reliability_auto_disable_enabled", false)
}

func ReliabilityAutoDetectUpstreamEnabled() bool {
	return settingBool("reliability_auto_detect_upstream_enabled", false)
}

func reliabilityAutoRecoverEnabled() bool {
	return settingBool("reliability_auto_recover_enabled", false)
}

func reliabilityDisableAfterFailures() int {
	return reliabilitySettingInt("reliability_disable_after_failures", reliabilityDefaultDisableAfterFailures, 1, 100)
}

func reliabilityDetectIntervalSeconds() int {
	return reliabilitySettingInt("reliability_auto_detect_interval_seconds", reliabilityDefaultDetectInterval, 30, 86400)
}

func reliabilityDetectTimeoutSeconds() int {
	return reliabilitySettingInt("reliability_auto_detect_timeout_seconds", reliabilityDefaultDetectTimeout, 3, 120)
}

func reliabilityRecoveryAfterSeconds() int {
	return reliabilitySettingInt("reliability_recovery_after_seconds", reliabilityDefaultRecoveryAfter, 60, 604800)
}

func RecordChannelSuccess(channelID uint) {
	if channelID == 0 {
		return
	}
	updates := map[string]interface{}{
		"consecutive_failures": 0,
		"last_health_status":   "up",
	}
	if err := model.DB.Model(&model.Channel{}).Where("id = ?", channelID).Updates(updates).Error; err != nil {
		log.Printf("failed to record channel success: channel_id=%d error=%v", channelID, err)
	}
}

func RecordChannelFailure(channelID uint, reason string) {
	if channelID == 0 || !ReliabilityAutoDisableEnabled() {
		return
	}
	reason = truncateReliabilityReason(reason)
	now := time.Now()
	threshold := reliabilityDisableAfterFailures()

	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var channel model.Channel
		if err := tx.First(&channel, channelID).Error; err != nil {
			return err
		}
		failures := channel.ConsecutiveFailures + 1
		updates := map[string]interface{}{
			"consecutive_failures": failures,
			"last_failure_at":      now,
			"last_failure_reason":  reason,
			"last_health_status":   "down",
		}
		if channel.Enabled && failures >= threshold {
			updates["enabled"] = false
			updates["auto_disabled_at"] = now
			updates["auto_disabled_reason"] = reason
		}
		return tx.Model(&model.Channel{}).Where("id = ?", channelID).Updates(updates).Error
	})
	if err != nil {
		log.Printf("failed to record channel failure: channel_id=%d error=%v", channelID, err)
	}
}

func recordUpstreamResult(channel *model.Channel, resp *http.Response, err error) {
	if channel == nil || channel.ID == 0 {
		return
	}
	if err != nil {
		RecordChannelFailure(channel.ID, "request failed: "+err.Error())
		return
	}
	if resp == nil {
		return
	}
	if shouldCountUpstreamFailure(resp.StatusCode) {
		RecordChannelFailure(channel.ID, fmt.Sprintf("upstream returned HTTP %d", resp.StatusCode))
		return
	}
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusBadRequest {
		RecordChannelSuccess(channel.ID)
	}
}

func shouldCountUpstreamFailure(statusCode int) bool {
	return statusCode == http.StatusUnauthorized ||
		statusCode == http.StatusForbidden ||
		statusCode == http.StatusRequestTimeout ||
		statusCode == http.StatusTooManyRequests ||
		statusCode >= http.StatusInternalServerError
}

func recoverAutoDisabledChannels() {
	if !reliabilityAutoRecoverEnabled() {
		return
	}
	cutoff := time.Now().Add(-time.Duration(reliabilityRecoveryAfterSeconds()) * time.Second)
	updates := map[string]interface{}{
		"enabled":                true,
		"consecutive_failures":   0,
		"auto_disabled_at":       nil,
		"auto_disabled_reason":   "",
		"last_health_status":     "pending",
		"last_health_checked_at": nil,
	}
	if err := model.DB.Model(&model.Channel{}).
		Where("enabled = ? AND auto_disabled_at IS NOT NULL AND auto_disabled_at <= ?", false, cutoff).
		Updates(updates).Error; err != nil {
		log.Printf("failed to recover auto-disabled channels: %v", err)
	}
}

func updateChannelHealthStatus(channelID uint, status string) {
	if channelID == 0 {
		return
	}
	now := time.Now()
	updates := map[string]interface{}{
		"last_health_checked_at": now,
		"last_health_status":     strings.TrimSpace(status),
	}
	if err := model.DB.Model(&model.Channel{}).Where("id = ?", channelID).Updates(updates).Error; err != nil {
		log.Printf("failed to update channel health status: channel_id=%d error=%v", channelID, err)
	}
}

func reliabilityHealthURL(channel *model.Channel) string {
	protocol := channelProtocol(channel.Type)
	switch protocol {
	case protocolGemini:
		fullURL := upstreamURLForRequest(channel.BaseURL, "/v1beta/models")
		if strings.TrimSpace(channel.APIKey) != "" {
			fullURL = withQueryParam(fullURL, "key", strings.TrimSpace(channel.APIKey))
		}
		return fullURL
	default:
		return upstreamURLForRequest(channel.BaseURL, "/v1/models")
	}
}

func reliabilitySettingInt(key string, fallback, min, max int) int {
	value, err := strconv.Atoi(strings.TrimSpace(model.GetSystemSetting(key, strconv.Itoa(fallback))))
	if err != nil {
		return fallback
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func truncateReliabilityReason(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 500 {
		return value
	}
	return value[:500]
}
