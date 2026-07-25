package service

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

const (
	taskKindImage      = "image"
	taskKindVideo      = "video"
	taskKindMidjourney = "midjourney"
)

func (s *ProxyService) HandleTokenBalance(c *gin.Context) {
	user, ok := currentUserFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, authErrorResponse(http.StatusUnauthorized, "Invalid authentication credentials", "authentication_error"))
		return
	}
	apiKey := currentAPIKey(c)
	if PersonalModeEnabled() {
		c.JSON(http.StatusOK, gin.H{
			"success":         true,
			"remain_balance":  decimal.NewFromInt(-1),
			"remain_credits":  balanceCredits(decimal.NewFromInt(-1)),
			"used_balance":    decimal.Zero,
			"used_credits":    0,
			"unlimited_quota": true,
		})
		return
	}
	if apiKey == nil {
		c.JSON(http.StatusOK, gin.H{
			"success":         true,
			"remain_balance":  user.Balance,
			"remain_credits":  balanceCredits(user.Balance),
			"used_balance":    decimal.Zero,
			"used_credits":    0,
			"unlimited_quota": true,
		})
		return
	}
	used, err := APIKeyUsageCostSince(model.DB, apiKey.ID, user.ID, apiKey.UsageResetAt)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "Failed to get token info: " + err.Error()})
		return
	}
	unlimited := apiKey.QuotaLimit.LessThanOrEqual(decimal.Zero)
	remain := apiKey.QuotaLimit.Sub(used)
	if remain.LessThan(decimal.Zero) {
		remain = decimal.Zero
	}
	if unlimited {
		remain = decimal.NewFromInt(-1)
	}
	c.JSON(http.StatusOK, gin.H{
		"success":         true,
		"remain_balance":  remain,
		"remain_credits":  balanceCredits(remain),
		"used_balance":    used,
		"used_credits":    balanceCredits(used),
		"unlimited_quota": unlimited,
	})
}

func (s *ProxyService) HandleUserBalance(c *gin.Context) {
	user, ok := currentUserFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, authErrorResponse(http.StatusUnauthorized, "Invalid authentication credentials", "authentication_error"))
		return
	}
	if PersonalModeEnabled() {
		c.JSON(http.StatusOK, gin.H{
			"success":        true,
			"remain_balance": decimal.NewFromInt(-1),
			"remain_credits": balanceCredits(decimal.NewFromInt(-1)),
			"used_balance":   decimal.Zero,
			"used_credits":   0,
		})
		return
	}
	summary, err := model.SummarizeTokenLogs(model.TokenLogFilter{UserID: &user.ID})
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "Failed to get user info: " + err.Error()})
		return
	}
	used := summary.TotalCost
	c.JSON(http.StatusOK, gin.H{
		"success":        true,
		"remain_balance": user.Balance,
		"remain_credits": balanceCredits(user.Balance),
		"used_balance":   used,
		"used_credits":   balanceCredits(used),
	})
}

func (s *ProxyService) HandleImageGenerationCompatible(c *gin.Context) {
	s.handleCompatibleJSONGeneration(c, compatibleGenerationOptions{
		Kind:         taskKindImage,
		UpstreamPath: "/v1/images/generations",
		EstimateUsage: func(target *proxyTarget, requestBody map[string]interface{}, responseData map[string]interface{}) (usageTokenCounts, int, string, bool) {
			return imageUsageTokenCounts(target.ModelName, requestBody, responseData), 0, "", true
		},
	})
}

func (s *ProxyService) HandleVideoGenerationCompatible(c *gin.Context) {
	s.handleCompatibleJSONGeneration(c, compatibleGenerationOptions{
		Kind:         taskKindVideo,
		UpstreamPath: "/v1/videos/generations",
		EstimateUsage: func(target *proxyTarget, requestBody map[string]interface{}, responseData map[string]interface{}) (usageTokenCounts, int, string, bool) {
			return videoUsageTokenCounts(target.ModelName, requestBody, responseData, target.billingModel())
		},
	})
}

func (s *ProxyService) HandleVideoRemix(c *gin.Context) {
	parentTaskID := strings.TrimSpace(c.Param("id"))
	if parentTaskID == "" {
		c.JSON(http.StatusBadRequest, authErrorResponse(http.StatusBadRequest, "Invalid task ID", "invalid_request_error"))
		return
	}
	s.handleCompatibleJSONGeneration(c, compatibleGenerationOptions{
		Kind:         taskKindVideo,
		UpstreamPath: "/v1/videos/" + url.PathEscape(parentTaskID) + "/remix",
		MutateRequest: func(requestBody map[string]interface{}) {
			requestBody["parent_task_id"] = parentTaskID
		},
		EstimateUsage: func(target *proxyTarget, requestBody map[string]interface{}, responseData map[string]interface{}) (usageTokenCounts, int, string, bool) {
			return videoUsageTokenCounts(target.ModelName, requestBody, responseData, target.billingModel())
		},
	})
}

