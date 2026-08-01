package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/veloce-ailab/veloce/internal/config"
	"github.com/veloce-ailab/veloce/internal/model"
)

type pluginUpdateStatus struct {
	ID              string `json:"id"`
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version,omitempty"`
	UpdateAvailable bool   `json:"update_available"`
	Error           string `json:"error,omitempty"`
}

type pluginUpdateProgressState struct {
	PluginID        string `json:"plugin_id"`
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version,omitempty"`
	InProgress      bool   `json:"in_progress"`
	Phase           string `json:"phase"`
	Progress        int    `json:"progress"`
	DownloadedBytes int64  `json:"downloaded_bytes"`
	TotalBytes      int64  `json:"total_bytes"`
	Error           string `json:"error,omitempty"`
	UpdatedAt       string `json:"updated_at"`
}

var pluginUpdateJobs = struct {
	sync.Mutex
	items map[string]pluginUpdateProgressState
}{items: map[string]pluginUpdateProgressState{}}

func (api *pluginAPI) checkPluginUpdates(c *gin.Context) {
	user, ok := currentUserFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if !requirePluginAdmin(c, user) {
		return
	}
	var plugins []model.Plugin
	if err := model.DB.Order("id asc").Find(&plugins).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list plugins"})
		return
	}
	statuses := make([]pluginUpdateStatus, len(plugins))
	var wait sync.WaitGroup
	semaphore := make(chan struct{}, 4)
	for index, plugin := range plugins {
		statuses[index] = pluginUpdateStatus{ID: plugin.ID, CurrentVersion: plugin.Version}
		wait.Add(1)
		go func(index int, plugin model.Plugin) {
			defer wait.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			statuses[index] = pluginUpdateStatusForManifest(c.Request.Context(), plugin)
		}(index, plugin)
	}
	wait.Wait()
	c.JSON(http.StatusOK, gin.H{"items": statuses})
}

func (api *pluginAPI) startPluginUpdate(c *gin.Context) {
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
	if _, _, ok := pluginGitHubFromPlugin(plugin); !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Plugin does not declare a valid GitHub repository"})
		return
	}
	pluginUpdateJobs.Lock()
	current := pluginUpdateJobs.items[plugin.ID]
	if current.InProgress {
		pluginUpdateJobs.Unlock()
		c.JSON(http.StatusConflict, gin.H{"error": "Plugin update is already running"})
		return
	}
	state := pluginUpdateProgressState{PluginID: plugin.ID, CurrentVersion: plugin.Version, InProgress: true, Phase: "checking", UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	pluginUpdateJobs.items[plugin.ID] = state
	pluginUpdateJobs.Unlock()
	go runPluginUpdate(plugin, user.ID)
	c.JSON(http.StatusAccepted, state)
}

