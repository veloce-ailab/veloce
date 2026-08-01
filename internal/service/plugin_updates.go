package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/veloce-ailab/veloce/internal/model"
)

type pluginUpdateStatus struct {
	ID              string `json:"id"`
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version,omitempty"`
	UpdateAvailable bool   `json:"update_available"`
	Error           string `json:"error,omitempty"`
}

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
