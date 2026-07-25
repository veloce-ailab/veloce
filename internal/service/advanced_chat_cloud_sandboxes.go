package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/cache"
	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

// AdvancedChatCloudSandboxHost is an administrator-owned sandboxd worker. It
// deliberately references a connector identity so sandboxd can reuse the
// existing connector authentication, heartbeat, polling, and task protocol.
type AdvancedChatCloudSandboxHost struct {
	ID                 string          `gorm:"primaryKey;size:80" json:"id"`
	Name               string          `gorm:"size:120;not null" json:"name"`
	ConnectorDeviceID  string          `gorm:"uniqueIndex;size:80;not null" json:"connector_device_id"`
	Enabled            bool            `gorm:"not null;default:true;index" json:"enabled"`
	SecurityPolicy     string          `gorm:"type:text;not null;default:'{}'" json:"security_policy"`
	RuntimePriceHour   decimal.Decimal `gorm:"type:decimal(20,10);not null;default:0" json:"runtime_price_hour"`
	CPUPriceHour       decimal.Decimal `gorm:"type:decimal(20,10);not null;default:0" json:"cpu_price_hour"`
	MemoryPriceGBHour  decimal.Decimal `gorm:"type:decimal(20,10);not null;default:0" json:"memory_price_gb_hour"`
	StoragePriceGBHour decimal.Decimal `gorm:"type:decimal(20,10);not null;default:0" json:"storage_price_gb_hour"`
	RuntimeMultiplier  decimal.Decimal `gorm:"type:decimal(20,10);not null;default:1" json:"runtime_multiplier"`
	LastSeenAt         *time.Time      `json:"last_seen_at,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

type AdvancedChatCloudSandbox struct {
	ID               string          `gorm:"primaryKey;size:80" json:"id"`
	UserID           uint            `gorm:"index;not null" json:"user_id"`
	HostID           string          `gorm:"index;size:80;not null" json:"host_id"`
	Name             string          `gorm:"size:120;not null" json:"name"`
	Image            string          `gorm:"size:255;not null" json:"image"`
	CPUCores         decimal.Decimal `gorm:"type:decimal(10,3);not null" json:"cpu_cores"`
	MemoryMB         int             `gorm:"not null" json:"memory_mb"`
	DiskGB           int             `gorm:"not null" json:"disk_gb"`
	Status           string          `gorm:"size:20;index;not null" json:"status"`
	StorageChargedAt *time.Time      `json:"storage_charged_at,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

type AdvancedChatCloudSandboxCharge struct {
	ID           string          `gorm:"primaryKey;size:80" json:"id"`
	UserID       uint            `gorm:"index;not null" json:"user_id"`
	SandboxID    string          `gorm:"index;size:80;not null" json:"sandbox_id"`
	HostID       string          `gorm:"index;size:80;not null" json:"host_id"`
	Kind         string          `gorm:"size:20;index;not null" json:"kind"`
	Reference    string          `gorm:"uniqueIndex;size:120;not null" json:"reference"`
	Quantity     decimal.Decimal `gorm:"type:decimal(20,10);not null" json:"quantity"`
	Cost         decimal.Decimal `gorm:"type:decimal(20,10);not null" json:"cost"`
	RateSnapshot string          `gorm:"type:text;not null" json:"rate_snapshot"`
	CreatedAt    time.Time       `json:"created_at"`
}

const (
	advancedChatConnectorModeSandboxd = "sandboxd"
	advancedChatCloudSandboxReady     = "ready"
	advancedChatCloudSandboxDeleted   = "deleted"
)

type advancedChatCloudSandboxHostInput struct {
	Name               string          `json:"name"`
	SecurityPolicy     json.RawMessage `json:"security_policy"`
	RuntimePriceHour   decimal.Decimal `json:"runtime_price_hour"`
	CPUPriceHour       decimal.Decimal `json:"cpu_price_hour"`
	MemoryPriceGBHour  decimal.Decimal `json:"memory_price_gb_hour"`
	StoragePriceGBHour decimal.Decimal `json:"storage_price_gb_hour"`
	RuntimeMultiplier  decimal.Decimal `json:"runtime_multiplier"`
	Enabled            *bool           `json:"enabled"`
}

type advancedChatCloudSandboxInput struct {
	HostID   string          `json:"host_id"`
	Name     string          `json:"name"`
	Image    string          `json:"image"`
	CPUCores decimal.Decimal `json:"cpu_cores"`
	MemoryMB int             `json:"memory_mb"`
	DiskGB   int             `json:"disk_gb"`
}

func registerAdvancedChatCloudSandboxAdminRoutes(group *gin.RouterGroup) {
	api := &advancedChatAPI{}
	group.GET("/advanced-chat/sandbox-hosts", api.listCloudSandboxHosts)
	group.POST("/advanced-chat/sandbox-hosts", api.createCloudSandboxHost)
	group.PUT("/advanced-chat/sandbox-hosts/:id", api.updateCloudSandboxHost)
	group.POST("/advanced-chat/sandbox-hosts/:id/token", api.rotateCloudSandboxHostToken)
}

// RegisterCloudSandboxAdminRoutes keeps the managed-sandbox control plane
// independent from the legacy advanced-chat feature registration path.
func RegisterCloudSandboxAdminRoutes(group *gin.RouterGroup) {
	registerAdvancedChatCloudSandboxAdminRoutes(group)
}

func registerAdvancedChatCloudSandboxUserRoutes(group *gin.RouterGroup) {
	api := &advancedChatAPI{}
	group.GET("/advanced-chat/sandbox-hosts/available", api.listAvailableCloudSandboxHosts)
	group.GET("/advanced-chat/cloud-sandboxes", api.listCloudSandboxes)
	group.POST("/advanced-chat/cloud-sandboxes", api.createCloudSandbox)
	group.GET("/advanced-chat/cloud-sandboxes/:id", api.getCloudSandbox)
	group.DELETE("/advanced-chat/cloud-sandboxes/:id", api.deleteCloudSandbox)
	group.GET("/advanced-chat/cloud-sandboxes/:id/charges", api.listCloudSandboxCharges)
}

// RegisterCloudSandboxUserRoutes registers user sandbox management directly.
func RegisterCloudSandboxUserRoutes(group *gin.RouterGroup) {
	registerAdvancedChatCloudSandboxUserRoutes(group)
}

// InitCloudSandboxFeatures owns its schema independently from the legacy
// advanced-chat startup hooks.
func InitCloudSandboxFeatures() error {
	return model.DB.AutoMigrate(
		&AdvancedChatCloudSandboxHost{},
		&AdvancedChatCloudSandbox{},
		&AdvancedChatCloudSandboxCharge{},
	)
}

func requireCloudSandboxAdmin(c *gin.Context) (*model.User, bool) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return nil, false
	}
	if !user.IsAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "Admin permission required"})
		return nil, false
	}
	return user, true
}

