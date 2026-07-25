package channel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	communityservice "github.com/veloce-ailab/veloce/internal/service"
)

var providerHTTPClient = &http.Client{Timeout: 15 * time.Second}

type webhookSummary struct {
	ExternalChatID    string
	ExternalUserID    string
	ExternalUserName  string
	ExternalMessageID string
	Content           string
}

type providerAdapter interface {
	Name() string
	ExtractWebhookSummary(body []byte) webhookSummary
	SendReply(ctx context.Context, integration MessageChannelIntegration, inbound MessageChannelMessage, content string) error
}

var providerAdapters = map[string]providerAdapter{
	providerTelegram:       telegramProvider{},
	providerDiscord:        discordProvider{},
	providerQQ:             qqProvider{},
	providerOneBot:         oneBotProvider{},
	providerWeixin:         weixinProvider{},
	providerTencentChannel: tencentChannelProvider{},
}

var pluginProviderPattern = regexp.MustCompile(`^plugin--([A-Za-z0-9][A-Za-z0-9_-]{1,79})--([A-Za-z0-9][A-Za-z0-9_-]{0,39})$`)

type providerDefinition struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	PluginID    string      `json:"plugin_id,omitempty"`
	Config      interface{} `json:"config,omitempty"`
}

type pluginChannelDescriptor struct {
	Provider      string
	Plugin        model.Plugin
	TypeID        string
	Name          string
	Description   string
	InboundAction string
	SendAction    string
	Config        interface{}
}

func supportedProviders() []providerDefinition {
	return supportedProvidersForUser(0)
}

func supportedProvidersForUser(userID uint) []providerDefinition {
	providers := []providerDefinition{
		{ID: providerTelegram, Name: "Telegram"},
		{ID: providerDiscord, Name: "Discord"},
		{ID: providerQQ, Name: "QQ Official Bot"},
		{ID: providerOneBot, Name: "OneBot"},
		{ID: providerWeixin, Name: "Weixin Bot"},
		{ID: providerTencentChannel, Name: "Tencent Channel Gateway"},
	}
	for _, descriptor := range installedPluginChannelDescriptors() {
		if userID != 0 && !communityservice.PluginEnabledForUser(descriptor.Plugin, userID) {
			continue
		}
		providers = append(providers, providerDefinition{ID: descriptor.Provider, Name: descriptor.Name, Description: descriptor.Description, PluginID: descriptor.Plugin.ID, Config: descriptor.Config})
	}
	return providers
}

func providerFor(provider string) (providerAdapter, bool) {
	if adapter, ok := providerAdapters[normalizeProvider(provider)]; ok {
		return adapter, true
	}
	descriptor, ok := pluginChannelDescriptorFor(provider)
	if !ok {
		return nil, false
	}
	return pluginChannelProvider{descriptor: descriptor}, true
}

func pluginChannelProviderID(pluginID, typeID string) string {
	return "plugin--" + pluginID + "--" + typeID
}

func pluginChannelDescriptorFor(provider string) (pluginChannelDescriptor, bool) {
	parts := pluginProviderPattern.FindStringSubmatch(strings.TrimSpace(provider))
	if len(parts) != 3 || model.DB == nil {
		return pluginChannelDescriptor{}, false
	}
	var plugin model.Plugin
	if err := model.DB.Where("id = ? AND enabled = ?", parts[1], true).First(&plugin).Error; err != nil {
		return pluginChannelDescriptor{}, false
	}
	return pluginChannelDescriptorFromPlugin(plugin, parts[2])
}

func installedPluginChannelDescriptors() []pluginChannelDescriptor {
	if model.DB == nil {
		return nil
	}
	var plugins []model.Plugin
	if err := model.DB.Where("enabled = ?", true).Find(&plugins).Error; err != nil {
		return nil
	}
	result := make([]pluginChannelDescriptor, 0)
	for _, plugin := range plugins {
		var manifest communityservice.PluginManifest
		if json.Unmarshal([]byte(plugin.ManifestJSON), &manifest) != nil {
			continue
		}
		for _, channel := range manifest.Channels {
			descriptor, ok := pluginChannelDescriptorFromManifest(plugin, channel)
			if ok {
				result = append(result, descriptor)
			}
		}
	}
	return result
}