func (s *ProxyService) HandleSeedancePrivateAvatar(c *gin.Context) {
	s.handleCompatibleJSONGeneration(c, compatibleGenerationOptions{
		Kind:         taskKindVideo,
		DefaultModel: "doubao-seedance-2.0",
		UpstreamPath: "/v1/seedance2/private-avatar",
		EstimateUsage: func(target *proxyTarget, requestBody map[string]interface{}, responseData map[string]interface{}) (usageTokenCounts, int, string, bool) {
			return videoUsageTokenCounts(target.ModelName, requestBody, responseData, target.billingModel())
		},
	})
}

func (s *ProxyService) HandleMidjourneyCreate(c *gin.Context) {
	path := c.Request.URL.Path
	if strings.TrimSpace(path) == "" {
		path = "/v1/midjourney/generations"
	}
	s.handleCompatibleJSONGeneration(c, compatibleGenerationOptions{
		Kind:         taskKindMidjourney,
		DefaultModel: "midjourney",
		UpstreamPath: path,
		EstimateUsage: func(target *proxyTarget, requestBody map[string]interface{}, responseData map[string]interface{}) (usageTokenCounts, int, string, bool) {
			return imageUsageTokenCounts(target.ModelName, requestBody, responseData), 0, "", true
		},
	})
}

type compatibleGenerationOptions struct {
	Kind          string
	DefaultModel  string
	UpstreamPath  string
	MutateRequest func(requestBody map[string]interface{})
	EstimateUsage func(target *proxyTarget, requestBody map[string]interface{}, responseData map[string]interface{}) (usageTokenCounts, int, string, bool)
}

func (s *ProxyService) handleCompatibleJSONGeneration(c *gin.Context, opts compatibleGenerationOptions) {
	requestBody, _, ok := readProxyJSONBody(c)
	if !ok {
		return
	}
	modelName := strings.TrimSpace(videoRequestModelName(requestBody))
	if modelName == "" && opts.DefaultModel != "" {
		modelName = opts.DefaultModel
		requestBody["model"] = modelName
	}
	if modelName == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Model not specified"})
		return
	}
	if opts.MutateRequest != nil {
		opts.MutateRequest(requestBody)
	}

	target, ok := s.resolveTarget(c, modelName)
	if !ok {
		return
	}
	if SensitiveFilterEnabled() && compatibleRequestBlocked(opts.Kind, requestBody) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Request blocked by content policy", "type": "content_policy"})
		return
	}
	if err := ValidateConfiguredHTTPURL(target.Channel.BaseURL); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Upstream URL blocked by SSRF protection", "type": "upstream_error"})
		return
	}

	prepared, err := s.prepareCompatibleGenerationRequest(c, opts, target, requestBody)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error(), "type": "invalid_request"})
		return
	}
	prepared.Context = c.Request.Context()
	resp, err := s.doUpstreamRequest(prepared, &target.Channel)
	if err != nil {
		logUpstreamRequestFailure(c, &target.Channel, prepared.URL, prepared.Body, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "Upstream request failed", "type": "upstream_error"})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to read upstream response"})
			return
		}
		logUpstreamError(c, &target.Channel, prepared.URL, resp.StatusCode, prepared.Body, respBody)
		c.JSON(resp.StatusCode, gin.H{"error": "Upstream request failed", "type": "upstream_error"})
		return
	}
	if isStreamingResponse(resp) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Streaming is not supported for this endpoint", "type": "unsupported_stream"})
		return
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to read upstream response"})
		return
	}
	var responseData map[string]interface{}
	_ = json.Unmarshal(respBody, &responseData)
	if SensitiveFilterEnabled() && SensitiveFilterScope() == SensitiveFilterScopeRequestResponse && compatibleResponseBlocked(opts.Kind, responseData) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Response blocked by content policy", "type": "content_policy"})
		return
	}

	usage, status, message, ok := opts.EstimateUsage(target, requestBody, responseData)
	if !ok {
		c.JSON(status, gin.H{"error": message, "type": "invalid_request"})
		return
	}
	cost, status, message, err := s.billUsageAndReturnCost(c, target.User, target.APIKey, &target.Channel, &target.ModelConfig, target.billingModelName(), usage, target.billingModel())
	if err != nil {
		c.JSON(status, gin.H{"error": message})
		return
	}

	upstreamTaskID := upstreamTaskIDFromPayload(responseData)
	if upstreamTaskID == "" {
		writeUpstreamResponse(c, resp, respBody)
		return
	}
	task, err := createCompatibleTask(target, requestBody, responseData, opts.Kind, upstreamTaskID, cost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create task"})
		return
	}
	c.JSON(http.StatusOK, compatibleTaskCreateResponse(task))
}

