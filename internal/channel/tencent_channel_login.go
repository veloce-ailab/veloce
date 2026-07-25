package channel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	communityservice "github.com/veloce-ailab/veloce/internal/service"
	"github.com/gin-gonic/gin"
	qrcode "github.com/skip2/go-qrcode"
)

const (
	tencentChannelCLIMinVersion      = "1.0.6"
	tencentChannelQRCodeDataURLWidth = 256
)

func (api *API) startTencentChannelLogin(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if !requireEnabled(c) {
		return
	}
	integration, ok := loadUserTencentChannelIntegration(c, user.ID)
	if !ok {
		return
	}
	version, ok := api.ensureTencentChannelCLI(c, integration)
	if !ok {
		return
	}
	result, err := runTencentChannelCLI(c.Request.Context(), integration, "tencent-channel-cli login --json --yes", 30)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error":        formatTencentChannelCLIError(err, result),
			"install_hint": "请在连接器所在设备执行 npm i -g tencent-channel-cli 后重试。",
		})
		return
	}
	payload := parseTencentChannelCLIJSON(result)
	if err := tencentChannelCLIResultError(payload); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	response := tencentChannelLoginStartResponse(payload)
	response["cli_version"] = version
	if strings.TrimSpace(stringFromMap(response, "qrcode_url")) == "" && strings.TrimSpace(stringFromMap(response, "verification_uri")) != "" {
		response["qrcode_url"] = stringFromMap(response, "verification_uri")
	}
	if strings.TrimSpace(stringFromMap(response, "qr_data_url")) == "" {
		if dataURL := tencentChannelQRCodeDataURL(stringFromMap(response, "qrcode_url")); dataURL != "" {
			response["qr_data_url"] = dataURL
		}
	}
	if stringFromMap(response, "status") == "" {
		response["status"] = "pending"
	}
	c.JSON(http.StatusOK, response)
}

func (api *API) waitTencentChannelLogin(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if !requireEnabled(c) {
		return
	}
	integration, ok := loadUserTencentChannelIntegration(c, user.ID)
	if !ok {
		return
	}
	version, ok := api.ensureTencentChannelCLI(c, integration)
	if !ok {
		return
	}
	pollCtx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Minute+10*time.Second)
	defer cancel()
	result, err := runTencentChannelCLI(pollCtx, integration, "tencent-channel-cli login poll-token --json", 610)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error":        formatTencentChannelCLIError(err, result),
			"install_hint": "请在连接器所在设备执行 npm i -g tencent-channel-cli 后重试。",
		})
		return
	}
	payload := parseTencentChannelCLIJSON(result)
	if err := tencentChannelCLIResultError(payload); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	response := tencentChannelLoginWaitResponse(payload)
	response["cli_version"] = version
	if tencentChannelLoginAuthorized(response) {
		_ = updateTencentChannelProviderConfig(integration.ID, func(config map[string]interface{}) {
			config["login_status"] = "connected"
			config["login_connected_at"] = time.Now().Format(time.RFC3339)
			if connectivity := stringFromMap(response, "connectivity"); connectivity != "" {
				config["connectivity"] = connectivity
			}
			if setupHint, ok := response["setup_hint"]; ok {
				config["setup_hint"] = setupHint
			}
		})
	}
	c.JSON(http.StatusOK, response)
}

func (api *API) tencentChannelGuilds(c *gin.Context) {
	api.runTencentChannelReadCommand(c, "tencent-channel-cli manage get-my-join-guild-info --json")
}

func (api *API) tencentChannelChannels(c *gin.Context) {
	integration, ok := api.loadTencentChannelNoticeIntegration(c)
	if !ok {
		return
	}
	var input struct {
		GuildID string `json:"guild_id"`
	}
	_ = c.ShouldBindJSON(&input)
	guildID := strings.TrimSpace(input.GuildID)
	if !regexp.MustCompile(`^\d+$`).MatchString(guildID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Tencent channel guild_id is required"})
		return
	}
	if _, ok := api.ensureTencentChannelCLI(c, integration); !ok {
		return
	}
	result, err := runTencentChannelCLI(c.Request.Context(), integration, "tencent-channel-cli manage get-guild-channel-list --guild-id "+guildID+" --json", 60)
	payload := parseTencentChannelCLIJSON(result)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": formatTencentChannelCLIError(err, result), "result": payload})
		return
	}
	if cliErr := tencentChannelCLIResultError(payload); cliErr != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": cliErr.Error(), "result": payload})
		return
	}
	c.JSON(http.StatusOK, payload)
}

