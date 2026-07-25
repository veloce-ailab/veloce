package service

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/shopspring/decimal"
)

func TestResponsesProxyPreservesFunctionCallHistory(t *testing.T) {
	requestBody := map[string]interface{}{
		"model": "gpt-5",
		"input": []interface{}{
			map[string]interface{}{"role": "user", "content": "查询天气"},
			map[string]interface{}{"type": "function_call", "call_id": "call_weather", "name": "weather", "arguments": `{"city":"Shanghai"}`},
			map[string]interface{}{"type": "function_call_output", "call_id": "call_weather", "output": `{"temperature":30}`},
		},
	}

	normalized := normalizeOpenAIRequest("/v1/responses", requestBody, "upstream-model")
	payload, err := openAIResponsesPayloadMap(normalized)
	if err != nil {
		t.Fatal(err)
	}
	input := payload["input"].([]map[string]interface{})
	if len(input) != 3 {
		t.Fatalf("input count = %d, want 3", len(input))
	}
	if !reflect.DeepEqual(input[1], requestBody["input"].([]interface{})[1]) {
		t.Fatalf("function call was changed: %#v", input[1])
	}
	if !reflect.DeepEqual(input[2], requestBody["input"].([]interface{})[2]) {
		t.Fatalf("function output was changed: %#v", input[2])
	}
}

func TestChatToolHistoryConvertsToResponsesInput(t *testing.T) {
	requestBody := map[string]interface{}{
		"model": "gpt-5",
		"messages": []interface{}{
			map[string]interface{}{"role": "user", "content": "查询天气"},
			map[string]interface{}{
				"role":       "assistant",
				"content":    "",
				"tool_calls": []interface{}{map[string]interface{}{"id": "call_weather", "type": "function", "function": map[string]interface{}{"name": "weather", "arguments": `{"city":"Shanghai"}`}}},
			},
			map[string]interface{}{"role": "tool", "tool_call_id": "call_weather", "content": `{"temperature":30}`},
		},
	}

	normalized := normalizeOpenAIRequest("/v1/chat/completions", requestBody, "upstream-model")
	payload, err := openAIResponsesPayloadMap(normalized)
	if err != nil {
		t.Fatal(err)
	}
	input := payload["input"].([]map[string]interface{})
	if len(input) != 3 {
		t.Fatalf("input count = %d, want 3", len(input))
	}
	if !reflect.DeepEqual(input[1], map[string]interface{}{"type": "function_call", "call_id": "call_weather", "name": "weather", "arguments": `{"city":"Shanghai"}`}) {
		t.Fatalf("function call = %#v", input[1])
	}
	if !reflect.DeepEqual(input[2], map[string]interface{}{"type": "function_call_output", "call_id": "call_weather", "output": `{"temperature":30}`}) {
		t.Fatalf("function output = %#v", input[2])
	}
}

func TestRawProviderRequestKeepsClaudeMessagesEndpoint(t *testing.T) {
	channel := &model.Channel{BaseURL: "https://anyrouter.top", APIKey: "upstream-key"}
	originalHeader := http.Header{
		"Authorization": []string{"Bearer user-key"},
		"x-api-key":     []string{"user-key"},
	}

	request := rawProviderRequest(channel, protocolClaude, http.MethodPost, "/v1/messages", []byte(`{}`), originalHeader)

	if request.URL != "https://anyrouter.top/v1/messages" {
		t.Fatalf("rawProviderRequest URL = %q, want Claude messages endpoint", request.URL)
	}
	if request.Header.Get("x-api-key") != "upstream-key" {
		t.Fatalf("x-api-key was not replaced with upstream key")
	}
	if request.Header.Get("Authorization") != "Bearer upstream-key" {
		t.Fatalf("Authorization was not replaced with upstream key")
	}
}