func (s *ProxyService) prepareCompatibleGenerationRequest(c *gin.Context, opts compatibleGenerationOptions, target *proxyTarget, requestBody map[string]interface{}) (preparedUpstreamRequest, error) {
	protocol := channelProtocol(target.Channel.Type)
	if opts.Kind == taskKindVideo && protocol == protocolKling {
		return prepareVideoGenerationRequest(&target.Channel, protocol, target.upstreamModelName(), requestBody)
	}
	payload := make(map[string]interface{}, len(requestBody))
	for key, value := range requestBody {
		payload[key] = value
	}
	payload["model"] = target.upstreamModelName()
	body, err := json.Marshal(payload)
	if err != nil {
		return preparedUpstreamRequest{}, err
	}
	headers := jsonHeaders()
	headers.Set("Authorization", "Bearer "+strings.TrimSpace(target.Channel.APIKey))
	return preparedUpstreamRequest{
		Method:  http.MethodPost,
		URL:     upstreamURLForRequest(target.Channel.BaseURL, opts.UpstreamPath),
		Body:    body,
		Header:  headers,
		Context: c.Request.Context(),
	}, nil
}

func createCompatibleTask(target *proxyTarget, requestBody map[string]interface{}, responseData map[string]interface{}, kind string, upstreamTaskID string, cost decimal.Decimal) (model.VideoTask, error) {
	taskID := newVideoTaskID()
	status := normalizeCompatibleTaskStatus(videoTaskStatusFromPayload(responseData))
	if status == "" {
		status = "pending"
	}
	requestPayload := make(map[string]interface{}, len(requestBody)+1)
	for key, value := range requestBody {
		requestPayload[key] = value
	}
	requestPayload["_task_kind"] = kind
	requestData, _ := json.Marshal(requestPayload)
	responsePayload, _ := json.Marshal(responseData)
	task := model.VideoTask{
		ID:                taskID,
		UserID:            target.User.ID,
		APIKeyID:          apiKeyID(target.APIKey),
		UserChannelID:     target.Channel.UserChannelID,
		ChannelID:         target.Channel.ID,
		ModelConfigID:     target.ModelConfig.ID,
		ModelName:         target.ModelName,
		BillingModelName:  target.billingModelName(),
		UpstreamTaskID:    upstreamTaskID,
		Status:            status,
		Cost:              cost,
		RequestPayload:    string(requestData),
		ResponsePayload:   string(responsePayload),
		LastStatusPayload: string(responsePayload),
	}
	return task, model.DB.Create(&task).Error
}

