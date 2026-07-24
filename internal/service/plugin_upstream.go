package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/WindyPear-Team/veloce/internal/model"
	"gorm.io/gorm"
)

const (
	pluginUpstreamPrefix         = "plugin--"
	pluginUpstreamConfigMaxBytes = 1 << 20
)

var pluginUpstreamTypePattern = regexp.MustCompile(`^plugin--([A-Za-z0-9][A-Za-z0-9_-]{1,79})--([A-Za-z0-9][A-Za-z0-9_-]{0,39})$`)

type PluginUpstreamDescriptor struct {
	Type     string
	Plugin   model.Plugin
	Upstream PluginUpstreamType
}

var pluginUpstreamRefreshOnce sync.Once

func startPluginUpstreamRefresh() error {
	pluginUpstreamRefreshOnce.Do(func() {
		go func() {
			refreshPluginUpstreamCredentials()
			ticker := time.NewTicker(10 * time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				refreshPluginUpstreamCredentials()
			}
		}()
	})
	return nil
}

// refreshPluginUpstreamCredentials renews at most 200 plugin-owned channel
// credentials. The plugin decides whether its expiration window requires a
// refresh, so this remains generic for other OAuth-backed providers.
func refreshPluginUpstreamCredentials() {
	if model.DB == nil {
		return
	}
	var channels []model.Channel
	if err := model.DB.Where("enabled = ? AND type LIKE ?", true, pluginUpstreamPrefix+"%").Limit(200).Find(&channels).Error; err != nil {
		return
	}
	for _, channel := range channels {
		descriptor, ok := PluginUpstreamForType(channel.Type)
		if !ok || strings.TrimSpace(descriptor.Upstream.RefreshAction) == "" {
			continue
		}
		config, err := pluginUpstreamChannelConfig(channel)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		result, err := InvokePluginAction(ctx, descriptor.Plugin, 0, newPluginRequestID(), descriptor.Upstream.RefreshAction, map[string]interface{}{
			"values": map[string]interface{}{"channel": map[string]interface{}{"id": channel.ID, "base_url": channel.BaseURL, "api_key": channel.APIKey, "config": config}},
		})
		cancel()
		if err != nil {
			recordPluginLog(0, descriptor.Plugin.ID, "warn", "upstream_refresh_failed", err.Error(), mustJSON(map[string]interface{}{"channel_id": channel.ID}))
			continue
		}
		if err := applyPluginUpstreamSettingsPatch(descriptor, result); err != nil {
			recordPluginLog(0, descriptor.Plugin.ID, "warn", "upstream_refresh_settings_failed", err.Error(), mustJSON(map[string]interface{}{"channel_id": channel.ID}))
			continue
		}
		updatedKey, _ := result["api_key"].(string)
		if strings.TrimSpace(updatedKey) != "" && updatedKey != channel.APIKey {
			_ = model.DB.Model(&model.Channel{}).Where("id = ?", channel.ID).Update("api_key", updatedKey).Error
		}
	}
}

// PluginUpstreamForType resolves a persisted channel type to its enabled WASM
// provider declaration. The stable type is plugin--<plugin-id>--<upstream-id>.
func PluginUpstreamForType(channelType string) (PluginUpstreamDescriptor, bool) {
	channelType = strings.TrimSpace(channelType)
	parts := pluginUpstreamTypePattern.FindStringSubmatch(channelType)
	if len(parts) != 3 || model.DB == nil {
		return PluginUpstreamDescriptor{}, false
	}
	var plugin model.Plugin
	if err := model.DB.Where("id = ? AND enabled = ?", parts[1], true).First(&plugin).Error; err != nil {
		return PluginUpstreamDescriptor{}, false
	}
	var manifest PluginManifest
	if json.Unmarshal([]byte(plugin.ManifestJSON), &manifest) != nil {
		return PluginUpstreamDescriptor{}, false
	}
	for _, upstream := range manifest.Upstreams {
		if upstream.ID == parts[2] && upstream.Protocol == "responses" && upstream.PrepareAction != "" {
			return PluginUpstreamDescriptor{Type: channelType, Plugin: plugin, Upstream: upstream}, true
		}
	}
	return PluginUpstreamDescriptor{}, false
}

type PluginUpstreamRequest struct {
	Method  string
	URL     string
	Headers http.Header
	Body    []byte
	APIKey  string
}

type pluginUpstreamConfigSchema struct {
	Fields []struct {
		Name     string `json:"name"`
		Label    string `json:"label"`
		Required bool   `json:"required"`
	} `json:"fields"`
}

// ValidatePluginUpstreamChannel validates the persisted per-channel settings
// declared by an installed WASM upstream provider.
func ValidatePluginUpstreamChannel(channel model.Channel) error {
	if !strings.HasPrefix(strings.TrimSpace(channel.Type), pluginUpstreamPrefix) {
		return nil
	}
	descriptor, ok := PluginUpstreamForType(channel.Type)
	if !ok {
		return errors.New("plugin upstream channel type is unavailable")
	}
	config, err := pluginUpstreamChannelConfig(channel)
	if err != nil {
		return err
	}
	if len(descriptor.Upstream.Config) == 0 {
		return nil
	}
	var schema pluginUpstreamConfigSchema
	if err := json.Unmarshal(descriptor.Upstream.Config, &schema); err != nil {
		return errors.New("plugin upstream configuration schema is invalid")
	}
	for _, field := range schema.Fields {
		if !field.Required || strings.TrimSpace(field.Name) == "" {
			continue
		}
		value, exists := config[field.Name]
		if !exists || isEmptyPluginUpstreamConfigValue(value) {
			label := strings.TrimSpace(field.Label)
			if label == "" {
				label = field.Name
			}
			return fmt.Errorf("plugin upstream setting %s is required", label)
		}
	}
	return nil
}

