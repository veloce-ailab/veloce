package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/veloce-ailab/veloce/internal/model"
	"gorm.io/gorm"
)

// fakeTerminalConnector stands in for the device-side agent: it claims queued
// terminal tasks the same way app/tasks.go does and answers with canned JSON.
type fakeTerminalConnector struct {
	stop     chan struct{}
	done     chan struct{}
	replies  map[string]string
	failWith string
}

func startFakeTerminalConnector(userID uint, deviceID string, replies map[string]string, failWith string) *fakeTerminalConnector {
	connector := &fakeTerminalConnector{
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		replies:  replies,
		failWith: failWith,
	}
	go func() {
		defer close(connector.done)
		for {
			select {
			case <-connector.stop:
				return
			case <-time.After(10 * time.Millisecond):
			}
			task, err := claimAdvancedChatConnectorTask(userID, deviceID)
			if err != nil || task == nil {
				continue
			}
			now := time.Now()
			updates := map[string]interface{}{
				"status":      advancedChatConnectorTaskStatusCompleted,
				"result":      connector.replies[task.Action],
				"finished_at": &now,
				"updated_at":  now,
			}
			if connector.failWith != "" {
				updates["status"] = advancedChatConnectorTaskStatusFailed
				updates["error_message"] = connector.failWith
			}
			_ = model.DB.Model(&AdvancedChatConnectorTask{}).Where("id = ?", task.ID).Updates(updates).Error
		}
	}()
	return connector
}

func (connector *fakeTerminalConnector) close() {
	close(connector.stop)
	<-connector.done
}

func setupTerminalTestRouter(t *testing.T) (*gin.Engine, model.User, AdvancedChatConnectorDevice) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:terminal-"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("access database pool: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.AutoMigrate(&model.User{}, &AdvancedChatConnectorDevice{}, &AdvancedChatConnectorTask{}); err != nil {
		t.Fatalf("migrate tables: %v", err)
	}
	previous := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previous })

	user := model.User{Username: "terminal-user", Email: "terminal@example.com", APIKey: "terminal-key"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	seen := time.Now()
	device := AdvancedChatConnectorDevice{
		ID:         "dev_terminal",
		UserID:     user.ID,
		Name:       "Workstation",
		OS:         "windows",
		Status:     advancedChatConnectorDeviceStatusOnline,
		LastSeenAt: &seen,
	}
	if err := db.Create(&device).Error; err != nil {
		t.Fatalf("create device: %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	group := router.Group("/api/user", func(c *gin.Context) {
		c.Set("user", &user)
		c.Next()
	})
	registerAdvancedChatTerminalRoutes(group)
	return router, user, device
}

func callTerminalAPI(t *testing.T, router *gin.Engine, method string, path string, body string) (int, map[string]interface{}) {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	payload := map[string]interface{}{}
	if recorder.Body.Len() > 0 {
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
		}
	}
	return recorder.Code, payload
}

func TestConnectorTerminalOpenInputReadClose(t *testing.T) {
	router, user, device := setupTerminalTestRouter(t)
	connector := startFakeTerminalConnector(user.ID, device.ID, map[string]string{
		"terminal_open":  `{"terminal_id":"term_abc","shell":"cmd.exe","os":"windows","cwd":"D:\\work","offset":0}`,
		"terminal_input": `{"ok":true,"terminal_id":"term_abc"}`,
		"terminal_read":  `{"terminal_id":"term_abc","data":"hello","offset":5,"alive":true}`,
		"terminal_close": `{"ok":true,"terminal_id":"term_abc"}`,
	}, "")
	defer connector.close()

	status, payload := callTerminalAPI(t, router, http.MethodPost, "/api/user/advanced-chat/terminal/open",
		`{"connector_device_id":"dev_terminal","connector_workspace_path":"D:\\work"}`)
	if status != http.StatusOK {
		t.Fatalf("open status %d: %v", status, payload)
	}
	if payload["terminal_id"] != "term_abc" || payload["shell"] != "cmd.exe" {
		t.Fatalf("unexpected open response: %v", payload)
	}

	status, payload = callTerminalAPI(t, router, http.MethodPost, "/api/user/advanced-chat/terminal/input",
		`{"connector_device_id":"dev_terminal","terminal_id":"term_abc","data":"echo hi\n"}`)
	if status != http.StatusOK || payload["ok"] != true {
		t.Fatalf("input status %d: %v", status, payload)
	}

	status, payload = callTerminalAPI(t, router, http.MethodGet,
		"/api/user/advanced-chat/terminal/output?connector_device_id=dev_terminal&terminal_id=term_abc&offset=0", "")
	if status != http.StatusOK {
		t.Fatalf("output status %d: %v", status, payload)
	}
	if payload["data"] != "hello" || payload["alive"] != true {
		t.Fatalf("unexpected output response: %v", payload)
	}

	status, payload = callTerminalAPI(t, router, http.MethodPost, "/api/user/advanced-chat/terminal/close",
		`{"connector_device_id":"dev_terminal","terminal_id":"term_abc"}`)
	if status != http.StatusOK || payload["ok"] != true {
		t.Fatalf("close status %d: %v", status, payload)
	}

	var tasks []AdvancedChatConnectorTask
	if err := model.DB.Where("user_id = ?", user.ID).Order("created_at").Find(&tasks).Error; err != nil {
		t.Fatalf("load tasks: %v", err)
	}
	actions := make([]string, 0, len(tasks))
	for _, task := range tasks {
		actions = append(actions, task.Action)
		if task.Status != advancedChatConnectorTaskStatusCompleted {
			t.Fatalf("task %s ended as %s", task.Action, task.Status)
		}
	}
	expected := "terminal_open,terminal_input,terminal_read,terminal_close"
	if strings.Join(actions, ",") != expected {
		t.Fatalf("dispatched actions %v, want %s", actions, expected)
	}
}

