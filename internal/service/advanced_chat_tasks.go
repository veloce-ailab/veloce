package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	advancedChatTaskScheduleManual   = "manual"
	advancedChatTaskScheduleOnce     = "once"
	advancedChatTaskScheduleInterval = "interval"
	advancedChatTaskSessionExisting  = "existing"
	advancedChatTaskSessionAuto      = "auto"
	advancedChatTaskStatusIdle       = "idle"
	advancedChatTaskStatusQueued     = "queued"
	advancedChatTaskStatusRunning    = "running"
	advancedChatTaskStatusCompleted  = "completed"
	advancedChatTaskStatusFailed     = "failed"
)

type AdvancedChatScheduledTask struct {
	ID                string     `gorm:"primaryKey;size:80" json:"id"`
	UserID            uint       `gorm:"index;not null" json:"user_id"`
	User              model.User `gorm:"foreignKey:UserID" json:"-"`
	Name              string     `gorm:"size:120;not null" json:"name"`
	Description       string     `gorm:"type:text;not null" json:"description"`
	AgentID           string     `gorm:"size:80" json:"agent_id"`
	ScheduleType      string     `gorm:"size:20;not null" json:"schedule_type"`
	RunAt             *time.Time `json:"run_at,omitempty"`
	IntervalSeconds   int        `gorm:"default:0" json:"interval_seconds"`
	SessionMode       string     `gorm:"size:20;not null" json:"session_mode"`
	SessionID         string     `gorm:"size:80" json:"session_id,omitempty"`
	AutoDeleteSession bool       `gorm:"default:false" json:"auto_delete_session"`
	Message           string     `gorm:"type:text;not null" json:"message"`
	TimeoutSeconds    int        `gorm:"default:300" json:"timeout_seconds"`
	DeliveryID        string     `gorm:"size:80" json:"delivery_id,omitempty"`
	ModelName         string     `gorm:"size:100" json:"model_name"`
	UserChannelID     uint       `gorm:"index" json:"user_channel_id"`
	MaxTokens         int        `gorm:"default:0" json:"max_tokens"`
	Temperature       *float64   `json:"temperature,omitempty"`
	ReasoningEffort   string     `gorm:"size:20" json:"reasoning_effort"`
	Enabled           bool       `gorm:"default:true;index" json:"enabled"`
	LastRunAt         *time.Time `json:"last_run_at,omitempty"`
	NextRunAt         *time.Time `gorm:"index" json:"next_run_at,omitempty"`
	LastRunID         string     `gorm:"size:80" json:"last_run_id,omitempty"`
	LastStatus        string     `gorm:"size:20;not null;default:'idle'" json:"last_status"`
	LastError         string     `gorm:"type:text;not null" json:"last_error,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type advancedChatScheduledTaskInput struct {
	Name              string     `json:"name"`
	Description       string     `json:"description"`
	AgentID           string     `json:"agent_id"`
	ScheduleType      string     `json:"schedule_type"`
	RunAt             *time.Time `json:"run_at"`
	IntervalSeconds   int        `json:"interval_seconds"`
	SessionMode       string     `json:"session_mode"`
	SessionID         string     `json:"session_id"`
	AutoDeleteSession bool       `json:"auto_delete_session"`
	Message           string     `json:"message"`
	TimeoutSeconds    int        `json:"timeout_seconds"`
	DeliveryID        string     `json:"delivery_id"`
	ModelName         string     `json:"model_name"`
	UserChannelID     uint       `json:"user_channel_id"`
	MaxTokens         int        `json:"max_tokens"`
	Temperature       *float64   `json:"temperature"`
	ReasoningEffort   string     `json:"reasoning_effort"`
	Enabled           *bool      `json:"enabled"`
}

var advancedChatScheduledTaskSchedulerOnce sync.Once

func startAdvancedChatScheduledTaskScheduler() {
	advancedChatScheduledTaskSchedulerOnce.Do(func() {
		// Only the primary dispatches scheduled tasks; otherwise every node
		// would fire the same task at its due time.
		RegisterScheduledJob(ScheduledJob{
			Name:        "advanced_chat_scheduled_tasks",
			Description: "调度用户创建的 AI 计划任务",
			PrimaryOnly: true,
			Interval:    func() time.Duration { return 30 * time.Second },
			Enabled:     func() bool { return advancedChatScheduledTasksEnabled() },
			Run: func(ctx context.Context) (string, bool, error) {
				dispatched, err := runDueAdvancedChatScheduledTasks(ctx)
				if err != nil {
					return "", true, err
				}
				if dispatched == 0 {
					return "", false, nil
				}
				return fmt.Sprintf("派发 %d 个计划任务", dispatched), true, nil
			},
		})
	})
}

func (api *advancedChatAPI) listScheduledTasks(c *gin.Context) {
	if !advancedChatScheduledTasksEnabled() {
		c.JSON(http.StatusForbidden, gin.H{"error": "Scheduled tasks are disabled"})
		return
	}
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var tasks []AdvancedChatScheduledTask
	if err := model.DB.Where("user_id = ?", user.ID).Order("created_at DESC").Find(&tasks).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list scheduled tasks"})
		return
	}
	c.JSON(http.StatusOK, tasks)
}

func (api *advancedChatAPI) createScheduledTask(c *gin.Context) {
	if !advancedChatScheduledTasksEnabled() {
		c.JSON(http.StatusForbidden, gin.H{"error": "Scheduled tasks are disabled"})
		return
	}
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var input advancedChatScheduledTaskInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	task, ok := advancedChatScheduledTaskFromInput(c, user.ID, input)
	if !ok {
		return
	}
	task.ID = newAdvancedChatID("act")
	task.LastStatus = advancedChatTaskStatusIdle
	if err := model.DB.Create(&task).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create scheduled task"})
		return
	}
	c.JSON(http.StatusOK, task)
}

func (api *advancedChatAPI) updateScheduledTask(c *gin.Context) {
	if !advancedChatScheduledTasksEnabled() {
		c.JSON(http.StatusForbidden, gin.H{"error": "Scheduled tasks are disabled"})
		return
	}
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var task AdvancedChatScheduledTask
	if err := model.DB.Where("id = ? AND user_id = ?", c.Param("id"), user.ID).First(&task).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Scheduled task not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load scheduled task"})
		return
	}
	if task.LastStatus == advancedChatTaskStatusRunning || task.LastStatus == advancedChatTaskStatusQueued {
		c.JSON(http.StatusConflict, gin.H{"error": "Scheduled task is running"})
		return
	}
	var input advancedChatScheduledTaskInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	next, ok := advancedChatScheduledTaskFromInput(c, user.ID, input)
	if !ok {
		return
	}
	if err := model.DB.Model(&task).Updates(map[string]interface{}{
		"name":                next.Name,
		"description":         next.Description,
		"agent_id":            next.AgentID,
		"schedule_type":       next.ScheduleType,
		"run_at":              next.RunAt,
		"interval_seconds":    next.IntervalSeconds,
		"session_mode":        next.SessionMode,
		"session_id":          next.SessionID,
		"auto_delete_session": next.AutoDeleteSession,
		"message":             next.Message,
		"timeout_seconds":     next.TimeoutSeconds,
		"delivery_id":         next.DeliveryID,
		"model_name":          next.ModelName,
		"user_channel_id":     next.UserChannelID,
		"max_tokens":          next.MaxTokens,
		"temperature":         next.Temperature,
		"reasoning_effort":    next.ReasoningEffort,
		"enabled":             next.Enabled,
		"next_run_at":         next.NextRunAt,
		"last_error":          "",
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update scheduled task"})
		return
	}
	_ = model.DB.Where("id = ? AND user_id = ?", task.ID, user.ID).First(&task).Error
	c.JSON(http.StatusOK, task)
}

func (api *advancedChatAPI) deleteScheduledTask(c *gin.Context) {
	if !advancedChatScheduledTasksEnabled() {
		c.JSON(http.StatusForbidden, gin.H{"error": "Scheduled tasks are disabled"})
		return
	}
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var running int64
	if err := model.DB.Model(&AdvancedChatScheduledTask{}).
		Where("id = ? AND user_id = ? AND last_status IN ?", c.Param("id"), user.ID, []string{advancedChatTaskStatusQueued, advancedChatTaskStatusRunning}).
		Count(&running).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check scheduled task"})
		return
	}
	if running > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "Scheduled task is running"})
		return
	}
	if err := model.DB.Where("id = ? AND user_id = ?", c.Param("id"), user.ID).Delete(&AdvancedChatScheduledTask{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete scheduled task"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Scheduled task deleted"})
}

func (api *advancedChatAPI) runScheduledTask(c *gin.Context) {
	if !advancedChatScheduledTasksEnabled() {
		c.JSON(http.StatusForbidden, gin.H{"error": "Scheduled tasks are disabled"})
		return
	}
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	session, run, status, message, err := startAdvancedChatScheduledTaskRun(c.Request.Context(), user.ID, c.Param("id"), false)
	if err != nil {
		c.JSON(status, gin.H{"error": message})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"session": session, "run": run})
}

func advancedChatScheduledTaskFromInput(c *gin.Context, userID uint, input advancedChatScheduledTaskInput) (AdvancedChatScheduledTask, bool) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Scheduled task name is required"})
		return AdvancedChatScheduledTask{}, false
	}
	if len([]rune(name)) > 120 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Scheduled task name is too long"})
		return AdvancedChatScheduledTask{}, false
	}
	description := strings.TrimSpace(input.Description)
	if len([]rune(description)) > 2000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Scheduled task description is too long"})
		return AdvancedChatScheduledTask{}, false
	}
	agentID := strings.TrimSpace(input.AgentID)
	if agentID != "" {
		if _, err := loadAdvancedChatAgent(userID, agentID); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Agent not found"})
			return AdvancedChatScheduledTask{}, false
		}
	}
	scheduleType := normalizeAdvancedChatTaskSchedule(input.ScheduleType)
	if scheduleType == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Schedule type is invalid"})
		return AdvancedChatScheduledTask{}, false
	}
	intervalSeconds := input.IntervalSeconds
	if scheduleType == advancedChatTaskScheduleInterval && intervalSeconds < 60 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Interval must be at least 60 seconds"})
		return AdvancedChatScheduledTask{}, false
	}
	if intervalSeconds > 31536000 {
		intervalSeconds = 31536000
	}
	runAt := input.RunAt
	if scheduleType == advancedChatTaskScheduleOnce && runAt == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Run time is required"})
		return AdvancedChatScheduledTask{}, false
	}
	sessionMode := normalizeAdvancedChatTaskSessionMode(input.SessionMode)
	if sessionMode == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Session mode is invalid"})
		return AdvancedChatScheduledTask{}, false
	}
	sessionID := normalizeAdvancedChatSessionID(input.SessionID)
	if sessionMode == advancedChatTaskSessionExisting {
		if sessionID == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Session is required"})
			return AdvancedChatScheduledTask{}, false
		}
		var session AdvancedChatSession
		if err := model.DB.Where("id = ? AND user_id = ?", sessionID, userID).First(&session).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Session not found"})
			return AdvancedChatScheduledTask{}, false
		}
	}
	message := strings.TrimSpace(input.Message)
	if message == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Message is required"})
		return AdvancedChatScheduledTask{}, false
	}
	if len([]rune(message)) > 20000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Message is too long"})
		return AdvancedChatScheduledTask{}, false
	}
	timeoutSeconds := input.TimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = int(advancedChatCompletionTimeout(advancedChatModeAssistant).Seconds())
	}
	if timeoutSeconds < 30 {
		timeoutSeconds = 30
	}
	if timeoutSeconds > 86400 {
		timeoutSeconds = 86400
	}
	deliveryID := normalizeAdvancedChatSessionID(input.DeliveryID)
	if deliveryID != "" {
		if !advancedChatMessageDeliveryEnabled() {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Message delivery is disabled"})
			return AdvancedChatScheduledTask{}, false
		}
		var delivery AdvancedChatDelivery
		if err := model.DB.Where("id = ? AND user_id = ?", deliveryID, userID).First(&delivery).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Delivery not found"})
			return AdvancedChatScheduledTask{}, false
		}
	}
	modelName := strings.TrimSpace(input.ModelName)
	if len([]rune(modelName)) > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Model name is too long"})
		return AdvancedChatScheduledTask{}, false
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	task := AdvancedChatScheduledTask{
		UserID:            userID,
		Name:              name,
		Description:       description,
		AgentID:           agentID,
		ScheduleType:      scheduleType,
		RunAt:             runAt,
		IntervalSeconds:   intervalSeconds,
		SessionMode:       sessionMode,
		SessionID:         sessionID,
		AutoDeleteSession: sessionMode == advancedChatTaskSessionAuto && input.AutoDeleteSession,
		Message:           message,
		TimeoutSeconds:    timeoutSeconds,
		DeliveryID:        deliveryID,
		ModelName:         modelName,
		UserChannelID:     input.UserChannelID,
		MaxTokens:         normalizeAdvancedChatMaxTokens(input.MaxTokens),
		Temperature:       normalizeAdvancedChatTemperature(input.Temperature),
		ReasoningEffort:   normalizeAdvancedChatReasoningEffort(input.ReasoningEffort),
		Enabled:           enabled,
	}
	task.NextRunAt = nextAdvancedChatTaskRunAt(task, time.Now())
	return task, true
}

func startAdvancedChatScheduledTaskRun(ctx context.Context, userID uint, taskID string, scheduler bool) (advancedChatSessionResponse, advancedChatRunResponse, int, string, error) {
	var task AdvancedChatScheduledTask
	if err := model.DB.Where("id = ? AND user_id = ?", strings.TrimSpace(taskID), userID).First(&task).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return advancedChatSessionResponse{}, advancedChatRunResponse{}, http.StatusNotFound, "Scheduled task not found", err
		}
		return advancedChatSessionResponse{}, advancedChatRunResponse{}, http.StatusInternalServerError, "Failed to load scheduled task", err
	}
	if scheduler && (!task.Enabled || task.NextRunAt == nil || task.NextRunAt.After(time.Now())) {
		return advancedChatSessionResponse{}, advancedChatRunResponse{}, http.StatusConflict, "Scheduled task is not due", errors.New("task not due")
	}
	now := time.Now()
	update := model.DB.Model(&AdvancedChatScheduledTask{}).
		Where("id = ? AND user_id = ? AND last_status NOT IN ?", task.ID, userID, []string{advancedChatTaskStatusQueued, advancedChatTaskStatusRunning}).
		Updates(map[string]interface{}{
			"last_status": advancedChatTaskStatusQueued,
			"last_error":  "",
			"last_run_at": &now,
		})
	if update.Error != nil {
		return advancedChatSessionResponse{}, advancedChatRunResponse{}, http.StatusInternalServerError, "Failed to queue scheduled task", update.Error
	}
	if update.RowsAffected == 0 {
		return advancedChatSessionResponse{}, advancedChatRunResponse{}, http.StatusConflict, "Scheduled task is already running", errors.New("task running")
	}

	prepared, sessionID, delivery, status, message, err := prepareAdvancedChatScheduledTaskRun(ctx, userID, task)
	if err != nil {
		failAdvancedChatScheduledTaskQueue(task.ID, userID, err.Error())
		return advancedChatSessionResponse{}, advancedChatRunResponse{}, status, message, err
	}
	prepared.delivery = delivery
	prepared.timeout = time.Duration(task.TimeoutSeconds) * time.Second
	session, run, status, message, err := createAdvancedChatAssistantRun(userID, prepared)
	if err != nil {
		failAdvancedChatScheduledTaskQueue(task.ID, userID, message)
		return session, run, status, message, err
	}
	_ = model.DB.Model(&AdvancedChatScheduledTask{}).Where("id = ? AND user_id = ?", task.ID, userID).Updates(map[string]interface{}{
		"last_status": advancedChatTaskStatusRunning,
		"last_run_id": run.ID,
	}).Error
	go runAdvancedChatScheduledTaskCompletion(task, run.ID, userID, prepared, sessionID)
	return session, run, http.StatusAccepted, "", nil
}

func prepareAdvancedChatScheduledTaskRun(ctx context.Context, userID uint, task AdvancedChatScheduledTask) (preparedAdvancedChatAssistantRun, string, *AdvancedChatDelivery, int, string, error) {
	input := advancedChatCompletionInput{
		Title:           task.Name,
		Mode:            advancedChatModeAssistant,
		AgentID:         task.AgentID,
		ModelName:       task.ModelName,
		UserChannelID:   task.UserChannelID,
		MaxTokens:       task.MaxTokens,
		Temperature:     task.Temperature,
		ReasoningEffort: task.ReasoningEffort,
		Messages: []advancedChatCompletionMessage{{
			ID:      newAdvancedChatID("acm"),
			Role:    "user",
			Content: task.Message,
			Parts:   normalizeAdvancedChatContentParts(nil, task.Message),
		}},
	}
	sessionID := ""
	if task.SessionMode == advancedChatTaskSessionExisting {
		var session AdvancedChatSession
		if err := model.DB.Where("id = ? AND user_id = ?", task.SessionID, userID).First(&session).Error; err != nil {
			return preparedAdvancedChatAssistantRun{}, "", nil, http.StatusBadRequest, "Session not found", err
		}
		sessionID = session.ID
		input.SessionID = session.ID
		input.Title = session.Title
		input.AgentID = firstNonEmpty(task.AgentID, session.AgentID)
		input.SkillIDs = decodeStringList(session.SkillIDs)
		input.MCPServerIDs = decodeStringList(session.MCPServerIDs)
		input.ConnectorDeviceID = session.ConnectorDeviceID
		input.ConnectorWorkspacePath = session.ConnectorWorkspacePath
		input.ConnectorAutoApprove = session.ConnectorAutoApprove
		input.ConnectorCommandPrefixes = decodeStringList(session.ConnectorCommandPrefixes)
		input.ModelName = firstNonEmpty(task.ModelName, session.ModelName)
		if task.UserChannelID == 0 {
			input.UserChannelID = session.UserChannelID
		}
		if task.MaxTokens == 0 {
			input.MaxTokens = session.MaxTokens
		}
		if task.Temperature == nil {
			input.Temperature = session.Temperature
		}
		if strings.TrimSpace(task.ReasoningEffort) == "" {
			input.ReasoningEffort = session.ReasoningEffort
		}
		messages, err := advancedChatCompletionMessagesFromSession(userID, session.ID)
		if err != nil {
			return preparedAdvancedChatAssistantRun{}, "", nil, http.StatusInternalServerError, "Failed to load session messages", err
		}
		input.Messages = append(messages, input.Messages...)
	} else {
		input.SessionID = newAdvancedChatID("acs")
		sessionID = input.SessionID
	}
	modelName := strings.TrimSpace(input.ModelName)
	if strings.TrimSpace(input.AgentID) != "" {
		if agent, err := loadAdvancedChatAgent(userID, input.AgentID); err == nil && agent != nil {
			if modelName == "" {
				modelName = strings.TrimSpace(agent.DefaultModel)
			}
			if input.UserChannelID == 0 && agent.UserChannelID > 0 {
				input.UserChannelID = agent.UserChannelID
			}
		}
	}
	if modelName == "" {
		return preparedAdvancedChatAssistantRun{}, "", nil, http.StatusBadRequest, "Model is required", errors.New("model is required")
	}
	var delivery *AdvancedChatDelivery
	if task.DeliveryID != "" && advancedChatMessageDeliveryEnabled() {
		var item AdvancedChatDelivery
		if err := model.DB.Where("id = ? AND user_id = ?", task.DeliveryID, userID).First(&item).Error; err != nil {
			return preparedAdvancedChatAssistantRun{}, "", nil, http.StatusBadRequest, "Delivery not found", err
		}
		delivery = &item
	}
	messages := normalizeAdvancedChatCompletionMessages(input.Messages)
	prepared, status, message, err := prepareAdvancedChatAssistantRun(ctx, userID, input, messages, modelName)
	return prepared, sessionID, delivery, status, message, err
}

func runAdvancedChatScheduledTaskCompletion(task AdvancedChatScheduledTask, runID string, userID uint, prepared preparedAdvancedChatAssistantRun, sessionID string) {
	runAdvancedChatAssistantCompletion(runID, userID, prepared)
	var run AdvancedChatRun
	status := advancedChatTaskStatusFailed
	errorMessage := "Failed to load run result"
	if err := model.DB.Where("id = ? AND user_id = ?", runID, userID).First(&run).Error; err == nil {
		if run.Status == advancedChatRunStatusCompleted {
			status = advancedChatTaskStatusCompleted
			errorMessage = ""
		} else if run.ErrorMessage != "" {
			errorMessage = run.ErrorMessage
		} else {
			errorMessage = run.Status
		}
	}
	nextRunAt := nextAdvancedChatTaskRunAtAfterFinish(task, time.Now())
	updates := map[string]interface{}{
		"last_status": status,
		"last_error":  errorMessage,
		"next_run_at": nextRunAt,
	}
	if task.ScheduleType == advancedChatTaskScheduleOnce && status == advancedChatTaskStatusCompleted {
		updates["enabled"] = false
	}
	_ = model.DB.Model(&AdvancedChatScheduledTask{}).Where("id = ? AND user_id = ?", task.ID, userID).Updates(updates).Error
	if task.SessionMode == advancedChatTaskSessionAuto && task.AutoDeleteSession && sessionID != "" {
		_ = deleteAdvancedChatSessionData(userID, sessionID)
	}
}

func runDueAdvancedChatScheduledTasks(ctx context.Context) (int, error) {
	if !advancedChatScheduledTasksEnabled() {
		return 0, nil
	}
	now := time.Now()
	var tasks []AdvancedChatScheduledTask
	if err := model.DB.
		Where("enabled = ? AND next_run_at IS NOT NULL AND next_run_at <= ? AND last_status NOT IN ?", true, now, []string{advancedChatTaskStatusQueued, advancedChatTaskStatusRunning}).
		Order("next_run_at ASC").
		Limit(20).
		Find(&tasks).Error; err != nil {
		return 0, err
	}
	for _, task := range tasks {
		go startAdvancedChatScheduledTaskRun(ctx, task.UserID, task.ID, true)
	}
	return len(tasks), nil
}

func failAdvancedChatScheduledTaskQueue(taskID string, userID uint, message string) {
	if strings.TrimSpace(message) == "" {
		message = "Failed to start scheduled task"
	}
	nextRunAt := nextAdvancedChatTaskRunAtForID(taskID, userID)
	_ = model.DB.Model(&AdvancedChatScheduledTask{}).Where("id = ? AND user_id = ?", taskID, userID).Updates(map[string]interface{}{
		"last_status": advancedChatTaskStatusFailed,
		"last_error":  message,
		"next_run_at": nextRunAt,
	}).Error
}

func nextAdvancedChatTaskRunAtForID(taskID string, userID uint) *time.Time {
	var task AdvancedChatScheduledTask
	if err := model.DB.Where("id = ? AND user_id = ?", taskID, userID).First(&task).Error; err != nil {
		return nil
	}
	return nextAdvancedChatTaskRunAtAfterFinish(task, time.Now())
}

func nextAdvancedChatTaskRunAtAfterFinish(task AdvancedChatScheduledTask, now time.Time) *time.Time {
	if task.ScheduleType == advancedChatTaskScheduleOnce {
		return nil
	}
	return nextAdvancedChatTaskRunAt(task, now)
}

func nextAdvancedChatTaskRunAt(task AdvancedChatScheduledTask, now time.Time) *time.Time {
	if !task.Enabled {
		return nil
	}
	switch task.ScheduleType {
	case advancedChatTaskScheduleOnce:
		if task.RunAt == nil {
			return nil
		}
		next := *task.RunAt
		return &next
	case advancedChatTaskScheduleInterval:
		seconds := task.IntervalSeconds
		if seconds < 60 {
			seconds = 60
		}
		next := now.Add(time.Duration(seconds) * time.Second)
		return &next
	default:
		return nil
	}
}

func normalizeAdvancedChatTaskSchedule(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", advancedChatTaskScheduleManual:
		return advancedChatTaskScheduleManual
	case advancedChatTaskScheduleOnce:
		return advancedChatTaskScheduleOnce
	case advancedChatTaskScheduleInterval:
		return advancedChatTaskScheduleInterval
	default:
		return ""
	}
}

func normalizeAdvancedChatTaskSessionMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case advancedChatTaskSessionExisting:
		return advancedChatTaskSessionExisting
	case "", advancedChatTaskSessionAuto:
		return advancedChatTaskSessionAuto
	default:
		return ""
	}
}

func advancedChatCompletionMessagesFromSession(userID uint, sessionID string) ([]advancedChatCompletionMessage, error) {
	var rows []AdvancedChatMessage
	if err := model.DB.Where("session_id = ? AND user_id = ?", sessionID, userID).Order("sort_order ASC, created_at ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	messages := make([]advancedChatCompletionMessage, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, advancedChatCompletionMessage{
			ID:        row.ID,
			Role:      row.Role,
			Content:   row.Content,
			Parts:     decodeAdvancedChatContentPartsWithFallback(row.ContentParts, row.Content),
			ToolCalls: decodeAdvancedChatToolCalls(row.ToolCalls),
		})
	}
	return messages, nil
}

func deleteAdvancedChatSessionData(userID uint, sessionID string) error {
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var runs []AdvancedChatRun
		if err := tx.Where("session_id = ? AND user_id = ?", sessionID, userID).Find(&runs).Error; err != nil {
			return err
		}
		for _, run := range runs {
			if err := tx.Where("run_id = ? AND user_id = ?", run.ID, userID).Delete(&AdvancedChatRunEvent{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("session_id = ? AND user_id = ?", sessionID, userID).Delete(&AdvancedChatRun{}).Error; err != nil {
			return err
		}
		if err := tx.Where("session_id = ? AND user_id = ?", sessionID, userID).Delete(&AdvancedChatMessage{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ? AND user_id = ?", sessionID, userID).Delete(&AdvancedChatSession{}).Error
	})
}

func stringArgument(value map[string]interface{}, key string) string {
	if value == nil {
		return ""
	}
	item, ok := value[key]
	if !ok {
		return ""
	}
	switch typed := item.(type) {
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}