func normalizeCloudSandboxSecurityPolicy(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "" {
		return `{"runtime":"docker","network":"none","read_only_rootfs":false,"pids_limit":256}`, nil
	}
	var value map[string]interface{}
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", errors.New("security_policy must be a JSON object")
	}
	encoded, err := json.Marshal(value)
	return string(encoded), err
}

func (api *advancedChatAPI) listCloudSandboxHosts(c *gin.Context) {
	if _, ok := requireCloudSandboxAdmin(c); !ok {
		return
	}
	var hosts []AdvancedChatCloudSandboxHost
	if err := model.DB.Order("created_at DESC").Find(&hosts).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list sandbox hosts"})
		return
	}
	c.JSON(http.StatusOK, hosts)
}

func (api *advancedChatAPI) createCloudSandboxHost(c *gin.Context) {
	admin, ok := requireCloudSandboxAdmin(c)
	if !ok {
		return
	}
	var input advancedChatCloudSandboxHostInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	name := truncateConnectorField(input.Name, 120)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Host name is required"})
		return
	}
	policy, err := normalizeCloudSandboxSecurityPolicy(input.SecurityPolicy)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if input.RuntimeMultiplier.LessThanOrEqual(decimal.Zero) {
		input.RuntimeMultiplier = decimal.NewFromInt(1)
	}
	token, err := newAdvancedChatConnectorToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create sandbox host token"})
		return
	}
	now := time.Now()
	device := AdvancedChatConnectorDevice{ID: newAdvancedChatID("acd"), UserID: admin.ID, TokenHash: hashAdvancedChatConnectorToken(token), Name: name, Kind: advancedChatConnectorDeviceKindCLI, Mode: advancedChatConnectorModeSandboxd, Status: advancedChatConnectorDeviceStatusOffline, Workspaces: "[]", CreatedAt: now, UpdatedAt: now}
	host := AdvancedChatCloudSandboxHost{ID: newAdvancedChatID("ash"), Name: name, ConnectorDeviceID: device.ID, Enabled: input.Enabled == nil || *input.Enabled, SecurityPolicy: policy, RuntimePriceHour: input.RuntimePriceHour, CPUPriceHour: input.CPUPriceHour, MemoryPriceGBHour: input.MemoryPriceGBHour, StoragePriceGBHour: input.StoragePriceGBHour, RuntimeMultiplier: input.RuntimeMultiplier, CreatedAt: now, UpdatedAt: now}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&device).Error; err != nil {
			return err
		}
		return tx.Create(&host).Error
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create sandbox host"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"host": host, "token": token, "command": "veloce-app -mode sandboxd -server <server-url> -token " + token})
}

