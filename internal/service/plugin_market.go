package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/veloce-ailab/veloce/internal/config"
	"github.com/veloce-ailab/veloce/internal/model"
)

const communityPluginMarketAPIBaseURL = "https://veloce-community.flweb.cn/api/v1"

type communityPluginMarketItem struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	RepositoryURL string `json:"repository_url"`
	Description   string `json:"description"`
	Author        string `json:"author"`
	AuthorLevel   int    `json:"author_level"`
	Status        string `json:"status"`
	Categories    []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"categories"`
}

type communityPluginMarketResponse struct {
	Items []communityPluginMarketItem `json:"items"`
}

// listPluginMarket proxies the approved listings from the fixed community
// origin. The frontend never gets to choose the remote URL.
func (api *pluginAPI) listPluginMarket(c *gin.Context) {
	user, ok := currentUserFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if !requirePluginAdmin(c, user) {
		return
	}
	if !CommunityFeatureEnabled() {
		c.JSON(http.StatusForbidden, gin.H{"error": "Community is disabled"})
		return
	}
	query := strings.TrimSpace(c.Query("q"))
	if len([]rune(query)) > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Search query is too long"})
		return
	}
	limit := c.DefaultQuery("limit", "100")
	remoteURL := communityPluginMarketAPIBaseURL + "/plugins?limit=" + url.QueryEscape(limit)
	if query != "" {
		remoteURL += "&q=" + url.QueryEscape(query)
	}
	var payload communityPluginMarketResponse
	if err := fetchCommunityPluginMarketJSON(c.Request.Context(), remoteURL, &payload); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Plugin market is temporarily unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": payload.Items})
}

// installPluginFromMarket retrieves the selected community plugin's current
// GitHub latest-release asset, then runs the same WASM manifest validation and
// persistence pipeline as a manual upload.
func (api *pluginAPI) installPluginFromMarket(c *gin.Context) {
	user, ok := currentUserFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if !requirePluginAdmin(c, user) {
		return
	}
	if !CommunityFeatureEnabled() {
		c.JSON(http.StatusForbidden, gin.H{"error": "Community is disabled"})
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" || len(id) > 120 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid community plugin id"})
		return
	}
	item := communityPluginMarketItem{}
	if err := fetchCommunityPluginMarketJSON(c.Request.Context(), communityPluginMarketAPIBaseURL+"/plugins/"+url.PathEscape(id), &item); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Community plugin not found"})
		return
	}
	if item.Status != "approved" {
		c.JSON(http.StatusNotFound, gin.H{"error": "Community plugin not found"})
		return
	}
	source, filename, err := fetchCommunityPluginRelease(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Plugin release is temporarily unavailable"})
		return
	}
	defer source.Close()
	plugin, err := installPluginStream(c.Request.Context(), source, filename, user.ID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"plugin": pluginListResponse(plugin, true), "market_plugin": item})
}

func fetchCommunityPluginMarketJSON(ctx context.Context, endpoint string, output interface{}) error {
	requestContext, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := communityKnowledgeHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("community service returned HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxCommunityKnowledgeImport+1)).Decode(output)
}

func fetchCommunityPluginRelease(ctx context.Context, id string) (io.ReadCloser, string, error) {
	requestContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, communityPluginMarketAPIBaseURL+"/plugins/"+url.PathEscape(id)+"/download", nil)
	if err != nil {
		cancel()
		return nil, "", err
	}
	client := &http.Client{
		Timeout: 45 * time.Second,
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if len(via) > 5 || !allowedCommunityPluginRedirect(next.URL) {
				return errors.New("unsafe plugin release redirect")
			}
			return nil
		},
	}
	response, err := client.Do(request)
	if err != nil {
		cancel()
		return nil, "", err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		response.Body.Close()
		cancel()
		return nil, "", fmt.Errorf("community plugin download returned HTTP %d", response.StatusCode)
	}
	filename := filenameFromPluginRelease(response)
	if !strings.HasSuffix(strings.ToLower(filename), ".wasm") {
		response.Body.Close()
		cancel()
		return nil, "", errors.New("latest plugin release asset must be a .wasm file")
	}
	return &communityPluginReleaseBody{ReadCloser: response.Body, cancel: cancel}, filename, nil
}

type communityPluginReleaseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (body *communityPluginReleaseBody) Close() error {
	body.cancel()
	return body.ReadCloser.Close()
}

func allowedCommunityPluginRedirect(target *url.URL) bool {
	if target == nil || target.Scheme != "https" || target.User != nil {
		return false
	}
	host := strings.ToLower(target.Hostname())
	return host == "github.com" || strings.HasSuffix(host, ".github.com") || strings.HasSuffix(host, ".githubusercontent.com")
}

func filenameFromPluginRelease(response *http.Response) string {
	if disposition := response.Header.Get("Content-Disposition"); disposition != "" {
		if _, params, err := mime.ParseMediaType(disposition); err == nil && params["filename"] != "" {
			return path.Base(params["filename"])
		}
	}
	if response.Request != nil && response.Request.URL != nil {
		return path.Base(response.Request.URL.Path)
	}
	return "plugin.wasm"
}

func installPluginStream(ctx context.Context, source io.Reader, filename string, userID uint) (model.Plugin, error) {
	tmpRoot := filepath.Join(config.DataPath, "tmp")
	if err := os.MkdirAll(tmpRoot, 0o755); err != nil {
		return model.Plugin{}, errors.New("failed to prepare plugin temp directory")
	}
	tmpDir, err := os.MkdirTemp(tmpRoot, "plugin-market-*")
	if err != nil {
		return model.Plugin{}, errors.New("failed to prepare plugin temp directory")
	}
	defer os.RemoveAll(tmpDir)
	manifest, manifestRaw, tempWASMPath, err := prepareUploadedPlugin(ctx, source, filename, tmpDir)
	if err != nil {
		return model.Plugin{}, err
	}
	if err := validatePluginManifest(manifest); err != nil {
		return model.Plugin{}, err
	}
	wasmPath := pluginWASMPath(manifest.ID)
	if err := os.Remove(wasmPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return model.Plugin{}, errors.New("failed to replace old plugin files")
	}
	if err := os.MkdirAll(filepath.Dir(wasmPath), 0o755); err != nil {
		return model.Plugin{}, errors.New("failed to prepare plugin directory")
	}
	if err := os.Rename(tempWASMPath, wasmPath); err != nil {
		return model.Plugin{}, errors.New("failed to install plugin WASM")
	}
	plugin := model.Plugin{ID: manifest.ID, Name: manifest.Name, Version: manifest.Version, Description: manifest.Description, Author: manifest.Author, Enabled: true, ManifestJSON: string(manifestRaw), PermissionsJSON: mustJSON(manifest.Permissions), HooksJSON: mustJSON(manifest.Hooks), FrontendJSON: string(manifest.Frontend), SettingsJSON: string(manifest.Settings), Path: filepath.Dir(wasmPath), WASMPath: wasmPath}
	var existing model.Plugin
	if err := model.DB.Select("global_config_json").Where("id = ?", plugin.ID).Limit(1).Find(&existing).Error; err == nil {
		plugin.GlobalConfigJSON = existing.GlobalConfigJSON
	}
	if err := model.DB.Where(&model.Plugin{ID: plugin.ID}).Assign(plugin).FirstOrCreate(&plugin).Error; err != nil {
		return model.Plugin{}, errors.New("failed to save plugin")
	}
	if err := setUserPluginEnabled(userID, plugin.ID, true); err != nil {
		return model.Plugin{}, errors.New("failed to enable plugin")
	}
	if err := InitializePluginWASM(ctx, plugin); err != nil {
		model.DB.Model(&plugin).Update("last_error", err.Error())
		recordPluginLog(userID, plugin.ID, "warn", "wasm_init_failed", err.Error(), "")
	} else if plugin.WASMPath != "" {
		model.DB.Model(&plugin).Update("last_error", "")
		recordPluginLog(userID, plugin.ID, "info", "wasm_init", "WASM plugin initialized from community market", "")
	}
	DispatchPluginHooks(ctx, PluginHookInput{Point: PluginHookPointPluginInstalled, UserID: userID, Source: "plugin_market", Payload: map[string]interface{}{"plugin_id": plugin.ID, "version": plugin.Version}})
	return plugin, nil
}
