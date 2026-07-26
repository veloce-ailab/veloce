package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	advancedChatConnectorDeviceStatusOffline = "offline"
	advancedChatConnectorDeviceStatusOnline  = "online"

	advancedChatConnectorModePlatform  = "platform"
	advancedChatConnectorModeWebServer = "web_server"

	advancedChatConnectorDeviceKindCLI     = "cli"
	advancedChatConnectorDeviceKindDesktop = "desktop"

	advancedChatConnectorTaskStatusPendingApproval = "pending_approval"
	advancedChatConnectorTaskStatusQueued          = "queued"
	advancedChatConnectorTaskStatusRunning         = "running"
	advancedChatConnectorTaskStatusCompleted       = "completed"
	advancedChatConnectorTaskStatusFailed          = "failed"

	advancedChatConnectorOnlineWindow = 60 * time.Second
	advancedChatConnectorTaskWait     = 10 * time.Minute
	advancedChatAgentSkillsLoadWait   = 20 * time.Second

	advancedChatAgentSkillsMaxFiles      = 40
	advancedChatAgentSkillsMaxFileBytes  = 64 * 1024
	advancedChatAgentSkillsMaxTotalBytes = 256 * 1024

	advancedChatStaticSiteMaxSites          = 20
	advancedChatStaticSiteMaxFiles          = 200
	advancedChatStaticSiteMaxFileBytes      = 2 * 1024 * 1024
	advancedChatStaticSiteMaxTotalBytes     = 20 * 1024 * 1024
	advancedChatStaticSiteDefaultListenPort = 8080

	advancedChatConnectorToolListFiles      = "workspace_list_files"
	advancedChatConnectorToolReadFile       = "workspace_read_file"
	advancedChatConnectorToolWriteFile      = "workspace_write_file"
	advancedChatConnectorToolReplaceText    = "workspace_replace_text"
	advancedChatConnectorToolRunCommand     = "workspace_run_command"
	advancedChatConnectorToolWebSearch      = "workspace_web_search"
	advancedChatConnectorToolWebFetch       = "workspace_web_fetch"
	advancedChatConnectorToolWindowsDrives  = "workspace_list_windows_drives"
	advancedChatConnectorToolListSites      = "list_static_sites"
	advancedChatConnectorToolDeploySite     = "deploy_static_site"
	advancedChatConnectorToolSetSiteEnabled = "set_static_site_enabled"
	advancedChatConnectorToolDeleteSite     = "delete_static_site"

	advancedChatConnectorPreviewOldContent          = "preview_old_content"
	advancedChatConnectorPreviewOldContentAvailable = "preview_old_content_available"
	advancedChatConnectorPreviewToolCallID          = "preview_tool_call_id"
	advancedChatConnectorTaskID                     = "connector_task_id"
	advancedChatConnectorApprovalManual             = "manual"
	advancedChatConnectorApprovalFullAccess         = "full_access"
	advancedChatConnectorApprovalAssistant          = "assistant"
)

var advancedChatDesktopConnectorEnsureMu sync.Mutex

