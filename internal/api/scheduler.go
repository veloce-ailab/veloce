package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/veloce-ailab/veloce/internal/service"
)

// SchedulerAPI exposes the unified background-job scheduler to the admin UI:
// job registry with live state, manual triggering, and run history.
type SchedulerAPI struct{}

func (api *SchedulerAPI) List(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"jobs": service.ScheduledJobSnapshots(),
		"node": gin.H{
			"name":    service.CurrentNodeName(),
			"primary": service.IsPrimaryNode(),
		},
	})
}

func (api *SchedulerAPI) Trigger(c *gin.Context) {
	name := strings.TrimSpace(c.Param("name"))
	if err := service.TriggerScheduledJob(name); err != nil {
		status := http.StatusBadRequest
		switch {
		case errors.Is(err, service.ErrScheduledJobNotFound):
			status = http.StatusNotFound
		case errors.Is(err, service.ErrScheduledJobRunning):
			status = http.StatusConflict
		}
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	var userID *uint
	if user, ok := currentUser(c); ok && user != nil {
		userID = &user.ID
	}
	service.RecordAuditLog(service.AuditLogInput{
		LogType:    service.AuditLogTypeAdmin,
		Action:     "scheduled_job_trigger",
		Resource:   "scheduled_job:" + name,
		UserID:     userID,
		Method:     c.Request.Method,
		Path:       c.Request.URL.Path,
		StatusCode: http.StatusAccepted,
		IPAddress:  c.ClientIP(),
		UserAgent:  c.Request.UserAgent(),
	})
	c.JSON(http.StatusAccepted, gin.H{"ok": true})
}

func (api *SchedulerAPI) Runs(c *gin.Context) {
	page, pageSize := parsePagination(c)
	filter := model.ScheduledTaskRunFilter{
		TaskName: strings.TrimSpace(c.Query("task")),
		Status:   strings.TrimSpace(c.Query("status")),
	}
	items, total, err := model.ListScheduledTaskRuns(filter, (page-1)*pageSize, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list scheduled task runs"})
		return
	}
	c.JSON(http.StatusOK, paginatedResponse{Items: items, Total: total, Page: page, PageSize: pageSize})
}