func (api *API) runTencentChannelReadCommand(c *gin.Context, command string) {
	integration, ok := api.loadTencentChannelNoticeIntegration(c)
	if !ok {
		return
	}
	if _, ok := api.ensureTencentChannelCLI(c, integration); !ok {
		return
	}
	result, err := runTencentChannelCLI(c.Request.Context(), integration, command, 60)
	payload := parseTencentChannelCLIJSON(result)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": formatTencentChannelCLIError(err, result), "result": payload})
		return
	}
	if cliErr := tencentChannelCLIResultError(payload); cliErr != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": cliErr.Error(), "result": payload})
		return
	}
	c.JSON(http.StatusOK, payload)
}

func (api *API) loadTencentChannelNoticeIntegration(c *gin.Context) (MessageChannelIntegration, bool) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return MessageChannelIntegration{}, false
	}
	if !requireEnabled(c) {
		return MessageChannelIntegration{}, false
	}
	return loadUserTencentChannelIntegration(c, user.ID)
}

func (api *API) ensureTencentChannelCLI(c *gin.Context, integration MessageChannelIntegration) (string, bool) {
	result, err := runTencentChannelCLI(c.Request.Context(), integration, "tencent-channel-cli version --json", 15)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{
			"error":        formatTencentChannelCLIError(err, result),
			"install_hint": "腾讯频道连接器需要先安装 CLI：npm i -g tencent-channel-cli",
		})
		return "", false
	}
	payload := parseTencentChannelCLIJSON(result)
	if err := tencentChannelCLIResultError(payload); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "install_hint": "腾讯频道连接器需要先安装 CLI：npm i -g tencent-channel-cli"})
		return "", false
	}
	version := firstTencentChannelString(payload, "version")
	if data, ok := payload["data"].(map[string]interface{}); ok && version == "" {
		version = firstTencentChannelString(data, "version")
	}
	if version == "" {
		version = strings.TrimSpace(result)
	}
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if !tencentChannelVersionAtLeast(version, tencentChannelCLIMinVersion) {
		c.JSON(http.StatusBadGateway, gin.H{
			"error":        "tencent-channel-cli version must be >= " + tencentChannelCLIMinVersion + ", current: " + version,
			"install_hint": "请在连接器所在设备执行 npm i -g tencent-channel-cli 升级后重试。",
		})
		return "", false
	}
	return version, true
}

func runTencentChannelCLI(ctx context.Context, integration MessageChannelIntegration, command string, timeoutSec int) (string, error) {
	if timeoutSec <= 0 {
		timeoutSec = 30
	}
	return communityservice.RunMessageChannelConnectorAction(
		ctx,
		integration.UserID,
		messageChannelApprovalRunID(integration.ID, "tencent-channel-login"),
		integration.DefaultDeviceID,
		integration.DefaultWorkspacePath,
		integration.DefaultWorkspaceUnrestricted,
		"run_command",
		map[string]interface{}{
			"command":     command,
			"timeout_sec": timeoutSec,
		},
		true,
		[]string{
			"tencent-channel-cli version",
			"tencent-channel-cli login",
			"tencent-channel-cli feed get-notices",
			"tencent-channel-cli manage get-my-join-guild-info",
			"tencent-channel-cli manage get-guild-channel-list",
		},
	)
}