func (api *advancedChatAPI) updateCloudSandboxHost(c *gin.Context) {
	if _, ok := requireCloudSandboxAdmin(c); !ok {
		return
	}
	var input advancedChatCloudSandboxHostInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var host AdvancedChatCloudSandboxHost
	if err := model.DB.Where("id = ?", strings.TrimSpace(c.Param("id"))).First(&host).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Sandbox host not found"})
		return
	}
	updates := map[string]interface{}{"updated_at": time.Now()}
	if strings.TrimSpace(input.Name) != "" {
		updates["name"] = truncateConnectorField(input.Name, 120)
	}
	if input.Enabled != nil {
		updates["enabled"] = *input.Enabled
	}
	if input.SecurityPolicy != nil {
		policy, err := normalizeCloudSandboxSecurityPolicy(input.SecurityPolicy)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		updates["security_policy"] = policy
	}
	updates["runtime_price_hour"] = input.RuntimePriceHour
	updates["cpu_price_hour"] = input.CPUPriceHour
	updates["memory_price_gb_hour"] = input.MemoryPriceGBHour
	updates["storage_price_gb_hour"] = input.StoragePriceGBHour
	if input.RuntimeMultiplier.GreaterThan(decimal.Zero) {
		updates["runtime_multiplier"] = input.RuntimeMultiplier
	}
	if err := model.DB.Model(&host).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update sandbox host"})
		return
	}
	if name, ok := updates["name"]; ok {
		_ = model.DB.Model(&AdvancedChatConnectorDevice{}).Where("id = ?", host.ConnectorDeviceID).Update("name", name)
	}
	if err := model.DB.Where("id = ?", host.ID).First(&host).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load sandbox host"})
		return
	}
	c.JSON(http.StatusOK, host)
}

func (api *advancedChatAPI) rotateCloudSandboxHostToken(c *gin.Context) {
	if _, ok := requireCloudSandboxAdmin(c); !ok {
		return
	}
	var host AdvancedChatCloudSandboxHost
	if err := model.DB.Where("id = ?", strings.TrimSpace(c.Param("id"))).First(&host).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Sandbox host not found"})
		return
	}
	token, err := newAdvancedChatConnectorToken()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create sandbox host token"})
		return
	}
	if err := model.DB.Model(&AdvancedChatConnectorDevice{}).Where("id = ? AND mode = ?", host.ConnectorDeviceID, advancedChatConnectorModeSandboxd).Updates(map[string]interface{}{"token_hash": hashAdvancedChatConnectorToken(token), "updated_at": time.Now()}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to rotate sandbox host token"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"host": host, "token": token, "command": "veloce-app -mode sandboxd -server <server-url> -token " + token})
}