func TestConnectorTerminalPassesWorkspaceAndOffsetToDevice(t *testing.T) {
	router, user, device := setupTerminalTestRouter(t)
	connector := startFakeTerminalConnector(user.ID, device.ID, map[string]string{
		"terminal_open": `{"terminal_id":"term_abc"}`,
		"terminal_read": `{"terminal_id":"term_abc","data":"","offset":42,"alive":true}`,
	}, "")
	defer connector.close()

	if status, payload := callTerminalAPI(t, router, http.MethodPost, "/api/user/advanced-chat/terminal/open",
		`{"connector_device_id":"dev_terminal","connector_workspace_path":"/srv/app"}`); status != http.StatusOK {
		t.Fatalf("open status %d: %v", status, payload)
	}
	if status, payload := callTerminalAPI(t, router, http.MethodGet,
		"/api/user/advanced-chat/terminal/output?connector_device_id=dev_terminal&terminal_id=term_abc&offset=42", ""); status != http.StatusOK {
		t.Fatalf("output status %d: %v", status, payload)
	}

	openPayload := terminalTaskPayload(t, user.ID, "terminal_open")
	if openPayload["workspace_path"] != "/srv/app" {
		t.Fatalf("workspace_path not forwarded: %v", openPayload)
	}
	readPayload := terminalTaskPayload(t, user.ID, "terminal_read")
	if readPayload["offset"] != float64(42) {
		t.Fatalf("offset not forwarded: %v", readPayload)
	}
}

func terminalTaskPayload(t *testing.T, userID uint, action string) map[string]interface{} {
	t.Helper()
	var task AdvancedChatConnectorTask
	if err := model.DB.Where("user_id = ? AND action = ?", userID, action).First(&task).Error; err != nil {
		t.Fatalf("load %s task: %v", action, err)
	}
	payload := map[string]interface{}{}
	if err := json.Unmarshal([]byte(task.Payload), &payload); err != nil {
		t.Fatalf("decode %s payload %q: %v", action, task.Payload, err)
	}
	return payload
}

func TestConnectorTerminalRejectsBadRequests(t *testing.T) {
	router, _, _ := setupTerminalTestRouter(t)

	if status, _ := callTerminalAPI(t, router, http.MethodPost, "/api/user/advanced-chat/terminal/input",
		`{"connector_device_id":"dev_terminal","terminal_id":"","data":"x"}`); status != http.StatusBadRequest {
		t.Fatalf("empty terminal id status %d, want 400", status)
	}
	if status, _ := callTerminalAPI(t, router, http.MethodGet,
		"/api/user/advanced-chat/terminal/output?connector_device_id=dev_terminal&terminal_id=term_abc&offset=-1", ""); status != http.StatusBadRequest {
		t.Fatalf("negative offset status %d, want 400", status)
	}
	if status, _ := callTerminalAPI(t, router, http.MethodPost, "/api/user/advanced-chat/terminal/open",
		`{"connector_device_id":"dev_terminal","shell":"nushell"}`); status != http.StatusBadRequest {
		t.Fatalf("unsupported shell status %d, want 400", status)
	}
	if status, _ := callTerminalAPI(t, router, http.MethodPost, "/api/user/advanced-chat/terminal/open",
		`{"connector_device_id":"dev_missing"}`); status != http.StatusBadRequest {
		t.Fatalf("unknown device status %d, want 400", status)
	}
	oversized := `{"connector_device_id":"dev_terminal","terminal_id":"term_abc","data":"` + strings.Repeat("a", advancedChatTerminalMaxInputSize+1) + `"}`
	if status, _ := callTerminalAPI(t, router, http.MethodPost, "/api/user/advanced-chat/terminal/input", oversized); status != http.StatusBadRequest {
		t.Fatalf("oversized input status %d, want 400", status)
	}
}

func TestConnectorTerminalRejectsOfflineDevice(t *testing.T) {
	router, _, device := setupTerminalTestRouter(t)
	stale := time.Now().Add(-10 * time.Minute)
	if err := model.DB.Model(&AdvancedChatConnectorDevice{}).Where("id = ?", device.ID).
		Update("last_seen_at", &stale).Error; err != nil {
		t.Fatalf("age device: %v", err)
	}
	if status, payload := callTerminalAPI(t, router, http.MethodPost, "/api/user/advanced-chat/terminal/open",
		`{"connector_device_id":"dev_terminal"}`); status != http.StatusBadRequest {
		t.Fatalf("offline device status %d: %v", status, payload)
	}
}

func TestConnectorTerminalSurfacesDeviceError(t *testing.T) {
	router, user, device := setupTerminalTestRouter(t)
	connector := startFakeTerminalConnector(user.ID, device.ID, map[string]string{}, "terminal term_gone is not open")
	defer connector.close()

	status, payload := callTerminalAPI(t, router, http.MethodGet,
		"/api/user/advanced-chat/terminal/output?connector_device_id=dev_terminal&terminal_id=term_gone&offset=0", "")
	if status != http.StatusBadGateway {
		t.Fatalf("device failure status %d: %v", status, payload)
	}
	if message, _ := payload["error"].(string); !strings.Contains(message, "term_gone") {
		t.Fatalf("expected the device error to be surfaced, got %v", payload)
	}
}