type AdvancedChatConnectorDevice struct {
	ID        string     `gorm:"primaryKey;size:80" json:"id"`
	UserID    uint       `gorm:"index;not null" json:"user_id"`
	User      model.User `gorm:"foreignKey:UserID" json:"-"`
	TokenHash string     `gorm:"uniqueIndex;size:64;not null" json:"-"`
	Name      string     `gorm:"size:120;not null" json:"name"`
	Remark    string     `gorm:"size:200;not null;default:''" json:"remark"`
	Hostname  string     `gorm:"size:120" json:"hostname"`
	OS        string     `gorm:"size:40" json:"os"`
	Arch      string     `gorm:"size:40" json:"arch"`
	Version   string     `gorm:"size:80" json:"version"`
	Kind      string     `gorm:"size:20;index;not null;default:'cli'" json:"kind"`
	// DesktopInstanceID identifies a Veloce Desktop installation, not a hardware fingerprint.
	DesktopInstanceID string `gorm:"size:120;index;not null;default:''" json:"desktop_instance_id,omitempty"`
	Mode              string `gorm:"size:40;not null;default:'platform'" json:"mode"`
	ListenPort        int    `gorm:"not null;default:0" json:"listen_port"`
	Status            string `gorm:"size:20;index;not null" json:"status"`
	// Kept for existing databases that already migrated the previous connector schema.
	// New connector versions no longer register workspace paths on the device.
	Workspaces string     `gorm:"type:text;not null" json:"-"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// CreateEnterpriseConnectorToken creates a normal connector identity that can
// be owned and governed through an enterprise device record.
func CreateEnterpriseConnectorToken(db *gorm.DB, userID uint, name, mode string, listenPort int) (AdvancedChatConnectorDevice, string, error) {
	if db == nil || userID == 0 {
		return AdvancedChatConnectorDevice{}, "", errors.New("connector owner is required")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return AdvancedChatConnectorDevice{}, "", errors.New("connector name is required")
	}
	if len([]rune(name)) > 120 {
		name = string([]rune(name)[:120])
	}
	token, err := newAdvancedChatConnectorToken()
	if err != nil {
		return AdvancedChatConnectorDevice{}, "", err
	}
	now := time.Now()
	device := AdvancedChatConnectorDevice{ID: newAdvancedChatID("acd"), UserID: userID, TokenHash: hashAdvancedChatConnectorToken(token), Name: name, Kind: advancedChatConnectorDeviceKindCLI, Mode: normalizeAdvancedChatConnectorMode(mode), ListenPort: normalizeAdvancedChatConnectorListenPort(listenPort, normalizeAdvancedChatConnectorMode(mode)), Status: advancedChatConnectorDeviceStatusOffline, Workspaces: "[]", CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&device).Error; err != nil {
		return AdvancedChatConnectorDevice{}, "", err
	}
	return device, token, nil
}

func RotateEnterpriseConnectorToken(db *gorm.DB, connectorID string) (AdvancedChatConnectorDevice, string, error) {
	if db == nil || strings.TrimSpace(connectorID) == "" {
		return AdvancedChatConnectorDevice{}, "", errors.New("connector device is required")
	}
	token, err := newAdvancedChatConnectorToken()
	if err != nil {
		return AdvancedChatConnectorDevice{}, "", err
	}
	var device AdvancedChatConnectorDevice
	if err := db.Where("id = ?", connectorID).First(&device).Error; err != nil {
		return AdvancedChatConnectorDevice{}, "", err
	}
	if err := db.Model(&device).Updates(map[string]interface{}{"token_hash": hashAdvancedChatConnectorToken(token), "updated_at": time.Now()}).Error; err != nil {
		return AdvancedChatConnectorDevice{}, "", err
	}
	return device, token, nil
}

type AdvancedChatStaticSite struct {
	ID         string                      `gorm:"primaryKey;size:80" json:"id"`
	UserID     uint                        `gorm:"index;not null;uniqueIndex:idx_advanced_chat_static_site_user_domain" json:"user_id"`
	User       model.User                  `gorm:"foreignKey:UserID" json:"-"`
	DeviceID   string                      `gorm:"index;size:80;not null" json:"device_id"`
	Device     AdvancedChatConnectorDevice `gorm:"foreignKey:DeviceID" json:"-"`
	DomainName string                      `gorm:"size:253;not null;uniqueIndex:idx_advanced_chat_static_site_user_domain" json:"domain_name"`
	Enabled    bool                        `gorm:"not null;default:true" json:"enabled"`
	LastTaskID string                      `gorm:"size:80" json:"last_task_id"`
	CreatedAt  time.Time                   `json:"created_at"`
	UpdatedAt  time.Time                   `json:"updated_at"`
}

type AdvancedChatConnectorTask struct {
	ID            string     `gorm:"primaryKey;size:80" json:"id"`
	UserID        uint       `gorm:"index;not null" json:"user_id"`
	DeviceID      string     `gorm:"index;size:80;not null" json:"device_id"`
	RunID         string     `gorm:"index;size:80" json:"run_id"`
	Action        string     `gorm:"size:80;not null" json:"action"`
	WorkspacePath string     `gorm:"type:text;not null" json:"workspace_path"`
	Payload       string     `gorm:"type:text;not null" json:"-"`
	Status        string     `gorm:"size:20;index;not null" json:"status"`
	Result        string     `gorm:"type:text;not null" json:"result"`
	ErrorMessage  string     `gorm:"type:text;not null" json:"error_message"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type advancedChatConnectorDeviceResponse struct {
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	Remark            string     `json:"remark"`
	Hostname          string     `json:"hostname,omitempty"`
	OS                string     `json:"os,omitempty"`
	Arch              string     `json:"arch,omitempty"`
	Version           string     `json:"version,omitempty"`
	Kind              string     `json:"kind"`
	DesktopInstanceID string     `json:"desktop_instance_id,omitempty"`
	Mode              string     `json:"mode"`
	ListenPort        int        `json:"listen_port,omitempty"`
	Status            string     `json:"status"`
	Online            bool       `json:"online"`
	LastSeenAt        *time.Time `json:"last_seen_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type advancedChatConnectorTokenInput struct {
	Name       string `json:"name"`
	Remark     string `json:"remark"`
	Mode       string `json:"mode"`
	ListenPort int    `json:"listen_port"`
}

type advancedChatConnectorDeviceUpdateInput struct {
	Name   *string `json:"name"`
	Remark *string `json:"remark"`
}

type advancedChatConnectorRegisterInput struct {
	Name              string `json:"name"`
	Hostname          string `json:"hostname"`
	OS                string `json:"os"`
	Arch              string `json:"arch"`
	Version           string `json:"version"`
	Mode              string `json:"mode"`
	ListenPort        int    `json:"listen_port"`
	Kind              string `json:"kind"`
	DesktopInstanceID string `json:"desktop_instance_id"`
}

type advancedChatDesktopConnectorEnsureInput struct {
	DesktopInstanceID string `json:"desktop_instance_id"`
	Hostname          string `json:"hostname"`
	OS                string `json:"os"`
	Arch              string `json:"arch"`
	Version           string `json:"version"`
}

type advancedChatStaticSiteResponse struct {
	ID         string    `json:"id"`
	DeviceID   string    `json:"device_id"`
	DeviceName string    `json:"device_name,omitempty"`
	DomainName string    `json:"domain_name"`
	Enabled    bool      `json:"enabled"`
	LastTaskID string    `json:"last_task_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type advancedChatStaticSiteUpdateInput struct {
	Enabled *bool `json:"enabled"`
}

type advancedChatConnectorTaskResponse struct {
	ID                    string                 `json:"id"`
	Action                string                 `json:"action"`
	WorkspacePath         string                 `json:"workspace_path"`
	WorkspaceUnrestricted bool                   `json:"workspace_unrestricted"`
	Payload               map[string]interface{} `json:"payload"`
	CreatedAt             time.Time              `json:"created_at"`
}

type advancedChatConnectorTaskApprovalResponse struct {
	ID                    string                 `json:"id"`
	DeviceID              string                 `json:"device_id"`
	DeviceName            string                 `json:"device_name"`
	RunID                 string                 `json:"run_id"`
	Action                string                 `json:"action"`
	WorkspacePath         string                 `json:"workspace_path"`
	WorkspaceUnrestricted bool                   `json:"workspace_unrestricted"`
	Payload               map[string]interface{} `json:"payload"`
	CreatedAt             time.Time              `json:"created_at"`
}

type advancedChatConnectorDeviceTaskResponse struct {
	ID                    string                 `json:"id"`
	DeviceID              string                 `json:"device_id"`
	RunID                 string                 `json:"run_id"`
	Action                string                 `json:"action"`
	WorkspacePath         string                 `json:"workspace_path"`
	WorkspaceUnrestricted bool                   `json:"workspace_unrestricted"`
	Payload               map[string]interface{} `json:"payload"`
	Status                string                 `json:"status"`
	Result                string                 `json:"result,omitempty"`
	ErrorMessage          string                 `json:"error_message,omitempty"`
	StartedAt             *time.Time             `json:"started_at,omitempty"`
	FinishedAt            *time.Time             `json:"finished_at,omitempty"`
	CreatedAt             time.Time              `json:"created_at"`
	UpdatedAt             time.Time              `json:"updated_at"`
}

type advancedChatConnectorTaskResultInput struct {
	Success  bool   `json:"success"`
	Result   string `json:"result"`
	Output   string `json:"output"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	Error    string `json:"error"`
	ExitCode *int   `json:"exit_code"`
}

type advancedChatConnectorTaskDecisionInput struct {
	Approved bool `json:"approved"`
}

type advancedChatWorkspaceGitActionInput struct {
	ConnectorDeviceID      string `json:"connector_device_id"`
	ConnectorWorkspacePath string `json:"connector_workspace_path"`
	ApprovalMode           string `json:"approval_mode"`
	Action                 string `json:"action"`
	Message                string `json:"message"`
}

type advancedChatWorkspaceDirectoryEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type advancedChatWorkspaceDirectoriesResponse struct {
	Path        string                                `json:"path"`
	Directories []advancedChatWorkspaceDirectoryEntry `json:"directories"`
}

type advancedChatWorkspaceGitStatusResponse struct {
	CurrentBranch string   `json:"current_branch"`
	CompareBranch string   `json:"compare_branch,omitempty"`
	Branches      []string `json:"branches"`
	ChangedFiles  int      `json:"changed_files"`
	Additions     int      `json:"additions"`
	Deletions     int      `json:"deletions"`
	Clean         bool     `json:"clean"`
}

type advancedChatConnectorMCPProcessStopInput struct {
	Key string `json:"key"`
}

type advancedChatWorkspaceSkillsRefreshInput struct {
	ConnectorDeviceID      string `json:"connector_device_id"`
	ConnectorWorkspacePath string `json:"connector_workspace_path"`
}

type advancedChatWorkspaceSkillResponse struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	Content     string `json:"content"`
	Size        int    `json:"size"`
	Truncated   bool   `json:"truncated"`
}

type advancedChatConnectorToolBinding struct {
	DeviceID        string
	DeviceName      string
	WorkspacePath   string
	CloudSandboxID  string
	Action          string
	AutoApprove     bool
	ApprovalMode    string
	CommandPrefixes []string
}

type advancedChatWorkspaceSkill struct {
	ID          string
	Name        string
	Description string
	Path        string
	Content     string
	Resources   []string
	Size        int
	Truncated   bool
}

func registerAdvancedChatConnectorRoutes(group *gin.RouterGroup) {
	api := &advancedChatAPI{}
	connectors := group.Group("/advanced-chat/connectors")
	connectors.POST("/register", api.connectorRegister)
	connectors.POST("/heartbeat", api.connectorHeartbeat)
	connectors.GET("/tasks/next", api.connectorNextTask)
	connectors.POST("/tasks/:id/result", api.connectorTaskResult)
}

func (api *advancedChatAPI) listConnectorDevices(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var devices []AdvancedChatConnectorDevice
	if err := model.DB.Where("user_id = ?", user.ID).Order("last_seen_at DESC, created_at DESC").Find(&devices).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list devices"})
		return
	}
	responses := make([]advancedChatConnectorDeviceResponse, 0, len(devices))
	for _, device := range devices {
		responses = append(responses, advancedChatConnectorDeviceResponseFromModel(device))
	}
	c.JSON(http.StatusOK, responses)
}

func (api *advancedChatAPI) getConnectorDevice(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	device, found := loadConnectorDeviceForResponse(c, user.ID)
	if !found {
		return
	}
	c.JSON(http.StatusOK, advancedChatConnectorDeviceResponseFromModel(device))
}

func (api *advancedChatAPI) createConnectorToken(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var input advancedChatConnectorTokenInput
	_ = c.ShouldBindJSON(&input)
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = "Local device"
	}
	if len([]rune(name)) > 120 {
		name = string([]rune(name)[:120])
	}
	remark := truncateConnectorField(input.Remark, 200)
	mode := normalizeAdvancedChatConnectorMode(input.Mode)
	listenPort := normalizeAdvancedChatConnectorListenPort(input.ListenPort, mode)
	token, err := newAdvancedChatConnectorToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create connector token"})
		return
	}
	now := time.Now()
	device := AdvancedChatConnectorDevice{
		ID:         newAdvancedChatID("acd"),
		UserID:     user.ID,
		TokenHash:  hashAdvancedChatConnectorToken(token),
		Name:       name,
		Remark:     remark,
		Kind:       advancedChatConnectorDeviceKindCLI,
		Mode:       mode,
		ListenPort: listenPort,
		Status:     advancedChatConnectorDeviceStatusOffline,
		Workspaces: "[]",
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := model.DB.Create(&device).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save connector token"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": token, "device": advancedChatConnectorDeviceResponseFromModel(device)})
}

// ensureDesktopConnector creates a connector identity for this Veloce Desktop
// installation, or confirms that the main process still owns its saved token.
// The raw connector token is only returned when a new one must be persisted.
func (api *advancedChatAPI) ensureDesktopConnector(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var input advancedChatDesktopConnectorEnsureInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	instanceID := truncateConnectorField(input.DesktopInstanceID, 120)
	if instanceID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Desktop instance id is required"})
		return
	}

	advancedChatDesktopConnectorEnsureMu.Lock()
	defer advancedChatDesktopConnectorEnsureMu.Unlock()

	var device AdvancedChatConnectorDevice
	err := model.DB.Where("user_id = ? AND kind = ? AND desktop_instance_id = ?", user.ID, advancedChatConnectorDeviceKindDesktop, instanceID).First(&device).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load desktop connector"})
		return
	}

	resumeToken := strings.TrimSpace(c.GetHeader("X-Desktop-Connector-Token"))
	if err == nil && resumeToken != "" && device.TokenHash == hashAdvancedChatConnectorToken(resumeToken) {
		c.JSON(http.StatusOK, gin.H{"device": advancedChatConnectorDeviceResponseFromModel(device), "reused": true})
		return
	}

	token, tokenErr := newAdvancedChatConnectorToken()
	if tokenErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create connector token"})
		return
	}
	now := time.Now()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		hostname := truncateConnectorField(input.Hostname, 120)
		name := "Veloce Desktop"
		if hostname != "" {
			name += " (" + hostname + ")"
		}
		device = AdvancedChatConnectorDevice{
			ID:                newAdvancedChatID("acd"),
			UserID:            user.ID,
			TokenHash:         hashAdvancedChatConnectorToken(token),
			Name:              name,
			Kind:              advancedChatConnectorDeviceKindDesktop,
			DesktopInstanceID: instanceID,
			Hostname:          hostname,
			OS:                truncateConnectorField(input.OS, 40),
			Arch:              truncateConnectorField(input.Arch, 40),
			Version:           truncateConnectorField(input.Version, 80),
			Mode:              advancedChatConnectorModePlatform,
			Status:            advancedChatConnectorDeviceStatusOffline,
			Workspaces:        "[]",
			CreatedAt:         now,
			UpdatedAt:         now,
		}
		if err := model.DB.Create(&device).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create desktop connector"})
			return
		}
	} else {
		if err := model.DB.Model(&device).Updates(map[string]interface{}{
			"token_hash": hashAdvancedChatConnectorToken(token),
			"updated_at": now,
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to refresh desktop connector"})
			return
		}
		device.TokenHash = hashAdvancedChatConnectorToken(token)
		device.UpdatedAt = now
	}
	c.JSON(http.StatusOK, gin.H{"token": token, "device": advancedChatConnectorDeviceResponseFromModel(device), "reused": false})
}

func (api *advancedChatAPI) rotateConnectorDeviceToken(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	deviceID := strings.TrimSpace(c.Param("id"))
	token, err := newAdvancedChatConnectorToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create connector token"})
		return
	}
	now := time.Now()
	update := model.DB.Model(&AdvancedChatConnectorDevice{}).
		Where("id = ? AND user_id = ?", deviceID, user.ID).
		Updates(map[string]interface{}{
			"token_hash": hashAdvancedChatConnectorToken(token),
			"updated_at": now,
		})
	if update.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save connector token"})
		return
	}
	if update.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Device not found"})
		return
	}
	var device AdvancedChatConnectorDevice
	if err := model.DB.Where("id = ? AND user_id = ?", deviceID, user.ID).First(&device).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load connector device"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": token, "device": advancedChatConnectorDeviceResponseFromModel(device)})
}

func (api *advancedChatAPI) updateConnectorDevice(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	deviceID := strings.TrimSpace(c.Param("id"))
	var input advancedChatConnectorDeviceUpdateInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	updates := map[string]interface{}{"updated_at": time.Now()}
	if input.Name != nil {
		name := truncateConnectorField(*input.Name, 120)
		if name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Device name is required"})
			return
		}
		updates["name"] = name
	}
	if input.Remark != nil {
		updates["remark"] = truncateConnectorField(*input.Remark, 200)
	}
	update := model.DB.Model(&AdvancedChatConnectorDevice{}).
		Where("id = ? AND user_id = ?", deviceID, user.ID).
		Updates(updates)
	if update.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update connector device"})
		return
	}
	if update.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Device not found"})
		return
	}
	var device AdvancedChatConnectorDevice
	if err := model.DB.Where("id = ? AND user_id = ?", deviceID, user.ID).First(&device).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load connector device"})
		return
	}
	c.JSON(http.StatusOK, advancedChatConnectorDeviceResponseFromModel(device))
}

func (api *advancedChatAPI) listConnectorDeviceTasks(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	device, found := loadConnectorDeviceForResponse(c, user.ID)
	if !found {
		return
	}
	limit := 50
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil && value > 0 && value <= 200 {
			limit = value
		}
	}
	var tasks []AdvancedChatConnectorTask
	if err := model.DB.
		Where("user_id = ? AND device_id = ?", user.ID, device.ID).
		Order("created_at DESC").
		Limit(limit).
		Find(&tasks).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list connector tasks"})
		return
	}
	responses := make([]advancedChatConnectorDeviceTaskResponse, 0, len(tasks))
	for _, task := range tasks {
		responses = append(responses, advancedChatConnectorDeviceTaskResponseFromModel(task))
	}
	c.JSON(http.StatusOK, responses)
}

func (api *advancedChatAPI) cancelConnectorDeviceTask(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	device, found := loadConnectorDeviceForResponse(c, user.ID)
	if !found {
		return
	}
	taskID := strings.TrimSpace(c.Param("task_id"))
	if taskID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Task id is required"})
		return
	}
	now := time.Now()
	update := model.DB.Model(&AdvancedChatConnectorTask{}).
		Where("id = ? AND user_id = ? AND device_id = ? AND status IN ?", taskID, user.ID, device.ID, []string{
			advancedChatConnectorTaskStatusPendingApproval,
			advancedChatConnectorTaskStatusQueued,
			advancedChatConnectorTaskStatusRunning,
		}).
		Updates(map[string]interface{}{
			"status":        advancedChatConnectorTaskStatusFailed,
			"error_message": "cancelled by user",
			"finished_at":   &now,
			"updated_at":    now,
		})
	if update.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to cancel connector task"})
		return
	}
	if update.RowsAffected == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "Connector task is not active or was not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (api *advancedChatAPI) listConnectorDeviceMCPProcesses(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	device, found := loadOnlineConnectorDeviceForManagement(c, user.ID)
	if !found {
		return
	}
	result, ok := callConnectorManagementTask(c, user.ID, device.ID, "list_mcp_processes", map[string]interface{}{})
	if !ok {
		return
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Invalid connector MCP process response"})
		return
	}
	c.JSON(http.StatusOK, payload)
}

func (api *advancedChatAPI) stopConnectorDeviceMCPProcess(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	device, found := loadOnlineConnectorDeviceForManagement(c, user.ID)
	if !found {
		return
	}
	var input advancedChatConnectorMCPProcessStopInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	key := strings.TrimSpace(input.Key)
	if key == "" || len(key) > 80 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "MCP process key is required"})
		return
	}
	result, ok := callConnectorManagementTask(c, user.ID, device.ID, "stop_mcp_process", map[string]interface{}{"key": key})
	if !ok {
		return
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Invalid connector MCP process response"})
		return
	}
	c.JSON(http.StatusOK, payload)
}

func (api *advancedChatAPI) listPendingConnectorTasks(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	runID := strings.TrimSpace(c.Param("id"))
	var tasks []AdvancedChatConnectorTask
	if err := model.DB.
		Where("user_id = ? AND run_id = ? AND status = ?", user.ID, runID, advancedChatConnectorTaskStatusPendingApproval).
		Order("created_at ASC").
		Find(&tasks).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list connector tasks"})
		return
	}
	deviceIDs := make([]string, 0, len(tasks))
	for _, task := range tasks {
		deviceIDs = append(deviceIDs, task.DeviceID)
	}
	devices := map[string]AdvancedChatConnectorDevice{}
	if len(deviceIDs) > 0 {
		var rows []AdvancedChatConnectorDevice
		if err := model.DB.Where("user_id = ? AND id IN ?", user.ID, deviceIDs).Find(&rows).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load connector devices"})
			return
		}
		for _, device := range rows {
			devices[device.ID] = device
		}
	}
	result := make([]advancedChatConnectorTaskApprovalResponse, 0, len(tasks))
	for _, task := range tasks {
		result = append(result, advancedChatConnectorTaskApprovalResponseFromModel(task, devices[task.DeviceID]))
	}
	c.JSON(http.StatusOK, result)
}

func (api *advancedChatAPI) decideConnectorTask(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var input advancedChatConnectorTaskDecisionInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	taskID := strings.TrimSpace(c.Param("id"))
	status, err := decideAdvancedChatConnectorTask(user.ID, taskID, input.Approved, "user", "")
	if err != nil {
		var taskErr advancedChatConnectorTaskDecisionConflict
		if errors.As(err, &taskErr) {
			c.JSON(http.StatusConflict, gin.H{"error": "Connector task already decided", "status": taskErr.Status})
			return
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Connector task not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update connector task"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "status": status})
}

func (api *advancedChatAPI) getConnectorTask(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var task AdvancedChatConnectorTask
	if err := model.DB.Where("id = ? AND user_id = ?", strings.TrimSpace(c.Param("id")), user.ID).First(&task).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Connector task not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load connector task"})
		return
	}
	c.JSON(http.StatusOK, advancedChatConnectorDeviceTaskResponseFromModel(task))
}

func (api *advancedChatAPI) getWorkspaceDirectories(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	device, _, err := loadAdvancedChatConnectorForRun(user.ID, c.Query("connector_device_id"), "")
	if err != nil || device == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "A connected device is required"})
		return
	}
	requestedPath := strings.TrimSpace(c.Query("path"))
	if requestedPath == "" && strings.EqualFold(device.OS, "windows") {
		output, err := callAdvancedChatWorkspaceDirectoryRoots(c.Request.Context(), user.ID, device)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to list local drives: " + err.Error()})
			return
		}
		c.JSON(http.StatusOK, advancedChatWorkspaceDirectoriesResponse{Directories: parseAdvancedChatWindowsDriveDirectories(output)})
		return
	}
	if requestedPath == "" {
		requestedPath = "/"
	}
	output, err := callAdvancedChatWorkspaceDirectoryListing(c.Request.Context(), user.ID, device, requestedPath)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to list directories: " + err.Error()})
		return
	}
	c.JSON(http.StatusOK, advancedChatWorkspaceDirectoriesResponse{
		Path:        requestedPath,
		Directories: parseAdvancedChatWorkspaceDirectories(output, requestedPath, strings.EqualFold(device.OS, "windows")),
	})
}

func (api *advancedChatAPI) getWorkspaceGitStatus(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	device, workspacePath, err := loadAdvancedChatConnectorForRun(user.ID, c.Query("connector_device_id"), c.Query("connector_workspace_path"))
	if err != nil || device == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "A connected workspace is required"})
		return
	}
	status, err := advancedChatWorkspaceGitStatus(c.Request.Context(), user.ID, device, workspacePath, c.Query("compare_branch"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, status)
}

func (api *advancedChatAPI) runWorkspaceGitAction(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var input advancedChatWorkspaceGitActionInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	device, workspacePath, err := loadAdvancedChatConnectorForRun(user.ID, input.ConnectorDeviceID, input.ConnectorWorkspacePath)
	if err != nil || device == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "A connected workspace is required"})
		return
	}
	command, err := advancedChatWorkspaceGitActionCommand(input.Action, input.Message)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	approvalMode := normalizeAdvancedChatConnectorApprovalMode(input.ApprovalMode)
	binding := advancedChatConnectorToolBinding{
		DeviceID:      device.ID,
		DeviceName:    device.Name,
		WorkspacePath: workspacePath,
		Action:        "run_command",
		ApprovalMode:  approvalMode,
		AutoApprove:   approvalMode == advancedChatConnectorApprovalFullAccess,
	}
	task, err := createAdvancedChatConnectorTask(user.ID, "", binding, map[string]interface{}{"command": command})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create Git task"})
		return
	}
	if approvalMode == advancedChatConnectorApprovalAssistant {
		settings := ensureAdvancedChatUserSettings(user.ID)
		checker, checkerErr := advancedChatApprovalCheckerForUserAgent(user.ID, settings.ConnectorApprovalAgentID)
		if checkerErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Approval assistant is not configured"})
			return
		}
		if _, checkerErr = approveAdvancedChatConnectorTaskWithChecker(c.Request.Context(), user, "", "", checker, task, binding, map[string]interface{}{"command": command}, advancedChatCompletionObserver{}, 0, 0, false); checkerErr != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "Approval assistant failed: " + checkerErr.Error()})
			return
		}
		_ = model.DB.Where("id = ? AND user_id = ?", task.ID, user.ID).First(&task).Error
	}
	c.JSON(http.StatusAccepted, advancedChatConnectorDeviceTaskResponseFromModel(task))
}

type advancedChatConnectorTaskDecisionConflict struct {
	Status string
}

func (e advancedChatConnectorTaskDecisionConflict) Error() string {
	return "connector task already decided"
}

func decideAdvancedChatConnectorTask(userID uint, taskID string, approved bool, actor string, opinion string) (string, error) {
	taskID = strings.TrimSpace(taskID)
	actor = strings.TrimSpace(actor)
	if actor == "" {
		actor = "user"
	}
	now := time.Now()
	updates := map[string]interface{}{
		"status":     advancedChatConnectorTaskStatusQueued,
		"updated_at": now,
	}
	status := advancedChatConnectorTaskStatusQueued
	if !approved {
		message := "denied by " + actor
		if trimmed := strings.TrimSpace(opinion); trimmed != "" {
			message += ": " + truncateConnectorTaskText(trimmed)
		}
		status = advancedChatConnectorTaskStatusFailed
		updates = map[string]interface{}{
			"status":        status,
			"error_message": message,
			"finished_at":   &now,
			"updated_at":    now,
		}
	}
	update := model.DB.Model(&AdvancedChatConnectorTask{}).
		Where("id = ? AND user_id = ? AND status = ?", taskID, userID, advancedChatConnectorTaskStatusPendingApproval).
		Updates(updates)
	if update.Error != nil {
		return "", update.Error
	}
	if update.RowsAffected > 0 {
		return status, nil
	}
	var task AdvancedChatConnectorTask
	if err := model.DB.Where("id = ? AND user_id = ?", taskID, userID).First(&task).Error; err != nil {
		return "", err
	}
	return "", advancedChatConnectorTaskDecisionConflict{Status: task.Status}
}

func (api *advancedChatAPI) deleteConnectorDevice(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	deviceID := strings.TrimSpace(c.Param("id"))
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("device_id = ? AND user_id = ?", deviceID, user.ID).Delete(&AdvancedChatConnectorTask{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ? AND user_id = ?", deviceID, user.ID).Delete(&AdvancedChatConnectorDevice{}).Error
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete connector device"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Device deleted"})
}

func (api *advancedChatAPI) listStaticSites(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var sites []AdvancedChatStaticSite
	if err := model.DB.Where("user_id = ?", user.ID).Order("updated_at DESC, created_at DESC").Find(&sites).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list static sites"})
		return
	}
	deviceIDs := make([]string, 0, len(sites))
	for _, site := range sites {
		if strings.TrimSpace(site.DeviceID) != "" {
			deviceIDs = append(deviceIDs, site.DeviceID)
		}
	}
	devices := map[string]AdvancedChatConnectorDevice{}
	if len(deviceIDs) > 0 {
		var rows []AdvancedChatConnectorDevice
		if err := model.DB.Where("user_id = ? AND id IN ?", user.ID, deviceIDs).Find(&rows).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load connector devices"})
			return
		}
		for _, device := range rows {
			devices[device.ID] = device
		}
	}
	responses := make([]advancedChatStaticSiteResponse, 0, len(sites))
	for _, site := range sites {
		responses = append(responses, advancedChatStaticSiteResponseFromModel(site, devices[site.DeviceID]))
	}
	c.JSON(http.StatusOK, gin.H{
		"sites":           responses,
		"max_sites":       advancedChatStaticSiteMaxSites,
		"max_files":       advancedChatStaticSiteMaxFiles,
		"max_file_bytes":  advancedChatStaticSiteMaxFileBytes,
		"max_total_bytes": advancedChatStaticSiteMaxTotalBytes,
	})
}

func (api *advancedChatAPI) updateStaticSite(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var input advancedChatStaticSiteUpdateInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if input.Enabled == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "enabled is required"})
		return
	}
	now := time.Now()
	update := model.DB.Model(&AdvancedChatStaticSite{}).
		Where("id = ? AND user_id = ?", c.Param("id"), user.ID).
		Updates(map[string]interface{}{"enabled": *input.Enabled, "updated_at": now})
	if update.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update static site"})
		return
	}
	if update.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Static site not found"})
		return
	}
	var site AdvancedChatStaticSite
	if err := model.DB.Where("id = ? AND user_id = ?", c.Param("id"), user.ID).First(&site).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load static site"})
		return
	}
	var device AdvancedChatConnectorDevice
	_ = model.DB.Where("id = ? AND user_id = ?", site.DeviceID, user.ID).First(&device).Error
	taskID, taskErr := createAdvancedChatStaticSiteControlTask(user.ID, site, "set_static_site_enabled", map[string]interface{}{
		"domain_name": site.DomainName,
		"enabled":     site.Enabled,
	})
	if taskErr == nil && taskID != "" {
		site.LastTaskID = taskID
		_ = model.DB.Model(&site).Update("last_task_id", taskID).Error
	}
	response := advancedChatStaticSiteResponseFromModel(site, device)
	if taskErr != nil {
		c.JSON(http.StatusOK, gin.H{"site": response, "warning": taskErr.Error()})
		return
	}
	c.JSON(http.StatusOK, response)
}

func (api *advancedChatAPI) deleteStaticSite(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var site AdvancedChatStaticSite
	if err := model.DB.Where("id = ? AND user_id = ?", c.Param("id"), user.ID).First(&site).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Static site not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load static site"})
		return
	}
	taskID, taskErr := createAdvancedChatStaticSiteControlTask(user.ID, site, "delete_static_site", map[string]interface{}{
		"domain_name": site.DomainName,
	})
	if err := model.DB.Where("id = ? AND user_id = ?", site.ID, user.ID).Delete(&AdvancedChatStaticSite{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete static site"})
		return
	}
	if taskErr != nil {
		c.JSON(http.StatusOK, gin.H{"ok": true, "warning": taskErr.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "connector_task_id": taskID})
}

func (api *advancedChatAPI) refreshWorkspaceSkills(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var input advancedChatWorkspaceSkillsRefreshInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	device, workspacePath, err := loadAdvancedChatConnectorForRun(user.ID, input.ConnectorDeviceID, input.ConnectorWorkspacePath)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	skills, err := loadAdvancedChatWorkspaceSkillsForRun(c.Request.Context(), user.ID, device, workspacePath)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	responses := make([]advancedChatWorkspaceSkillResponse, 0, len(skills))
	for _, skill := range skills {
		responses = append(responses, advancedChatWorkspaceSkillResponse{
			ID:          skill.ID,
			Name:        skill.Name,
			Description: skill.Description,
			Path:        skill.Path,
			Content:     skill.Content,
			Size:        skill.Size,
			Truncated:   skill.Truncated,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"skills":          responses,
		"max_files":       advancedChatAgentSkillsMaxFiles,
		"max_file_bytes":  advancedChatAgentSkillsMaxFileBytes,
		"max_total_bytes": advancedChatAgentSkillsMaxTotalBytes,
	})
}

func (api *advancedChatAPI) connectorRegister(c *gin.Context) {
	device, ok := authenticateAdvancedChatConnector(c)
	if !ok {
		return
	}
	var input advancedChatConnectorRegisterInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	now := time.Now()
	updates := connectorDeviceUpdates(input, now)
	if err := model.DB.Model(device).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to register connector"})
		return
	}
	syncCloudSandboxHostHeartbeat(device.ID, now)
	if err := model.DB.Where("id = ? AND user_id = ?", device.ID, device.UserID).First(device).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load connector"})
		return
	}
	c.JSON(http.StatusOK, advancedChatConnectorDeviceResponseFromModel(*device))
}

func (api *advancedChatAPI) connectorHeartbeat(c *gin.Context) {
	device, ok := authenticateAdvancedChatConnector(c)
	if !ok {
		return
	}
	var input advancedChatConnectorRegisterInput
	_ = c.ShouldBindJSON(&input)
	now := time.Now()
	updates := connectorDeviceUpdates(input, now)
	if err := model.DB.Model(device).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update connector"})
		return
	}
	syncCloudSandboxHostHeartbeat(device.ID, now)
	c.JSON(http.StatusOK, gin.H{"ok": true, "device_id": device.ID})
}

func (api *advancedChatAPI) connectorNextTask(c *gin.Context) {
	device, ok := authenticateAdvancedChatConnector(c)
	if !ok {
		return
	}
	deadline := time.Now().Add(25 * time.Second)
	for {
		task, err := claimAdvancedChatConnectorTask(device.UserID, device.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load connector task"})
			return
		}
		if task != nil {
			c.JSON(http.StatusOK, gin.H{"task": advancedChatConnectorTaskResponseFromModel(*task)})
			return
		}
		if time.Now().After(deadline) {
			c.JSON(http.StatusOK, gin.H{"task": nil})
			return
		}
		select {
		case <-c.Request.Context().Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func (api *advancedChatAPI) connectorTaskResult(c *gin.Context) {
	device, ok := authenticateAdvancedChatConnector(c)
	if !ok {
		return
	}
	var input advancedChatConnectorTaskResultInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	status := advancedChatConnectorTaskStatusCompleted
	if !input.Success {
		status = advancedChatConnectorTaskStatusFailed
	}
	now := time.Now()
	result := normalizeConnectorTaskResultText(input)
	errMessage := normalizeConnectorTaskErrorMessage(input)
	query := model.DB.Model(&AdvancedChatConnectorTask{}).
		Where("id = ? AND device_id = ? AND status = ?", c.Param("id"), device.ID, advancedChatConnectorTaskStatusRunning)
	if normalizeAdvancedChatConnectorMode(device.Mode) != advancedChatConnectorModeSandboxd {
		query = query.Where("user_id = ?", device.UserID)
	}
	update := query.
		Updates(map[string]interface{}{
			"status":        status,
			"result":        result,
			"error_message": errMessage,
			"finished_at":   &now,
			"updated_at":    now,
		})
	if update.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save connector task result"})
		return
	}
	if update.RowsAffected == 0 {
		c.JSON(http.StatusOK, gin.H{"ok": true, "ignored": true})
		return
	}
	if normalizeAdvancedChatConnectorMode(device.Mode) == advancedChatConnectorModeSandboxd {
		if err := recordCloudSandboxTaskCharge(c.Param("id"), now); err != nil {
			log.Printf("cloud sandbox billing failed for task %s: %v", c.Param("id"), err)
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func authenticateAdvancedChatConnector(c *gin.Context) (*AdvancedChatConnectorDevice, bool) {
	token := strings.TrimSpace(c.GetHeader("X-Connector-Token"))
	if token == "" {
		auth := strings.TrimSpace(c.GetHeader("Authorization"))
		if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			token = strings.TrimSpace(auth[7:])
		}
	}
	if token == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Connector token is required"})
		return nil, false
	}
	var device AdvancedChatConnectorDevice
	if err := model.DB.Where("token_hash = ?", hashAdvancedChatConnectorToken(token)).Limit(1).Find(&device).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to authenticate connector"})
		return nil, false
	}
	if device.ID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid connector token"})
		return nil, false
	}
	return &device, true
}

func connectorDeviceUpdates(input advancedChatConnectorRegisterInput, now time.Time) map[string]interface{} {
	name := strings.TrimSpace(input.Name)
	updates := map[string]interface{}{
		"hostname":     truncateConnectorField(input.Hostname, 120),
		"os":           truncateConnectorField(input.OS, 40),
		"arch":         truncateConnectorField(input.Arch, 40),
		"version":      truncateConnectorField(input.Version, 80),
		"mode":         normalizeAdvancedChatConnectorMode(input.Mode),
		"listen_port":  normalizeAdvancedChatConnectorListenPort(input.ListenPort, input.Mode),
		"status":       advancedChatConnectorDeviceStatusOnline,
		"last_seen_at": &now,
		"updated_at":   now,
	}
	if name != "" {
		updates["name"] = truncateConnectorField(name, 120)
	}
	return updates
}

func claimAdvancedChatConnectorTask(userID uint, deviceID string) (*AdvancedChatConnectorTask, error) {
	var task AdvancedChatConnectorTask
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		query := tx.Where("device_id = ? AND status = ?", deviceID, advancedChatConnectorTaskStatusQueued)
		var device AdvancedChatConnectorDevice
		if err := tx.Select("mode").Where("id = ?", deviceID).First(&device).Error; err != nil {
			return err
		}
		if normalizeAdvancedChatConnectorMode(device.Mode) != advancedChatConnectorModeSandboxd {
			query = query.Where("user_id = ?", userID)
		}
		if err := query.
			Order("created_at ASC").
			Limit(1).
			Find(&task).Error; err != nil {
			return err
		}
		if task.ID == "" {
			return nil
		}
		now := time.Now()
		update := tx.Model(&AdvancedChatConnectorTask{}).
			Where("id = ? AND status = ?", task.ID, advancedChatConnectorTaskStatusQueued).
			Updates(map[string]interface{}{
				"status":     advancedChatConnectorTaskStatusRunning,
				"started_at": &now,
				"updated_at": now,
			})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected == 0 {
			task = AdvancedChatConnectorTask{}
			return nil
		}
		task.Status = advancedChatConnectorTaskStatusRunning
		task.StartedAt = &now
		return nil
	})
	if err != nil {
		return nil, err
	}
	if task.ID == "" {
		return nil, nil
	}
	return &task, nil
}

func advancedChatConnectorTaskResponseFromModel(task AdvancedChatConnectorTask) advancedChatConnectorTaskResponse {
	payload := map[string]interface{}{}
	if strings.TrimSpace(task.Payload) != "" {
		_ = json.Unmarshal([]byte(task.Payload), &payload)
	}
	return advancedChatConnectorTaskResponse{
		ID:                    task.ID,
		Action:                task.Action,
		WorkspacePath:         task.WorkspacePath,
		WorkspaceUnrestricted: strings.TrimSpace(task.WorkspacePath) == "",
		Payload:               stripAdvancedChatConnectorPreviewFields(payload),
		CreatedAt:             task.CreatedAt,
	}
}

func advancedChatConnectorTaskApprovalResponseFromModel(task AdvancedChatConnectorTask, device AdvancedChatConnectorDevice) advancedChatConnectorTaskApprovalResponse {
	payload := map[string]interface{}{}
	if strings.TrimSpace(task.Payload) != "" {
		_ = json.Unmarshal([]byte(task.Payload), &payload)
	}
	return advancedChatConnectorTaskApprovalResponse{
		ID:                    task.ID,
		DeviceID:              task.DeviceID,
		DeviceName:            device.Name,
		RunID:                 task.RunID,
		Action:                task.Action,
		WorkspacePath:         task.WorkspacePath,
		WorkspaceUnrestricted: strings.TrimSpace(task.WorkspacePath) == "",
		Payload:               payload,
		CreatedAt:             task.CreatedAt,
	}
}

func advancedChatConnectorDeviceTaskResponseFromModel(task AdvancedChatConnectorTask) advancedChatConnectorDeviceTaskResponse {
	payload := map[string]interface{}{}
	if strings.TrimSpace(task.Payload) != "" {
		_ = json.Unmarshal([]byte(task.Payload), &payload)
	}
	return advancedChatConnectorDeviceTaskResponse{
		ID:                    task.ID,
		DeviceID:              task.DeviceID,
		RunID:                 task.RunID,
		Action:                task.Action,
		WorkspacePath:         task.WorkspacePath,
		WorkspaceUnrestricted: strings.TrimSpace(task.WorkspacePath) == "",
		Payload:               stripAdvancedChatConnectorPreviewFields(payload),
		Status:                task.Status,
		Result:                task.Result,
		ErrorMessage:          task.ErrorMessage,
		StartedAt:             task.StartedAt,
		FinishedAt:            task.FinishedAt,
		CreatedAt:             task.CreatedAt,
		UpdatedAt:             task.UpdatedAt,
	}
}

func advancedChatConnectorDeviceResponseFromModel(device AdvancedChatConnectorDevice) advancedChatConnectorDeviceResponse {
	online := advancedChatConnectorDeviceOnline(device)
	status := device.Status
	if !online {
		status = advancedChatConnectorDeviceStatusOffline
	}
	return advancedChatConnectorDeviceResponse{
		ID:                device.ID,
		Name:              device.Name,
		Remark:            device.Remark,
		Hostname:          device.Hostname,
		OS:                device.OS,
		Arch:              device.Arch,
		Version:           device.Version,
		Kind:              normalizeAdvancedChatConnectorDeviceKind(device.Kind),
		DesktopInstanceID: device.DesktopInstanceID,
		Mode:              normalizeAdvancedChatConnectorMode(device.Mode),
		ListenPort:        device.ListenPort,
		Status:            status,
		Online:            online,
		LastSeenAt:        device.LastSeenAt,
		CreatedAt:         device.CreatedAt,
		UpdatedAt:         device.UpdatedAt,
	}
}

func loadConnectorDeviceForResponse(c *gin.Context, userID uint) (AdvancedChatConnectorDevice, bool) {
	deviceID := strings.TrimSpace(c.Param("id"))
	if deviceID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Device id is required"})
		return AdvancedChatConnectorDevice{}, false
	}
	var device AdvancedChatConnectorDevice
	if err := model.DB.Where("id = ? AND user_id = ?", deviceID, userID).First(&device).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Device not found"})
			return AdvancedChatConnectorDevice{}, false
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load connector device"})
		return AdvancedChatConnectorDevice{}, false
	}
	return device, true
}

func loadOnlineConnectorDeviceForManagement(c *gin.Context, userID uint) (AdvancedChatConnectorDevice, bool) {
	device, ok := loadConnectorDeviceForResponse(c, userID)
	if !ok {
		return AdvancedChatConnectorDevice{}, false
	}
	if !advancedChatConnectorDeviceOnline(device) {
		c.JSON(http.StatusConflict, gin.H{"error": "Connector device is offline"})
		return AdvancedChatConnectorDevice{}, false
	}
	return device, true
}

func callConnectorManagementTask(c *gin.Context, userID uint, deviceID string, action string, payload map[string]interface{}) (string, bool) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	task, err := createAdvancedChatRawConnectorTask(userID, "", advancedChatConnectorToolBinding{
		DeviceID: deviceID,
		Action:   action,
	}, payload, false)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create connector management task"})
		return "", false
	}
	result, err := waitAdvancedChatConnectorTask(ctx, task.ID, userID)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return "", false
	}
	return result, true
}

func advancedChatStaticSiteResponseFromModel(site AdvancedChatStaticSite, device AdvancedChatConnectorDevice) advancedChatStaticSiteResponse {
	return advancedChatStaticSiteResponse{
		ID:         site.ID,
		DeviceID:   site.DeviceID,
		DeviceName: device.Name,
		DomainName: site.DomainName,
		Enabled:    site.Enabled,
		LastTaskID: site.LastTaskID,
		CreatedAt:  site.CreatedAt,
		UpdatedAt:  site.UpdatedAt,
	}
}

func advancedChatConnectorDeviceOnline(device AdvancedChatConnectorDevice) bool {
	return device.LastSeenAt != nil &&
		device.Status == advancedChatConnectorDeviceStatusOnline &&
		time.Since(*device.LastSeenAt) <= advancedChatConnectorOnlineWindow
}

func loadAdvancedChatConnectorForRun(userID uint, deviceID string, workspacePath string) (*AdvancedChatConnectorDevice, string, error) {
	device, workspacePath, err := loadAdvancedChatConnectorForSession(userID, deviceID, workspacePath)
	if err != nil || device == nil {
		return device, workspacePath, err
	}
	if !advancedChatConnectorDeviceOnline(*device) {
		return nil, "", errors.New("connector device is offline")
	}
	return device, workspacePath, nil
}

func loadAdvancedChatConnectorForSession(userID uint, deviceID string, workspacePath string) (*AdvancedChatConnectorDevice, string, error) {
	deviceID = strings.TrimSpace(deviceID)
	workspacePath = strings.TrimSpace(workspacePath)
	if deviceID == "" && workspacePath == "" {
		return nil, "", nil
	}
	if deviceID == "" {
		return nil, "", errors.New("connector device is required")
	}
	var device AdvancedChatConnectorDevice
	if err := model.DB.Where("id = ? AND user_id = ?", deviceID, userID).First(&device).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, "", errors.New("connector device not found")
		}
		return nil, "", err
	}
	if len([]rune(workspacePath)) > 1000 {
		return nil, "", errors.New("workspace path is too long")
	}
	return &device, workspacePath, nil
}

func advancedChatConnectorTools(device *AdvancedChatConnectorDevice, workspacePath string, autoApprove bool, commandPrefixes []string) ([]ChatExecutorTool, map[string]advancedChatConnectorToolBinding) {
	return advancedChatConnectorToolsWithApprovalMode(device, workspacePath, legacyConnectorApprovalMode(autoApprove), commandPrefixes)
}

func advancedChatConnectorToolsWithApprovalMode(device *AdvancedChatConnectorDevice, workspacePath string, approvalMode string, commandPrefixes []string) ([]ChatExecutorTool, map[string]advancedChatConnectorToolBinding) {
	return advancedChatConnectorToolsWithApprovalModeAndSandbox(device, workspacePath, approvalMode, commandPrefixes, "")
}

func advancedChatConnectorToolsWithApprovalModeAndSandbox(device *AdvancedChatConnectorDevice, workspacePath string, approvalMode string, commandPrefixes []string, cloudSandboxID string) ([]ChatExecutorTool, map[string]advancedChatConnectorToolBinding) {
	if device == nil {
		return nil, nil
	}
	approvalMode = normalizeAdvancedChatConnectorApprovalMode(approvalMode)
	workspacePath = strings.TrimSpace(workspacePath)
	unrestricted := workspacePath == ""
	bindings := map[string]advancedChatConnectorToolBinding{}
	bind := func(name string, action string) {
		if !advancedChatAssistantConnectorActionEnabled(action) {
			return
		}
		bindings[name] = advancedChatConnectorToolBinding{
			DeviceID:        device.ID,
			DeviceName:      device.Name,
			WorkspacePath:   workspacePath,
			CloudSandboxID:  cloudSandboxID,
			Action:          action,
			AutoApprove:     approvalMode == advancedChatConnectorApprovalFullAccess,
			ApprovalMode:    approvalMode,
			CommandPrefixes: normalizeConnectorCommandPrefixes(commandPrefixes),
		}
	}
	tools := []ChatExecutorTool{}
	add := func(action string, tool ChatExecutorTool) {
		if !advancedChatAssistantConnectorActionEnabled(action) {
			return
		}
		tools = append(tools, tool)
	}

	bind(advancedChatConnectorToolListFiles, "list_files")
	listDescription := "List files under the selected local workspace. Paths must be relative to the workspace root."
	if unrestricted {
		listDescription = "List files from the connected local device. Absolute paths are allowed because this message channel is configured without a workspace limit."
	}
	add("list_files", ChatExecutorTool{
		Name:        advancedChatConnectorToolListFiles,
		Description: listDescription,
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path":        map[string]interface{}{"type": "string", "description": "Directory path. Relative to workspace root when workspace-limited; absolute paths are allowed when unrestricted."},
				"max_entries": map[string]interface{}{"type": "integer", "description": "Maximum entries to return.", "minimum": 1, "maximum": 500},
			},
		},
	})
	if unrestricted && strings.EqualFold(device.OS, "windows") {
		bind(advancedChatConnectorToolWindowsDrives, "list_windows_drives")
		add("list_windows_drives", ChatExecutorTool{
			Name:        advancedChatConnectorToolWindowsDrives,
			Description: "List available Windows drive roots on the connected local device. Use this before choosing an absolute path in unrestricted Windows message channels.",
			Schema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		})
	}
	bind(advancedChatConnectorToolReadFile, "read_file")
	readDescription := "Read a UTF-8 or text-like file from the selected local workspace. Paths must be relative to the workspace root."
	if unrestricted {
		readDescription = "Read a UTF-8 or text-like file from the connected local device. Absolute paths are allowed because this message channel is configured without a workspace limit."
	}
	add("read_file", ChatExecutorTool{
		Name:        advancedChatConnectorToolReadFile,
		Description: readDescription,
		Schema: map[string]interface{}{
			"type":     "object",
			"required": []string{"path"},
			"properties": map[string]interface{}{
				"path":      map[string]interface{}{"type": "string", "description": "File path. Relative to workspace root when workspace-limited; absolute paths are allowed when unrestricted."},
				"max_bytes": map[string]interface{}{"type": "integer", "description": "Maximum bytes to return.", "minimum": 1, "maximum": 200000},
			},
		},
	})
	bind(advancedChatConnectorToolWriteFile, "write_file")
	writeDescription := "Write a file in the selected local workspace. The web frontend asks the user for approval before the connector receives write tasks."
	if unrestricted {
		writeDescription = "Write a file on the connected local device. Absolute paths are allowed because this message channel is configured without a workspace limit. This requires approval unless auto approval is enabled."
	}
	add("write_file", ChatExecutorTool{
		Name:        advancedChatConnectorToolWriteFile,
		Description: writeDescription,
		Schema: map[string]interface{}{
			"type":     "object",
			"required": []string{"path", "content"},
			"properties": map[string]interface{}{
				"path":        map[string]interface{}{"type": "string", "description": "File path. Relative to workspace root when workspace-limited; absolute paths are allowed when unrestricted."},
				"content":     map[string]interface{}{"type": "string", "description": "Full file content to write."},
				"overwrite":   map[string]interface{}{"type": "boolean", "description": "Whether to overwrite an existing file."},
				"create_dirs": map[string]interface{}{"type": "boolean", "description": "Whether to create parent directories."},
			},
		},
	})
	bind(advancedChatConnectorToolReplaceText, "replace_text")
	replaceDescription := "Replace one or more text blocks inside files in the selected local workspace. Use old_text/new_text for a single replacement, or replacements for multiple replacements in one tool call. The web frontend asks the user for approval before the connector receives edit tasks."
	if unrestricted {
		replaceDescription = "Replace one or more text blocks inside files on the connected local device. Absolute paths are allowed because this message channel is configured without a workspace limit. This requires approval unless auto approval is enabled."
	}
	add("replace_text", ChatExecutorTool{
		Name:        advancedChatConnectorToolReplaceText,
		Description: replaceDescription,
		Schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path":     map[string]interface{}{"type": "string", "description": "File path for a single replacement, or default path for replacements entries. Relative to workspace root when workspace-limited; absolute paths are allowed when unrestricted."},
				"old_text": map[string]interface{}{"type": "string", "description": "Exact text to replace for a single replacement."},
				"new_text": map[string]interface{}{"type": "string", "description": "Replacement text for a single replacement."},
				"replacements": map[string]interface{}{
					"type":        "array",
					"description": "Multiple replacements. Each item may include path, old_text, and new_text.",
					"items": map[string]interface{}{
						"type":     "object",
						"required": []string{"old_text", "new_text"},
						"properties": map[string]interface{}{
							"path":     map[string]interface{}{"type": "string", "description": "File path. Falls back to the top-level path."},
							"old_text": map[string]interface{}{"type": "string", "description": "Exact text to replace."},
							"new_text": map[string]interface{}{"type": "string", "description": "Replacement text."},
						},
					},
				},
			},
		},
	})
	bind(advancedChatConnectorToolRunCommand, "run_command")
	runDescription := "Run a shell command in the selected local workspace. This always requires approval unless the full command starts with a command prefix explicitly allowed in the session settings."
	if unrestricted {
		runDescription = "Run a shell command on the connected local device. It runs without a workspace limit. This always requires approval unless the full command starts with a command prefix explicitly allowed in the session settings."
	}
	add("run_command", ChatExecutorTool{
		Name:        advancedChatConnectorToolRunCommand,
		Description: runDescription,
		Schema: map[string]interface{}{
			"type":     "object",
			"required": []string{"command"},
			"properties": map[string]interface{}{
				"command":     map[string]interface{}{"type": "string", "description": "Command line to execute in the workspace, or on the connected local device when unrestricted."},
				"timeout_sec": map[string]interface{}{"type": "integer", "description": "Maximum execution time in seconds.", "minimum": 1, "maximum": 120},
			},
		},
	})
	bind(advancedChatConnectorToolWebSearch, "web_search")
	add("web_search", ChatExecutorTool{
		Name:        advancedChatConnectorToolWebSearch,
		Description: "Search the web from the local connector and return concise result titles, URLs, and snippets. Use this when current or external information is needed. Choose a search engine when the task benefits from a specific source; otherwise use auto.",
		Schema: map[string]interface{}{
			"type":     "object",
			"required": []string{"query"},
			"properties": map[string]interface{}{
				"query":       map[string]interface{}{"type": "string", "description": "Search query."},
				"engine":      map[string]interface{}{"type": "string", "description": "Search engine to use. Use auto unless the user or task implies a specific engine.", "enum": []string{"auto", "duckduckgo", "bing", "baidu", "google"}},
				"max_results": map[string]interface{}{"type": "integer", "description": "Maximum results to return.", "minimum": 1, "maximum": 10},
				"language":    map[string]interface{}{"type": "string", "description": "Preferred result language, such as en, zh-CN, or ja."},
				"region":      map[string]interface{}{"type": "string", "description": "Preferred result region, such as us, cn, or jp."},
				"time_range":  map[string]interface{}{"type": "string", "description": "Optional freshness filter: day, week, month, or year.", "enum": []string{"day", "week", "month", "year"}},
			},
		},
	})
	bind(advancedChatConnectorToolWebFetch, "web_fetch")
	add("web_fetch", ChatExecutorTool{
		Name:        advancedChatConnectorToolWebFetch,
		Description: "Fetch a specific HTTP or HTTPS webpage from the local connector and return readable page text or response content. Use this when the user provides a URL or after web_search returns a relevant URL.",
		Schema: map[string]interface{}{
			"type":     "object",
			"required": []string{"url"},
			"properties": map[string]interface{}{
				"url":       map[string]interface{}{"type": "string", "description": "HTTP or HTTPS URL to fetch."},
				"max_bytes": map[string]interface{}{"type": "integer", "description": "Maximum response bytes to return after extraction.", "minimum": 1000, "maximum": 200000},
			},
		},
	})

	if normalizeAdvancedChatConnectorMode(device.Mode) != advancedChatConnectorModeWebServer {
		return tools, bindings
	}

	bind(advancedChatConnectorToolListSites, "list_static_sites")
	add("list_static_sites", ChatExecutorTool{
		Name:        advancedChatConnectorToolListSites,
		Description: "List static sites registered for this connector device. Returns site id, domain, enabled state, and recent deployment task metadata.",
		Schema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
	})
	bind(advancedChatConnectorToolDeploySite, "deploy_static_site")
	add("deploy_static_site", ChatExecutorTool{
		Name:        advancedChatConnectorToolDeploySite,
		Description: "Deploy or update a static website on the selected connector web server. Generate the complete HTML/CSS/JS asset tree first, then provide base64-encoded file contents for atomic publication.",
		Schema: map[string]interface{}{
			"type":     "object",
			"required": []string{"domain_name", "files"},
			"properties": map[string]interface{}{
				"site_id":     map[string]interface{}{"type": "string", "description": "Optional existing site id. Omit to create or update by domain."},
				"domain_name": map[string]interface{}{"type": "string", "description": "Target hostname/domain, such as site.example.com. Schemes and paths are not allowed."},
				"enabled":     map[string]interface{}{"type": "boolean", "description": "Whether the site should serve traffic after deployment. Defaults to true."},
				"files": map[string]interface{}{
					"type":        "array",
					"description": "Static files to publish. Each path is relative to the site's public root and each content value is base64.",
					"items": map[string]interface{}{
						"type":     "object",
						"required": []string{"path", "content"},
						"properties": map[string]interface{}{
							"path":    map[string]interface{}{"type": "string", "description": "Relative path, for example index.html or css/main.css."},
							"content": map[string]interface{}{"type": "string", "description": "Base64-encoded file bytes."},
						},
					},
				},
			},
		},
	})
	bind(advancedChatConnectorToolSetSiteEnabled, "set_static_site_enabled")
	add("set_static_site_enabled", ChatExecutorTool{
		Name:        advancedChatConnectorToolSetSiteEnabled,
		Description: "Enable or suspend a static site hosted by this connector. Suspended sites should return 403 at the edge.",
		Schema: map[string]interface{}{
			"type":     "object",
			"required": []string{"site_id", "enabled"},
			"properties": map[string]interface{}{
				"site_id": map[string]interface{}{"type": "string", "description": "Static site id."},
				"enabled": map[string]interface{}{"type": "boolean", "description": "true to serve the site, false to suspend it."},
			},
		},
	})
	bind(advancedChatConnectorToolDeleteSite, "delete_static_site")
	add("delete_static_site", ChatExecutorTool{
		Name:        advancedChatConnectorToolDeleteSite,
		Description: "Delete a static site from the control plane and ask the connector to remove its hosted files and route.",
		Schema: map[string]interface{}{
			"type":     "object",
			"required": []string{"site_id"},
			"properties": map[string]interface{}{
				"site_id": map[string]interface{}{"type": "string", "description": "Static site id."},
			},
		},
	})

	return tools, bindings
}

func callAdvancedChatConnectorTool(ctx context.Context, userID uint, runID string, binding advancedChatConnectorToolBinding, arguments map[string]interface{}) (string, error) {
	task, err := createAdvancedChatConnectorTask(userID, runID, binding, arguments)
	if err != nil {
		return "", err
	}
	if task.ID == "" && strings.TrimSpace(task.Result) != "" {
		return task.Result, nil
	}
	return waitAdvancedChatConnectorTask(ctx, task.ID, userID)
}

func createAdvancedChatConnectorTask(userID uint, runID string, binding advancedChatConnectorToolBinding, arguments map[string]interface{}) (AdvancedChatConnectorTask, error) {
	if isAdvancedChatStaticSiteControlAction(binding.Action) {
		return createAdvancedChatStaticSiteToolTask(userID, runID, binding, arguments)
	}
	if strings.TrimSpace(binding.CloudSandboxID) != "" {
		var err error
		arguments, err = cloudSandboxTaskArguments(userID, binding.CloudSandboxID, arguments)
		if err != nil {
			return AdvancedChatConnectorTask{}, err
		}
	}
	payload, err := json.Marshal(arguments)
	if err != nil {
		return AdvancedChatConnectorTask{}, err
	}
	status := advancedChatConnectorTaskStatusQueued
	if advancedChatConnectorTaskRequiresApproval(binding, arguments) {
		status = advancedChatConnectorTaskStatusPendingApproval
	}
	task := AdvancedChatConnectorTask{
		ID:            newAdvancedChatID("act"),
		UserID:        userID,
		DeviceID:      binding.DeviceID,
		RunID:         runID,
		Action:        binding.Action,
		WorkspacePath: binding.WorkspacePath,
		Payload:       string(payload),
		Status:        status,
		Result:        "",
		ErrorMessage:  "",
	}
	if err := model.DB.Create(&task).Error; err != nil {
		return AdvancedChatConnectorTask{}, err
	}
	return task, nil
}

func isAdvancedChatStaticSiteControlAction(action string) bool {
	switch action {
	case "list_static_sites", "deploy_static_site", "set_static_site_enabled", "delete_static_site":
		return true
	default:
		return false
	}
}

func createAdvancedChatStaticSiteToolTask(userID uint, runID string, binding advancedChatConnectorToolBinding, arguments map[string]interface{}) (AdvancedChatConnectorTask, error) {
	switch binding.Action {
	case "list_static_sites":
		result, err := advancedChatStaticSitesJSON(userID, binding.DeviceID)
		if err != nil {
			return AdvancedChatConnectorTask{}, err
		}
		return AdvancedChatConnectorTask{Result: result}, nil
	case "deploy_static_site":
		site, payload, err := prepareAdvancedChatStaticSiteDeployment(userID, binding.DeviceID, arguments)
		if err != nil {
			return AdvancedChatConnectorTask{}, err
		}
		task, err := createAdvancedChatRawConnectorTask(userID, runID, binding, payload, false)
		if err != nil {
			return AdvancedChatConnectorTask{}, err
		}
		_ = model.DB.Model(&AdvancedChatStaticSite{}).Where("id = ? AND user_id = ?", site.ID, userID).Updates(map[string]interface{}{
			"last_task_id": task.ID,
			"updated_at":   time.Now(),
		}).Error
		return task, nil
	case "set_static_site_enabled":
		site, payload, err := prepareAdvancedChatStaticSiteEnabled(userID, binding.DeviceID, arguments)
		if err != nil {
			return AdvancedChatConnectorTask{}, err
		}
		task, err := createAdvancedChatRawConnectorTask(userID, runID, binding, payload, false)
		if err != nil {
			return AdvancedChatConnectorTask{}, err
		}
		_ = model.DB.Model(&AdvancedChatStaticSite{}).Where("id = ? AND user_id = ?", site.ID, userID).Updates(map[string]interface{}{
			"last_task_id": task.ID,
			"updated_at":   time.Now(),
		}).Error
		return task, nil
	case "delete_static_site":
		site, payload, err := prepareAdvancedChatStaticSiteDelete(userID, binding.DeviceID, arguments)
		if err != nil {
			return AdvancedChatConnectorTask{}, err
		}
		task, err := createAdvancedChatRawConnectorTask(userID, runID, binding, payload, false)
		if err != nil {
			return AdvancedChatConnectorTask{}, err
		}
		_ = model.DB.Where("id = ? AND user_id = ?", site.ID, userID).Delete(&AdvancedChatStaticSite{}).Error
		return task, nil
	default:
		return AdvancedChatConnectorTask{}, fmt.Errorf("unsupported static site action: %s", binding.Action)
	}
}

func createAdvancedChatRawConnectorTask(userID uint, runID string, binding advancedChatConnectorToolBinding, arguments map[string]interface{}, allowApproval bool) (AdvancedChatConnectorTask, error) {
	payload, err := json.Marshal(arguments)
	if err != nil {
		return AdvancedChatConnectorTask{}, err
	}
	status := advancedChatConnectorTaskStatusQueued
	if allowApproval && advancedChatConnectorTaskRequiresApproval(binding, arguments) {
		status = advancedChatConnectorTaskStatusPendingApproval
	}
	task := AdvancedChatConnectorTask{
		ID:            newAdvancedChatID("act"),
		UserID:        userID,
		DeviceID:      binding.DeviceID,
		RunID:         runID,
		Action:        binding.Action,
		WorkspacePath: "",
		Payload:       string(payload),
		Status:        status,
		Result:        "",
		ErrorMessage:  "",
	}
	if err := model.DB.Create(&task).Error; err != nil {
		return AdvancedChatConnectorTask{}, err
	}
	return task, nil
}

func advancedChatStaticSitesJSON(userID uint, deviceID string) (string, error) {
	var sites []AdvancedChatStaticSite
	query := model.DB.Where("user_id = ?", userID)
	if strings.TrimSpace(deviceID) != "" {
		query = query.Where("device_id = ?", strings.TrimSpace(deviceID))
	}
	if err := query.Order("updated_at DESC, created_at DESC").Find(&sites).Error; err != nil {
		return "", err
	}
	deviceIDs := make([]string, 0, len(sites))
	for _, site := range sites {
		deviceIDs = append(deviceIDs, site.DeviceID)
	}
	devices := map[string]AdvancedChatConnectorDevice{}
	if len(deviceIDs) > 0 {
		var rows []AdvancedChatConnectorDevice
		if err := model.DB.Where("user_id = ? AND id IN ?", userID, deviceIDs).Find(&rows).Error; err != nil {
			return "", err
		}
		for _, device := range rows {
			devices[device.ID] = device
		}
	}
	responses := make([]advancedChatStaticSiteResponse, 0, len(sites))
	for _, site := range sites {
		responses = append(responses, advancedChatStaticSiteResponseFromModel(site, devices[site.DeviceID]))
	}
	payload, err := json.Marshal(map[string]interface{}{
		"sites":           responses,
		"max_sites":       advancedChatStaticSiteMaxSites,
		"max_files":       advancedChatStaticSiteMaxFiles,
		"max_file_bytes":  advancedChatStaticSiteMaxFileBytes,
		"max_total_bytes": advancedChatStaticSiteMaxTotalBytes,
	})
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func prepareAdvancedChatStaticSiteDeployment(userID uint, deviceID string, arguments map[string]interface{}) (AdvancedChatStaticSite, map[string]interface{}, error) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return AdvancedChatStaticSite{}, nil, errors.New("connector device is required")
	}
	if err := ensureAdvancedChatStaticSiteConnectorDevice(userID, deviceID); err != nil {
		return AdvancedChatStaticSite{}, nil, err
	}
	if err := validateAdvancedChatConnectorIDArgument(arguments, deviceID); err != nil {
		return AdvancedChatStaticSite{}, nil, err
	}
	domain, err := normalizeAdvancedChatStaticSiteDomain(stringMapValue(arguments, "domain_name"))
	if err != nil {
		return AdvancedChatStaticSite{}, nil, err
	}
	files, totalBytes, err := normalizeAdvancedChatStaticSiteFiles(arguments["files"])
	if err != nil {
		return AdvancedChatStaticSite{}, nil, err
	}
	enabled := true
	if raw, ok := arguments["enabled"].(bool); ok {
		enabled = raw
	}
	siteID := strings.TrimSpace(stringMapValue(arguments, "site_id"))
	site, err := upsertAdvancedChatStaticSite(userID, deviceID, siteID, domain, enabled)
	if err != nil {
		return AdvancedChatStaticSite{}, nil, err
	}
	payload := map[string]interface{}{
		"site_id":      site.ID,
		"domain_name":  site.DomainName,
		"enabled":      site.Enabled,
		"files":        files,
		"file_count":   len(files),
		"total_bytes":  totalBytes,
		"public_root":  "public",
		"storage_root": "/data/sites/" + site.DomainName + "/public",
	}
	return site, payload, nil
}

func prepareAdvancedChatStaticSiteEnabled(userID uint, deviceID string, arguments map[string]interface{}) (AdvancedChatStaticSite, map[string]interface{}, error) {
	site, err := loadAdvancedChatStaticSiteForTool(userID, deviceID, arguments)
	if err != nil {
		return AdvancedChatStaticSite{}, nil, err
	}
	enabled, ok := arguments["enabled"].(bool)
	if !ok {
		return AdvancedChatStaticSite{}, nil, errors.New("enabled is required")
	}
	now := time.Now()
	if err := model.DB.Model(&AdvancedChatStaticSite{}).Where("id = ? AND user_id = ?", site.ID, userID).Updates(map[string]interface{}{
		"enabled":    enabled,
		"updated_at": now,
	}).Error; err != nil {
		return AdvancedChatStaticSite{}, nil, err
	}
	site.Enabled = enabled
	site.UpdatedAt = now
	return site, map[string]interface{}{
		"site_id":     site.ID,
		"domain_name": site.DomainName,
		"enabled":     site.Enabled,
	}, nil
}

func prepareAdvancedChatStaticSiteDelete(userID uint, deviceID string, arguments map[string]interface{}) (AdvancedChatStaticSite, map[string]interface{}, error) {
	site, err := loadAdvancedChatStaticSiteForTool(userID, deviceID, arguments)
	if err != nil {
		return AdvancedChatStaticSite{}, nil, err
	}
	return site, map[string]interface{}{
		"site_id":     site.ID,
		"domain_name": site.DomainName,
	}, nil
}

func createAdvancedChatStaticSiteControlTask(userID uint, site AdvancedChatStaticSite, action string, payload map[string]interface{}) (string, error) {
	if err := ensureAdvancedChatStaticSiteConnectorDevice(userID, site.DeviceID); err != nil {
		return "", err
	}
	binding := advancedChatConnectorToolBinding{
		DeviceID:   site.DeviceID,
		DeviceName: site.DeviceID,
		Action:     action,
	}
	task, err := createAdvancedChatRawConnectorTask(userID, "", binding, payload, false)
	if err != nil {
		return "", err
	}
	return task.ID, nil
}

func ensureAdvancedChatStaticSiteConnectorDevice(userID uint, deviceID string) error {
	var device AdvancedChatConnectorDevice
	if err := model.DB.Where("id = ? AND user_id = ?", deviceID, userID).First(&device).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errors.New("connector device not found")
		}
		return err
	}
	if !advancedChatConnectorDeviceOnline(device) {
		return errors.New("connector device is offline")
	}
	if normalizeAdvancedChatConnectorMode(device.Mode) != advancedChatConnectorModeWebServer {
		return errors.New("connector device is not running in web_server mode")
	}
	return nil
}

func validateAdvancedChatConnectorIDArgument(arguments map[string]interface{}, deviceID string) error {
	raw := strings.TrimSpace(stringMapValue(arguments, "connector_id"))
	if raw == "" {
		return nil
	}
	if raw != deviceID {
		return errors.New("connector_id does not match the selected connector device")
	}
	return nil
}

func upsertAdvancedChatStaticSite(userID uint, deviceID string, siteID string, domain string, enabled bool) (AdvancedChatStaticSite, error) {
	var existing AdvancedChatStaticSite
	query := model.DB.Where("user_id = ?", userID)
	if siteID != "" {
		query = query.Where("id = ?", siteID)
	} else {
		query = query.Where("domain_name = ?", domain)
	}
	err := query.First(&existing).Error
	now := time.Now()
	if err == nil {
		if existing.DeviceID != deviceID {
			return AdvancedChatStaticSite{}, errors.New("static site belongs to another connector device")
		}
		if existing.DomainName != domain {
			return AdvancedChatStaticSite{}, errors.New("site_id and domain_name refer to different sites")
		}
		existing.Enabled = enabled
		existing.UpdatedAt = now
		if err := model.DB.Model(&existing).Updates(map[string]interface{}{"enabled": enabled, "updated_at": now}).Error; err != nil {
			return AdvancedChatStaticSite{}, err
		}
		return existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return AdvancedChatStaticSite{}, err
	}
	var count int64
	if err := model.DB.Model(&AdvancedChatStaticSite{}).Where("user_id = ?", userID).Count(&count).Error; err != nil {
		return AdvancedChatStaticSite{}, err
	}
	if count >= advancedChatStaticSiteMaxSites {
		return AdvancedChatStaticSite{}, fmt.Errorf("static site quota exceeded: max %d sites", advancedChatStaticSiteMaxSites)
	}
	site := AdvancedChatStaticSite{
		ID:         newAdvancedChatID("acs"),
		UserID:     userID,
		DeviceID:   deviceID,
		DomainName: domain,
		Enabled:    enabled,
		LastTaskID: "",
	}
	if err := model.DB.Create(&site).Error; err != nil {
		return AdvancedChatStaticSite{}, err
	}
	return site, nil
}

func loadAdvancedChatStaticSiteForTool(userID uint, deviceID string, arguments map[string]interface{}) (AdvancedChatStaticSite, error) {
	if strings.TrimSpace(deviceID) == "" {
		return AdvancedChatStaticSite{}, errors.New("connector device is required")
	}
	if err := validateAdvancedChatConnectorIDArgument(arguments, deviceID); err != nil {
		return AdvancedChatStaticSite{}, err
	}
	siteID := strings.TrimSpace(stringMapValue(arguments, "site_id"))
	domain := strings.TrimSpace(stringMapValue(arguments, "domain_name"))
	if siteID == "" && domain == "" {
		return AdvancedChatStaticSite{}, errors.New("site_id or domain_name is required")
	}
	query := model.DB.Where("user_id = ? AND device_id = ?", userID, deviceID)
	if siteID != "" {
		query = query.Where("id = ?", siteID)
	} else {
		normalized, err := normalizeAdvancedChatStaticSiteDomain(domain)
		if err != nil {
			return AdvancedChatStaticSite{}, err
		}
		query = query.Where("domain_name = ?", normalized)
	}
	var site AdvancedChatStaticSite
	if err := query.First(&site).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return AdvancedChatStaticSite{}, errors.New("static site not found")
		}
		return AdvancedChatStaticSite{}, err
	}
	return site, nil
}

func normalizeAdvancedChatStaticSiteDomain(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "http://")
	value = strings.TrimPrefix(value, "https://")
	if slash := strings.Index(value, "/"); slash >= 0 {
		value = value[:slash]
	}
	value = strings.TrimSuffix(value, ".")
	if value == "" || len(value) > 253 {
		return "", errors.New("domain_name is invalid")
	}
	if strings.ContainsAny(value, " \t\r\n\\@:") || strings.Contains(value, "..") {
		return "", errors.New("domain_name must be a hostname without scheme, path, port, or credentials")
	}
	if value == "localhost" {
		return value, nil
	}
	labels := strings.Split(value, ".")
	if len(labels) < 2 {
		return "", errors.New("domain_name must contain at least two labels")
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", errors.New("domain_name contains an invalid label")
		}
		for _, r := range label {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
				continue
			}
			return "", errors.New("domain_name may only contain letters, numbers, hyphens, and dots")
		}
	}
	return value, nil
}

func normalizeAdvancedChatStaticSiteFiles(raw interface{}) ([]map[string]interface{}, int, error) {
	items, ok := raw.([]interface{})
	if !ok || len(items) == 0 {
		return nil, 0, errors.New("files is required")
	}
	if len(items) > advancedChatStaticSiteMaxFiles {
		return nil, 0, fmt.Errorf("too many files: max %d", advancedChatStaticSiteMaxFiles)
	}
	files := make([]map[string]interface{}, 0, len(items))
	seen := map[string]bool{}
	totalBytes := 0
	for _, item := range items {
		row, ok := item.(map[string]interface{})
		if !ok {
			return nil, 0, errors.New("files items must be objects")
		}
		relativePath, err := normalizeAdvancedChatStaticSiteFilePath(stringMapValue(row, "path"))
		if err != nil {
			return nil, 0, err
		}
		if seen[relativePath] {
			return nil, 0, fmt.Errorf("duplicate static site file path: %s", relativePath)
		}
		content := strings.TrimSpace(stringMapValue(row, "content"))
		if content == "" {
			return nil, 0, fmt.Errorf("file %s content is required", relativePath)
		}
		decoded, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			return nil, 0, fmt.Errorf("file %s content must be base64", relativePath)
		}
		if len(decoded) > advancedChatStaticSiteMaxFileBytes {
			return nil, 0, fmt.Errorf("file %s exceeds max size %d bytes", relativePath, advancedChatStaticSiteMaxFileBytes)
		}
		totalBytes += len(decoded)
		if totalBytes > advancedChatStaticSiteMaxTotalBytes {
			return nil, 0, fmt.Errorf("static site payload exceeds max total size %d bytes", advancedChatStaticSiteMaxTotalBytes)
		}
		seen[relativePath] = true
		files = append(files, map[string]interface{}{"path": relativePath, "content": content, "size": len(decoded)})
	}
	return files, totalBytes, nil
}

func normalizeAdvancedChatStaticSiteFilePath(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if strings.HasPrefix(value, "/") || strings.Contains(value, "\x00") {
		return "", errors.New("static site file path must be relative")
	}
	rawParts := strings.Split(value, "/")
	for _, part := range rawParts {
		if part == ".." {
			return "", errors.New("static site file path must be relative")
		}
	}
	value = path.Clean("/" + value)
	value = strings.TrimPrefix(value, "/")
	if value == "" || value == "." {
		return "", errors.New("static site file path is invalid")
	}
	if len([]rune(value)) > 500 {
		return "", errors.New("static site file path is too long")
	}
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", errors.New("static site file path must be relative")
		}
	}
	return value, nil
}

func normalizeAdvancedChatConnectorMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case advancedChatConnectorModeWebServer:
		return advancedChatConnectorModeWebServer
	case advancedChatConnectorModeSandboxd:
		return advancedChatConnectorModeSandboxd
	default:
		return advancedChatConnectorModePlatform
	}
}

func normalizeAdvancedChatConnectorDeviceKind(value string) string {
	if strings.EqualFold(strings.TrimSpace(value), advancedChatConnectorDeviceKindDesktop) {
		return advancedChatConnectorDeviceKindDesktop
	}
	return advancedChatConnectorDeviceKindCLI
}

func normalizeAdvancedChatConnectorListenPort(port int, mode string) int {
	if port <= 0 {
		if normalizeAdvancedChatConnectorMode(mode) == advancedChatConnectorModeWebServer {
			return advancedChatStaticSiteDefaultListenPort
		}
		return 0
	}
	if port > 65535 {
		return 0
	}
	return port
}

func stringMapValue(values map[string]interface{}, key string) string {
	value, ok := values[key]
	if !ok {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func callAdvancedChatConnectorToolExpanded(ctx context.Context, userID uint, runID string, binding advancedChatConnectorToolBinding, arguments map[string]interface{}) (string, error) {
	calls := expandAdvancedChatConnectorToolArguments(binding, arguments)
	results := make([]string, 0, len(calls))
	for _, callArguments := range calls {
		result, err := callAdvancedChatConnectorTool(ctx, userID, runID, binding, callArguments)
		if strings.TrimSpace(result) != "" {
			results = append(results, result)
		}
		if err != nil {
			return strings.Join(results, "\n"), err
		}
	}
	return strings.Join(results, "\n"), nil
}

func loadAdvancedChatWorkspaceSkillsForRun(ctx context.Context, userID uint, device *AdvancedChatConnectorDevice, workspacePath string) ([]advancedChatWorkspaceSkill, error) {
	if device == nil {
		return []advancedChatWorkspaceSkill{}, nil
	}
	_ = syncAdvancedChatUploadedSkillsToConnector(ctx, userID, device)
	binding := advancedChatConnectorToolBinding{
		DeviceID:      device.ID,
		DeviceName:    device.Name,
		WorkspacePath: workspacePath,
		Action:        "list_agent_skills",
	}
	loadCtx, cancel := context.WithTimeout(ctx, advancedChatAgentSkillsLoadWait)
	defer cancel()
	result, err := callAdvancedChatConnectorTool(loadCtx, userID, "", binding, map[string]interface{}{})
	if err != nil {
		return nil, fmt.Errorf("failed to load connector agent skills: %w", err)
	}
	return parseAdvancedChatWorkspaceSkills(result)
}

func readAdvancedChatWorkspaceSkillForRun(ctx context.Context, userID uint, device *AdvancedChatConnectorDevice, workspacePath string, skill advancedChatWorkspaceSkill) (advancedChatWorkspaceSkill, error) {
	if device == nil {
		return advancedChatWorkspaceSkill{}, errors.New("connector device is not available")
	}
	binding := advancedChatConnectorToolBinding{
		DeviceID:      device.ID,
		DeviceName:    device.Name,
		WorkspacePath: workspacePath,
		Action:        "read_agent_skill",
	}
	readCtx, cancel := context.WithTimeout(ctx, advancedChatAgentSkillsLoadWait)
	defer cancel()
	result, err := callAdvancedChatConnectorTool(readCtx, userID, "", binding, map[string]interface{}{
		"id":        skill.ID,
		"path":      skill.Path,
		"max_bytes": advancedChatAgentSkillsMaxFileBytes,
	})
	if err != nil {
		return advancedChatWorkspaceSkill{}, err
	}
	parsed, err := parseAdvancedChatWorkspaceSkillRead(result)
	if err != nil {
		return advancedChatWorkspaceSkill{}, err
	}
	if strings.TrimSpace(parsed.ID) == "" {
		parsed.ID = skill.ID
	}
	if strings.TrimSpace(parsed.Path) == "" {
		parsed.Path = skill.Path
	}
	if strings.TrimSpace(parsed.Name) == "" {
		parsed.Name = skill.Name
	}
	if strings.TrimSpace(parsed.Description) == "" {
		parsed.Description = skill.Description
	}
	return parsed, nil
}

func readAdvancedChatWorkspaceSkillResourceForRun(ctx context.Context, userID uint, device *AdvancedChatConnectorDevice, workspacePath string, skill advancedChatWorkspaceSkill, resourcePath string, maxBytes int) (string, error) {
	if device == nil {
		return "", errors.New("connector device is not available")
	}
	binding := advancedChatConnectorToolBinding{
		DeviceID:      device.ID,
		DeviceName:    device.Name,
		WorkspacePath: workspacePath,
		Action:        "read_agent_skill_resource",
	}
	readCtx, cancel := context.WithTimeout(ctx, advancedChatAgentSkillsLoadWait)
	defer cancel()
	result, err := callAdvancedChatConnectorTool(readCtx, userID, "", binding, map[string]interface{}{
		"id":        skill.ID,
		"path":      skill.Path,
		"resource":  resourcePath,
		"max_bytes": maxBytes,
	})
	if err != nil {
		return "", err
	}
	return result, nil
}

func syncAdvancedChatUploadedSkillsToConnector(ctx context.Context, userID uint, device *AdvancedChatConnectorDevice) error {
	if device == nil {
		return nil
	}
	payload, err := advancedChatUploadedSkillsSyncPayload(userID)
	if err != nil {
		return err
	}
	binding := advancedChatConnectorToolBinding{
		DeviceID:   device.ID,
		DeviceName: device.Name,
		Action:     "sync_agent_skills",
	}
	syncCtx, cancel := context.WithTimeout(ctx, advancedChatAgentSkillsLoadWait)
	defer cancel()
	_, err = callAdvancedChatConnectorTool(syncCtx, userID, "", binding, payload)
	return err
}

func parseAdvancedChatWorkspaceSkills(raw string) ([]advancedChatWorkspaceSkill, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []advancedChatWorkspaceSkill{}, nil
	}
	var payload struct {
		Skills []struct {
			ID          string   `json:"id"`
			Name        string   `json:"name"`
			Description string   `json:"description"`
			Path        string   `json:"path"`
			Content     string   `json:"content"`
			Resources   []string `json:"resource_paths"`
			Size        int      `json:"size"`
			Truncated   bool     `json:"truncated"`
		} `json:"skills"`
		Truncated      bool `json:"truncated"`
		TotalBytesRead int  `json:"total_bytes_read"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, fmt.Errorf("failed to decode connector agent skills: %w", err)
	}
	if len(payload.Skills) > advancedChatAgentSkillsMaxFiles || payload.TotalBytesRead > advancedChatAgentSkillsMaxTotalBytes {
		return nil, errors.New("connector agent skills exceeded server limits")
	}
	result := make([]advancedChatWorkspaceSkill, 0, len(payload.Skills))
	totalBytes := 0
	seenPaths := map[string]bool{}
	for _, skill := range payload.Skills {
		path := sanitizeWorkspaceSkillPath(skill.Path)
		content := strings.TrimSpace(skill.Content)
		if path == "" || seenPaths[path] {
			continue
		}
		size := len([]byte(content))
		if content != "" && size > advancedChatAgentSkillsMaxFileBytes {
			content = truncateBytes(content, advancedChatAgentSkillsMaxFileBytes)
			size = len([]byte(content))
			skill.Truncated = true
		}
		if content != "" && totalBytes+size > advancedChatAgentSkillsMaxTotalBytes {
			break
		}
		name := strings.TrimSpace(skill.Name)
		if name == "" {
			name = path
		}
		if len([]rune(name)) > 120 {
			name = string([]rune(name)[:120])
		}
		description := strings.TrimSpace(skill.Description)
		if description == "" && content != "" {
			if manifest, _, _, _, ok := parseAdvancedChatSkillManifest(content); ok {
				description = strings.TrimSpace(manifest.Description)
			}
		}
		if len([]rune(description)) > 2000 {
			description = string([]rune(description)[:2000])
		}
		id := strings.TrimSpace(skill.ID)
		if len([]rune(id)) > 120 {
			id = string([]rune(id)[:120])
		}
		seenPaths[path] = true
		totalBytes += size
		result = append(result, advancedChatWorkspaceSkill{
			ID:          id,
			Name:        name,
			Description: description,
			Path:        path,
			Content:     content,
			Resources:   sanitizeWorkspaceSkillResourcePaths(skill.Resources),
			Size:        size,
			Truncated:   skill.Truncated,
		})
	}
	return result, nil
}

