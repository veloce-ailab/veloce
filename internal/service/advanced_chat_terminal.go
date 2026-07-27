package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/veloce-ailab/veloce/internal/model"
)

const (
	advancedChatTerminalTaskWait     = 25 * time.Second
	advancedChatTerminalTaskPoll     = 150 * time.Millisecond
	advancedChatTerminalMaxInputSize = 8 * 1024
	advancedChatTerminalIDMaxLength  = 80
)

type advancedChatTerminalOpenInput struct {
	ConnectorDeviceID      string `json:"connector_device_id"`
	ConnectorWorkspacePath string `json:"connector_workspace_path"`
	Shell                  string `json:"shell"`
}

type advancedChatTerminalInputInput struct {
	ConnectorDeviceID string `json:"connector_device_id"`
	TerminalID        string `json:"terminal_id"`
	Data              string `json:"data"`
}

type advancedChatTerminalCloseInput struct {
	ConnectorDeviceID string `json:"connector_device_id"`
	TerminalID        string `json:"terminal_id"`
}

func registerAdvancedChatTerminalRoutes(group *gin.RouterGroup) {
	api := &advancedChatAPI{}
	group.POST("/advanced-chat/terminal/open", api.openConnectorTerminal)
	group.POST("/advanced-chat/terminal/input", api.writeConnectorTerminal)
	group.GET("/advanced-chat/terminal/output", api.readConnectorTerminal)
	group.POST("/advanced-chat/terminal/close", api.closeConnectorTerminal)
}

func (api *advancedChatAPI) openConnectorTerminal(c *gin.Context) {
	var input advancedChatTerminalOpenInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	user, device, ok := loadAdvancedChatTerminalDevice(c, input.ConnectorDeviceID)
	if !ok {
		return
	}
	shell := strings.ToLower(strings.TrimSpace(input.Shell))
	if shell != "" && !advancedChatTerminalShellAllowed(shell) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Unsupported terminal shell"})
		return
	}
	payload := map[string]interface{}{
		"workspace_path": strings.TrimSpace(input.ConnectorWorkspacePath),
	}
	if shell != "" {
		payload["shell"] = shell
	}
	respondAdvancedChatTerminalTask(c, user.ID, device.ID, "terminal_open", payload)
}

func (api *advancedChatAPI) writeConnectorTerminal(c *gin.Context) {
	var input advancedChatTerminalInputInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	user, device, ok := loadAdvancedChatTerminalDevice(c, input.ConnectorDeviceID)
	if !ok {
		return
	}
	terminalID, ok := advancedChatTerminalID(c, input.TerminalID)
	if !ok {
		return
	}
	if len(input.Data) > advancedChatTerminalMaxInputSize {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Terminal input is too large"})
		return
	}
	respondAdvancedChatTerminalTask(c, user.ID, device.ID, "terminal_input", map[string]interface{}{
		"terminal_id": terminalID,
		"data":        input.Data,
	})
}

func (api *advancedChatAPI) readConnectorTerminal(c *gin.Context) {
	user, device, ok := loadAdvancedChatTerminalDevice(c, c.Query("connector_device_id"))
	if !ok {
		return
	}
	terminalID, ok := advancedChatTerminalID(c, c.Query("terminal_id"))
	if !ok {
		return
	}
	offset, err := strconv.ParseInt(strings.TrimSpace(c.DefaultQuery("offset", "0")), 10, 64)
	if err != nil || offset < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Terminal offset must be a non-negative integer"})
		return
	}
	respondAdvancedChatTerminalTask(c, user.ID, device.ID, "terminal_read", map[string]interface{}{
		"terminal_id": terminalID,
		"offset":      offset,
	})
}

func (api *advancedChatAPI) closeConnectorTerminal(c *gin.Context) {
	var input advancedChatTerminalCloseInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	user, device, ok := loadAdvancedChatTerminalDevice(c, input.ConnectorDeviceID)
	if !ok {
		return
	}
	terminalID, ok := advancedChatTerminalID(c, input.TerminalID)
	if !ok {
		return
	}
	respondAdvancedChatTerminalTask(c, user.ID, device.ID, "terminal_close", map[string]interface{}{
		"terminal_id": terminalID,
	})
}

