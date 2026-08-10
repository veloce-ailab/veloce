package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
)

type paginatedResponse struct {
	Items    interface{} `json:"items"`
	Total    int64       `json:"total"`
	Page     int         `json:"page"`
	PageSize int         `json:"page_size"`
}

func wantsPaginatedResponse(c *gin.Context) bool {
	for _, key := range []string{"page", "page_size", "start_time", "end_time", "start_date", "end_date", "api_key_id", "model_name", "user_channel_id", "channel_id"} {
		if strings.TrimSpace(c.Query(key)) != "" {
			return true
		}
	}
	for key := range c.Request.URL.Query() {
		if key == "sort_by" || key == "sort_order" || strings.HasPrefix(key, "filter_") {
			return true
		}
	}
	return false
}

// applyTableQuery only accepts columns supplied by the caller's whitelist.
// This keeps table sorting and filtering from ever becoming raw SQL input.
func applyTableQuery(query *gorm.DB, c *gin.Context, columns map[string]string, defaultOrder string) *gorm.DB {
	for key, values := range tableFilterMap(c, columns) {
		if column := columns[key]; len(values) > 0 {
			query = query.Where(column+" IN ?", values)
		}
	}
	order := defaultOrder
	if sortBy, sortDesc := tableSort(c, columns); sortBy != "" {
		column := columns[sortBy]
		direction := "ASC"
		if sortDesc {
			direction = "DESC"
		}
		order = column + " " + direction
	}
	return query.Order(order)
}

func tableSort(c *gin.Context, columns map[string]string) (string, bool) {
	key := strings.TrimSpace(c.Query("sort_by"))
	if _, ok := columns[key]; !ok {
		return "", false
	}
	return key, strings.EqualFold(strings.TrimSpace(c.Query("sort_order")), "desc")
}

func tableFilterMap(c *gin.Context, columns map[string]string) map[string][]string {
	filters := make(map[string][]string)
	for key := range columns {
		if values := tableFilterValues(c, key); len(values) > 0 {
			filters[key] = values
		}
	}
	return filters
}

func tableFilterValues(c *gin.Context, key string) []string {
	raw := strings.TrimSpace(c.Query("filter_" + key))
	if raw == "" {
		return nil
	}
	values := make([]string, 0)
	for _, value := range strings.Split(raw, ",") {
		if value = strings.TrimSpace(value); value != "" && len(values) < 50 {
			values = append(values, value)
		}
	}
	return values
}

func parsePagination(c *gin.Context) (int, int) {
	page := positiveIntQuery(c, "page", 1)
	pageSize := positiveIntQuery(c, "page_size", defaultPageSize)
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	return page, pageSize
}

func positiveIntQuery(c *gin.Context, key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(c.Query(key)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func applyCreatedAtRange(query *gorm.DB, c *gin.Context, column string) (*gorm.DB, error) {
	if startRaw := firstNonEmptyString(c.Query("start_time"), c.Query("start_date")); strings.TrimSpace(startRaw) != "" {
		start, _, err := parseTimeBoundary(startRaw, false)
		if err != nil {
			return query, err
		}
		query = query.Where(column+" >= ?", start)
	}
	if endRaw := firstNonEmptyString(c.Query("end_time"), c.Query("end_date")); strings.TrimSpace(endRaw) != "" {
		end, exclusive, err := parseTimeBoundary(endRaw, true)
		if err != nil {
			return query, err
		}
		if exclusive {
			query = query.Where(column+" < ?", end)
		} else {
			query = query.Where(column+" <= ?", end)
		}
	}
	return query, nil
}

func applyDateStringRange(query *gorm.DB, c *gin.Context, column string) (*gorm.DB, error) {
	if startRaw := firstNonEmptyString(c.Query("start_date"), c.Query("start_time")); strings.TrimSpace(startRaw) != "" {
		start, err := parseDateString(startRaw)
		if err != nil {
			return query, err
		}
		query = query.Where(column+" >= ?", start)
	}
	if endRaw := firstNonEmptyString(c.Query("end_date"), c.Query("end_time")); strings.TrimSpace(endRaw) != "" {
		end, err := parseDateString(endRaw)
		if err != nil {
			return query, err
		}
		query = query.Where(column+" <= ?", end)
	}
	return query, nil
}

func parseTimeBoundary(raw string, end bool) (time.Time, bool, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return time.Time{}, false, errors.New("empty time")
	}
	if date, err := time.ParseInLocation("2006-01-02", value, time.Local); err == nil {
		if end {
			return date.AddDate(0, 0, 1), true, nil
		}
		return date, false, nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04"} {
		if parsed, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return parsed, false, nil
		}
	}
	return time.Time{}, false, errors.New("invalid time format")
}

func parseDateString(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if len(value) >= len("2006-01-02") {
		value = value[:len("2006-01-02")]
	}
	if _, err := time.ParseInLocation("2006-01-02", value, time.Local); err != nil {
		return "", errors.New("invalid date format")
	}
	return value, nil
}

func writePaginationError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	return true
}