func (api *advancedChatAPI) listAvailableCloudSandboxHosts(c *gin.Context) {
	if _, ok := currentAdvancedChatUser(c); !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var hosts []AdvancedChatCloudSandboxHost
	if err := model.DB.Where("enabled = ?", true).Order("name ASC").Find(&hosts).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list sandbox hosts"})
		return
	}
	c.JSON(http.StatusOK, hosts)
}

func validateCloudSandboxInput(input advancedChatCloudSandboxInput) error {
	if strings.TrimSpace(input.HostID) == "" {
		return errors.New("Sandbox host is required")
	}
	if strings.TrimSpace(input.Name) == "" {
		return errors.New("Sandbox name is required")
	}
	if strings.TrimSpace(input.Image) == "" {
		return errors.New("Container image is required")
	}
	if input.CPUCores.LessThanOrEqual(decimal.Zero) || input.CPUCores.GreaterThan(decimal.NewFromInt(64)) {
		return errors.New("CPU cores must be between 0 and 64")
	}
	if input.MemoryMB < 128 || input.MemoryMB > 262144 {
		return errors.New("Memory must be between 128 MB and 262144 MB")
	}
	if input.DiskGB < 1 || input.DiskGB > 4096 {
		return errors.New("Disk must be between 1 GB and 4096 GB")
	}
	return nil
}

func (api *advancedChatAPI) listCloudSandboxes(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var rows []AdvancedChatCloudSandbox
	if err := model.DB.Where("user_id = ? AND status <> ?", user.ID, advancedChatCloudSandboxDeleted).Order("updated_at DESC").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list cloud sandboxes"})
		return
	}
	c.JSON(http.StatusOK, rows)
}

func (api *advancedChatAPI) createCloudSandbox(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var input advancedChatCloudSandboxInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := validateCloudSandboxInput(input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var host AdvancedChatCloudSandboxHost
	if err := model.DB.Where("id = ? AND enabled = ?", strings.TrimSpace(input.HostID), true).First(&host).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Sandbox host is unavailable"})
		return
	}
	sandbox := AdvancedChatCloudSandbox{ID: newAdvancedChatID("acs"), UserID: user.ID, HostID: host.ID, Name: truncateConnectorField(input.Name, 120), Image: strings.TrimSpace(input.Image), CPUCores: input.CPUCores, MemoryMB: input.MemoryMB, DiskGB: input.DiskGB, Status: advancedChatCloudSandboxReady, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := model.DB.Create(&sandbox).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create cloud sandbox"})
		return
	}
	c.JSON(http.StatusCreated, sandbox)
}

func loadCloudSandboxForUser(userID uint, sandboxID string) (AdvancedChatCloudSandbox, AdvancedChatCloudSandboxHost, AdvancedChatConnectorDevice, error) {
	var sandbox AdvancedChatCloudSandbox
	if err := model.DB.Where("id = ? AND user_id = ? AND status = ?", strings.TrimSpace(sandboxID), userID, advancedChatCloudSandboxReady).First(&sandbox).Error; err != nil {
		return sandbox, AdvancedChatCloudSandboxHost{}, AdvancedChatConnectorDevice{}, err
	}
	var host AdvancedChatCloudSandboxHost
	if err := model.DB.Where("id = ? AND enabled = ?", sandbox.HostID, true).First(&host).Error; err != nil {
		return sandbox, host, AdvancedChatConnectorDevice{}, err
	}
	var device AdvancedChatConnectorDevice
	if err := model.DB.Where("id = ? AND mode = ?", host.ConnectorDeviceID, advancedChatConnectorModeSandboxd).First(&device).Error; err != nil {
		return sandbox, host, device, err
	}
	return sandbox, host, device, nil
}

func (api *advancedChatAPI) getCloudSandbox(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	sandbox, _, _, err := loadCloudSandboxForUser(user.ID, c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Cloud sandbox not found"})
		return
	}
	c.JSON(http.StatusOK, sandbox)
}

