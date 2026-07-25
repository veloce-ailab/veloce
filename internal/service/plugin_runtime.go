package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

const (
	pluginWASMTimeout     = 5 * time.Second
	pluginWASMMemoryPages = 4096 // 256 MiB
)

// InitializePluginWASM verifies that the plugin WASM can be loaded and calls
// plugin_init when the module exports it.
func InitializePluginWASM(ctx context.Context, plugin model.Plugin) error {
	if strings.TrimSpace(plugin.WASMPath) == "" {
		return nil
	}
	_, err := runPluginWASM(ctx, plugin, "plugin_init", nil, pluginRuntimeInvocation{})
	return err
}

func ReadPluginManifestFromWASM(ctx context.Context, wasmPath string) (PluginManifest, []byte, error) {
	plugin := model.Plugin{ID: "manifest", WASMPath: wasmPath}
	stdout, err := runPluginWASM(ctx, plugin, "plugin_manifest", nil, pluginRuntimeInvocation{})
	if err != nil {
		return PluginManifest{}, nil, err
	}
	raw := []byte(strings.TrimSpace(stdout))
	if len(raw) == 0 {
		return PluginManifest{}, nil, errors.New("plugin_manifest returned empty output")
	}
	var manifest PluginManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return PluginManifest{}, raw, fmt.Errorf("plugin_manifest returned invalid JSON: %w", err)
	}
	return manifest, raw, nil
}

// InvokePluginAction is the backend entry point for declarative frontend
// actions. The full JSON memory ABI is intentionally kept behind this function
// so the HTTP surface stays stable while the ABI evolves.
func InvokePluginAction(ctx context.Context, plugin model.Plugin, userID uint, requestID, action string, payload map[string]interface{}) (map[string]interface{}, error) {
	if strings.TrimSpace(plugin.WASMPath) == "" {
		return nil, errors.New("plugin has no WASM module")
	}
	metadata := mustJSON(map[string]interface{}{
		"user_id":    userID,
		"request_id": requestID,
		"action":     action,
		"payload":    payload,
		"settings":   pluginConfigForUser(userID, plugin.ID),
	})
	recordPluginLog(userID, plugin.ID, "info", "action_requested", "Plugin action requested", metadata)
	stdout, err := runPluginWASM(ctx, plugin, "plugin_handle_action", []byte(metadata), pluginRuntimeInvocation{UserID: userID, RequestID: requestID})
	if err != nil {
		return nil, err
	}
	stdout = strings.TrimSpace(stdout)
	if stdout == "" {
		return map[string]interface{}{"ok": true}, nil
	}
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		return nil, fmt.Errorf("plugin action returned invalid JSON: %w", err)
	}
	if ok, exists := result["ok"].(bool); exists && !ok {
		code, _ := result["code"].(string)
		message, _ := result["error"].(string)
		if message == "" {
			message, _ = result["message"].(string)
		}
		if message == "" {
			message = "plugin action failed"
		}
		return nil, &PluginActionError{Code: code, Message: message}
	}
	return result, nil
}

type PluginActionError struct {
	Code    string
	Message string
}

func (e *PluginActionError) Error() string { return e.Message }

func runPluginWASM(ctx context.Context, plugin model.Plugin, functionName string, stdin []byte, invocation pluginRuntimeInvocation) (string, error) {
	wasmPath := strings.TrimSpace(plugin.WASMPath)
	if wasmPath == "" {
		return "", nil
	}
	wasm, err := os.ReadFile(wasmPath)
	if err != nil {
		return "", fmt.Errorf("failed to read plugin WASM: %w", err)
	}

	runCtx, cancel := context.WithTimeout(ctx, pluginWASMTimeout)
	defer cancel()

	runtimeConfig := wazero.NewRuntimeConfig().
		WithMemoryLimitPages(pluginWASMMemoryPages).
		WithCloseOnContextDone(true)
	runtime := wazero.NewRuntimeWithConfig(runCtx, runtimeConfig)
	defer runtime.Close(runCtx)
	wasi_snapshot_preview1.MustInstantiate(runCtx, runtime)
	if err := instantiatePluginHost(runCtx, runtime, plugin, invocation); err != nil {
		return "", fmt.Errorf("failed to initialize plugin host: %w", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := wazero.NewModuleConfig().
		WithStdout(&stdout).
		WithStderr(&stderr).
		WithStdin(io.Reader(bytes.NewReader(stdin))).
		WithFSConfig(wazero.NewFSConfig()).
		WithStartFunctions("_initialize")
	module, err := runtime.InstantiateWithConfig(runCtx, wasm, config)
	if err != nil {
		return stdout.String(), fmt.Errorf("failed to instantiate plugin WASM: %w%s", err, pluginStderrSuffix(stderr.String()))
	}
	defer module.Close(runCtx)

	fn := module.ExportedFunction(functionName)
	if fn == nil {
		return stdout.String(), nil
	}
	if _, err := fn.Call(runCtx); err != nil {
		return stdout.String(), fmt.Errorf("plugin %s failed: %w%s", functionName, err, pluginStderrSuffix(stderr.String()))
	}
	return stdout.String(), nil
}

func pluginStderrSuffix(stderr string) string {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return ""
	}
	return ": " + stderr
}
