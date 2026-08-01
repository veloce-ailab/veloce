package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/veloce-ailab/veloce/internal/config"
	"github.com/veloce-ailab/veloce/internal/model"
	"gorm.io/gorm"
)

const pluginMaxPackageBytes int64 = 100 << 20

var (
	pluginIDPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{1,79}$`)
	pluginHookPointPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:-]{1,120}$`)
	pluginChannelTypeID    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,39}$`)
)

type pluginAPI struct{}

type PluginManifest struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Version     string               `json:"version"`
	Description string               `json:"description"`
	Author      string               `json:"author"`
	GitHub      string               `json:"github,omitempty"`
	WASM        string               `json:"wasm"`
	Permissions []string             `json:"permissions"`
	Hooks       []PluginHook         `json:"hooks"`
	Frontend    json.RawMessage      `json:"frontend"`
	Settings    json.RawMessage      `json:"settings"`
	Channels    []PluginChannelType  `json:"channels"`
	Upstreams   []PluginUpstreamType `json:"upstreams"`
}

// PluginChannelType declares a message-channel provider implemented by a WASM
// plugin. The channel package reads this persisted manifest at runtime.
type PluginChannelType struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Description   string          `json:"description"`
	InboundAction string          `json:"inbound_action"`
	SendAction    string          `json:"send_action"`
	Config        json.RawMessage `json:"config"`
}

// PluginUpstreamType declares an AI provider implemented by a WASM plugin.
// The proxy invokes PrepareAction before forwarding the resulting request.
type PluginUpstreamType struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	Protocol       string          `json:"protocol"`
	DefaultBaseURL string          `json:"default_base_url,omitempty"`
	PrepareAction  string          `json:"prepare_action"`
	RefreshAction  string          `json:"refresh_action"`
	Config         json.RawMessage `json:"config,omitempty"`
}

type PluginHook struct {
	Point    string          `json:"point"`
	Mode     string          `json:"mode"`
	Action   string          `json:"action,omitempty"`
	Priority int             `json:"priority,omitempty"`
	Config   json.RawMessage `json:"config,omitempty"`
}

type pluginListItem struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Version     string               `json:"version"`
	Description string               `json:"description"`
	Author      string               `json:"author"`
	GitHub      string               `json:"github,omitempty"`
	Enabled     bool                 `json:"enabled"`
	Permissions []string             `json:"permissions"`
	Hooks       []PluginHook         `json:"hooks"`
	Frontend    json.RawMessage      `json:"frontend,omitempty"`
	Settings    json.RawMessage      `json:"settings,omitempty"`
	Upstreams   []PluginUpstreamType `json:"upstreams,omitempty"`
	LastError   string               `json:"last_error,omitempty"`
	CreatedAt   time.Time            `json:"created_at"`
	UpdatedAt   time.Time            `json:"updated_at"`
}

func init() {
	RegisterStartupHook(loadPluginsOnStartup)
	RegisterStartupHook(startPluginUpstreamRefresh)
	RegisterUserRouteHook(registerPluginUserRoutes)
	RegisterAdvancedChatRuntimeExtensionHook(pluginAdvancedChatRuntimeExtension)
}

func registerPluginUserRoutes(group *gin.RouterGroup) {
	api := &pluginAPI{}
	plugins := group.Group("/plugins")
	plugins.GET("", api.listPlugins)
	plugins.GET("/market", api.listPluginMarket)
	plugins.POST("/market/:id/install", api.installPluginFromMarket)
	plugins.GET("/frontend", api.frontendExtensions)
	plugins.GET("/updates", api.checkPluginUpdates)
	plugins.POST("", api.installPlugin)
	plugins.GET("/:id", api.getPlugin)
	plugins.POST("/:id/update", api.startPluginUpdate)
	plugins.GET("/:id/update/progress", api.pluginUpdateProgress)
	plugins.POST("/:id/enable", api.enablePlugin)
	plugins.POST("/:id/disable", api.disablePlugin)
	plugins.DELETE("/:id", api.uninstallPlugin)
	plugins.GET("/:id/settings", api.getPluginSettings)
	plugins.PUT("/:id/settings", api.updatePluginSettings)
	plugins.POST("/:id/actions/:action", api.runPluginAction)
}

func (api *pluginAPI) listPlugins(c *gin.Context) {
	user, ok := currentUserFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if !requirePluginAdmin(c, user) {
		return
	}
	var plugins []model.Plugin
	if err := model.DB.Order("created_at desc").Find(&plugins).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list plugins"})
		return
	}
	states := userPluginStates(user.ID)
	items := make([]pluginListItem, 0, len(plugins))
	for _, plugin := range plugins {
		items = append(items, pluginListResponse(plugin, pluginUserEnabled(plugin, states)))
	}
	c.JSON(http.StatusOK, gin.H{"plugins": items})
}

func (api *pluginAPI) frontendExtensions(c *gin.Context) {
	user, ok := currentUserFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var plugins []model.Plugin
	if err := model.DB.Where("enabled = ?", true).Order("created_at asc").Find(&plugins).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list plugins"})
		return
	}
	states := userPluginStates(user.ID)
	items := make([]pluginListItem, 0, len(plugins))
	for _, plugin := range plugins {
		if !pluginUserEnabled(plugin, states) || strings.TrimSpace(plugin.FrontendJSON) == "" {
			continue
		}
		item := pluginListResponse(plugin, true)
		item.Frontend = filterPluginFrontendForUser(item.Frontend, user.IsAdmin)
		if !pluginFrontendHasContent(item.Frontend) {
			continue
		}
		items = append(items, item)
	}
	c.JSON(http.StatusOK, gin.H{"plugins": items})
}

func (api *pluginAPI) getPlugin(c *gin.Context) {
	user, ok := currentUserFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	plugin, ok := loadPlugin(c)
	if !ok {
		return
	}
	if !pluginUserEnabled(plugin, userPluginStates(user.ID)) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Plugin is disabled"})
		return
	}
	item := pluginListResponse(plugin, true)
	item.Frontend = filterPluginFrontendForUser(item.Frontend, user.IsAdmin)
	c.JSON(http.StatusOK, item)
}

func (api *pluginAPI) installPlugin(c *gin.Context) {
	user, ok := currentUserFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if !requirePluginAdmin(c, user) {
		return
	}
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Plugin WASM file is required"})
		return
	}
	if file.Size > pluginMaxPackageBytes {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Plugin WASM is too large"})
		return
	}
	src, err := file.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to open plugin package"})
		return
	}
	defer src.Close()

	tmpRoot := filepath.Join(config.DataPath, "tmp")
	if err := os.MkdirAll(tmpRoot, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to prepare plugin temp directory"})
		return
	}
	tmpDir, err := os.MkdirTemp(tmpRoot, "plugin-*")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to prepare plugin temp directory"})
		return
	}
	defer os.RemoveAll(tmpDir)

	manifest, manifestRaw, tempWASMPath, err := prepareUploadedPlugin(c.Request.Context(), src, file.Filename, tmpDir)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := validatePluginManifest(manifest); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	wasmPath := pluginWASMPath(manifest.ID)
	if err := removePluginModuleFiles(manifest.ID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to replace old plugin files"})
		return
	}
	if err := os.MkdirAll(filepath.Dir(wasmPath), 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to prepare plugin directory"})
		return
	}
	if err := os.Rename(tempWASMPath, wasmPath); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to install plugin WASM"})
		return
	}

	permissionsJSON := mustJSON(manifest.Permissions)
	hooksJSON := mustJSON(manifest.Hooks)
	plugin := model.Plugin{
		ID:              manifest.ID,
		Name:            manifest.Name,
		Version:         manifest.Version,
		Description:     manifest.Description,
		Author:          manifest.Author,
		Enabled:         true,
		ManifestJSON:    string(manifestRaw),
		PermissionsJSON: permissionsJSON,
		HooksJSON:       hooksJSON,
		FrontendJSON:    string(manifest.Frontend),
		SettingsJSON:    string(manifest.Settings),
		Path:            filepath.Dir(wasmPath),
		WASMPath:        wasmPath,
	}
	var existingPlugin model.Plugin
	if err := model.DB.Select("global_config_json").Where("id = ?", plugin.ID).Limit(1).Find(&existingPlugin).Error; err == nil {
		plugin.GlobalConfigJSON = existingPlugin.GlobalConfigJSON
	}
	if err := model.DB.Where(&model.Plugin{ID: plugin.ID}).Assign(plugin).FirstOrCreate(&plugin).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save plugin"})
		return
	}
	if err := setUserPluginEnabled(user.ID, plugin.ID, true); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to enable plugin"})
		return
	}
	if err := InitializePluginWASM(c.Request.Context(), plugin); err != nil {
		model.DB.Model(&plugin).Update("last_error", err.Error())
		recordPluginLog(user.ID, plugin.ID, "warn", "wasm_init_failed", err.Error(), "")
	} else if plugin.WASMPath != "" {
		model.DB.Model(&plugin).Update("last_error", "")
		recordPluginLog(user.ID, plugin.ID, "info", "wasm_init", "WASM plugin initialized", "")
	}
	DispatchPluginHooks(c.Request.Context(), PluginHookInput{
		Point:   PluginHookPointPluginInstalled,
		UserID:  user.ID,
		Source:  "plugin_management",
		Payload: map[string]interface{}{"plugin_id": plugin.ID, "version": plugin.Version},
	})
	c.JSON(http.StatusOK, gin.H{"plugin": pluginListResponse(plugin, true)})
}

func prepareUploadedPlugin(ctx context.Context, src io.Reader, filename string, tmpDir string) (PluginManifest, []byte, string, error) {
	lower := strings.ToLower(strings.TrimSpace(filename))
	if !strings.HasSuffix(lower, ".wasm") {
		return PluginManifest{}, nil, "", errors.New("plugin upload must be a single .wasm file")
	}
	wasmPath := filepath.Join(tmpDir, "plugin.wasm")
	if err := writeUploadedWASM(wasmPath, src); err != nil {
		return PluginManifest{}, nil, "", err
	}
	manifest, raw, err := ReadPluginManifestFromWASM(ctx, wasmPath)
	if err != nil {
		return PluginManifest{}, raw, "", err
	}
	manifest = normalizePluginManifest(manifest)
	manifest.WASM = filepath.Base(wasmPath)
	return manifest, raw, wasmPath, nil
}

func (api *pluginAPI) enablePlugin(c *gin.Context) {
	api.setPluginEnabled(c, true)
}

func (api *pluginAPI) disablePlugin(c *gin.Context) {
	api.setPluginEnabled(c, false)
}

func (api *pluginAPI) setPluginEnabled(c *gin.Context, enabled bool) {
	user, ok := currentUserFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if !requirePluginAdmin(c, user) {
		return
	}
	plugin, ok := loadPlugin(c)
	if !ok {
		return
	}
	if !enabled {
		DispatchPluginHooks(c.Request.Context(), PluginHookInput{
			Point:   PluginHookPointPluginDisabled,
			UserID:  user.ID,
			Source:  "plugin_management",
			Payload: map[string]interface{}{"plugin_id": plugin.ID, "enabled": false},
		})
	}
	plugin, err := setPluginModuleEnabled(plugin, enabled)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	if err := setUserPluginEnabled(user.ID, plugin.ID, enabled); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update plugin state"})
		return
	}
	if enabled {
		DispatchPluginHooks(c.Request.Context(), PluginHookInput{
			Point:   PluginHookPointPluginEnabled,
			UserID:  user.ID,
			Source:  "plugin_management",
			Payload: map[string]interface{}{"plugin_id": plugin.ID, "enabled": true},
		})
	}
	c.JSON(http.StatusOK, gin.H{"plugin": pluginListResponse(plugin, enabled)})
}

func (api *pluginAPI) uninstallPlugin(c *gin.Context) {
	user, ok := currentUserFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if !requirePluginAdmin(c, user) {
		return
	}
	plugin, ok := loadPlugin(c)
	if !ok {
		return
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("plugin_id = ?", plugin.ID).Delete(&model.UserPluginState{}).Error; err != nil {
			return err
		}
		if err := tx.Where("plugin_id = ?", plugin.ID).Delete(&model.UserPluginConfig{}).Error; err != nil {
			return err
		}
		if err := tx.Where("plugin_id = ?", plugin.ID).Delete(&model.PluginKV{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&plugin).Error; err != nil {
			return err
		}
		return nil
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to uninstall plugin"})
		return
	}
	_ = removePluginModuleFiles(plugin.ID)
	_ = os.RemoveAll(filepath.Join(config.DataPath, "plugin-data", fmt.Sprint(user.ID), plugin.ID))
	recordPluginLog(user.ID, plugin.ID, "info", "uninstall", "Plugin uninstalled", "")
	c.JSON(http.StatusOK, gin.H{"message": "Plugin uninstalled"})
}

func (api *pluginAPI) getPluginSettings(c *gin.Context) {
	user, ok := currentUserFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	plugin, ok := loadPlugin(c)
	if !ok {
		return
	}
	if !pluginUserEnabled(plugin, userPluginStates(user.ID)) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Plugin is disabled"})
		return
	}
	if pluginUsesGlobalSettings(plugin) && !requirePluginAdmin(c, user) {
		return
	}
	config := pluginConfigForUser(user.ID, plugin.ID)
	c.JSON(http.StatusOK, gin.H{
		"schema": json.RawMessage(nonEmptyJSON(plugin.SettingsJSON, "{}")),
		"config": config,
		"scope":  pluginSettingsScope(plugin),
	})
}

func (api *pluginAPI) updatePluginSettings(c *gin.Context) {
	user, ok := currentUserFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	plugin, ok := loadPlugin(c)
	if !ok {
		return
	}
	if !pluginUserEnabled(plugin, userPluginStates(user.ID)) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Plugin is disabled"})
		return
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to read settings"})
		return
	}
	var payload map[string]interface{}
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Settings must be a JSON object"})
			return
		}
	}
	raw := mustJSON(payload)
	if pluginUsesGlobalSettings(plugin) {
		if !requirePluginAdmin(c, user) {
			return
		}
		if err := model.DB.Model(&model.Plugin{}).Where("id = ?", plugin.ID).Update("global_config_json", raw).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save settings"})
			return
		}
	} else {
		cfg := model.UserPluginConfig{UserID: user.ID, PluginID: plugin.ID}
		if err := model.DB.Where(&model.UserPluginConfig{UserID: user.ID, PluginID: plugin.ID}).
			Assign(model.UserPluginConfig{ConfigJSON: raw}).
			FirstOrCreate(&cfg).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save settings"})
			return
		}
	}
	DispatchPluginHooks(c.Request.Context(), PluginHookInput{
		Point:   PluginHookPointPluginSettingsUpdated,
		UserID:  user.ID,
		Source:  "plugin_settings",
		Payload: map[string]interface{}{"plugin_id": plugin.ID, "config": payload, "scope": pluginSettingsScope(plugin)},
	})
	c.JSON(http.StatusOK, gin.H{"config": payload, "scope": pluginSettingsScope(plugin)})
}

func (api *pluginAPI) runPluginAction(c *gin.Context) {
	user, ok := currentUserFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	plugin, ok := loadPlugin(c)
	if !ok {
		return
	}
	states := userPluginStates(user.ID)
	if !pluginUserEnabled(plugin, states) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Plugin is disabled"})
		return
	}
	var payload map[string]interface{}
	_ = c.ShouldBindJSON(&payload)
	action := strings.TrimSpace(c.Param("action"))
	requestID := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if requestID == "" {
		requestID = newPluginRequestID()
	}
	if len(requestID) > 160 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Idempotency-Key is too long"})
		return
	}
	c.Header("Idempotency-Key", requestID)
	beforeResults := DispatchPluginHooks(c.Request.Context(), PluginHookInput{
		Point:   PluginHookPointPluginActionBefore,
		Action:  action,
		UserID:  user.ID,
		Source:  "plugin_action",
		Payload: map[string]interface{}{"plugin_id": plugin.ID, "request_id": requestID, "values": payload},
	})
	if err := pluginActionAllowed(beforeResults); err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
		return
	}
	result, err := InvokePluginAction(c.Request.Context(), plugin, user.ID, requestID, action, payload)
	if err != nil {
		recordPluginLog(user.ID, plugin.ID, "error", "action_failed", err.Error(), mustJSON(gin.H{"action": action}))
		DispatchPluginHooks(c.Request.Context(), PluginHookInput{
			Point:   PluginHookPointPluginActionError,
			Action:  action,
			UserID:  user.ID,
			Source:  "plugin_action",
			Payload: map[string]interface{}{"plugin_id": plugin.ID, "request_id": requestID, "values": payload, "error": err.Error()},
		})
		c.JSON(pluginActionErrorStatus(err), gin.H{"error": err.Error()})
		return
	}
	DispatchPluginHooks(c.Request.Context(), PluginHookInput{
		Point:   PluginHookPointPluginActionAfter,
		Action:  action,
		UserID:  user.ID,
		Source:  "plugin_action",
		Payload: map[string]interface{}{"plugin_id": plugin.ID, "request_id": requestID, "values": payload, "result": result},
	})
	c.JSON(http.StatusOK, result)
}

func newPluginRequestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return fmt.Sprintf("%x", raw[:])
	}
	return fmt.Sprintf("plugin-%d", time.Now().UnixNano())
}

func pluginActionErrorStatus(err error) int {
	var actionErr *PluginActionError
	if !errors.As(err, &actionErr) {
		return http.StatusBadGateway
	}
	switch actionErr.Code {
	case "insufficient_balance":
		return http.StatusPaymentRequired
	case "permission_denied":
		return http.StatusForbidden
	case "idempotency_conflict":
		return http.StatusConflict
	case "participation_limit":
		return http.StatusTooManyRequests
	case "lottery_closed", "lottery_not_started", "lottery_ended":
		return http.StatusConflict
	case "invalid_lottery_config":
		return http.StatusUnprocessableEntity
	case "invalid_request", "invalid_amount", "invalid_settlement", "invalid_limit", "action_not_found":
		return http.StatusBadRequest
	default:
		return http.StatusBadGateway
	}
}

func loadPlugin(c *gin.Context) (model.Plugin, bool) {
	id := strings.TrimSpace(c.Param("id"))
	var plugin model.Plugin
	if id == "" || model.DB.Where("id = ?", id).Limit(1).Find(&plugin).Error != nil || plugin.ID == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "Plugin not found"})
		return model.Plugin{}, false
	}
	return plugin, true
}

func requirePluginAdmin(c *gin.Context, user *model.User) bool {
	if user == nil || !user.IsAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "Admin permission required"})
		return false
	}
	return true
}

func pluginListResponse(plugin model.Plugin, enabled bool) pluginListItem {
	var manifest PluginManifest
	_ = json.Unmarshal([]byte(plugin.ManifestJSON), &manifest)
	frontend := json.RawMessage(nonEmptyJSON(plugin.FrontendJSON, "null"))
	if len(manifest.Frontend) > 0 && string(manifest.Frontend) != "null" {
		frontend = manifest.Frontend
	}
	return pluginListItem{
		ID:          plugin.ID,
		Name:        plugin.Name,
		Version:     plugin.Version,
		Description: plugin.Description,
		Author:      plugin.Author,
		GitHub:      manifest.GitHub,
		Enabled:     enabled,
		Permissions: decodePluginStringList(plugin.PermissionsJSON),
		Hooks:       decodeHooks(plugin.HooksJSON),
		Frontend:    frontend,
		Settings:    json.RawMessage(nonEmptyJSON(plugin.SettingsJSON, "null")),
		Upstreams:   manifest.Upstreams,
		LastError:   plugin.LastError,
		CreatedAt:   plugin.CreatedAt,
		UpdatedAt:   plugin.UpdatedAt,
	}
}

// filterPluginFrontendForUser removes frontend entries the current user is not
// allowed to discover. Access defaults to "public"; "admin" entries are only
// returned to administrators. The legacy "visibility" key is accepted as an
// alias so older hand-written plugin manifests can adopt the rule gradually.
func filterPluginFrontendForUser(raw json.RawMessage, isAdmin bool) json.RawMessage {
	var frontend map[string]interface{}
	if err := json.Unmarshal(raw, &frontend); err != nil || frontend == nil {
		return raw
	}

	declaredRoutes := make(map[string]bool)
	routeAccess := make(map[string]bool)
	if routes, ok := frontend["routes"].([]interface{}); ok {
		visibleRoutes := make([]interface{}, 0, len(routes))
		for _, rawRoute := range routes {
			route, ok := rawRoute.(map[string]interface{})
			if !ok {
				continue
			}
			path := pluginFrontendPath(stringFromValue(route["path"]))
			declaredRoutes[path] = true
			if !pluginFrontendEntryVisible(route, isAdmin) {
				continue
			}
			visibleRoutes = append(visibleRoutes, route)
			routeAccess[path] = true
		}
		frontend["routes"] = visibleRoutes
	}

	if sidebar, ok := frontend["sidebar"].([]interface{}); ok {
		visibleSidebar := make([]interface{}, 0, len(sidebar))
		for _, rawItem := range sidebar {
			item, ok := rawItem.(map[string]interface{})
			if !ok || !pluginFrontendEntryVisible(item, isAdmin) {
				continue
			}
			path := pluginFrontendPath(stringFromValue(item["path"]))
			if declaredRoutes[path] && !routeAccess[path] {
				continue
			}
			if path == "" && len(declaredRoutes) > 0 && len(routeAccess) == 0 {
				continue
			}
			visibleSidebar = append(visibleSidebar, item)
		}
		frontend["sidebar"] = visibleSidebar
	}

	encoded, err := json.Marshal(frontend)
	if err != nil {
		return raw
	}
	return encoded
}

func pluginFrontendHasContent(raw json.RawMessage) bool {
	var frontend map[string]interface{}
	if err := json.Unmarshal(raw, &frontend); err != nil || frontend == nil {
		return false
	}
	if frontend["page"] != nil {
		return true
	}
	for _, key := range []string{"routes", "sidebar"} {
		if entries, ok := frontend[key].([]interface{}); ok && len(entries) > 0 {
			return true
		}
	}
	return false
}

func pluginFrontendEntryVisible(entry map[string]interface{}, isAdmin bool) bool {
	access, _ := pluginFrontendEntryAccess(entry)
	switch access {
	case "", "public", "user":
		return true
	case "admin":
		return isAdmin
	default:
		return false
	}
}

func pluginFrontendEntryAccess(entry map[string]interface{}) (string, bool) {
	for _, key := range []string{"access", "visibility"} {
		if value, exists := entry[key]; exists {
			return strings.ToLower(strings.TrimSpace(stringFromValue(value))), true
		}
	}
	return "", false
}

func pluginFrontendPath(path string) string {
	return strings.Trim(strings.TrimSpace(path), "/")
}

func userPluginStates(userID uint) map[string]bool {
	var states []model.UserPluginState
	_ = model.DB.Where("user_id = ?", userID).Find(&states).Error
	result := map[string]bool{}
	for _, state := range states {
		result[state.PluginID] = state.Enabled
	}
	return result
}

func pluginConfigForUser(userID uint, pluginID string) map[string]interface{} {
	if strings.TrimSpace(pluginID) == "" {
		return map[string]interface{}{}
	}
	var plugin model.Plugin
	if err := model.DB.Select("id", "permissions_json", "global_config_json").Where("id = ?", pluginID).Limit(1).Find(&plugin).Error; err != nil || plugin.ID == "" {
		return map[string]interface{}{}
	}
	if pluginUsesGlobalSettings(plugin) {
		values := map[string]interface{}{}
		if strings.TrimSpace(plugin.GlobalConfigJSON) != "" {
			_ = json.Unmarshal([]byte(plugin.GlobalConfigJSON), &values)
		}
		return values
	}
	if userID == 0 {
		return map[string]interface{}{}
	}
	var config model.UserPluginConfig
	if err := model.DB.Where("user_id = ? AND plugin_id = ?", userID, pluginID).Limit(1).Find(&config).Error; err != nil {
		return map[string]interface{}{}
	}
	values := map[string]interface{}{}
	if strings.TrimSpace(config.ConfigJSON) != "" {
		_ = json.Unmarshal([]byte(config.ConfigJSON), &values)
	}
	return values
}

func pluginUsesGlobalSettings(plugin model.Plugin) bool {
	return pluginHasPermission(plugin, "plugin.settings.global")
}

func pluginSettingsScope(plugin model.Plugin) string {
	if pluginUsesGlobalSettings(plugin) {
		return "global"
	}
	return "user"
}

func pluginUserEnabled(plugin model.Plugin, states map[string]bool) bool {
	if !plugin.Enabled {
		return false
	}
	enabled, ok := states[plugin.ID]
	return !ok || enabled
}

// PluginEnabledForUser reports whether an installed plugin remains available to
// a particular user. Channel integrations use it before invoking plugin code.
func PluginEnabledForUser(plugin model.Plugin, userID uint) bool {
	if userID == 0 {
		return false
	}
	return pluginUserEnabled(plugin, userPluginStates(userID))
}

func loadPluginsOnStartup() error {
	root := filepath.Join(config.DataPath, "plugins")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := strings.ToLower(entry.Name())
		enabled := strings.HasSuffix(name, ".wasm")
		disabled := strings.HasSuffix(name, ".wasm.disabled")
		if !enabled && !disabled {
			continue
		}
		filePath := filepath.Join(root, entry.Name())
		manifest, raw, err := ReadPluginManifestFromWASM(context.Background(), filePath)
		if err != nil {
			recordPluginLog(0, "", "warn", "startup_load_failed", err.Error(), mustJSON(gin.H{"path": filePath}))
			continue
		}
		manifest = normalizePluginManifest(manifest)
		if err := validatePluginManifest(manifest); err != nil {
			recordPluginLog(0, manifest.ID, "warn", "startup_load_failed", err.Error(), mustJSON(gin.H{"path": filePath}))
			continue
		}
		plugin := model.Plugin{
			ID:              manifest.ID,
			Name:            manifest.Name,
			Version:         manifest.Version,
			Description:     manifest.Description,
			Author:          manifest.Author,
			Enabled:         enabled,
			ManifestJSON:    string(raw),
			PermissionsJSON: mustJSON(manifest.Permissions),
			HooksJSON:       mustJSON(manifest.Hooks),
			FrontendJSON:    string(manifest.Frontend),
			SettingsJSON:    string(manifest.Settings),
			Path:            root,
			WASMPath:        filePath,
		}
		var existingPlugin model.Plugin
		if err := model.DB.Select("global_config_json").Where("id = ?", plugin.ID).Limit(1).Find(&existingPlugin).Error; err == nil {
			plugin.GlobalConfigJSON = existingPlugin.GlobalConfigJSON
		}
		if err := model.DB.Where(&model.Plugin{ID: plugin.ID}).Assign(plugin).FirstOrCreate(&plugin).Error; err != nil {
			recordPluginLog(0, manifest.ID, "warn", "startup_load_failed", err.Error(), mustJSON(gin.H{"path": filePath}))
			continue
		}
		if !enabled {
			model.DB.Model(&plugin).Update("last_error", "")
			continue
		}
		if err := InitializePluginWASM(context.Background(), plugin); err != nil {
			model.DB.Model(&plugin).Update("last_error", err.Error())
			recordPluginLog(0, plugin.ID, "warn", "startup_init_failed", err.Error(), mustJSON(gin.H{"path": filePath}))
			continue
		}
		model.DB.Model(&plugin).Update("last_error", "")
	}
	DispatchPluginHooks(context.Background(), PluginHookInput{
		Point:  PluginHookPointAppBoot,
		Source: "startup",
		Payload: map[string]interface{}{
			"phase": "ready",
		},
	})
	return nil
}

func pluginDisabledWASMPath(id string) string {
	return pluginWASMPath(id) + ".disabled"
}

func removePluginModuleFiles(id string) error {
	for _, modulePath := range []string{pluginWASMPath(id), pluginDisabledWASMPath(id)} {
		if err := os.Remove(modulePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// setPluginModuleEnabled changes the on-disk suffix as the durable global
// state. A disabled module still contains its manifest and is therefore
// discoverable at startup without being initialized.
func setPluginModuleEnabled(plugin model.Plugin, enabled bool) (model.Plugin, error) {
	activePath, disabledPath := pluginWASMPath(plugin.ID), pluginDisabledWASMPath(plugin.ID)
	source, target := disabledPath, activePath
	if !enabled {
		source, target = activePath, disabledPath
	}
	if _, err := os.Stat(source); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return model.Plugin{}, errors.New("plugin module file is unavailable")
		}
		return model.Plugin{}, fmt.Errorf("could not access plugin module: %w", err)
	}
	if _, err := os.Stat(target); err == nil {
		return model.Plugin{}, errors.New("plugin module state is inconsistent")
	} else if !errors.Is(err, os.ErrNotExist) {
		return model.Plugin{}, fmt.Errorf("could not access plugin module: %w", err)
	}
	if err := os.Rename(source, target); err != nil {
		return model.Plugin{}, fmt.Errorf("could not update plugin module state: %w", err)
	}
	plugin.Enabled, plugin.WASMPath = enabled, target
	if err := model.DB.Model(&model.Plugin{}).Where("id = ?", plugin.ID).Updates(map[string]interface{}{"enabled": enabled, "wasm_path": target}).Error; err != nil {
		_ = os.Rename(target, source)
		return model.Plugin{}, errors.New("failed to save plugin state")
	}
	if enabled {
		if err := InitializePluginWASM(context.Background(), plugin); err != nil {
			model.DB.Model(&model.Plugin{}).Where("id = ?", plugin.ID).Update("last_error", err.Error())
			return plugin, nil
		}
		_ = model.DB.Model(&model.Plugin{}).Where("id = ?", plugin.ID).Update("last_error", "").Error
	}
	return plugin, nil
}

func setUserPluginEnabled(userID uint, pluginID string, enabled bool) error {
	state := model.UserPluginState{UserID: userID, PluginID: pluginID}
	return model.DB.Where(&model.UserPluginState{UserID: userID, PluginID: pluginID}).
		Assign(model.UserPluginState{Enabled: enabled}).
		FirstOrCreate(&state).Error
}

func validatePluginManifest(manifest PluginManifest) error {
	if !pluginIDPattern.MatchString(manifest.ID) {
		return errors.New("plugin id must match ^[A-Za-z0-9][A-Za-z0-9_-]{1,79}$")
	}
	if strings.TrimSpace(manifest.Name) == "" {
		return errors.New("plugin name is required")
	}
	if strings.TrimSpace(manifest.Version) == "" {
		return errors.New("plugin version is required")
	}
	if manifest.GitHub != "" {
		if _, _, ok := pluginGitHubRepository(manifest.GitHub); !ok {
			return errors.New("plugin github must be an https://github.com/owner/repository URL")
		}
	}
	for _, permission := range manifest.Permissions {
		if strings.TrimSpace(permission) == "" {
			return errors.New("plugin permissions cannot contain empty values")
		}
	}
	if err := validatePluginFrontendAccess(manifest.Frontend); err != nil {
		return err
	}
	for _, hook := range manifest.Hooks {
		if strings.TrimSpace(hook.Point) == "" {
			return errors.New("plugin hook point is required")
		}
		if !validPluginHookPointName(hook.Point) {
			return fmt.Errorf("invalid plugin hook point: %s", hook.Point)
		}
		if strings.TrimSpace(hook.Mode) != "" && hook.Mode != "sync" && hook.Mode != "async" {
			return fmt.Errorf("unsupported plugin hook mode: %s", hook.Mode)
		}
	}
	seenChannels := map[string]struct{}{}
	for _, channel := range manifest.Channels {
		if !pluginChannelTypeID.MatchString(strings.TrimSpace(channel.ID)) {
			return fmt.Errorf("invalid plugin channel type id: %s", channel.ID)
		}
		if strings.TrimSpace(channel.Name) == "" || len([]rune(strings.TrimSpace(channel.Name))) > 120 {
			return fmt.Errorf("plugin channel type %s requires a name up to 120 characters", channel.ID)
		}
		if strings.TrimSpace(channel.InboundAction) == "" || strings.TrimSpace(channel.SendAction) == "" {
			return fmt.Errorf("plugin channel type %s requires inbound_action and send_action", channel.ID)
		}
		if len(channel.Config) > 0 {
			var config map[string]interface{}
			if err := json.Unmarshal(channel.Config, &config); err != nil || config == nil {
				return fmt.Errorf("plugin channel type %s config must be a JSON object", channel.ID)
			}
		}
		if _, exists := seenChannels[channel.ID]; exists {
			return fmt.Errorf("duplicate plugin channel type id: %s", channel.ID)
		}
		seenChannels[channel.ID] = struct{}{}
	}
	seenUpstreams := map[string]struct{}{}
	for _, upstream := range manifest.Upstreams {
		if !pluginChannelTypeID.MatchString(strings.TrimSpace(upstream.ID)) {
			return fmt.Errorf("invalid plugin upstream type id: %s", upstream.ID)
		}
		if strings.TrimSpace(upstream.Name) == "" || len([]rune(strings.TrimSpace(upstream.Name))) > 120 {
			return fmt.Errorf("plugin upstream type %s requires a name up to 120 characters", upstream.ID)
		}
		if strings.TrimSpace(upstream.Protocol) != "responses" {
			return fmt.Errorf("plugin upstream type %s must use the responses protocol", upstream.ID)
		}
		if strings.TrimSpace(upstream.PrepareAction) == "" {
			return fmt.Errorf("plugin upstream type %s requires prepare_action", upstream.ID)
		}
		if len(upstream.Config) > 0 {
			var config map[string]interface{}
			if err := json.Unmarshal(upstream.Config, &config); err != nil || config == nil {
				return fmt.Errorf("plugin upstream type %s config must be a JSON object", upstream.ID)
			}
		}
		if _, exists := seenUpstreams[upstream.ID]; exists {
			return fmt.Errorf("duplicate plugin upstream type id: %s", upstream.ID)
		}
		seenUpstreams[upstream.ID] = struct{}{}
	}
	return nil
}

func validatePluginFrontendAccess(raw json.RawMessage) error {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return nil
	}
	var frontend map[string]interface{}
	if err := json.Unmarshal(raw, &frontend); err != nil || frontend == nil {
		return errors.New("plugin frontend must be a JSON object")
	}
	for _, key := range []string{"sidebar", "routes"} {
		entries, exists := frontend[key]
		if !exists {
			continue
		}
		list, ok := entries.([]interface{})
		if !ok {
			return fmt.Errorf("plugin frontend %s must be an array", key)
		}
		for _, rawEntry := range list {
			entry, ok := rawEntry.(map[string]interface{})
			if !ok {
				return fmt.Errorf("plugin frontend %s entries must be objects", key)
			}
			access, set := pluginFrontendEntryAccess(entry)
			if !set {
				continue
			}
			switch access {
			case "public", "user", "admin":
			default:
				return fmt.Errorf("plugin frontend %s access must be public or admin", key)
			}
		}
	}
	return nil
}

func normalizePluginManifest(manifest PluginManifest) PluginManifest {
	manifest.ID = strings.TrimSpace(manifest.ID)
	manifest.Name = strings.TrimSpace(manifest.Name)
	manifest.Version = strings.TrimSpace(manifest.Version)
	manifest.Author = strings.TrimSpace(manifest.Author)
	manifest.GitHub = strings.TrimSpace(manifest.GitHub)
	manifest.WASM = strings.TrimSpace(manifest.WASM)
	for i := range manifest.Permissions {
		manifest.Permissions[i] = strings.TrimSpace(manifest.Permissions[i])
	}
	for i := range manifest.Hooks {
		manifest.Hooks[i].Point = strings.TrimSpace(manifest.Hooks[i].Point)
		manifest.Hooks[i].Mode = strings.TrimSpace(manifest.Hooks[i].Mode)
		manifest.Hooks[i].Action = strings.TrimSpace(manifest.Hooks[i].Action)
		if manifest.Hooks[i].Mode == "" {
			manifest.Hooks[i].Mode = "sync"
		}
		if len(manifest.Hooks[i].Config) == 0 {
			manifest.Hooks[i].Config = nil
		}
	}
	for i := range manifest.Channels {
		manifest.Channels[i].ID = strings.TrimSpace(manifest.Channels[i].ID)
		manifest.Channels[i].Name = strings.TrimSpace(manifest.Channels[i].Name)
		manifest.Channels[i].Description = strings.TrimSpace(manifest.Channels[i].Description)
		manifest.Channels[i].InboundAction = strings.TrimSpace(manifest.Channels[i].InboundAction)
		manifest.Channels[i].SendAction = strings.TrimSpace(manifest.Channels[i].SendAction)
		if len(manifest.Channels[i].Config) == 0 {
			manifest.Channels[i].Config = nil
		}
	}
	for i := range manifest.Upstreams {
		manifest.Upstreams[i].ID = strings.TrimSpace(manifest.Upstreams[i].ID)
		manifest.Upstreams[i].Name = strings.TrimSpace(manifest.Upstreams[i].Name)
		manifest.Upstreams[i].Description = strings.TrimSpace(manifest.Upstreams[i].Description)
		manifest.Upstreams[i].Protocol = strings.TrimSpace(manifest.Upstreams[i].Protocol)
		manifest.Upstreams[i].DefaultBaseURL = strings.TrimSpace(manifest.Upstreams[i].DefaultBaseURL)
		manifest.Upstreams[i].PrepareAction = strings.TrimSpace(manifest.Upstreams[i].PrepareAction)
		manifest.Upstreams[i].RefreshAction = strings.TrimSpace(manifest.Upstreams[i].RefreshAction)
		if len(manifest.Upstreams[i].Config) == 0 {
			manifest.Upstreams[i].Config = nil
		}
	}
	sort.SliceStable(manifest.Hooks, func(i, j int) bool {
		if manifest.Hooks[i].Priority == manifest.Hooks[j].Priority {
			return manifest.Hooks[i].Point < manifest.Hooks[j].Point
		}
		return manifest.Hooks[i].Priority > manifest.Hooks[j].Priority
	})
	return manifest
}

func writeUploadedWASM(target string, src io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	written, err := io.Copy(out, io.LimitReader(src, pluginMaxPackageBytes+1))
	if err != nil {
		return err
	}
	if written > pluginMaxPackageBytes {
		return errors.New("plugin WASM is too large")
	}
	return nil
}

func pluginWASMPath(id string) string {
	return filepath.Join(config.DataPath, "plugins", id+".wasm")
}

func recordPluginLog(userID uint, pluginID, level, event, message, metadata string) {
	var uid *uint
	if userID != 0 {
		uid = &userID
	}
	database, err := model.LogDB()
	if err != nil {
		return
	}
	_ = database.Create(&model.PluginLog{
		ID:       model.NextLogID(),
		UserID:   uid,
		PluginID: pluginID,
		Level:    level,
		Event:    event,
		Message:  message,
		Metadata: metadata,
	}).Error
}

func decodePluginStringList(raw string) []string {
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil
	}
	sort.Strings(values)
	return values
}

func decodeHooks(raw string) []PluginHook {
	var hooks []PluginHook
	_ = json.Unmarshal([]byte(raw), &hooks)
	return hooks
}

func mustJSON(value interface{}) string {
	if value == nil {
		return "{}"
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func nonEmptyJSON(raw, fallback string) string {
	if strings.TrimSpace(raw) == "" {
		return fallback
	}
	return raw
}