func (api *advancedChatAPI) deleteCloudSandbox(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var sandbox AdvancedChatCloudSandbox
	if err := model.DB.Where("id = ? AND user_id = ? AND status <> ?", c.Param("id"), user.ID, advancedChatCloudSandboxDeleted).First(&sandbox).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Cloud sandbox not found"})
		return
	}
	if err := model.DB.Model(&sandbox).Updates(map[string]interface{}{"status": advancedChatCloudSandboxDeleted, "updated_at": time.Now()}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete cloud sandbox"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (api *advancedChatAPI) listCloudSandboxCharges(c *gin.Context) {
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var rows []AdvancedChatCloudSandboxCharge
	if err := model.DB.Where("user_id = ? AND sandbox_id = ?", user.ID, c.Param("id")).Order("created_at DESC").Limit(200).Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list sandbox charges"})
		return
	}
	c.JSON(http.StatusOK, rows)
}

func cloudSandboxTaskArguments(userID uint, sandboxID string, arguments map[string]interface{}) (map[string]interface{}, error) {
	sandbox, host, _, err := loadCloudSandboxForUser(userID, sandboxID)
	if err != nil {
		return nil, errors.New("cloud sandbox is unavailable")
	}
	result := make(map[string]interface{}, len(arguments)+2)
	for key, value := range arguments {
		result[key] = value
	}
	var policy map[string]interface{}
	if err := json.Unmarshal([]byte(host.SecurityPolicy), &policy); err != nil {
		return nil, errors.New("sandbox host security policy is invalid")
	}
	result["cloud_sandbox_id"] = sandbox.ID
	result["cloud_sandbox_spec"] = map[string]interface{}{"image": sandbox.Image, "cpu_cores": sandbox.CPUCores.String(), "memory_mb": sandbox.MemoryMB, "disk_gb": sandbox.DiskGB, "security_policy": policy}
	return result, nil
}

func syncCloudSandboxHostHeartbeat(deviceID string, seenAt time.Time) {
	_ = model.DB.Model(&AdvancedChatCloudSandboxHost{}).Where("connector_device_id = ?", deviceID).Updates(map[string]interface{}{"last_seen_at": &seenAt, "updated_at": seenAt}).Error
}

// recordCloudSandboxTaskCharge is deliberately idempotent: a connector can
// retry result delivery without charging a completed task twice.
func recordCloudSandboxTaskCharge(taskID string, finishedAt time.Time) error {
	var taskOwner AdvancedChatConnectorTask
	if err := model.DB.Select("user_id").Where("id = ?", taskID).First(&taskOwner).Error; err != nil {
		return err
	}
	billingContext := context.Background()
	release := cache.AcquireUserBillingLock(billingContext, taskOwner.UserID)
	defer release()

	var (
		balance    decimal.Decimal
		hasBalance bool
	)
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var task AdvancedChatConnectorTask
		if err := tx.Where("id = ?", taskID).First(&task).Error; err != nil {
			return err
		}
		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(task.Payload), &payload); err != nil {
			return nil
		}
		sandboxID, _ := payload["cloud_sandbox_id"].(string)
		if strings.TrimSpace(sandboxID) == "" || task.StartedAt == nil {
			return nil
		}
		var sandbox AdvancedChatCloudSandbox
		if err := tx.Where("id = ? AND user_id = ?", sandboxID, task.UserID).First(&sandbox).Error; err != nil {
			return err
		}
		var host AdvancedChatCloudSandboxHost
		if err := tx.Where("id = ?", sandbox.HostID).First(&host).Error; err != nil {
			return err
		}
		elapsed := finishedAt.Sub(*task.StartedAt)
		if elapsed < 0 {
			elapsed = 0
		}
		// Bill in whole minutes, with a one-minute minimum per dispatched task.
		minutes := decimal.NewFromInt(int64(elapsed / time.Minute))
		if elapsed%time.Minute != 0 {
			minutes = minutes.Add(decimal.NewFromInt(1))
		}
		if minutes.IsZero() {
			minutes = decimal.NewFromInt(1)
		}
		hours := minutes.Div(decimal.NewFromInt(60))
		memoryGB := decimal.NewFromInt(int64(sandbox.MemoryMB)).Div(decimal.NewFromInt(1024))
		rate := host.RuntimePriceHour.Add(host.CPUPriceHour.Mul(sandbox.CPUCores)).Add(host.MemoryPriceGBHour.Mul(memoryGB)).Mul(host.RuntimeMultiplier)
		cost := hours.Mul(rate)
		snapshot, _ := json.Marshal(map[string]string{"runtime_price_hour": host.RuntimePriceHour.String(), "cpu_price_hour": host.CPUPriceHour.String(), "memory_price_gb_hour": host.MemoryPriceGBHour.String(), "runtime_multiplier": host.RuntimeMultiplier.String()})
		charge := AdvancedChatCloudSandboxCharge{ID: newAdvancedChatID("asc"), UserID: task.UserID, SandboxID: sandbox.ID, HostID: host.ID, Kind: "runtime", Reference: "task:" + task.ID, Quantity: minutes, Cost: cost, RateSnapshot: string(snapshot), CreatedAt: finishedAt}
		if err := tx.Create(&charge).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return nil
			}
			return err
		}
		if cost.GreaterThan(decimal.Zero) {
			update := tx.Model(&model.User{}).Where("id = ? AND balance >= ?", task.UserID, cost).UpdateColumn("balance", gorm.Expr("balance - ?", cost))
			if update.Error != nil {
				return update.Error
			}
			if update.RowsAffected == 0 {
				return ErrInsufficientBalance
			}
		}
		if err := chargeCloudSandboxStorage(tx, sandbox, host, finishedAt); err != nil {
			return err
		}
		var err error
		balance, err = committedBillingBalance(tx, task.UserID)
		hasBalance = err == nil
		return err
	})
	if err != nil {
		cache.InvalidateUserBillingBalance(billingContext, taskOwner.UserID)
		return err
	}
	if hasBalance {
		cache.StoreUserBillingBalance(billingContext, taskOwner.UserID, balance)
	}
	return nil
}