func (s *ProxyService) HandleUnifiedTaskStatus(c *gin.Context) {
	task, payload, ok := s.compatibleTask(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": http.StatusOK, "data": compatibleTaskData(task, payload)})
}

func (s *ProxyService) HandleImageGenerationTaskStatus(c *gin.Context) {
	task, payload, ok := s.compatibleTask(c)
	if !ok {
		return
	}
	data := compatibleTaskData(task, payload)
	if urlValue := firstResultURL(data); urlValue != "" {
		data["url"] = urlValue
	}
	c.JSON(http.StatusOK, data)
}

func (s *ProxyService) HandleMidjourneyStatus(c *gin.Context) {
	task, payload, ok := s.compatibleTask(c)
	if !ok {
		return
	}
	data := compatibleTaskData(task, payload)
	result, _ := data["result"].(map[string]interface{})
	imageURLs := resultURLs(result, "images")
	response := gin.H{
		"id":                task.ID,
		"task_id":           task.ID,
		"status":            data["status"],
		"progress":          data["progress"],
		"cost":              data["cost"],
		"credits_cost":      data["credits_cost"],
		"image_urls":        imageURLs,
		"result":            result,
		"upstream_id":       task.UpstreamTaskID,
		"upstream_response": payload,
	}
	if len(imageURLs) > 0 {
		response["grid_image_url"] = imageURLs[0]
	}
	if buttons := compatibleButtons(payload); len(buttons) > 0 {
		response["buttons"] = buttons
	}
	c.JSON(http.StatusOK, response)
}

func (s *ProxyService) compatibleTask(c *gin.Context) (model.VideoTask, map[string]interface{}, bool) {
	taskID := strings.TrimSpace(c.Param("id"))
	if taskID == "" {
		taskID = strings.TrimSpace(c.Param("task_id"))
	}
	if taskID == "" {
		c.JSON(http.StatusBadRequest, authErrorResponse(http.StatusBadRequest, "Invalid task ID", "invalid_request_error"))
		return model.VideoTask{}, nil, false
	}
	user, ok := currentUserFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, authErrorResponse(http.StatusUnauthorized, "Invalid authentication credentials", "authentication_error"))
		return model.VideoTask{}, nil, false
	}
	var task model.VideoTask
	if err := model.DB.Where("id = ? AND user_id = ?", taskID, user.ID).First(&task).Error; err != nil {
		c.JSON(http.StatusNotFound, authErrorResponse(http.StatusNotFound, "Task not found", "invalid_request_error"))
		return model.VideoTask{}, nil, false
	}

	payload := map[string]interface{}{}
	if !terminalCompatibleTaskStatus(task.Status) && strings.TrimSpace(task.UpstreamTaskID) != "" && task.UpstreamTaskID != task.ID {
		var channel model.Channel
		if err := model.DB.First(&channel, task.ChannelID).Error; err != nil {
			c.JSON(http.StatusInternalServerError, authErrorResponse(http.StatusInternalServerError, "Failed to load task channel", "server_error"))
			return model.VideoTask{}, nil, false
		}
		if err := ValidateConfiguredHTTPURL(channel.BaseURL); err != nil {
			c.JSON(http.StatusBadGateway, authErrorResponse(http.StatusBadGateway, "Upstream URL blocked by SSRF protection", "bad_gateway"))
			return model.VideoTask{}, nil, false
		}
		body, statusPayload, ok := s.fetchCompatibleTask(c, &channel, taskKindFromTask(task), task.UpstreamTaskID)
		if !ok {
			return model.VideoTask{}, nil, false
		}
		payload = statusPayload
		nextStatus := normalizeCompatibleTaskStatus(videoTaskStatusFromPayload(statusPayload))
		if nextStatus == "" {
			nextStatus = task.Status
		}
		updates := map[string]interface{}{
			"status":              nextStatus,
			"last_status_payload": string(body),
		}
		if err := model.DB.Model(&task).Updates(updates).Error; err == nil {
			task.Status = nextStatus
			task.LastStatusPayload = string(body)
		}
	} else {
		_ = json.Unmarshal([]byte(firstNonEmptyString(task.LastStatusPayload, task.ResponsePayload)), &payload)
	}
	return task, payload, true
}

func (s *ProxyService) fetchCompatibleTask(c *gin.Context, channel *model.Channel, kind string, upstreamTaskID string) ([]byte, map[string]interface{}, bool) {
	path := compatibleTaskStatusPath(channelProtocol(channel.Type), kind, upstreamTaskID)
	headers := jsonHeaders()
	headers.Set("Authorization", "Bearer "+strings.TrimSpace(channel.APIKey))
	prepared := preparedUpstreamRequest{
		Method:  http.MethodGet,
		URL:     upstreamURLForRequest(channel.BaseURL, path),
		Header:  headers,
		Context: c.Request.Context(),
	}
	resp, err := s.doUpstreamRequest(prepared, channel)
	if err != nil {
		logUpstreamRequestFailure(c, channel, prepared.URL, nil, err)
		c.JSON(http.StatusBadGateway, authErrorResponse(http.StatusBadGateway, "Bad gateway. The server is temporarily unavailable", "bad_gateway"))
		return nil, nil, false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.JSON(http.StatusBadGateway, authErrorResponse(http.StatusBadGateway, "Failed to read upstream response", "bad_gateway"))
		return nil, nil, false
	}
	if resp.StatusCode >= http.StatusBadRequest {
		logUpstreamError(c, channel, prepared.URL, resp.StatusCode, nil, body)
		c.JSON(resp.StatusCode, authErrorResponse(resp.StatusCode, "Upstream request failed", "bad_gateway"))
		return nil, nil, false
	}
	payload := map[string]interface{}{}
	_ = json.Unmarshal(body, &payload)
	return body, payload, true
}