func TestRawProviderRequestKeepsGeminiEndpointAndChannelKey(t *testing.T) {
	channel := &model.Channel{BaseURL: "https://example.com", APIKey: "upstream-key"}
	originalHeader := http.Header{"x-goog-api-key": []string{"user-key"}}

	request := rawProviderRequest(channel, protocolGemini, http.MethodPost, "/v1beta/models/gemini-pro:generateContent", []byte(`{}`), originalHeader)

	if !strings.HasPrefix(request.URL, "https://example.com/v1beta/models/gemini-pro:generateContent?") {
		t.Fatalf("rawProviderRequest URL = %q, want Gemini generateContent endpoint", request.URL)
	}
	if !strings.Contains(request.URL, "key=upstream-key") {
		t.Fatalf("Gemini request URL did not use upstream key: %q", request.URL)
	}
	if strings.Contains(request.URL, "user-key") || request.Header.Get("x-goog-api-key") == "user-key" {
		t.Fatalf("Gemini request leaked user key")
	}
}

func TestPrepareOpenAIImageGenerationRequestRewritesModel(t *testing.T) {
	channel := &model.Channel{BaseURL: "https://example.com/v1", APIKey: "upstream-key"}
	requestBody := map[string]interface{}{
		"model":  "dall-e-3",
		"prompt": "draw a pear",
		"n":      float64(2),
	}

	request, err := prepareOpenAIImageGenerationRequest(channel, "upstream-image-model", requestBody)
	if err != nil {
		t.Fatalf("prepareOpenAIImageGenerationRequest returned error: %v", err)
	}
	if request.URL != "https://example.com/v1/images/generations" {
		t.Fatalf("image generation URL = %q", request.URL)
	}
	if request.Header.Get("Authorization") != "Bearer upstream-key" {
		t.Fatalf("Authorization was not set from channel key")
	}
	if requestBody["model"] != "dall-e-3" {
		t.Fatalf("original request body was mutated")
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(request.Body, &payload); err != nil {
		t.Fatalf("failed to decode prepared body: %v", err)
	}
	if payload["model"] != "upstream-image-model" {
		t.Fatalf("prepared model = %q, want upstream model", payload["model"])
	}
	if payload["prompt"] != "draw a pear" {
		t.Fatalf("prepared prompt was not preserved")
	}
}

func TestPrepareOpenAIImageEditRequestRewritesModel(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", "gpt-image-1"); err != nil {
		t.Fatalf("failed to write model field: %v", err)
	}
	if err := writer.WriteField("prompt", "add a pear"); err != nil {
		t.Fatalf("failed to write prompt field: %v", err)
	}
	part, err := writer.CreateFormFile("image", "input.png")
	if err != nil {
		t.Fatalf("failed to create image part: %v", err)
	}
	if _, err := part.Write([]byte("fake image")); err != nil {
		t.Fatalf("failed to write image part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}
	reader := multipart.NewReader(bytes.NewReader(body.Bytes()), strings.TrimPrefix(writer.FormDataContentType(), "multipart/form-data; boundary="))
	form, err := reader.ReadForm(1024)
	if err != nil {
		t.Fatalf("failed to parse multipart form: %v", err)
	}
	channel := &model.Channel{BaseURL: "https://example.com/v1", APIKey: "upstream-key"}

	request, err := prepareOpenAIImageEditRequest(channel, "upstream-image-model", form)
	if err != nil {
		t.Fatalf("prepareOpenAIImageEditRequest returned error: %v", err)
	}
	if request.URL != "https://example.com/v1/images/edits" {
		t.Fatalf("image edit URL = %q", request.URL)
	}
	if request.Header.Get("Authorization") != "Bearer upstream-key" {
		t.Fatalf("Authorization was not set from channel key")
	}
	if !strings.Contains(string(request.Body), `name="model"`) || !strings.Contains(string(request.Body), "upstream-image-model") {
		t.Fatalf("prepared multipart body did not include rewritten model")
	}
	if !strings.Contains(string(request.Body), `name="image"; filename="input.png"`) {
		t.Fatalf("prepared multipart body did not include image file")
	}
}

func TestPrepareOpenAIVideoGenerationRequestRewritesModel(t *testing.T) {
	channel := &model.Channel{BaseURL: "https://example.com/v1", APIKey: "upstream-key"}
	requestBody := map[string]interface{}{
		"model":  "doubao-seedance",
		"prompt": "make a pear video",
		"n":      float64(2),
	}

	request, err := prepareOpenAIVideoGenerationRequest(channel, "upstream-video-model", requestBody)
	if err != nil {
		t.Fatalf("prepareOpenAIVideoGenerationRequest returned error: %v", err)
	}
	if request.URL != "https://example.com/v1/video/generations" {
		t.Fatalf("video generation URL = %q", request.URL)
	}
	if request.Header.Get("Authorization") != "Bearer upstream-key" {
		t.Fatalf("Authorization was not set from channel key")
	}
	if requestBody["model"] != "doubao-seedance" {
		t.Fatalf("original request body was mutated")
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(request.Body, &payload); err != nil {
		t.Fatalf("failed to decode prepared body: %v", err)
	}
	if payload["model"] != "upstream-video-model" {
		t.Fatalf("prepared model = %q, want upstream model", payload["model"])
	}
	if payload["prompt"] != "make a pear video" {
		t.Fatalf("prepared prompt was not preserved")
	}
}

func TestPrepareKlingImageToVideoRequestRewritesModel(t *testing.T) {
	channel := &model.Channel{BaseURL: "https://example.com", APIKey: "upstream-key"}
	requestBody := map[string]interface{}{
		"model_name":      "client-video-model",
		"prompt":          "make a pear video",
		"image_url":       "https://example.com/input.png",
		"size":            "16:9",
		"duration":        float64(5),
		"n":               float64(2),
		"negative_prompt": "low quality",
	}

	request, err := prepareKlingImageToVideoRequest(channel, "kling-v3-turbo", requestBody)
	if err != nil {
		t.Fatalf("prepareKlingImageToVideoRequest returned error: %v", err)
	}
	if request.URL != "https://example.com/v1/videos/image2video" {
		t.Fatalf("kling image-to-video URL = %q", request.URL)
	}
	if request.Header.Get("Authorization") != "Bearer upstream-key" {
		t.Fatalf("Authorization was not set from channel key")
	}
	if requestBody["model_name"] != "client-video-model" {
		t.Fatalf("original request body was mutated")
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(request.Body, &payload); err != nil {
		t.Fatalf("failed to decode prepared body: %v", err)
	}
	if payload["model_name"] != "kling-v3-turbo" {
		t.Fatalf("prepared model_name = %q, want upstream model", payload["model_name"])
	}
	if payload["image"] != "https://example.com/input.png" {
		t.Fatalf("prepared image = %q", payload["image"])
	}
	if payload["aspect_ratio"] != "16:9" {
		t.Fatalf("prepared aspect_ratio = %q", payload["aspect_ratio"])
	}
	if payload["num_videos"] != float64(2) {
		t.Fatalf("prepared num_videos = %v", payload["num_videos"])
	}
	if _, exists := payload["model"]; exists {
		t.Fatalf("prepared payload should not include OpenAI model field")
	}
}

func TestEstimateImageUsageUsesResponseImageCount(t *testing.T) {
	requestBody := map[string]interface{}{
		"model":  "gpt-image-1",
		"prompt": "draw a pear",
		"n":      float64(4),
	}
	responseData := map[string]interface{}{
		"data": []interface{}{
			map[string]interface{}{"url": "https://example.com/1.png"},
			map[string]interface{}{"url": "https://example.com/2.png"},
		},
	}

	usage := estimateImageUsageTokens("gpt-image-1", requestBody, responseData)
	if usage.InputTokens <= 0 {
		t.Fatalf("expected prompt input tokens, got %d", usage.InputTokens)
	}
	if usage.OutputTokens != 2000000 {
		t.Fatalf("expected one million output units per returned image, got %d", usage.OutputTokens)
	}
}

func TestEstimateVideoUsageUsesResponseVideoCount(t *testing.T) {
	requestBody := map[string]interface{}{
		"model":  "doubao-seedance",
		"prompt": "make a pear video",
		"n":      float64(4),
	}
	responseData := map[string]interface{}{
		"data": []interface{}{
			map[string]interface{}{"url": "https://example.com/1.mp4"},
			map[string]interface{}{"url": "https://example.com/2.mp4"},
		},
	}

	usage := estimateVideoUsageTokens("doubao-seedance", requestBody, responseData)
	if usage.InputTokens <= 0 {
		t.Fatalf("expected prompt input tokens, got %d", usage.InputTokens)
	}
	if usage.OutputTokens != 2000000 {
		t.Fatalf("expected one million output units per returned video, got %d", usage.OutputTokens)
	}
}

func TestCalculatePerCallUsageCost(t *testing.T) {
	got := calculateModelUsageCost(usageTokenCounts{
		OutputTokens: 2000000,
	}, model.Model{
		QuotaType:   1,
		OutputPrice: decimal.RequireFromString("0.12"),
	})
	want := decimal.RequireFromString("0.24")
	if !got.Equal(want) {
		t.Fatalf("calculateModelUsageCost() = %s, want %s", got.String(), want.String())
	}
}

func TestCalculateVideoResolutionDurationCostUsesCombination(t *testing.T) {
	requestBody := map[string]interface{}{
		"model":    "video-model",
		"prompt":   "make a pear video",
		"size":     "720p",
		"duration": float64(10),
		"n":        float64(2),
	}
	cost, err := calculateVideoBillingCost(requestBody, nil, model.VideoBillingConfig{
		Resolutions: []model.VideoResolutionPrice{
			{
				Resolution: "720p",
				Durations: []model.VideoDurationPrice{
					{Seconds: 5, Price: decimal.RequireFromString("0.20")},
					{Seconds: 10, Price: decimal.RequireFromString("0.35")},
				},
			},
			{
				Resolution:        "1080p",
				DurationUnitPrice: decimal.RequireFromString("0.08"),
			},
		},
	})
	if err != nil {
		t.Fatalf("calculateVideoBillingCost returned error: %v", err)
	}
	want := decimal.RequireFromString("0.70")
	if !cost.Equal(want) {
		t.Fatalf("calculateVideoBillingCost() = %s, want %s", cost.String(), want.String())
	}
}

func TestCalculateVideoResolutionDurationCostUsesResolutionUnitPrice(t *testing.T) {
	requestBody := map[string]interface{}{
		"model":    "video-model",
		"prompt":   "make a pear video",
		"size":     "1080p",
		"duration": float64(6),
	}
	cost, err := calculateVideoBillingCost(requestBody, nil, model.VideoBillingConfig{
		Resolutions: []model.VideoResolutionPrice{{
			Resolution:        "1080p",
			DurationUnitPrice: decimal.RequireFromString("0.08"),
		}},
	})
	if err != nil {
		t.Fatalf("calculateVideoBillingCost returned error: %v", err)
	}
	want := decimal.RequireFromString("0.48")
	if !cost.Equal(want) {
		t.Fatalf("calculateVideoBillingCost() = %s, want %s", cost.String(), want.String())
	}
}

func TestVideoTaskPayloadHelpers(t *testing.T) {
	payload := map[string]interface{}{
		"task_id": "upstream-123",
		"state":   "processing",
	}
	if got := upstreamTaskIDFromPayload(payload); got != "upstream-123" {
		t.Fatalf("upstreamTaskIDFromPayload() = %q", got)
	}
	if got := videoTaskStatusFromPayload(payload); got != "processing" {
		t.Fatalf("videoTaskStatusFromPayload() = %q", got)
	}
}

func TestKlingVideoTaskPayloadHelpers(t *testing.T) {
	payload := map[string]interface{}{
		"data": map[string]interface{}{
			"task_id":     "kling-task-123",
			"task_status": "succeed",
			"task_result": map[string]interface{}{
				"videos": []interface{}{
					map[string]interface{}{"id": "video-1", "url": "https://example.com/video.mp4"},
				},
			},
		},
	}
	if got := upstreamTaskIDFromPayload(payload); got != "kling-task-123" {
		t.Fatalf("upstreamTaskIDFromPayload() = %q", got)
	}
	if got := videoTaskStatusFromPayload(payload); got != "succeeded" {
		t.Fatalf("videoTaskStatusFromPayload() = %q", got)
	}
	data, ok := videoTaskDataFromPayload(payload).([]map[string]interface{})
	if !ok || len(data) != 1 || data[0]["url"] != "https://example.com/video.mp4" {
		t.Fatalf("videoTaskDataFromPayload() = %#v", videoTaskDataFromPayload(payload))
	}
	if got := videoTaskStatusPath(protocolKling, "task/123"); got != "/v1/videos/image2video/task%2F123" {
		t.Fatalf("videoTaskStatusPath() = %q", got)
	}
}

func TestParseImageTotalUsageTokens(t *testing.T) {
	usage, ok := parseImageTotalUsageTokens(map[string]interface{}{
		"usage": map[string]interface{}{
			"input_tokens": float64(23),
			"total_tokens": float64(123),
		},
	})
	if !ok {
		t.Fatal("expected image usage from total tokens")
	}
	if usage.InputTokens != 23 || usage.OutputTokens != 100 {
		t.Fatalf("unexpected usage: input=%d output=%d", usage.InputTokens, usage.OutputTokens)
	}
}

func TestSelectModelConfigRoundRobin(t *testing.T) {
	service := NewProxyService()
	userChannelID := uint(7)
	candidates := []model.ModelConfig{
		{Channel: model.Channel{ID: 1, UserChannelID: &userChannelID, UserChannel: model.UserChannel{RoutingAlgorithm: RoutingRoundRobin}}},
		{Channel: model.Channel{ID: 2, UserChannelID: &userChannelID, UserChannel: model.UserChannel{RoutingAlgorithm: RoutingRoundRobin}}},
	}

	first := service.selectModelConfig(candidates, "gpt-test")
	second := service.selectModelConfig(candidates, "gpt-test")
	third := service.selectModelConfig(candidates, "gpt-test")

	if first.Channel.ID != 1 || second.Channel.ID != 2 || third.Channel.ID != 1 {
		t.Fatalf("round robin selected channel ids %d, %d, %d", first.Channel.ID, second.Channel.ID, third.Channel.ID)
	}
}

func TestSelectModelConfigWeightedRoundRobin(t *testing.T) {
	service := NewProxyService()
	userChannelID := uint(9)
	candidates := []model.ModelConfig{
		{Channel: model.Channel{ID: 1, UserChannelID: &userChannelID, Weight: 1, UserChannel: model.UserChannel{RoutingAlgorithm: RoutingWeightedRoundRobin}}},
		{Channel: model.Channel{ID: 2, UserChannelID: &userChannelID, Weight: 2, UserChannel: model.UserChannel{RoutingAlgorithm: RoutingWeightedRoundRobin}}},
	}

	ids := []uint{
		service.selectModelConfig(candidates, "gpt-test").Channel.ID,
		service.selectModelConfig(candidates, "gpt-test").Channel.ID,
		service.selectModelConfig(candidates, "gpt-test").Channel.ID,
		service.selectModelConfig(candidates, "gpt-test").Channel.ID,
	}
	want := []uint{1, 2, 2, 1}
	for index, id := range ids {
		if id != want[index] {
			t.Fatalf("weighted round robin selected %v, want %v", ids, want)
		}
	}
}