// Storage is charged when a sandbox performs work. This keeps the first
// iteration scheduler-free; a periodic reconciliation can call the same
// helper later for inactive sandboxes.
func chargeCloudSandboxStorage(tx *gorm.DB, sandbox AdvancedChatCloudSandbox, host AdvancedChatCloudSandboxHost, now time.Time) error {
	if host.StoragePriceGBHour.LessThanOrEqual(decimal.Zero) {
		return nil
	}
	start := sandbox.CreatedAt
	if sandbox.StorageChargedAt != nil {
		start = *sandbox.StorageChargedAt
	}
	if !now.After(start) {
		return nil
	}
	hours := decimal.NewFromInt(now.Unix() - start.Unix()).Div(decimal.NewFromInt(3600))
	if hours.LessThanOrEqual(decimal.Zero) {
		return nil
	}
	cost := hours.Mul(decimal.NewFromInt(int64(sandbox.DiskGB))).Mul(host.StoragePriceGBHour)
	if cost.LessThanOrEqual(decimal.Zero) {
		return nil
	}
	snapshot, _ := json.Marshal(map[string]string{"storage_price_gb_hour": host.StoragePriceGBHour.String(), "disk_gb": decimal.NewFromInt(int64(sandbox.DiskGB)).String()})
	charge := AdvancedChatCloudSandboxCharge{ID: newAdvancedChatID("asc"), UserID: sandbox.UserID, SandboxID: sandbox.ID, HostID: host.ID, Kind: "storage", Reference: "storage:" + sandbox.ID + ":" + now.UTC().Format(time.RFC3339Nano), Quantity: hours, Cost: cost, RateSnapshot: string(snapshot), CreatedAt: now}
	if err := tx.Create(&charge).Error; err != nil {
		return err
	}
	update := tx.Model(&model.User{}).Where("id = ? AND balance >= ?", sandbox.UserID, cost).UpdateColumn("balance", gorm.Expr("balance - ?", cost))
	if update.Error != nil {
		return update.Error
	}
	if update.RowsAffected == 0 {
		return ErrInsufficientBalance
	}
	return tx.Model(&sandbox).Updates(map[string]interface{}{"storage_charged_at": &now, "updated_at": now}).Error
}
