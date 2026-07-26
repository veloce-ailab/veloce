package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Session tasks let the model plan multi-step work inside one chat session and
// execute it item by item. The plan lives with the session, every tool result
// carries the full list so the streaming UI can mirror progress live.
const (
	sessionTasksPlanToolName   = "tasks_plan"
	sessionTasksUpdateToolName = "tasks_update"
	sessionTasksListToolName   = "tasks_list"

	sessionTaskStatusPending    = "pending"
	sessionTaskStatusInProgress = "in_progress"
	sessionTaskStatusCompleted  = "completed"
	sessionTaskStatusSkipped    = "skipped"

	maxSessionTasksPerPlan   = 50
	maxSessionTaskTitleChars = 200
	maxSessionTaskTextChars  = 2000
)

type AdvancedChatSessionTask struct {
	ID          string     `gorm:"primaryKey;size:80" json:"id"`
	UserID      uint       `gorm:"index:idx_session_task_owner;not null" json:"-"`
	User        model.User `gorm:"foreignKey:UserID" json:"-"`
	SessionID   string     `gorm:"size:80;index:idx_session_task_owner;not null" json:"-"`
	Position    int        `gorm:"not null" json:"position"`
	Title       string     `gorm:"size:200;not null" json:"title"`
	Description string     `gorm:"type:text;not null;default:''" json:"description,omitempty"`
	Status      string     `gorm:"size:20;not null;default:'pending'" json:"status"`
	Note        string     `gorm:"type:text;not null;default:''" json:"note,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

func init() {
	RegisterAdvancedChatRuntimeExtensionHook(sessionTasksRuntimeExtension)
	RegisterAdvancedChatToolHandler(sessionTasksPlanToolName, handleSessionTasksPlan)
	RegisterAdvancedChatToolHandler(sessionTasksUpdateToolName, handleSessionTasksUpdate)
	RegisterAdvancedChatToolHandler(sessionTasksListToolName, handleSessionTasksList)
}

func sessionTasksRuntimeExtension(_ context.Context, input AdvancedChatRuntimeContext) (AdvancedChatRuntimeExtension, error) {
	if strings.TrimSpace(input.SessionID) == "" || advancedChatToolGroupDisabled(input.DisabledToolGroups, advancedChatToolGroupTasks) {
		return AdvancedChatRuntimeExtension{}, nil
	}
	return AdvancedChatRuntimeExtension{
		SystemPrompt: "Task planning: for any request that needs three or more distinct steps, call tasks_plan first to record the plan, then execute tasks one at a time. Before starting a task mark it in_progress with tasks_update; mark it completed as soon as it is done, then move to the next. Keep exactly one task in_progress at a time. Skip planning for trivial or single-step requests.",
		Tools: []ChatExecutorTool{
			{
				Name:        sessionTasksPlanToolName,
				Description: "Create or replace the task plan for this session. Provide the full ordered list of tasks; any previous plan is replaced.",
				Schema: map[string]interface{}{
					"type":     "object",
					"required": []string{"tasks"},
					"properties": map[string]interface{}{
						"tasks": map[string]interface{}{
							"type":        "array",
							"description": "Ordered task list, first task to execute first.",
							"items": map[string]interface{}{
								"type":     "object",
								"required": []string{"title"},
								"properties": map[string]interface{}{
									"title":       map[string]interface{}{"type": "string", "description": "Short imperative task title."},
									"description": map[string]interface{}{"type": "string", "description": "Optional detail about what this task covers."},
								},
							},
						},
					},
				},
			},
			{
				Name:        sessionTasksUpdateToolName,
				Description: "Update one task's status while executing the plan. Mark a task in_progress before working on it and completed right after finishing it.",
				Schema: map[string]interface{}{
					"type":     "object",
					"required": []string{"id", "status"},
					"properties": map[string]interface{}{
						"id":     map[string]interface{}{"type": "string", "description": "Task id from tasks_plan or tasks_list."},
						"status": map[string]interface{}{"type": "string", "enum": []string{sessionTaskStatusPending, sessionTaskStatusInProgress, sessionTaskStatusCompleted, sessionTaskStatusSkipped}},
						"note":   map[string]interface{}{"type": "string", "description": "Optional short result or reason (for example why a task was skipped)."},
					},
				},
			},
			{
				Name:        sessionTasksListToolName,
				Description: "Read the session's current task plan and statuses.",
				Schema:      map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
			},
		},
	}, nil
}

func handleSessionTasksPlan(_ context.Context, input AdvancedChatToolCallInput) (string, error) {
	sessionID := strings.TrimSpace(input.SessionID)
	if sessionID == "" {
		return "", errors.New("task planning requires a persisted session")
	}
	rawTasks, ok := input.Arguments["tasks"].([]interface{})
	if !ok || len(rawTasks) == 0 {
		return "", errors.New("tasks must be a non-empty array")
	}
	if len(rawTasks) > maxSessionTasksPerPlan {
		return "", errors.New("too many tasks in one plan")
	}
	now := time.Now().UTC()
	tasks := make([]AdvancedChatSessionTask, 0, len(rawTasks))
	for index, rawTask := range rawTasks {
		item, ok := rawTask.(map[string]interface{})
		if !ok {
			return "", errors.New("each task must be an object with a title")
		}
		title := truncateSessionTaskText(stringFromMap(item, "title"), maxSessionTaskTitleChars)
		if title == "" {
			return "", errors.New("each task needs a non-empty title")
		}
		tasks = append(tasks, AdvancedChatSessionTask{
			ID:          newAdvancedChatID("task"),
			UserID:      input.UserID,
			SessionID:   sessionID,
			Position:    index + 1,
			Title:       title,
			Description: truncateSessionTaskText(stringFromMap(item, "description"), maxSessionTaskTextChars),
			Status:      sessionTaskStatusPending,
			CreatedAt:   now,
			UpdatedAt:   now,
		})
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ? AND session_id = ?", input.UserID, sessionID).Delete(&AdvancedChatSessionTask{}).Error; err != nil {
			return err
		}
		return tx.Create(&tasks).Error
	}); err != nil {
		return "", err
	}
	return sessionTasksToolResult(input.UserID, sessionID)
}

func handleSessionTasksUpdate(_ context.Context, input AdvancedChatToolCallInput) (string, error) {
	sessionID := strings.TrimSpace(input.SessionID)
	if sessionID == "" {
		return "", errors.New("task updates require a persisted session")
	}
	taskID := strings.TrimSpace(stringFromMap(input.Arguments, "id"))
	status := normalizeSessionTaskStatus(stringFromMap(input.Arguments, "status"))
	if taskID == "" || status == "" {
		return "", errors.New("id and a valid status are required")
	}
	updates := map[string]interface{}{"status": status, "updated_at": time.Now().UTC()}
	if note := truncateSessionTaskText(stringFromMap(input.Arguments, "note"), maxSessionTaskTextChars); note != "" {
		updates["note"] = note
	}
	result := model.DB.Model(&AdvancedChatSessionTask{}).
		Where("id = ? AND user_id = ? AND session_id = ?", taskID, input.UserID, sessionID).
		Updates(updates)
	if result.Error != nil {
		return "", result.Error
	}
	if result.RowsAffected == 0 {
		return "", errors.New("task not found in this session")
	}
	return sessionTasksToolResult(input.UserID, sessionID)
}

func handleSessionTasksList(_ context.Context, input AdvancedChatToolCallInput) (string, error) {
	sessionID := strings.TrimSpace(input.SessionID)
	if sessionID == "" {
		return "", errors.New("task listing requires a persisted session")
	}
	return sessionTasksToolResult(input.UserID, sessionID)
}

func listSessionTasks(userID uint, sessionID string) ([]AdvancedChatSessionTask, error) {
	tasks := []AdvancedChatSessionTask{}
	err := model.DB.Where("user_id = ? AND session_id = ?", userID, sessionID).
		Order("position ASC, created_at ASC").
		Find(&tasks).Error
	return tasks, err
}

// sessionTasksToolResult returns the whole plan as JSON: the model reads it as
// the tool result and the streaming frontend parses the same payload to render
// live progress.
func sessionTasksToolResult(userID uint, sessionID string) (string, error) {
	tasks, err := listSessionTasks(userID, sessionID)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(gin.H{"tasks": tasks})
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func normalizeSessionTaskStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case sessionTaskStatusPending:
		return sessionTaskStatusPending
	case sessionTaskStatusInProgress:
		return sessionTaskStatusInProgress
	case sessionTaskStatusCompleted:
		return sessionTaskStatusCompleted
	case sessionTaskStatusSkipped:
		return sessionTaskStatusSkipped
	default:
		return ""
	}
}

func truncateSessionTaskText(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func (api *advancedChatAPI) listSessionTasks(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	sessionID := strings.TrimSpace(c.Param("id"))
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Session id is required"})
		return
	}
	tasks, err := listSessionTasks(user.ID, sessionID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list session tasks"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"tasks": tasks})
}
