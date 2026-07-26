package service

import (
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
)

const (
	AuditLogTypeAPI    = "api"
	AuditLogTypeLogin  = "login"
	AuditLogTypeAdmin  = "admin"
	AuditLogTypeSystem = "system"
)

type AuditLogInput struct {
	LogType    string
	Action     string
	Resource   string
	UserID     *uint
	APIKeyID   *uint
	Method     string
	Path       string
	Query      string
	StatusCode int
	IPAddress  string
	UserAgent  string
	Message    string
	Metadata   string
	DurationMs int64
}

type LogCleanupService struct{}

func NewLogCleanupService() *LogCleanupService {
	return &LogCleanupService{}
}

func (s *LogCleanupService) Start() {
	go func() {
		s.Run()
		for {
			time.Sleep(time.Duration(logCleanupIntervalHours()) * time.Hour)
			s.Run()
		}
	}()
}

func (s *LogCleanupService) Run() {
	// One node is enough to prune shared log storage.
	if !IsPrimaryNode() {
		return
	}
	retentionDays := logRetentionDays("log_retention_days")
	if retentionDays <= 0 {
		return
	}
	deleted, err := model.CleanupLogsBefore(time.Now().AddDate(0, 0, -retentionDays))
	if err != nil {
		log.Printf("failed to cleanup log databases: %v", err)
		return
	}
	if deleted > 0 {
		RecordAuditLog(AuditLogInput{
			LogType:  AuditLogTypeSystem,
			Action:   "log_cleanup",
			Resource: "log_databases",
			Message:  "cleaned expired logs",
			Metadata: `{"rows":` + strconv.FormatInt(deleted, 10) + `}`,
		})
	}
}

func RecordAuditLog(input AuditLogInput) {
	logType := normalizeAuditLogType(input.LogType)
	if logType == "" {
		logType = AuditLogTypeAPI
	}
	action := truncateAuditValue(firstNonEmptyString(strings.TrimSpace(input.Action), logType), 100)
	record := model.AuditLog{
		ID:         model.NextLogID(),
		LogType:    logType,
		Action:     action,
		Resource:   truncateAuditValue(input.Resource, 255),
		UserID:     input.UserID,
		APIKeyID:   input.APIKeyID,
		Method:     truncateAuditValue(strings.ToUpper(strings.TrimSpace(input.Method)), 12),
		Path:       truncateAuditValue(input.Path, 255),
		Query:      truncateAuditValue(input.Query, 1000),
		StatusCode: input.StatusCode,
		IPAddress:  truncateAuditValue(input.IPAddress, 45),
		UserAgent:  truncateAuditValue(input.UserAgent, 500),
		Message:    truncateAuditValue(input.Message, 1000),
		Metadata:   input.Metadata,
		DurationMs: input.DurationMs,
	}
	database, err := model.LogDB()
	if err != nil {
		log.Printf("failed to open log database: %v", err)
		return
	}
	if err := database.Create(&record).Error; err != nil {
		log.Printf("failed to record audit log: type=%s action=%s error=%v", logType, action, err)
	}
}

func AuditLogTypeForRequest(method, path string) string {
	method = strings.ToUpper(strings.TrimSpace(method))
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, "/auth/") || strings.HasPrefix(path, "/api/setup") {
		return AuditLogTypeLogin
	}
	if isAdminMutation(method, path) {
		return AuditLogTypeAdmin
	}
	return AuditLogTypeAPI
}

func AuditActionForRequest(method, path string, statusCode int) string {
	method = strings.ToUpper(strings.TrimSpace(method))
	if strings.HasPrefix(path, "/auth/") {
		if statusCode >= http.StatusBadRequest {
			return "auth_failed"
		}
		return "auth_request"
	}
	if isAdminMutation(method, path) {
		if statusCode >= http.StatusBadRequest {
			return "admin_change_failed"
		}
		return "admin_change"
	}
	if statusCode >= http.StatusBadRequest {
		return "api_failed"
	}
	return "api_request"
}

func IsAuditablePath(path string) bool {
	return strings.HasPrefix(path, "/api/") ||
		strings.HasPrefix(path, "/auth/") ||
		strings.HasPrefix(path, "/v1/") ||
		strings.HasPrefix(path, "/v1beta/")
}

func isAdminMutation(method, path string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return false
	}
	if path == "/api/settings" {
		return true
	}
	if strings.HasPrefix(path, "/api/user/") || strings.HasPrefix(path, "/api/public/") {
		return false
	}
	return strings.HasPrefix(path, "/api/")
}

func normalizeAuditLogType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case AuditLogTypeAPI, AuditLogTypeLogin, AuditLogTypeAdmin, AuditLogTypeSystem:
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func logRetentionDays(key string) int {
	return auditSettingInt(key, 0, 0, 3650)
}

func logCleanupIntervalHours() int {
	return auditSettingInt("log_retention_cleanup_interval_hours", 24, 1, 168)
}

func auditSettingInt(key string, fallback, min, max int) int {
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

func truncateAuditValue(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return value[:max]
}