func (s *ProxyService) HandleUploadImage(c *gin.Context) {
	if err := c.Request.ParseMultipartForm(20 << 20); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "missing or invalid file field: " + err.Error(), "type": "invalid_request_error"}})
		return
	}
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "missing or invalid file field: " + err.Error(), "type": "invalid_request_error"}})
		return
	}
	defer file.Close()
	if header.Size > 20<<20 {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": gin.H{"message": fmt.Sprintf("file size %d exceeds maximum %d bytes", header.Size, 20<<20), "type": "invalid_request_error"}})
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, (20<<20)+1))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": "failed to upload image", "type": "server_error"}})
		return
	}
	if len(data) > 20<<20 {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": gin.H{"message": fmt.Sprintf("file size %d exceeds maximum %d bytes", len(data), 20<<20), "type": "invalid_request_error"}})
		return
	}
	contentType := http.DetectContentType(data)
	if !allowedUploadImageType(contentType, header.Filename) {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": "unsupported image type: " + contentType + ", allowed: jpeg, png, gif, webp", "type": "invalid_request_error"}})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"url":          "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data),
		"filename":     header.Filename,
		"content_type": contentType,
		"bytes":        len(data),
		"created_at":   time.Now().Unix(),
	})
}

func (s *ProxyService) HandleAudioSpeech(c *gin.Context) {
	requestBody, bodyBytes, ok := readProxyJSONBody(c)
	if !ok {
		return
	}
	modelName := strings.TrimSpace(stringFromValue(requestBody["model"]))
	if modelName == "" {
		modelName = "gpt-4o-mini-tts"
		requestBody["model"] = modelName
		bodyBytes, _ = json.Marshal(requestBody)
	}
	target, ok := s.resolveTarget(c, modelName)
	if !ok {
		return
	}
	prepared, ok := s.prepareRawCompatibleRequest(c, target, http.MethodPost, "/v1/audio/speech", bodyBytes, jsonHeaders())
	if !ok {
		return
	}
	resp, respBody, ok := s.readCompatibleUpstreamResponse(c, target, prepared)
	if !ok {
		return
	}
	usage := usageTokenCounts{
		InputTokens:       CountTokens(target.ModelName, contentToText(requestBody["input"])),
		AudioInputTokens:  CountTokens(target.ModelName, contentToText(requestBody["input"])),
		OutputTokens:      len(respBody) / 1000,
		AudioOutputTokens: len(respBody) / 1000,
	}
	if usage.OutputTokens <= 0 {
		usage.OutputTokens = 1
		usage.AudioOutputTokens = 1
	}
	if status, message, err := s.billUsage(c, target.User, target.APIKey, &target.Channel, &target.ModelConfig, target.billingModelName(), usage, target.billingModel()); err != nil {
		c.JSON(status, gin.H{"error": message})
		return
	}
	writeUpstreamResponse(c, resp, respBody)
}

func (s *ProxyService) HandleAudioTranscription(c *gin.Context) {
	if err := c.Request.ParseMultipartForm(25 << 20); err != nil {
		c.JSON(http.StatusBadRequest, authErrorResponse(http.StatusBadRequest, "Invalid multipart form", "invalid_request_error"))
		return
	}
	modelName := strings.TrimSpace(c.PostForm("model"))
	if modelName == "" {
		modelName = "whisper-1"
	}
	target, ok := s.resolveTarget(c, modelName)
	if !ok {
		return
	}
	body, contentType, err := multipartBodyWithModel(c.Request.MultipartForm, target.upstreamModelName())
	if err != nil {
		c.JSON(http.StatusBadRequest, authErrorResponse(http.StatusBadRequest, err.Error(), "invalid_request_error"))
		return
	}
	headers := http.Header{}
	headers.Set("Content-Type", contentType)
	headers.Set("Accept", "application/json")
	prepared, ok := s.prepareRawCompatibleRequest(c, target, http.MethodPost, "/v1/audio/transcriptions", body, headers)
	if !ok {
		return
	}
	resp, respBody, ok := s.readCompatibleUpstreamResponse(c, target, prepared)
	if !ok {
		return
	}
	values := multipartFormValues(c.Request.MultipartForm)
	usage := usageTokenCounts{
		InputTokens:      multipartFileTokenEstimate(c.Request.MultipartForm),
		AudioInputTokens: multipartFileTokenEstimate(c.Request.MultipartForm),
		OutputTokens:     CountTokens(target.ModelName, string(respBody)),
	}
	if prompt := contentToText(values["prompt"]); prompt != "" {
		usage.InputTokens += CountTokens(target.ModelName, prompt)
	}
	if status, message, err := s.billUsage(c, target.User, target.APIKey, &target.Channel, &target.ModelConfig, target.billingModelName(), usage, target.billingModel()); err != nil {
		c.JSON(status, gin.H{"error": message})
		return
	}
	writeUpstreamResponse(c, resp, respBody)
}