func parseAdvancedChatWorkspaceSkillRead(raw string) (advancedChatWorkspaceSkill, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return advancedChatWorkspaceSkill{}, errors.New("connector returned empty skill")
	}
	var payload struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Path        string   `json:"path"`
		Content     string   `json:"content"`
		Resources   []string `json:"resource_paths"`
		Size        int      `json:"size"`
		Truncated   bool     `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return advancedChatWorkspaceSkill{}, err
	}
	content := strings.TrimSpace(payload.Content)
	if content == "" {
		return advancedChatWorkspaceSkill{}, errors.New("connector skill content is empty")
	}
	if len([]byte(content)) > advancedChatAgentSkillsMaxFileBytes {
		content = truncateBytes(content, advancedChatAgentSkillsMaxFileBytes)
		payload.Truncated = true
	}
	description := strings.TrimSpace(payload.Description)
	if description == "" {
		if manifest, _, _, _, ok := parseAdvancedChatSkillManifest(content); ok {
			description = strings.TrimSpace(manifest.Description)
		}
	}
	return advancedChatWorkspaceSkill{
		ID:          strings.TrimSpace(payload.ID),
		Name:        truncateRunes(payload.Name, 120),
		Description: truncateRunes(description, 2000),
		Path:        sanitizeWorkspaceSkillPath(payload.Path),
		Content:     content,
		Resources:   sanitizeWorkspaceSkillResourcePaths(payload.Resources),
		Size:        len([]byte(content)),
		Truncated:   payload.Truncated,
	}, nil
}

func sanitizeWorkspaceSkillResourcePaths(paths []string) []string {
	result := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, item := range paths {
		path := sanitizeWorkspaceSkillPath(item)
		if path == "" || strings.EqualFold(path, "SKILL.md") || seen[path] {
			continue
		}
		seen[path] = true
		result = append(result, path)
		if len(result) >= 100 {
			break
		}
	}
	return result
}

func sanitizeWorkspaceSkillPath(path string) string {
	path = strings.TrimSpace(strings.ReplaceAll(path, "\\", "/"))
	if path == "" || strings.HasPrefix(path, "/") || strings.Contains(path, "\x00") {
		return ""
	}
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return ""
		}
	}
	if !strings.HasPrefix(strings.ToLower(path), ".agents/") || !strings.HasSuffix(strings.ToLower(path), ".md") {
		return ""
	}
	if len([]rune(path)) > 500 {
		return ""
	}
	return path
}

func advancedChatConnectorToolPreviewArguments(ctx context.Context, userID uint, runID string, binding advancedChatConnectorToolBinding, arguments map[string]interface{}) map[string]interface{} {
	if binding.Action != "write_file" {
		return arguments
	}
	if !advancedChatAssistantConnectorReadFileEnabled() {
		return arguments
	}
	path, _ := arguments["path"].(string)
	if strings.TrimSpace(path) == "" {
		return arguments
	}
	previewArguments := cloneAdvancedChatConnectorArguments(arguments)
	readBinding := binding
	readBinding.Action = "read_file"
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	content, err := callAdvancedChatConnectorTool(readCtx, userID, runID, readBinding, map[string]interface{}{
		"path":      path,
		"max_bytes": 200000,
	})
	if err != nil {
		previewArguments[advancedChatConnectorPreviewOldContentAvailable] = false
		return previewArguments
	}
	previewArguments[advancedChatConnectorPreviewOldContent] = content
	previewArguments[advancedChatConnectorPreviewOldContentAvailable] = true
	return previewArguments
}

func advancedChatConnectorArgumentsWithToolCallID(arguments map[string]interface{}, toolCallID string) map[string]interface{} {
	if strings.TrimSpace(toolCallID) == "" {
		return arguments
	}
	previewArguments := cloneAdvancedChatConnectorArguments(arguments)
	previewArguments[advancedChatConnectorPreviewToolCallID] = toolCallID
	return previewArguments
}

func advancedChatConnectorArgumentsWithTaskID(arguments map[string]interface{}, taskID string) map[string]interface{} {
	if strings.TrimSpace(taskID) == "" {
		return arguments
	}
	previewArguments := cloneAdvancedChatConnectorArguments(arguments)
	previewArguments[advancedChatConnectorTaskID] = taskID
	return previewArguments
}

func cloneAdvancedChatConnectorArguments(arguments map[string]interface{}) map[string]interface{} {
	clone := make(map[string]interface{}, len(arguments))
	for key, value := range arguments {
		clone[key] = value
	}
	return clone
}

func stripAdvancedChatConnectorPreviewFields(payload map[string]interface{}) map[string]interface{} {
	if len(payload) == 0 {
		return payload
	}
	sanitized := make(map[string]interface{}, len(payload))
	for key, value := range payload {
		if key == advancedChatConnectorPreviewOldContent || key == advancedChatConnectorPreviewOldContentAvailable || key == advancedChatConnectorPreviewToolCallID || key == advancedChatConnectorTaskID || key == advancedChatAgentStudioSandboxIDArg || key == advancedChatAgentStudioSandboxBackendArg || key == "packages" {
			continue
		}
		if key == "server" {
			sanitized[key] = stripAdvancedChatConnectorMCPServerPayload(value)
			continue
		}
		sanitized[key] = value
	}
	return sanitized
}

func stripAdvancedChatConnectorMCPServerPayload(value interface{}) interface{} {
	server, ok := value.(map[string]interface{})
	if !ok {
		return value
	}
	sanitized := make(map[string]interface{}, len(server))
	for key, item := range server {
		if key == "env" {
			if env, ok := item.(map[string]interface{}); ok && len(env) > 0 {
				redacted := make(map[string]interface{}, len(env))
				for envKey := range env {
					redacted[envKey] = "********"
				}
				sanitized[key] = redacted
				continue
			}
		}
		sanitized[key] = item
	}
	return sanitized
}

func expandAdvancedChatConnectorToolArguments(binding advancedChatConnectorToolBinding, arguments map[string]interface{}) []map[string]interface{} {
	if binding.Action != "replace_text" {
		return []map[string]interface{}{arguments}
	}
	raw, ok := arguments["replacements"].([]interface{})
	if !ok || len(raw) == 0 {
		return []map[string]interface{}{arguments}
	}
	calls := make([]map[string]interface{}, 0, len(raw))
	defaultPath, _ := arguments["path"].(string)
	for _, item := range raw {
		row, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		oldText, _ := row["old_text"].(string)
		newText, _ := row["new_text"].(string)
		path, _ := row["path"].(string)
		if strings.TrimSpace(path) == "" {
			path = defaultPath
		}
		if oldText == "" && newText == "" {
			continue
		}
		calls = append(calls, map[string]interface{}{
			"path":     path,
			"old_text": oldText,
			"new_text": newText,
		})
	}
	if len(calls) == 0 {
		return []map[string]interface{}{arguments}
	}
	return calls
}

func advancedChatConnectorTaskRequiresApproval(binding advancedChatConnectorToolBinding, arguments map[string]interface{}) bool {
	approvalMode := normalizeAdvancedChatConnectorApprovalMode(binding.ApprovalMode)
	if approvalMode == advancedChatConnectorApprovalFullAccess {
		return false
	}
	switch binding.Action {
	case "list_files", "read_file", "file_sha256", "web_search", "web_fetch", "list_agent_skills", "read_agent_skill", "read_agent_skill_resource", "sync_agent_skills", "list_windows_drives":
		return false
	case "list_static_sites":
		return false
	case "run_command":
		if approvalMode == advancedChatConnectorApprovalAssistant {
			return true
		}
		command, _ := arguments["command"].(string)
		return !connectorCommandAutoApproved(command, binding.CommandPrefixes)
	case "write_file", "replace_text", "commit_delta", "deploy_static_site", "set_static_site_enabled", "delete_static_site":
		return !binding.AutoApprove
	default:
		return true
	}
}

func normalizeAdvancedChatConnectorApprovalMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case advancedChatConnectorApprovalFullAccess:
		return advancedChatConnectorApprovalFullAccess
	case advancedChatConnectorApprovalAssistant:
		return advancedChatConnectorApprovalAssistant
	default:
		return advancedChatConnectorApprovalManual
	}
}

func legacyConnectorApprovalMode(autoApprove bool) string {
	if autoApprove {
		return advancedChatConnectorApprovalFullAccess
	}
	return advancedChatConnectorApprovalManual
}

func connectorCommandAutoApproved(command string, prefixes []string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	for _, prefix := range normalizeConnectorCommandPrefixes(prefixes) {
		if strings.HasPrefix(command, prefix) {
			return true
		}
	}
	return false
}

func waitAdvancedChatConnectorTask(ctx context.Context, taskID string, userID uint) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, advancedChatConnectorTaskWait)
	defer cancel()
	ticker := time.NewTicker(time.Second)
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
				return task.Result, errors.New("connector task failed")
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
					"error_message": "connector task timed out",
					"finished_at":   &now,
					"updated_at":    now,
				}).Error
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

func advancedChatConnectorSystemPrompt(device *AdvancedChatConnectorDevice, workspacePath string) string {
	if device == nil {
		return ""
	}
	workspacePath = strings.TrimSpace(workspacePath)
	osName := strings.TrimSpace(device.OS)
	if osName == "" {
		osName = "unknown"
	}
	archName := strings.TrimSpace(device.Arch)
	if archName == "" {
		archName = "unknown"
	}
	if workspacePath == "" {
		windowsPathHint := ""
		if strings.EqualFold(device.OS, "windows") {
			windowsPathHint = "\nThe connected device is Windows. Use workspace_list_windows_drives to discover available drive roots before selecting absolute paths when the drive is not already known."
		}
		return fmt.Sprintf(`A local device connector is available without a workspace limit.