func pluginChannelDescriptorFromPlugin(plugin model.Plugin, typeID string) (pluginChannelDescriptor, bool) {
	var manifest communityservice.PluginManifest
	if json.Unmarshal([]byte(plugin.ManifestJSON), &manifest) != nil {
		return pluginChannelDescriptor{}, false
	}
	for _, channel := range manifest.Channels {
		if channel.ID == typeID {
			return pluginChannelDescriptorFromManifest(plugin, channel)
		}
	}
	return pluginChannelDescriptor{}, false
}

func pluginChannelDescriptorFromManifest(plugin model.Plugin, channel communityservice.PluginChannelType) (pluginChannelDescriptor, bool) {
	if strings.TrimSpace(channel.ID) == "" || strings.TrimSpace(channel.InboundAction) == "" || strings.TrimSpace(channel.SendAction) == "" {
		return pluginChannelDescriptor{}, false
	}
	var config interface{}
	if len(channel.Config) > 0 && json.Unmarshal(channel.Config, &config) != nil {
		return pluginChannelDescriptor{}, false
	}
	return pluginChannelDescriptor{Provider: pluginChannelProviderID(plugin.ID, channel.ID), Plugin: plugin, TypeID: channel.ID, Name: channel.Name, Description: channel.Description, InboundAction: channel.InboundAction, SendAction: channel.SendAction, Config: config}, true
}

func sendProviderReply(ctx context.Context, integration MessageChannelIntegration, externalChatID string, content string) error {
	return sendProviderReplyForMessage(ctx, integration, MessageChannelMessage{ExternalChatID: externalChatID}, content)
}

func sendProviderReplyForMessage(ctx context.Context, integration MessageChannelIntegration, inbound MessageChannelMessage, content string) error {
	externalChatID := strings.TrimSpace(inbound.ExternalChatID)
	if strings.TrimSpace(externalChatID) == "" {
		return errors.New("external chat id is empty")
	}
	adapter, ok := providerFor(integration.Provider)
	if !ok {
		return errors.New("unsupported provider")
	}
	return adapter.SendReply(ctx, integration, inbound, content)
}

func postProviderJSON(ctx context.Context, endpoint string, authorization string, payload map[string]interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := providerHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		message := strings.TrimSpace(string(respBody))
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return errors.New("provider returned status " + strconv.Itoa(resp.StatusCode) + ": " + message)
	}
	return nil
}

func providerConfig(integration MessageChannelIntegration) map[string]interface{} {
	options := decodeAdvancedOptions(integration.AdvancedOptions)
	raw := strings.TrimSpace(options.CustomProviderConfigJSON)
	if raw == "" {
		return map[string]interface{}{}
	}
	var config map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return map[string]interface{}{}
	}
	return config
}

func configString(config map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value := stringValue(config[key]); value != "" {
			return value
		}
	}
	return ""
}

func joinEndpoint(base string, path string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return ""
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return base + "/" + strings.TrimLeft(path, "/")
}

func objectAt(data map[string]interface{}, key string) map[string]interface{} {
	value, _ := data[key].(map[string]interface{})
	if value == nil {
		return map[string]interface{}{}
	}
	return value
}

func stringID(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64:
		return strconv.FormatInt(int64(typed), 10)
	default:
		return ""
	}
}

func stringValue(value interface{}) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func firstStringValue(values ...interface{}) string {
	for _, value := range values {
		if text := stringValue(value); text != "" {
			return text
		}
	}
	return ""
}

func joinedNameParts(values ...interface{}) string {
	parts := []string{}
	for _, value := range values {
		if text := stringValue(value); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}