func (s *ProxyService) HandleModeration(c *gin.Context) {
	requestBody, bodyBytes, ok := readProxyJSONBody(c)
	if !ok {
		return
	}
	modelName := strings.TrimSpace(stringFromValue(requestBody["model"]))
	if modelName == "" {
		modelName = "omni-moderation-latest"
		requestBody["model"] = modelName
		bodyBytes, _ = json.Marshal(requestBody)
	}
	target, ok := s.resolveTarget(c, modelName)
	if !ok {
		return
	}
	prepared, ok := s.prepareRawCompatibleRequest(c, target, http.MethodPost, "/v1/moderations", bodyBytes, jsonHeaders())
	if !ok {
		return
	}
	resp, respBody, ok := s.readCompatibleUpstreamResponse(c, target, prepared)
	if !ok {
		return
	}
	usage := usageTokenCounts{InputTokens: CountTokens(target.ModelName, contentToText(requestBody["input"]))}
	usage.OutputTokens = CountTokens(target.ModelName, string(respBody))
	if status, message, err := s.billUsage(c, target.User, target.APIKey, &target.Channel, &target.ModelConfig, target.billingModelName(), usage, target.billingModel()); err != nil {
		c.JSON(status, gin.H{"error": message})
		return
	}
	writeUpstreamResponse(c, resp, respBody)
}

func (s *ProxyService) prepareRawCompatibleRequest(c *gin.Context, target *proxyTarget, method string, path string, body []byte, headers http.Header) (preparedUpstreamRequest, bool) {
	if err := ValidateConfiguredHTTPURL(target.Channel.BaseURL); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Upstream URL blocked by SSRF protection", "type": "upstream_error"})
		return preparedUpstreamRequest{}, false
	}
	if headers == nil {
		headers = jsonHeaders()
	}
	headers.Set("Authorization", "Bearer "+strings.TrimSpace(target.Channel.APIKey))
	return preparedUpstreamRequest{
		Method:  method,
		URL:     upstreamURLForRequest(target.Channel.BaseURL, path),
		Body:    body,
		Header:  headers,
		Context: c.Request.Context(),
	}, true
}

func (s *ProxyService) readCompatibleUpstreamResponse(c *gin.Context, target *proxyTarget, prepared preparedUpstreamRequest) (*http.Response, []byte, bool) {
	resp, err := s.doUpstreamRequest(prepared, &target.Channel)
	if err != nil {
		logUpstreamRequestFailure(c, &target.Channel, prepared.URL, prepared.Body, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "Upstream request failed", "type": "upstream_error"})
		return nil, nil, false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Failed to read upstream response"})
		return nil, nil, false
	}
	if resp.StatusCode >= http.StatusBadRequest {
		logUpstreamError(c, &target.Channel, prepared.URL, resp.StatusCode, prepared.Body, body)
		writeUpstreamResponse(c, resp, body)
		return nil, nil, false
	}
	return resp, body, true
}

