package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"gorm.io/gorm"
)

const (
	StatusPending = "pending"
	StatusUp      = "up"
	StatusDown    = "down"

	StatusCheckHTTP = "http"
	StatusCheckTCP  = "tcp"
)

// StatusService runs configured public status checks.
type StatusService struct {
	client *http.Client
}

func NewStatusService() *StatusService {
	return &StatusService{
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (s *StatusService) Start() {
	go func() {
		s.RunDueChecks(context.Background())
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			s.RunDueChecks(context.Background())
		}
	}()
}

func (s *StatusService) RunDueChecks(ctx context.Context) {
	if !systemSettingBool("status_monitor_enabled", false) {
		return
	}

	var monitors []model.StatusMonitor
	if err := model.DB.Where("enabled = ?", true).Find(&monitors).Error; err != nil {
		log.Printf("status monitor scan failed: %v", err)
		return
	}

	now := time.Now()
	for index := range monitors {
		monitor := &monitors[index]
		interval := time.Duration(statusIntervalSeconds(monitor.IntervalSeconds)) * time.Second
		if monitor.LastCheckedAt != nil && now.Sub(*monitor.LastCheckedAt) < interval {
			continue
		}
		checkCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		if _, err := s.CheckMonitor(checkCtx, monitor); err != nil {
			log.Printf("status monitor check failed: monitor_id=%d name=%q error=%v", monitor.ID, monitor.Name, err)
		}
		cancel()
	}
}

func (s *StatusService) CheckMonitorByID(ctx context.Context, id uint) (model.StatusCheck, error) {
	var monitor model.StatusMonitor
	if err := model.DB.First(&monitor, id).Error; err != nil {
		return model.StatusCheck{}, err
	}
	return s.CheckMonitor(ctx, &monitor)
}

func (s *StatusService) CheckMonitor(ctx context.Context, monitor *model.StatusMonitor) (model.StatusCheck, error) {
	if monitor == nil || monitor.ID == 0 {
		return model.StatusCheck{}, errors.New("monitor is required")
	}

	status, latencyMs, statusCode, message := s.performCheck(ctx, monitor)
	checkedAt := time.Now()
	check := model.StatusCheck{
		ID:         model.NextLogID(),
		MonitorID:  monitor.ID,
		Status:     status,
		LatencyMs:  latencyMs,
		StatusCode: statusCode,
		Message:    message,
		CheckedAt:  checkedAt,
	}

	err := model.DB.Transaction(func(tx *gorm.DB) error {
		updates := map[string]interface{}{
			"last_status":      status,
			"last_latency_ms":  latencyMs,
			"last_status_code": statusCode,
			"last_message":     message,
			"last_checked_at":  checkedAt,
		}
		if err := tx.Model(&model.StatusMonitor{}).Where("id = ?", monitor.ID).Updates(updates).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return model.StatusCheck{}, err
	}
	database, err := model.LogDB()
	if err != nil {
		return model.StatusCheck{}, err
	}
	if err := database.Create(&check).Error; err != nil {
		return model.StatusCheck{}, err
	}
	retention := statusRetentionHours(monitor.RetentionHours)
	if err := database.Where("monitor_id = ? AND checked_at < ?", monitor.ID, checkedAt.Add(-time.Duration(retention)*time.Hour)).Delete(&model.StatusCheck{}).Error; err != nil {
		return model.StatusCheck{}, err
	}
	return check, nil
}

func (s *StatusService) performCheck(ctx context.Context, monitor *model.StatusMonitor) (string, int, int, string) {
	start := time.Now()
	switch normalizeStatusCheckType(monitor.CheckType) {
	case StatusCheckTCP:
		statusCode, message, err := checkTCP(ctx, monitor.TargetURL)
		latencyMs := int(time.Since(start).Milliseconds())
		if err != nil {
			return StatusDown, latencyMs, statusCode, truncateStatusMessage(err.Error())
		}
		return StatusUp, latencyMs, statusCode, message
	default:
		statusCode, message, err := s.checkHTTP(ctx, monitor.TargetURL, monitor.Method)
		latencyMs := int(time.Since(start).Milliseconds())
		if err != nil {
			return StatusDown, latencyMs, statusCode, truncateStatusMessage(err.Error())
		}
		if statusCode >= 200 && statusCode < 400 {
			return StatusUp, latencyMs, statusCode, message
		}
		return StatusDown, latencyMs, statusCode, message
	}
}

func (s *StatusService) checkHTTP(ctx context.Context, targetURL string, method string) (int, string, error) {
	targetURL = strings.TrimSpace(targetURL)
	parsed, err := url.Parse(targetURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return 0, "Invalid URL", errors.New("invalid http target")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return 0, "Invalid URL scheme", errors.New("http target must use http or https")
	}
	if err := ValidateConfiguredHTTPURL(targetURL); err != nil {
		return 0, "Unsafe target URL", err
	}

	method = strings.ToUpper(strings.TrimSpace(method))
	if method != http.MethodHead {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, targetURL, nil)
	if err != nil {
		return 0, "Invalid request", err
	}
	req.Header.Set("User-Agent", "WindyPear-Status/1.0")

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, "Request failed", err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	return resp.StatusCode, fmt.Sprintf("HTTP %d", resp.StatusCode), nil
}

func checkTCP(ctx context.Context, target string) (int, string, error) {
	address, err := tcpTargetAddress(target)
	if err != nil {
		return 0, "Invalid TCP target", err
	}
	if err := ValidateConfiguredTCPAddress(address); err != nil {
		return 0, "Unsafe TCP target", err
	}
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return 0, "TCP failed", err
	}
	_ = conn.Close()
	return 0, "TCP connected", nil
}

func tcpTargetAddress(target string) (string, error) {
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

func normalizeStatusCheckType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case StatusCheckTCP:
		return StatusCheckTCP
	default:
		return StatusCheckHTTP
	}
}

func statusIntervalSeconds(value int) int {
	if value < 10 {
		return 10
	}
	if value > 86400 {
		return 86400
	}
	return value
}

func statusRetentionHours(value int) int {
	if value < 1 {
		return 168
	}
	if value > 8760 {
		return 8760
	}
	return value
}

func truncateStatusMessage(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 500 {
		return value
	}
	return value[:500]
}

func systemSettingBool(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(model.GetSystemSetting(key, strconv.FormatBool(fallback))))
	switch value {
	case "1", "true", "yes", "on", "enabled":
		return true
	case "0", "false", "no", "off", "disabled":
		return false
	default:
		return fallback
	}
}