func parseTencentChannelCLIJSON(output string) map[string]interface{} {
	output = strings.TrimSpace(output)
	if output == "" {
		return map[string]interface{}{}
	}
	var payload map[string]interface{}
	if json.Unmarshal([]byte(output), &payload) == nil {
		if wrapped := tencentChannelCLIWrappedOutput(payload); wrapped != "" && wrapped != output {
			if inner := parseTencentChannelCLIJSON(wrapped); tencentChannelCLIJSONLooksUseful(inner) {
				return inner
			}
		}
		return payload
	}
	best := map[string]interface{}{}
	bestScore := 0
	bestLength := 0
	for start := 0; start < len(output); start++ {
		if output[start] != '{' {
			continue
		}
		candidate := extractTencentChannelJSONObject(output[start:])
		if candidate == "" {
			continue
		}
		if json.Unmarshal([]byte(candidate), &payload) == nil {
			if wrapped := tencentChannelCLIWrappedOutput(payload); wrapped != "" && wrapped != candidate {
				if inner := parseTencentChannelCLIJSON(wrapped); tencentChannelCLIJSONLooksUseful(inner) {
					payload = inner
				}
			}
			score := tencentChannelCLIJSONScore(payload)
			if score > bestScore || (score == bestScore && len(candidate) > bestLength) {
				best = payload
				bestScore = score
				bestLength = len(candidate)
			}
		}
	}
	if bestScore > 0 {
		return best
	}
	return map[string]interface{}{"raw": output}
}

func extractTencentChannelJSONObject(text string) string {
	depth := 0
	inString := false
	escaped := false
	for index, r := range text {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch r {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch r {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[:index+1]
			}
			if depth < 0 {
				return ""
			}
		}
	}
	return ""
}

func tencentChannelCLIWrappedOutput(payload map[string]interface{}) string {
	for _, key := range []string{"stdout", "result", "output"} {
		if text := firstTencentChannelString(payload, key); text != "" {
			return text
		}
	}
	return ""
}

func tencentChannelCLIJSONLooksUseful(payload map[string]interface{}) bool {
	return tencentChannelCLIJSONScore(payload) > 0
}

func tencentChannelCLIJSONScore(payload map[string]interface{}) int {
	if len(payload) == 0 {
		return 0
	}
	score := 0
	if _, ok := payload["data"]; ok {
		score += 10
	}
	if _, ok := payload["error"]; ok {
		score += 10
	}
	if _, ok := payload["success"]; ok {
		if tencentChannelCLIWrappedOutput(payload) == "" {
			score += 10
		}
	}
	for _, key := range []string{"qr_code", "qrcode", "verification_uri", "status", "version"} {
		if firstTencentChannelString(payload, key) != "" {
			score += 2
		}
	}
	return score
}

func tencentChannelCLIResultError(payload map[string]interface{}) error {
	if len(payload) == 0 {
		return errors.New("empty tencent-channel-cli response")
	}
	if success, ok := payload["success"].(bool); ok && !success {
		if message := tencentChannelErrorMessage(payload["error"]); message != "" {
			return errors.New(message)
		}
		if message := firstTencentChannelString(payload, "message"); message != "" {
			return errors.New(message)
		}
		return errors.New("tencent-channel-cli returned success=false")
	}
	return nil
}