func (api *pluginAPI) pluginUpdateProgress(c *gin.Context) {
	user, ok := currentUserFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if !requirePluginAdmin(c, user) {
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	pluginUpdateJobs.Lock()
	state, exists := pluginUpdateJobs.items[id]
	pluginUpdateJobs.Unlock()
	if !exists {
		c.JSON(http.StatusOK, gin.H{"plugin_id": id, "in_progress": false, "phase": "idle"})
		return
	}
	c.JSON(http.StatusOK, state)
}

func runPluginUpdate(plugin model.Plugin, userID uint) {
	setPluginUpdateProgress(plugin.ID, func(state *pluginUpdateProgressState) { state.Phase = "checking" })
	owner, repository, ok := pluginGitHubFromPlugin(plugin)
	if !ok {
		finishPluginUpdate(plugin.ID, "Plugin does not declare a valid GitHub repository")
		return
	}
	requestContext, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	release, asset, err := fetchPluginReleaseCandidate(requestContext, owner, repository)
	if err != nil {
		finishPluginUpdate(plugin.ID, err.Error())
		return
	}
	if !isNewerRelease(release.TagName, plugin.Version) {
		setPluginUpdateProgress(plugin.ID, func(state *pluginUpdateProgressState) {
			state.LatestVersion = release.TagName
			state.Phase = "completed"
			state.Progress = 100
			state.InProgress = false
		})
		return
	}
	setPluginUpdateProgress(plugin.ID, func(state *pluginUpdateProgressState) {
		state.LatestVersion = release.TagName
		state.Phase = "downloading"
	})
	temporaryPath, err := downloadPluginReleaseAsset(requestContext, plugin.ID, asset, func(downloaded, total int64) {
		setPluginUpdateProgress(plugin.ID, func(state *pluginUpdateProgressState) {
			state.DownloadedBytes, state.TotalBytes = downloaded, total
			if total > 0 {
				state.Progress = int(downloaded * 85 / total)
			}
		})
	})
	if err != nil {
		finishPluginUpdate(plugin.ID, err.Error())
		return
	}
	defer os.Remove(temporaryPath)
	setPluginUpdateProgress(plugin.ID, func(state *pluginUpdateProgressState) { state.Phase = "installing"; state.Progress = 90 })
	if err := replacePluginFromUpdate(requestContext, plugin, temporaryPath, userID); err != nil {
		finishPluginUpdate(plugin.ID, err.Error())
		return
	}
	setPluginUpdateProgress(plugin.ID, func(state *pluginUpdateProgressState) {
		state.Phase = "completed"
		state.Progress = 100
		state.InProgress = false
	})
}

func setPluginUpdateProgress(id string, update func(*pluginUpdateProgressState)) {
	pluginUpdateJobs.Lock()
	state := pluginUpdateJobs.items[id]
	update(&state)
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	pluginUpdateJobs.items[id] = state
	pluginUpdateJobs.Unlock()
}

func finishPluginUpdate(id, message string) {
	setPluginUpdateProgress(id, func(state *pluginUpdateProgressState) {
		state.InProgress = false
		state.Phase = "failed"
		state.Error = message
	})
}

func pluginGitHubFromPlugin(plugin model.Plugin) (string, string, bool) {
	var manifest PluginManifest
	if json.Unmarshal([]byte(plugin.ManifestJSON), &manifest) != nil {
		return "", "", false
	}
	return pluginGitHubRepository(manifest.GitHub)
}

func fetchPluginReleaseCandidate(ctx context.Context, owner, repository string) (githubRelease, githubReleaseAsset, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+owner+"/"+repository+"/releases/latest", nil)
	if err != nil {
		return githubRelease{}, githubReleaseAsset{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "veloce-plugin-updater")
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return githubRelease{}, githubReleaseAsset{}, errors.New("GitHub release check failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return githubRelease{}, githubReleaseAsset{}, errors.New("GitHub has no available latest release")
	}
	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&release); err != nil || release.Draft {
		return githubRelease{}, githubReleaseAsset{}, errors.New("GitHub release response is invalid")
	}
	if !validSemanticVersion(release.TagName) {
		return githubRelease{}, githubReleaseAsset{}, errors.New("latest GitHub release must use semantic versioning")
	}
	for _, asset := range release.Assets {
		if strings.HasSuffix(strings.ToLower(strings.TrimSpace(asset.Name)), ".wasm") && validPluginReleaseDownloadURL(asset.BrowserDownloadURL) {
			return release, asset, nil
		}
	}
	return githubRelease{}, githubReleaseAsset{}, errors.New("latest GitHub release has no .wasm asset")
}

func validPluginReleaseDownloadURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && allowedCommunityPluginRedirect(parsed)
}

func downloadPluginReleaseAsset(ctx context.Context, pluginID string, asset githubReleaseAsset, onProgress func(int64, int64)) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.BrowserDownloadURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", "veloce-plugin-updater")
	response, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return "", errors.New("GitHub plugin download failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub plugin download returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > pluginMaxPackageBytes {
		return "", errors.New("plugin WASM is too large")
	}
	tmpRoot := filepath.Join(config.DataPath, "tmp")
	if err := os.MkdirAll(tmpRoot, 0o755); err != nil {
		return "", errors.New("failed to prepare plugin temp directory")
	}
	file, err := os.CreateTemp(tmpRoot, pluginID+"-update-*.wasm")
	if err != nil {
		return "", errors.New("failed to prepare plugin update")
	}
	temporaryPath := file.Name()
	reader := &pluginDownloadProgressReader{Reader: io.LimitReader(response.Body, pluginMaxPackageBytes+1), total: response.ContentLength, onProgress: onProgress}
	written, copyErr := io.Copy(file, reader)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || written > pluginMaxPackageBytes {
		_ = os.Remove(temporaryPath)
		if written > pluginMaxPackageBytes {
			return "", errors.New("plugin WASM is too large")
		}
		return "", errors.New("failed to download plugin update")
	}
	return temporaryPath, nil
}

type pluginDownloadProgressReader struct {
	io.Reader
	total      int64
	downloaded int64
	onProgress func(int64, int64)
}

func (reader *pluginDownloadProgressReader) Read(buffer []byte) (int, error) {
	count, err := reader.Reader.Read(buffer)
	if count > 0 {
		reader.downloaded += int64(count)
		if reader.onProgress != nil {
			reader.onProgress(reader.downloaded, reader.total)
		}
	}
	return count, err
}