func multipartBodyWithModel(form *multipart.Form, upstreamModelName string) ([]byte, string, error) {
	if form == nil {
		return nil, "", errors.New("multipart form is required")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, values := range form.Value {
		if key == "model" {
			continue
		}
		for _, value := range values {
			if err := writer.WriteField(key, value); err != nil {
				return nil, "", err
			}
		}
	}
	if err := writer.WriteField("model", upstreamModelName); err != nil {
		return nil, "", err
	}
	for key, files := range form.File {
		for _, fileHeader := range files {
			source, err := fileHeader.Open()
			if err != nil {
				return nil, "", err
			}
			part, err := writer.CreateFormFile(key, fileHeader.Filename)
			if err != nil {
				source.Close()
				return nil, "", err
			}
			if _, err := io.Copy(part, source); err != nil {
				source.Close()
				return nil, "", err
			}
			source.Close()
		}
	}
	if len(form.File["file"]) == 0 {
		return nil, "", errors.New("file is required")
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return body.Bytes(), writer.FormDataContentType(), nil
}

func compatibleTaskCreateResponse(task model.VideoTask) gin.H {
	return gin.H{
		"code": http.StatusOK,
		"data": []gin.H{{
			"status":  "submitted",
			"task_id": task.ID,
		}},
	}
}

func compatibleTaskData(task model.VideoTask, payload map[string]interface{}) gin.H {
	status := normalizeCompatibleTaskStatus(task.Status)
	result := compatibleTaskResult(payload)
	progress := compatibleTaskProgress(status, payload)
	data := gin.H{
		"id":                task.ID,
		"task_id":           task.ID,
		"status":            status,
		"cost":              task.Cost,
		"credits_cost":      balanceCredits(task.Cost),
		"progress":          progress,
		"result":            result,
		"created":           task.CreatedAt.Unix(),
		"estimated_time":    60,
		"upstream_id":       task.UpstreamTaskID,
		"upstream_response": payload,
	}
	if terminalCompatibleTaskStatus(status) {
		data["completed"] = task.UpdatedAt.Unix()
		data["actual_time"] = int(task.UpdatedAt.Sub(task.CreatedAt).Seconds())
	}
	if errValue := compatibleTaskError(payload); errValue != nil {
		data["error"] = errValue
	}
	return data
}

func compatibleTaskResult(payload map[string]interface{}) map[string]interface{} {
	if payload == nil {
		return map[string]interface{}{}
	}
	for _, key := range []string{"result", "task_result", "taskResult"} {
		if result, ok := payload[key].(map[string]interface{}); ok {
			return normalizeCompatibleResult(result)
		}
	}
	if data, ok := payload["data"].(map[string]interface{}); ok {
		if result := compatibleTaskResult(data); len(result) > 0 {
			return result
		}
	}
	result := map[string]interface{}{}
	if images := compatibleImagesFromPayload(payload); len(images) > 0 {
		result["images"] = images
	}
	if videos := compatibleVideosFromPayload(payload); len(videos) > 0 {
		result["videos"] = videos
	}
	return result
}

func normalizeCompatibleResult(result map[string]interface{}) map[string]interface{} {
	normalized := map[string]interface{}{}
	for key, value := range result {
		normalized[key] = value
	}
	if images := compatibleImagesFromValue(result["images"]); len(images) > 0 {
		normalized["images"] = images
	}
	if videos := compatibleVideosFromValue(result["videos"]); len(videos) > 0 {
		normalized["videos"] = videos
	}
	return normalized
}

func compatibleImagesFromPayload(payload map[string]interface{}) []map[string]interface{} {
	if data, ok := payload["data"].([]interface{}); ok {
		return compatibleImagesFromValue(data)
	}
	if data, ok := payload["data"].(map[string]interface{}); ok {
		return compatibleImagesFromPayload(data)
	}
	return nil
}

func compatibleImagesFromValue(raw interface{}) []map[string]interface{} {
	items, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	images := make([]map[string]interface{}, 0, len(items))
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]interface{})
		if !ok {
			continue
		}
		urlValue := firstNonEmptyString(
			stringFromValue(item["url"]),
			stringFromValue(item["image_url"]),
			stringFromValue(item["imageUrl"]),
		)
		if urlValue == "" {
			continue
		}
		image := map[string]interface{}{"url": []string{urlValue}}
		if expiresAt, exists := item["expires_at"]; exists {
			image["expires_at"] = expiresAt
		}
		images = append(images, image)
	}
	return images
}

func compatibleVideosFromPayload(payload map[string]interface{}) []map[string]interface{} {
	if videos := klingTaskResultVideos(payload); len(videos) > 0 {
		return compatibleVideosFromValue(videos)
	}
	if data, ok := payload["data"].([]interface{}); ok {
		return compatibleVideosFromValue(data)
	}
	if data, ok := payload["data"].(map[string]interface{}); ok {
		return compatibleVideosFromPayload(data)
	}
	return nil
}

func compatibleVideosFromValue(raw interface{}) []map[string]interface{} {
	var items []interface{}
	switch typed := raw.(type) {
	case []interface{}:
		items = typed
	case []map[string]interface{}:
		items = make([]interface{}, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
	default:
		return nil
	}
	videos := make([]map[string]interface{}, 0, len(items))
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]interface{})
		if !ok {
			continue
		}
		urlValue := firstNonEmptyString(
			stringFromValue(item["url"]),
			stringFromValue(item["video_url"]),
			stringFromValue(item["videoUrl"]),
		)
		if urlValue == "" {
			continue
		}
		video := make(map[string]interface{}, len(item)+1)
		for key, value := range item {
			video[key] = value
		}
		video["url"] = urlValue
		videos = append(videos, video)
	}
	return videos
}

func compatibleTaskStatusPath(protocol proxyProtocol, kind string, upstreamTaskID string) string {
	escaped := url.PathEscape(strings.TrimSpace(upstreamTaskID))
	switch {
	case protocol == protocolMidjourney || kind == taskKindMidjourney:
		return "/v1/midjourney/" + escaped
	case protocol == protocolKling:
		return videoTaskStatusPath(protocol, escaped)
	case kind == taskKindImage:
		return "/v1/tasks/" + escaped
	default:
		return "/v1/tasks/" + escaped
	}
}