func tencentChannelErrorMessage(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case map[string]interface{}:
		if message := firstTencentChannelString(typed, "message", "error", "detail"); message != "" {
			return message
		}
		data, _ := json.Marshal(typed)
		return strings.TrimSpace(string(data))
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func tencentChannelLoginStartResponse(payload map[string]interface{}) map[string]interface{} {
	data := tencentChannelPayloadData(payload)
	response := map[string]interface{}{
		"status":           firstTencentChannelString(data, "status"),
		"verification_uri": firstTencentChannelString(data, "verification_uri", "verification_url", "auth_url", "url"),
		"qrcode_url":       firstTencentChannelString(data, "qrcode_url", "qr_url", "verification_uri", "verification_url", "auth_url", "url"),
		"qrcode_path":      firstTencentChannelString(data, "qrcode_path", "qr_path"),
	}
	if seconds := firstTencentChannelNumber(data, "expires_in_s", "expires_in", "expire_seconds"); seconds > 0 {
		response["expires_in_s"] = seconds
	}
	if message := firstTencentChannelString(data, "message"); message != "" {
		response["message"] = message
	}
	if dataURL := tencentChannelQRCodeBase64DataURL(firstTencentChannelString(data, "qr_code", "qrcode", "qr_code_base64", "qrcode_base64", "qrcode_png_base64")); dataURL != "" {
		response["qr_data_url"] = dataURL
	}
	return response
}

func tencentChannelLoginWaitResponse(payload map[string]interface{}) map[string]interface{} {
	data := tencentChannelPayloadData(payload)
	response := map[string]interface{}{
		"status":       firstTencentChannelString(data, "status"),
		"connectivity": firstTencentChannelString(data, "connectivity"),
		"message":      firstTencentChannelString(data, "message"),
	}
	if response["status"] == "" && payload["success"] == true {
		response["status"] = "authorized"
	}
	if setupHint, ok := data["setup_hint"]; ok {
		response["setup_hint"] = setupHint
	}
	if subscribeHint, ok := data["subscribe_hint"]; ok {
		response["subscribe_hint"] = subscribeHint
	}
	return response
}

func tencentChannelPayloadData(payload map[string]interface{}) map[string]interface{} {
	if data, ok := payload["data"].(map[string]interface{}); ok {
		return data
	}
	return payload
}

func tencentChannelLoginAuthorized(response map[string]interface{}) bool {
	status := strings.ToLower(strings.TrimSpace(stringFromMap(response, "status")))
	switch status {
	case "authorized", "connected", "success", "ok":
		return true
	default:
		return false
	}
}

func tencentChannelQRCodeDataURL(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	png, err := qrcode.Encode(content, qrcode.Medium, tencentChannelQRCodeDataURLWidth)
	if err != nil {
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
}

func tencentChannelQRCodeBase64DataURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "data:image/") {
		return value
	}
	if _, err := base64.StdEncoding.DecodeString(value); err != nil {
		return ""
	}
	return "data:image/png;base64," + value
}

func firstTencentChannelString(payload map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := payload[key]; ok {
			if text := strings.TrimSpace(fmt.Sprint(value)); text != "" && text != "<nil>" {
				return text
			}
		}
	}
	return ""
}

func firstTencentChannelNumber(payload map[string]interface{}, keys ...string) int {
	for _, key := range keys {
		value, ok := payload[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case float64:
			return int(typed)
		case int:
			return typed
		case string:
			if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
				return parsed
			}
		}
	}
	return 0
}

func formatTencentChannelCLIError(err error, output string) string {
	message := strings.TrimSpace(output)
	if message == "" && err != nil {
		message = err.Error()
	}
	if strings.Contains(strings.ToLower(message), "not recognized") ||
		strings.Contains(strings.ToLower(message), "not found") ||
		strings.Contains(strings.ToLower(message), "executable file not found") {
		return "连接器未安装 tencent-channel-cli，请先执行 npm i -g tencent-channel-cli。"
	}
	if message == "" {
		return "tencent-channel-cli command failed"
	}
	return message
}

func tencentChannelVersionAtLeast(current string, minimum string) bool {
	currentParts := parseTencentChannelVersion(current)
	minimumParts := parseTencentChannelVersion(minimum)
	for i := 0; i < len(minimumParts); i++ {
		if currentParts[i] > minimumParts[i] {
			return true
		}
		if currentParts[i] < minimumParts[i] {
			return false
		}
	}
	return true
}

func parseTencentChannelVersion(value string) [3]int {
	var result [3]int
	matches := regexp.MustCompile(`\d+`).FindAllString(value, 3)
	for i, match := range matches {
		parsed, _ := strconv.Atoi(match)
		result[i] = parsed
	}
	return result
}

func stringFromMap(payload map[string]interface{}, key string) string {
	if payload == nil {
		return ""
	}
	value, ok := payload[key]
	if !ok || value == nil {
		return ""
	}
	text := strings.TrimSpace(fmt.Sprint(value))
	if text == "<nil>" {
		return ""
	}
	return text
}