func replacePluginFromUpdate(ctx context.Context, previous model.Plugin, downloadedPath string, userID uint) error {
	manifest, raw, err := ReadPluginManifestFromWASM(ctx, downloadedPath)
	if err != nil {
		return err
	}
	manifest = normalizePluginManifest(manifest)
	if err := validatePluginManifest(manifest); err != nil {
		return err
	}
	if manifest.ID != previous.ID {
		return errors.New("plugin update manifest id does not match installed plugin")
	}
	target := pluginWASMPath(previous.ID)
	if !previous.Enabled {
		target = pluginDisabledWASMPath(previous.ID)
	}
	backup := target + ".update-backup"
	if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("failed to prepare plugin update")
	}
	if err := os.Rename(target, backup); err != nil {
		return errors.New("installed plugin module is unavailable")
	}
	if err := os.Rename(downloadedPath, target); err != nil {
		_ = os.Rename(backup, target)
		return errors.New("failed to install plugin update")
	}
	next := previous
	next.Name, next.Version, next.Description, next.Author = manifest.Name, manifest.Version, manifest.Description, manifest.Author
	next.ManifestJSON, next.PermissionsJSON, next.HooksJSON, next.FrontendJSON, next.SettingsJSON, next.WASMPath = string(raw), mustJSON(manifest.Permissions), mustJSON(manifest.Hooks), string(manifest.Frontend), string(manifest.Settings), target
	if err := model.DB.Model(&model.Plugin{}).Where("id = ?", previous.ID).Updates(map[string]interface{}{"name": next.Name, "version": next.Version, "description": next.Description, "author": next.Author, "manifest_json": next.ManifestJSON, "permissions_json": next.PermissionsJSON, "hooks_json": next.HooksJSON, "frontend_json": next.FrontendJSON, "settings_json": next.SettingsJSON, "wasm_path": target, "last_error": ""}).Error; err != nil {
		_ = os.Remove(target)
		_ = os.Rename(backup, target)
		return errors.New("failed to save plugin update")
	}
	_ = os.Remove(backup)
	if next.Enabled {
		if err := InitializePluginWASM(ctx, next); err != nil {
			model.DB.Model(&model.Plugin{}).Where("id = ?", next.ID).Update("last_error", err.Error())
			recordPluginLog(userID, next.ID, "warn", "wasm_init_failed", err.Error(), "")
		}
	}
	recordPluginLog(userID, next.ID, "info", "updated", "Plugin updated from GitHub release", mustJSON(map[string]string{"version": next.Version}))
	return nil
}

func pluginUpdateStatusForManifest(ctx context.Context, plugin model.Plugin) pluginUpdateStatus {
	status := pluginUpdateStatus{ID: plugin.ID, CurrentVersion: plugin.Version}
	var manifest PluginManifest
	if err := json.Unmarshal([]byte(plugin.ManifestJSON), &manifest); err != nil {
		status.Error = "plugin manifest is invalid"
		return status
	}
	owner, repository, ok := pluginGitHubRepository(manifest.GitHub)
	if !ok {
		status.Error = "plugin does not declare a GitHub repository"
		return status
	}
	requestContext, cancel := contextWithShortTimeout(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(requestContext, http.MethodGet, "https://api.github.com/repos/"+owner+"/"+repository+"/releases/latest", nil)
	if err != nil {
		status.Error = "could not create GitHub request"
		return status
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "veloce-plugin-update-check")
	client := &http.Client{Timeout: 12 * time.Second}
	response, err := client.Do(req)
	if err != nil {
		status.Error = "GitHub release check failed"
		return status
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		status.Error = "GitHub has no available latest release"
		return status
	}
	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&release); err != nil || release.Draft {
		status.Error = "GitHub release response is invalid"
		return status
	}
	status.LatestVersion = strings.TrimSpace(release.TagName)
	if validSemanticVersion(status.LatestVersion) && validSemanticVersion(status.CurrentVersion) {
		status.UpdateAvailable = isNewerRelease(status.LatestVersion, status.CurrentVersion)
	} else {
		status.Error = "plugin and release versions must use semantic versioning"
	}
	return status
}

func contextWithShortTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 12*time.Second)
}

func pluginGitHubRepository(raw string) (owner, repository string, ok bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil || parsed.RawPath != "" {
		return "", "", false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 || !pluginGitHubPart(parts[0]) {
		return "", "", false
	}
	repository = strings.TrimSuffix(parts[1], ".git")
	if !pluginGitHubPart(repository) {
		return "", "", false
	}
	return parts[0], repository, true
}

func pluginGitHubPart(value string) bool {
	if value == "" || len(value) > 100 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.') {
			return false
		}
	}
	return true
}