func taskKindFromTask(task model.VideoTask) string {
	var payload map[string]interface{}
	_ = json.Unmarshal([]byte(task.RequestPayload), &payload)
	if kind := strings.TrimSpace(stringFromValue(payload["_task_kind"])); kind != "" {
		return kind
	}
	pathKind := strings.ToLower(task.ModelName)
	switch {
	case strings.Contains(pathKind, "midjourney"):
		return taskKindMidjourney
	case strings.Contains(pathKind, "image"):
		return taskKindImage
	default:
		return taskKindVideo
	}
}

func normalizeCompatibleTaskStatus(status string) string {
	switch normalizeVideoTaskStatus(status) {
	case "succeeded":
		return "completed"
	case "queued":
		return "pending"
	default:
		return strings.TrimSpace(status)
	}
}

func terminalCompatibleTaskStatus(status string) bool {
	switch normalizeCompatibleTaskStatus(status) {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func compatibleTaskProgress(status string, payload map[string]interface{}) int {
	if value, ok := tokenValueAsInt(payload["progress"]); ok {
		return value
	}
	if data, ok := payload["data"].(map[string]interface{}); ok {
		if value, ok := tokenValueAsInt(data["progress"]); ok {
			return value
		}
	}
	switch normalizeCompatibleTaskStatus(status) {
	case "completed", "failed", "cancelled":
		return 100
	case "processing":
		return 50
	default:
		return 0
	}
}

func compatibleTaskError(payload map[string]interface{}) interface{} {
	if payload == nil {
		return nil
	}
	if errValue, exists := payload["error"]; exists {
		return errValue
	}
	if data, ok := payload["data"].(map[string]interface{}); ok {
		return compatibleTaskError(data)
	}
	return nil
}

func firstResultURL(data gin.H) string {
	result, _ := data["result"].(map[string]interface{})
	for _, key := range []string{"images", "videos"} {
		urls := resultURLs(result, key)
		if len(urls) > 0 {
			return urls[0]
		}
	}
	return ""
}

func resultURLs(result map[string]interface{}, key string) []string {
	if result == nil {
		return nil
	}
	rawItems, ok := result[key].([]map[string]interface{})
	if !ok {
		if asInterface, ok := result[key].([]interface{}); ok {
			rawItems = make([]map[string]interface{}, 0, len(asInterface))
			for _, raw := range asInterface {
				if item, ok := raw.(map[string]interface{}); ok {
					rawItems = append(rawItems, item)
				}
			}
		}
	}
	urls := []string{}
	for _, item := range rawItems {
		switch value := item["url"].(type) {
		case string:
			if strings.TrimSpace(value) != "" {
				urls = append(urls, value)
			}
		case []string:
			for _, urlValue := range value {
				if strings.TrimSpace(urlValue) != "" {
					urls = append(urls, urlValue)
				}
			}
		case []interface{}:
			for _, raw := range value {
				if urlValue, ok := raw.(string); ok && strings.TrimSpace(urlValue) != "" {
					urls = append(urls, urlValue)
				}
			}
		}
	}
	return urls
}

func compatibleButtons(payload map[string]interface{}) []interface{} {
	if payload == nil {
		return nil
	}
	for _, key := range []string{"buttons", "actions"} {
		if buttons, ok := payload[key].([]interface{}); ok {
			return buttons
		}
	}
	if data, ok := payload["data"].(map[string]interface{}); ok {
		return compatibleButtons(data)
	}
	return nil
}

func compatibleRequestBlocked(kind string, requestBody map[string]interface{}) bool {
	var text string
	switch kind {
	case taskKindVideo:
		text = videoRequestText(requestBody)
	default:
		text = imageRequestText(requestBody)
	}
	_, matched := MatchSensitiveWords(text)
	return matched
}

func compatibleResponseBlocked(kind string, responseData map[string]interface{}) bool {
	var text string
	switch kind {
	case taskKindVideo:
		text = videoResponseText(responseData)
	default:
		text = imageResponseText(responseData)
	}
	_, matched := MatchSensitiveWords(text)
	return matched
}

func balanceCredits(value decimal.Decimal) interface{} {
	if value.Equal(decimal.NewFromInt(-1)) {
		return -1
	}
	return value.Mul(decimal.NewFromInt(10))
}

func authErrorResponse(code int, message string, typ string) gin.H {
	return gin.H{"error": gin.H{"code": code, "message": message, "type": typ}}
}

func allowedUploadImageType(contentType string, filename string) bool {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	}
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp":
		return true
	default:
		return false
	}
}

func multipartFileTokenEstimate(form *multipart.Form) int {
	if form == nil {
		return 0
	}
	total := int64(0)
	for _, files := range form.File {
		for _, file := range files {
			total += file.Size
		}
	}
	tokens := int(total / 1000)
	if tokens <= 0 && total > 0 {
		return 1
	}
	return tokens
}
