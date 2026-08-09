package service

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/veloce-ailab/veloce/internal/model"
)

func stringFromMap(values map[string]interface{}, key string) string {
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

const (
	PluginHookPointAppBoot               = "app.boot"
	PluginHookPointPluginActionBefore    = "plugin.action.before"
	PluginHookPointPluginActionAfter     = "plugin.action.after"
	PluginHookPointPluginActionError     = "plugin.action.error"
	PluginHookPointPluginSettingsUpdated = "plugin.settings.updated"
	PluginHookPointPluginEnabled         = "plugin.enabled"
	PluginHookPointPluginDisabled        = "plugin.disabled"
	PluginHookPointPluginInstalled       = "plugin.installed"
)

type PluginHookInput struct {
	Point   string                 `json:"point"`
	Action  string                 `json:"action,omitempty"`
	UserID  uint                   `json:"user_id,omitempty"`
	Source  string                 `json:"source,omitempty"`
	Payload map[string]interface{} `json:"payload,omitempty"`
}

type PluginHookResult struct {
	PluginID string                 `json:"plugin_id"`
	Hook     PluginHook             `json:"hook"`
	Output   map[string]interface{} `json:"output"`
}

func DispatchPluginHooks(ctx context.Context, input PluginHookInput) []PluginHookResult {
	input.Point = strings.TrimSpace(input.Point)
	input.Action = strings.TrimSpace(input.Action)
	if input.Point == "" {
		return nil
	}
	entries := enabledPluginHooks(input.UserID, input.Point, input.Action)
	if len(entries) == 0 {
		return nil
	}
	results := make([]PluginHookResult, 0, len(entries))
	for _, entry := range entries {
		if strings.EqualFold(entry.Hook.Mode, "async") {
			go invokePluginHook(context.Background(), entry.Plugin, entry.Hook, input)
			continue
		}
		output, err := invokePluginHook(ctx, entry.Plugin, entry.Hook, input)
		if err != nil {
			continue
		}
		if output != nil {
			results = append(results, PluginHookResult{PluginID: entry.Plugin.ID, Hook: entry.Hook, Output: output})
		}
	}
	return results
}

type pluginHookEntry struct {
	Plugin model.Plugin
	Hook   PluginHook
}

func enabledPluginHooks(userID uint, point string, action string) []pluginHookEntry {
	point = strings.TrimSpace(point)
	action = strings.TrimSpace(action)
	if point == "" {
		return nil
	}
	var plugins []model.Plugin
	if err := model.DB.Where("enabled = ?", true).Find(&plugins).Error; err != nil {
		recordPluginLog(userID, "", "warn", "hook_scan_failed", err.Error(), mustJSON(gin.H{"point": point, "action": action}))
		return nil
	}
	var states map[string]bool
	if userID > 0 {
		states = userPluginStates(userID)
	}
	entries := []pluginHookEntry{}
	for _, plugin := range plugins {
		if userID > 0 && !pluginUserEnabled(plugin, states) {
			continue
		}
		for _, hook := range decodeHooks(plugin.HooksJSON) {
			if !pluginHookMatches(hook, point, action) {
				continue
			}
			entries = append(entries, pluginHookEntry{Plugin: plugin, Hook: hook})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Hook.Priority == entries[j].Hook.Priority {
			return entries[i].Plugin.ID < entries[j].Plugin.ID
		}
		return entries[i].Hook.Priority > entries[j].Hook.Priority
	})
	return entries
}

func invokePluginHook(ctx context.Context, plugin model.Plugin, hook PluginHook, input PluginHookInput) (map[string]interface{}, error) {
	payload := map[string]interface{}{
		"point":     input.Point,
		"action":    input.Action,
		"user_id":   input.UserID,
		"source":    input.Source,
		"payload":   input.Payload,
		"hook":      hook,
		"plugin_id": plugin.ID,
		"settings":  pluginConfigForUser(input.UserID, plugin.ID),
	}
	if len(hook.Config) > 0 {
		var cfg interface{}
		if err := json.Unmarshal(hook.Config, &cfg); err == nil {
			payload["config"] = cfg
		}
	}
	stdout, err := runPluginWASM(ctx, plugin, "plugin_handle_hook", []byte(mustJSON(payload)), pluginRuntimeInvocation{UserID: input.UserID})
	if err != nil {
		model.DB.Model(&plugin).Update("last_error", err.Error())
		recordPluginLog(input.UserID, plugin.ID, "warn", "hook_failed", err.Error(), mustJSON(gin.H{"point": input.Point, "action": input.Action}))
		return nil, err
	}
	stdout = strings.TrimSpace(stdout)
	if stdout == "" {
		return nil, nil
	}
	var output map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		recordPluginLog(input.UserID, plugin.ID, "warn", "hook_invalid_output", err.Error(), mustJSON(gin.H{"point": input.Point, "action": input.Action}))
		return nil, err
	}
	recordPluginLog(input.UserID, plugin.ID, "info", "hook_invoked", "Plugin hook invoked", mustJSON(gin.H{"point": input.Point, "action": input.Action}))
	return output, nil
}

func pluginActionAllowed(results []PluginHookResult) error {
	for _, result := range results {
		if result.Output == nil {
			continue
		}
		if denied, ok := result.Output["deny"].(bool); ok && denied {
			return pluginHookDeniedError(result)
		}
		if allowed, ok := result.Output["allow"].(bool); ok && !allowed {
			return pluginHookDeniedError(result)
		}
	}
	return nil
}

func pluginHookDeniedError(result PluginHookResult) error {
	message := stringFromMap(result.Output, "message")
	if message == "" {
		message = "Plugin action denied by " + result.PluginID
	}
	return errors.New(message)
}

func pluginHookMatches(hook PluginHook, point string, action string) bool {
	if strings.TrimSpace(hook.Point) != point {
		return false
	}
	if action == "" {
		return true
	}
	hookAction := strings.TrimSpace(hook.Action)
	return hookAction == action || hookAction == "*"
}

func validPluginHookPointName(point string) bool {
	return pluginHookPointPattern.MatchString(strings.TrimSpace(point))
}