Device: %s
Environment: OS=%s Arch=%s
Use workspace tools when you need to inspect or edit files on this device.
Absolute paths are allowed. Ask for or infer concrete paths before reading or changing files.%s
Read-only workspace tools, web search, web fetch, and Windows drive listing do not require approval. The user will be asked in the message channel to reply yes before file operations that change files are sent to the local connector, unless the message channel enables automatic approval. Commands always require approval unless the command starts with a prefix explicitly allowed in the message channel settings.`, device.Name, osName, archName, windowsPathHint)
	}
	return fmt.Sprintf(`A local workspace connector is available.
Device: %s
Environment: OS=%s Arch=%s
Workspace: %s
Use workspace tools when you need to inspect or edit files in this workspace.
Use only relative paths in workspace tool arguments.
Read-only workspace tools, web search, and web fetch do not require approval. The web frontend will ask the user for approval before file operations that change files are sent to the local connector, unless the session enables automatic approval. Commands always require approval unless the command starts with a prefix explicitly allowed in the session settings.`, device.Name, osName, archName, workspacePath)
}

func advancedChatWorkspaceGitStatus(ctx context.Context, userID uint, device *AdvancedChatConnectorDevice, workspacePath string, compareBranch string) (advancedChatWorkspaceGitStatusResponse, error) {
	compareBranch = strings.TrimSpace(compareBranch)
	if !validAdvancedChatGitRef(compareBranch) {
		return advancedChatWorkspaceGitStatusResponse{}, errors.New("invalid comparison branch")
	}
	statusOutput, err := callAdvancedChatWorkspaceGitReadCommand(ctx, userID, device, workspacePath, "git status --porcelain=v1 --branch")
	if err != nil {
		return advancedChatWorkspaceGitStatusResponse{}, fmt.Errorf("git status failed: %w", err)
	}
	branchesOutput, err := callAdvancedChatWorkspaceGitReadCommand(ctx, userID, device, workspacePath, "git branch --no-color")
	if err != nil {
		return advancedChatWorkspaceGitStatusResponse{}, fmt.Errorf("git branch failed: %w", err)
	}
	diffCommand := "git diff --numstat && git diff --cached --numstat"
	if compareBranch != "" {
		diffCommand = "git diff --numstat " + compareBranch + "...HEAD"
	}
	diffOutput, err := callAdvancedChatWorkspaceGitReadCommand(ctx, userID, device, workspacePath, diffCommand)
	if err != nil {
		return advancedChatWorkspaceGitStatusResponse{}, fmt.Errorf("git diff failed: %w", err)
	}
	branch, changedFiles := parseAdvancedChatGitStatus(statusOutput)
	additions, deletions := parseAdvancedChatGitNumstat(diffOutput)
	return advancedChatWorkspaceGitStatusResponse{
		CurrentBranch: branch,
		CompareBranch: compareBranch,
		Branches:      parseAdvancedChatGitBranches(branchesOutput),
		ChangedFiles:  changedFiles,
		Additions:     additions,
		Deletions:     deletions,
		Clean:         changedFiles == 0 && additions == 0 && deletions == 0,
	}, nil
}

func callAdvancedChatWorkspaceDirectoryRoots(ctx context.Context, userID uint, device *AdvancedChatConnectorDevice) (string, error) {
	binding := advancedChatConnectorToolBinding{
		DeviceID:      device.ID,
		DeviceName:    device.Name,
		Action:        "list_windows_drives",
		ApprovalMode:  advancedChatConnectorApprovalManual,
		WorkspacePath: "",
	}
	return callAdvancedChatConnectorTool(ctx, userID, "", binding, map[string]interface{}{})
}

func callAdvancedChatWorkspaceDirectoryListing(ctx context.Context, userID uint, device *AdvancedChatConnectorDevice, directoryPath string) (string, error) {
	binding := advancedChatConnectorToolBinding{
		DeviceID:      device.ID,
		DeviceName:    device.Name,
		Action:        "list_files",
		ApprovalMode:  advancedChatConnectorApprovalManual,
		WorkspacePath: "",
	}
	return callAdvancedChatConnectorTool(ctx, userID, "", binding, map[string]interface{}{
		"path":        directoryPath,
		"max_entries": 500,
	})
}

func parseAdvancedChatWindowsDriveDirectories(value string) []advancedChatWorkspaceDirectoryEntry {
	directories := []advancedChatWorkspaceDirectoryEntry{}
	for _, line := range strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n") {
		root := strings.TrimSpace(line)
		if len(root) < 3 || root[1] != ':' || (root[2] != '\\' && root[2] != '/') {
			continue
		}
		directories = append(directories, advancedChatWorkspaceDirectoryEntry{Name: root, Path: root})
	}
	return directories
}

func parseAdvancedChatWorkspaceDirectories(value string, parentPath string, windows bool) []advancedChatWorkspaceDirectoryEntry {
	directories := []advancedChatWorkspaceDirectoryEntry{}
	for _, line := range strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) < 2 || fields[0] != "dir" {
			continue
		}
		name := strings.TrimSpace(fields[1])
		if name == "" {
			continue
		}
		directories = append(directories, advancedChatWorkspaceDirectoryEntry{
			Name: name,
			Path: advancedChatWorkspaceDirectoryJoin(parentPath, name, windows),
		})
	}
	return directories
}

func advancedChatWorkspaceDirectoryJoin(parentPath string, name string, windows bool) string {
	if windows {
		parentPath = strings.TrimRight(strings.TrimSpace(parentPath), "\\/")
		if len(parentPath) == 2 && parentPath[1] == ':' {
			return parentPath + "\\" + name
		}
		return parentPath + "\\" + name
	}
	return path.Join(parentPath, name)
}

func callAdvancedChatWorkspaceGitReadCommand(ctx context.Context, userID uint, device *AdvancedChatConnectorDevice, workspacePath string, command string) (string, error) {
	binding := advancedChatConnectorToolBinding{
		DeviceID:        device.ID,
		DeviceName:      device.Name,
		WorkspacePath:   workspacePath,
		Action:          "run_command",
		ApprovalMode:    advancedChatConnectorApprovalManual,
		CommandPrefixes: []string{command},
	}
	return callAdvancedChatConnectorTool(ctx, userID, "", binding, map[string]interface{}{"command": command})
}

func advancedChatWorkspaceGitActionCommand(action string, message string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "commit":
		message = strings.TrimSpace(message)
		if message == "" || len([]rune(message)) > 200 || strings.ContainsAny(message, "\r\n\"") {
			return "", errors.New("commit message must be 1-200 characters and cannot contain quotes or line breaks")
		}
		return "git add -A; git commit -m \"" + message + "\"", nil
	case "push":
		return "git push", nil
	default:
		return "", errors.New("unsupported Git action")
	}
}

func validAdvancedChatGitRef(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 160 || strings.HasPrefix(value, "-") || strings.Contains(value, "..") || strings.ContainsAny(value, " ~^:?*[]\\\t\r\n") {
		return false
	}
	for _, char := range value {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-' || char == '/') {
			return false
		}
	}
	return true
}

func parseAdvancedChatGitStatus(value string) (string, int) {
	branch := ""
	changedFiles := 0
	for _, line := range strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "## ") {
			branch = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			if index := strings.Index(branch, "..."); index >= 0 {
				branch = branch[:index]
			}
			continue
		}
		if line != "" {
			changedFiles++
		}
	}
	return branch, changedFiles
}

func parseAdvancedChatGitNumstat(value string) (int, int) {
	additions := 0
	deletions := 0
	for _, line := range strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if added, err := strconv.Atoi(fields[0]); err == nil {
			additions += added
		}
		if removed, err := strconv.Atoi(fields[1]); err == nil {
			deletions += removed
		}
	}
	return additions, deletions
}

func parseAdvancedChatGitBranches(value string) []string {
	branches := []string{}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n") {
		branch := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "*"))
		if branch == "" || strings.Contains(branch, " -> ") || seen[branch] {
			continue
		}
		seen[branch] = true
		branches = append(branches, branch)
	}
	return branches
}

func normalizeConnectorCommandPrefixes(values []string) []string {
	result := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func newAdvancedChatConnectorToken() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	encoded := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(data))
	return "wpc_" + encoded, nil
}

func hashAdvancedChatConnectorToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

func truncateConnectorField(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes])
	}
	return value
}

func truncateConnectorTaskText(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > 200000 {
		return string(runes[:200000]) + "\n...(truncated)"
	}
	return value
}

func truncateBytes(value string, maxBytes int) string {
	if maxBytes <= 0 || len([]byte(value)) <= maxBytes {
		return value
	}
	total := 0
	var builder strings.Builder
	for _, r := range value {
		size := len(string(r))
		if total+size > maxBytes {
			break
		}
		builder.WriteRune(r)
		total += size
	}
	return builder.String()
}

func normalizeConnectorTaskResultText(input advancedChatConnectorTaskResultInput) string {
	sections := make([]string, 0, 4)
	if text := strings.TrimSpace(input.Result); text != "" {
		sections = append(sections, text)
	}
	if text := strings.TrimSpace(input.Output); text != "" && !connectorTaskSectionAlreadyIncluded(sections, text) {
		sections = append(sections, text)
	}
	if text := strings.TrimSpace(input.Stdout); text != "" && !connectorTaskSectionAlreadyIncluded(sections, text) {
		sections = append(sections, "stdout:\n"+text)
	}
	if text := strings.TrimSpace(input.Stderr); text != "" && !connectorTaskSectionAlreadyIncluded(sections, text) {
		sections = append(sections, "stderr:\n"+text)
	}
	return truncateConnectorTaskText(strings.Join(sections, "\n\n"))
}

func normalizeConnectorTaskErrorMessage(input advancedChatConnectorTaskResultInput) string {
	message := strings.TrimSpace(input.Error)
	if input.ExitCode != nil {
		exitMessage := fmt.Sprintf("exit code %d", *input.ExitCode)
		if message == "" {
			message = exitMessage
		} else if !strings.Contains(strings.ToLower(message), strings.ToLower(exitMessage)) {
			message += "; " + exitMessage
		}
	}
	return truncateConnectorTaskText(message)
}

func connectorTaskSectionAlreadyIncluded(sections []string, text string) bool {
	for _, section := range sections {
		if strings.TrimSpace(section) == text {
			return true
		}
	}
	return false
}