// loadAdvancedChatTerminalDevice resolves the caller's own online connector and
// enforces the same admin switch that gates connector shell commands.
func loadAdvancedChatTerminalDevice(c *gin.Context, deviceID string) (*model.User, *AdvancedChatConnectorDevice, bool) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return nil, nil, false
	}
	if !advancedChatAssistantConnectorRunCommandEnabled() {
		c.JSON(http.StatusForbidden, gin.H{"error": "Connector commands are disabled by the administrator"})
		return nil, nil, false
	}
	requested := strings.TrimSpace(deviceID)
	if requested == "" {
		requested = strings.TrimSpace(c.Query("connector_device_id"))
	}
	device, _, err := loadAdvancedChatConnectorForRun(user.ID, requested, "")
	if err != nil || device == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "A connected device is required"})
		return nil, nil, false
	}
	return user, device, true
}

func advancedChatTerminalID(c *gin.Context, value string) (string, bool) {
	terminalID := strings.TrimSpace(value)
	if terminalID == "" || len(terminalID) > advancedChatTerminalIDMaxLength {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Terminal id is required"})
		return "", false
	}
	return terminalID, true
}

func advancedChatTerminalShellAllowed(shell string) bool {
	switch shell {
	case "cmd", "powershell", "pwsh", "bash", "zsh", "sh":
		return true
	default:
		return false
	}
}

// respondAdvancedChatTerminalTask dispatches one terminal action to the
// connector and forwards its JSON reply verbatim.
func respondAdvancedChatTerminalTask(c *gin.Context, userID uint, deviceID string, action string, payload map[string]interface{}) {
	result, err := callAdvancedChatTerminalTask(c.Request.Context(), userID, deviceID, action, payload)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	var response map[string]interface{}
	if err := json.Unmarshal([]byte(result), &response); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Invalid connector terminal response"})
		return
	}
	c.JSON(http.StatusOK, response)
}

func callAdvancedChatTerminalTask(ctx context.Context, userID uint, deviceID string, action string, payload map[string]interface{}) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, advancedChatTerminalTaskWait)
	defer cancel()
	task, err := createAdvancedChatRawConnectorTask(userID, "", advancedChatConnectorToolBinding{
		DeviceID: deviceID,
		Action:   action,
	}, payload, false)
	if err != nil {
		return "", errors.New("failed to create connector terminal task")
	}
	return waitAdvancedChatTerminalTask(ctx, task.ID, userID)
}

// waitAdvancedChatTerminalTask polls more aggressively than the shared connector
// wait so keystrokes and output do not lag a full second behind the device.
func waitAdvancedChatTerminalTask(ctx context.Context, taskID string, userID uint) (string, error) {
	ticker := time.NewTicker(advancedChatTerminalTaskPoll)
	defer ticker.Stop()
	for {
		var task AdvancedChatConnectorTask
		if err := model.DB.Where("id = ? AND user_id = ?", taskID, userID).First(&task).Error; err != nil {
			return "", err
		}
		switch task.Status {
		case advancedChatConnectorTaskStatusCompleted:
			return task.Result, nil
		case advancedChatConnectorTaskStatusFailed:
			if strings.TrimSpace(task.ErrorMessage) == "" {
				return task.Result, errors.New("connector terminal task failed")
			}
			return task.Result, errors.New(task.ErrorMessage)
		}
		select {
		case <-ctx.Done():
			now := time.Now()
			_ = model.DB.Model(&AdvancedChatConnectorTask{}).
				Where("id = ? AND user_id = ?", taskID, userID).
				Updates(map[string]interface{}{
					"status":        advancedChatConnectorTaskStatusFailed,
					"error_message": "connector terminal task timed out",
					"finished_at":   &now,
					"updated_at":    now,
				}).Error
			return "", errors.New("connector terminal task timed out")
		case <-ticker.C:
		}
	}
}
