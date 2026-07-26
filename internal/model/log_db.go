package model

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const (
	LogStorageSingle = "single"
	LogStorageDaily  = "daily"
)

var (
	logDatabasesMu sync.Mutex
	logDatabases   = map[string]*gorm.DB{}
	logRoot        = "log"
	logSequence    atomic.Uint64
)

// InitLogDB initializes the independent SQLite storage used for all append-only
// operational records. It intentionally runs after system settings are ready.
func InitLogDB() error {
	if _, err := LogDB(); err != nil {
		return err
	}
	return ClearLegacyLogs()
}

// LogDB returns the current write database. In daily mode the filename changes
// at midnight without requiring a service restart.
func LogDB() (*gorm.DB, error) {
	return openLogDatabase(logDatabasePath(time.Now()))
}

// LogDatabases returns every managed log database, oldest first. It includes
// both storage layouts so switching mode never hides historical records.
func LogDatabases() ([]*gorm.DB, error) {
	if _, err := LogDB(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(logDirectory())
	if err != nil {
		return nil, fmt.Errorf("read log directory: %w", err)
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		if entry.Name() == "flai-log.db" || (strings.HasPrefix(entry.Name(), "flai-log-") && strings.HasSuffix(entry.Name(), ".db")) {
			paths = append(paths, filepath.Join(logDirectory(), entry.Name()))
		}
	}
	sort.Strings(paths)
	databases := make([]*gorm.DB, 0, len(paths))
	for _, path := range paths {
		database, err := openLogDatabase(path)
		if err != nil {
			return nil, err
		}
		databases = append(databases, database)
	}
	return databases, nil
}

func LogStorageMode() string {
	switch strings.ToLower(strings.TrimSpace(GetSystemSetting("log_storage_mode", LogStorageSingle))) {
	case LogStorageDaily:
		return LogStorageDaily
	default:
		return LogStorageSingle
	}
}

func logDirectory() string {
	return logRoot
}

func logDatabasePath(now time.Time) string {
	if LogStorageMode() == LogStorageDaily {
		return filepath.Join(logDirectory(), "flai-log-"+now.Format("2006-01-02")+".db")
	}
	return filepath.Join(logDirectory(), "flai-log.db")
}

func openLogDatabase(path string) (*gorm.DB, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	logDatabasesMu.Lock()
	defer logDatabasesMu.Unlock()
	if database := logDatabases[path]; database != nil {
		return database, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	logConfig := gormConfig()
	logConfig.DisableForeignKeyConstraintWhenMigrating = true
	database, err := gorm.Open(sqlite.Open(sqliteDSN(path)), logConfig)
	if err != nil {
		return nil, fmt.Errorf("open log database %q: %w", path, err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		return nil, err
	}
	if err := configureDatabaseConnection(sqlDB, true); err != nil {
		return nil, err
	}
	if err := database.AutoMigrate(&TokenLog{}, &AuditLog{}, &PluginLog{}, &StatusCheck{}, &ScheduledTaskRun{}); err != nil {
		return nil, fmt.Errorf("migrate log database %q: %w", path, err)
	}
	logDatabases[path] = database
	return database, nil
}

// NextLogID produces IDs that remain unique even when daily databases start
// with empty tables. This keeps references from the primary database stable.
func NextLogID() uint {
	for {
		current := logSequence.Load()
		if current == 0 {
			seed := uint64(time.Now().UnixNano())
			if logSequence.CompareAndSwap(0, seed) {
				continue
			}
			continue
		}
		return uint(logSequence.Add(1))
	}
}

func ClearLegacyLogs() error {
	if DB == nil {
		return nil
	}
	for _, entry := range []interface{}{&AuditLog{}, &TokenLog{}, &PluginLog{}, &StatusCheck{}, &ScheduledTaskRun{}} {
		if !DB.Migrator().HasTable(entry) {
			continue
		}
		if err := DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(entry).Error; err != nil {
			return fmt.Errorf("clear legacy %s: %w", DB.NamingStrategy.TableName(fmt.Sprintf("%T", entry)), err)
		}
	}
	return nil
}

// DeleteLogs deletes all persisted log records while retaining the managed
// database files and schemas.
func DeleteLogs() (int64, error) {
	databases, err := LogDatabases()
	if err != nil {
		return 0, err
	}
	var deleted int64
	for _, database := range databases {
		for _, entry := range []interface{}{&AuditLog{}, &TokenLog{}, &PluginLog{}, &StatusCheck{}, &ScheduledTaskRun{}} {
			result := database.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(entry)
			if result.Error != nil {
				return deleted, result.Error
			}
			deleted += result.RowsAffected
		}
	}
	return deleted, nil
}

func CleanupLogsBefore(cutoff time.Time) (int64, error) {
	databases, err := LogDatabases()
	if err != nil {
		return 0, err
	}
	var deleted int64
	for _, database := range databases {
		for _, entry := range []struct {
			model interface{}
			field string
		}{
			{&AuditLog{}, "created_at"},
			{&TokenLog{}, "created_at"},
			{&PluginLog{}, "created_at"},
			{&StatusCheck{}, "checked_at"},
			{&ScheduledTaskRun{}, "started_at"},
		} {
			result := database.Where(entry.field+" < ?", cutoff).Delete(entry.model)
			if result.Error != nil {
				return deleted, result.Error
			}
			deleted += result.RowsAffected
		}
	}
	return deleted, nil
}

func DeleteStatusChecksForMonitor(monitorID uint) error {
	databases, err := LogDatabases()
	if err != nil {
		return err
	}
	for _, database := range databases {
		if err := database.Where("monitor_id = ?", monitorID).Delete(&StatusCheck{}).Error; err != nil {
			return err
		}
	}
	return nil
}

func RecentStatusChecks(monitorIDs []uint, limit int) ([]StatusCheck, error) {
	if len(monitorIDs) == 0 || limit <= 0 {
		return nil, nil
	}
	databases, err := LogDatabases()
	if err != nil {
		return nil, err
	}
	checks := make([]StatusCheck, 0, len(monitorIDs)*limit)
	for _, database := range databases {
		var batch []StatusCheck
		if err := database.Where("monitor_id IN ?", monitorIDs).Order("checked_at DESC").Limit(limit * len(monitorIDs)).Find(&batch).Error; err != nil {
			return nil, err
		}
		checks = append(checks, batch...)
	}
	sort.Slice(checks, func(left, right int) bool { return checks[left].CheckedAt.After(checks[right].CheckedAt) })
	return checks, nil
}

// RecordTokenLog persists billing history outside the primary business DB.
func RecordTokenLog(entry TokenLog) error {
	database, err := LogDB()
	if err != nil {
		return err
	}
	if entry.ID == 0 {
		entry.ID = NextLogID()
	}
	return database.Create(&entry).Error
}

type TokenLogFilter struct {
	UserID        *uint
	APIKeyID      *uint
	UserChannelID *uint
	ChannelID     *uint
	ModelName     string
	Since         *time.Time
	Until         *time.Time
}

type TokenLogSummary struct {
	RequestCount      int64
	InputTokens       int64
	OutputTokens      int64
	CachedInputTokens int64
	TotalTokens       int64
	TotalCost         decimal.Decimal
}

func ListTokenLogs(filter TokenLogFilter, offset, limit int) ([]TokenLog, int64, error) {
	databases, err := LogDatabases()
	if err != nil {
		return nil, 0, err
	}
	all := make([]TokenLog, 0)
	var total int64
	perDatabaseLimit := offset + limit
	if perDatabaseLimit <= 0 {
		perDatabaseLimit = 100
	}
	for _, database := range databases {
		query := applyTokenLogFilter(database.Model(&TokenLog{}), filter)
		var count int64
		if err := query.Count(&count).Error; err != nil {
			return nil, 0, err
		}
		total += count
		var batch []TokenLog
		if err := query.Order("created_at DESC").Limit(perDatabaseLimit).Find(&batch).Error; err != nil {
			return nil, 0, err
		}
		all = append(all, batch...)
	}
	sort.Slice(all, func(left, right int) bool { return all[left].CreatedAt.After(all[right].CreatedAt) })
	if offset >= len(all) {
		return []TokenLog{}, total, nil
	}
	end := len(all)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return all[offset:end], total, nil
}

func SummarizeTokenLogs(filter TokenLogFilter) (TokenLogSummary, error) {
	databases, err := LogDatabases()
	if err != nil {
		return TokenLogSummary{}, err
	}
	summary := TokenLogSummary{TotalCost: decimal.Zero}
	for _, database := range databases {
		var partial TokenLogSummary
		if err := applyTokenLogFilter(database.Model(&TokenLog{}), filter).Select(`COUNT(*) AS request_count, COALESCE(SUM(input_tokens), 0) AS input_tokens, COALESCE(SUM(output_tokens), 0) AS output_tokens, COALESCE(SUM(cached_input_tokens), 0) AS cached_input_tokens, COALESCE(SUM(input_tokens + output_tokens), 0) AS total_tokens, COALESCE(SUM(cost), 0) AS total_cost`).Scan(&partial).Error; err != nil {
			return TokenLogSummary{}, err
		}
		summary.RequestCount += partial.RequestCount
		summary.InputTokens += partial.InputTokens
		summary.OutputTokens += partial.OutputTokens
		summary.CachedInputTokens += partial.CachedInputTokens
		summary.TotalTokens += partial.TotalTokens
		summary.TotalCost = summary.TotalCost.Add(partial.TotalCost)
	}
	return summary, nil
}

func applyTokenLogFilter(query *gorm.DB, filter TokenLogFilter) *gorm.DB {
	if filter.UserID != nil {
		query = query.Where("user_id = ?", *filter.UserID)
	}
	if filter.APIKeyID != nil {
		query = query.Where("api_key_id = ?", *filter.APIKeyID)
	}
	if filter.UserChannelID != nil {
		query = query.Where("user_channel_id = ?", *filter.UserChannelID)
	}
	if filter.ChannelID != nil {
		query = query.Where("channel_id = ?", *filter.ChannelID)
	}
	if filter.ModelName != "" {
		query = query.Where("LOWER(model_name) LIKE ?", "%"+strings.ToLower(filter.ModelName)+"%")
	}
	if filter.Since != nil {
		query = query.Where("created_at >= ?", *filter.Since)
	}
	if filter.Until != nil {
		query = query.Where("created_at <= ?", *filter.Until)
	}
	return query
}

type ScheduledTaskRunFilter struct {
	TaskName string
	Status   string
}

// ListScheduledTaskRuns pages through scheduler run history across every log
// database file, newest first.
func ListScheduledTaskRuns(filter ScheduledTaskRunFilter, offset, limit int) ([]ScheduledTaskRun, int64, error) {
	databases, err := LogDatabases()
	if err != nil {
		return nil, 0, err
	}
	all := make([]ScheduledTaskRun, 0)
	var total int64
	perDatabaseLimit := offset + limit
	if perDatabaseLimit <= 0 {
		perDatabaseLimit = 100
	}
	for _, database := range databases {
		query := database.Model(&ScheduledTaskRun{})
		if filter.TaskName != "" {
			query = query.Where("task_name = ?", filter.TaskName)
		}
		if filter.Status != "" {
			query = query.Where("status = ?", strings.ToLower(filter.Status))
		}
		var count int64
		if err := query.Count(&count).Error; err != nil {
			return nil, 0, err
		}
		total += count
		var batch []ScheduledTaskRun
		if err := query.Order("started_at DESC").Limit(perDatabaseLimit).Find(&batch).Error; err != nil {
			return nil, 0, err
		}
		all = append(all, batch...)
	}
	sort.Slice(all, func(left, right int) bool { return all[left].StartedAt.After(all[right].StartedAt) })
	if offset >= len(all) {
		return []ScheduledTaskRun{}, total, nil
	}
	end := len(all)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return all[offset:end], total, nil
}

type AuditLogFilter struct {
	LogType    string
	Action     string
	Path       string
	UserID     *uint
	StatusCode int
	Since      *time.Time
	Until      *time.Time
}

func ListAuditLogs(filter AuditLogFilter, offset, limit int) ([]AuditLog, int64, error) {
	databases, err := LogDatabases()
	if err != nil {
		return nil, 0, err
	}
	all := make([]AuditLog, 0)
	var total int64
	perDatabaseLimit := offset + limit
	if perDatabaseLimit <= 0 {
		perDatabaseLimit = 100
	}
	for _, database := range databases {
		query := applyAuditLogFilter(database.Model(&AuditLog{}), filter)
		var count int64
		if err := query.Count(&count).Error; err != nil {
			return nil, 0, err
		}
		total += count
		var batch []AuditLog
		if err := query.Order("created_at DESC").Limit(perDatabaseLimit).Find(&batch).Error; err != nil {
			return nil, 0, err
		}
		all = append(all, batch...)
	}
	sort.Slice(all, func(left, right int) bool { return all[left].CreatedAt.After(all[right].CreatedAt) })
	if offset >= len(all) {
		return []AuditLog{}, total, nil
	}
	end := len(all)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	return all[offset:end], total, nil
}

func applyAuditLogFilter(query *gorm.DB, filter AuditLogFilter) *gorm.DB {
	if filter.LogType != "" {
		query = query.Where("log_type = ?", strings.ToLower(filter.LogType))
	}
	if filter.Action != "" {
		query = query.Where("LOWER(action) LIKE ?", "%"+strings.ToLower(filter.Action)+"%")
	}
	if filter.Path != "" {
		query = query.Where("LOWER(path) LIKE ?", "%"+strings.ToLower(filter.Path)+"%")
	}
	if filter.UserID != nil {
		query = query.Where("user_id = ?", *filter.UserID)
	}
	if filter.StatusCode > 0 {
		query = query.Where("status_code = ?", filter.StatusCode)
	}
	if filter.Since != nil {
		query = query.Where("created_at >= ?", *filter.Since)
	}
	if filter.Until != nil {
		query = query.Where("created_at <= ?", *filter.Until)
	}
	return query
}