func pluginUpstreamChannelConfig(channel model.Channel) (map[string]interface{}, error) {
	raw := strings.TrimSpace(channel.PluginConfigJSON)
	if raw == "" {
		return map[string]interface{}{}, nil
	}
	if len(raw) > pluginUpstreamConfigMaxBytes {
		return nil, errors.New("plugin upstream configuration is too large")
	}
	var config map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &config); err != nil || config == nil {
		return nil, errors.New("plugin upstream configuration must be a JSON object")
	}
	return config, nil
}

func isEmptyPluginUpstreamConfigValue(value interface{}) bool {
	if value == nil {
		return true
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text) == ""
	}
	return false
}

func applyPluginUpstreamSettingsPatch(descriptor PluginUpstreamDescriptor, result map[string]interface{}) error {
	patch, exists := result["settings_patch"]
	if !exists || patch == nil {
		return nil
	}
	if !pluginUsesGlobalSettings(descriptor.Plugin) {
		return errors.New("plugin upstream cannot update settings without plugin.settings.global")
	}
	patchMap, ok := patch.(map[string]interface{})
	if !ok {
		return errors.New("plugin upstream settings_patch must be a JSON object")
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var plugin model.Plugin
		if err := tx.Select("id", "permissions_json", "global_config_json").Where("id = ?", descriptor.Plugin.ID).First(&plugin).Error; err != nil {
			return err
		}
		if !pluginUsesGlobalSettings(plugin) {
			return errors.New("plugin upstream cannot update non-global settings")
		}
		values := map[string]interface{}{}
		if raw := strings.TrimSpace(plugin.GlobalConfigJSON); raw != "" {
			if err := json.Unmarshal([]byte(raw), &values); err != nil || values == nil {
				return errors.New("stored plugin settings are invalid")
			}
		}
		for key, value := range patchMap {
			values[key] = value
		}
		raw, err := json.Marshal(values)
		if err != nil {
			return errors.New("plugin upstream settings_patch is invalid")
		}
		if len(raw) > pluginUpstreamConfigMaxBytes {
			return errors.New("plugin upstream settings_patch is too large")
		}
		return tx.Model(&model.Plugin{}).Where("id = ?", plugin.ID).Update("global_config_json", string(raw)).Error
	})
}

// PreparePluginUpstreamRequest executes a provider's request transformer. A
// plugin never receives a client API key; it sees only the selected upstream
// channel's credential and the normalized Responses payload.
func PreparePluginUpstreamRequest(ctx context.Context, descriptor PluginUpstreamDescriptor, userID uint, requestID string, channel model.Channel, payload map[string]interface{}, stream bool, compact bool) (PluginUpstreamRequest, error) {
	if !PluginEnabledForUser(descriptor.Plugin, userID) {
		return PluginUpstreamRequest{}, errors.New("plugin upstream is disabled for this user")
	}
	config, err := pluginUpstreamChannelConfig(channel)
	if err != nil {
		return PluginUpstreamRequest{}, err
	}
	result, err := InvokePluginAction(ctx, descriptor.Plugin, userID, requestID, descriptor.Upstream.PrepareAction, map[string]interface{}{
		"values": map[string]interface{}{
			"channel": map[string]interface{}{"id": channel.ID, "base_url": channel.BaseURL, "api_key": channel.APIKey, "config": config},
			"request": map[string]interface{}{"payload": payload, "stream": stream, "compact": compact},
		},
	})
	if err != nil {
		return PluginUpstreamRequest{}, err
	}
	if err := applyPluginUpstreamSettingsPatch(descriptor, result); err != nil {
		return PluginUpstreamRequest{}, err
	}
	request, ok := result["request"].(map[string]interface{})
	if !ok {
		return PluginUpstreamRequest{}, errors.New("plugin upstream returned no request")
	}
	method, _ := request["method"].(string)
	endpoint, _ := request["url"].(string)
	body, _ := request["body"].(string)
	if strings.ToUpper(strings.TrimSpace(method)) != http.MethodPost || strings.TrimSpace(endpoint) == "" {
		return PluginUpstreamRequest{}, errors.New("plugin upstream returned an invalid request")
	}
	if err := ValidateConfiguredHTTPURL(endpoint); err != nil {
		return PluginUpstreamRequest{}, fmt.Errorf("plugin upstream URL blocked by SSRF protection: %w", err)
	}
	headers := http.Header{}
	rawHeaders, _ := request["headers"].(map[string]interface{})
	if len(rawHeaders) > 50 {
		return PluginUpstreamRequest{}, errors.New("plugin upstream returned too many headers")
	}
	for key, value := range rawHeaders {
		text, ok := value.(string)
		if strings.TrimSpace(key) == "" || !ok || len(key) > 120 || len(text) > 4096 || strings.EqualFold(key, "Host") {
			return PluginUpstreamRequest{}, errors.New("plugin upstream returned an invalid header")
		}
		headers.Set(key, text)
	}
	if headers.Get("Content-Type") != "application/json" {
		return PluginUpstreamRequest{}, errors.New("plugin upstream must use Content-Type application/json")
	}
	if len(body) > pluginHostMaxRequest {
		return PluginUpstreamRequest{}, errors.New("plugin upstream request body is too large")
	}
	updatedKey, _ := result["api_key"].(string)
	return PluginUpstreamRequest{Method: http.MethodPost, URL: endpoint, Headers: headers, Body: []byte(body), APIKey: strings.TrimSpace(updatedKey)}, nil
}
