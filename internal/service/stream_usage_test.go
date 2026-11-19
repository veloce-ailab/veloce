package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestStreamBillingUsesUpstreamUsagePastRiskBuffer(t *testing.T) {
	longEvent := `data: {"type":"response.output_text.delta","delta":"hi","metadata":"` + strings.Repeat("x", maxBufferedStreamBytes) + "\"}\n\n"
	usageEvent := `data: {"type":"response.completed","response":{"usage":{"input_tokens":20000,"output_tokens":123,"total_tokens":20123}}}`
	body := longEvent + usageEvent
	resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var meter streamUsageMeter
	prefix, err := streamUpstreamResponse(c, resp, &meter)
	meter.finish()
	if err != nil || w.Body.String() != body || len(prefix) != maxBufferedStreamBytes {
		t.Fatalf("forwarding changed: err=%v body len=%d prefix len=%d", err, w.Body.Len(), len(prefix))
	}
	if got := meter.usageOrEstimate("gpt-4o", normalizedAIRequest{}); !meter.usage.ok || got.InputTokens != 20000 || got.OutputTokens != 123 {
		t.Fatalf("expected the upstream's 20123 tokens, got %+v", got)
	}
}

func TestStreamBillingEstimatesOnlyGeneratedContent(t *testing.T) {
	const text = "hello world"
	data := `data: {"id":"` + strings.Repeat("not output ", 100) + `","choices":[{"delta":{"content":"hello"}}]}` + "\r\n\r\n" +
		`data: {"choices":[{"delta":{"content":" world"}}]}` + "\n\ndata: [DONE]\n\n"
	var meter streamUsageMeter
	for i := range len(data) {
		meter.consume([]byte{data[i]})
	}
	meter.finish()
	got := meter.usageOrEstimate("gpt-4o", normalizedAIRequest{Messages: []normalizedAIMessage{{Content: "prompt"}}})
	if meter.usage.ok || got.OutputTokens != CountTokens("gpt-4o", text) || got.InputTokens != CountTokens("gpt-4o", "prompt") {
		t.Fatalf("expected an estimate based only on model content, got %+v", got)
	}
}

func TestClaudeCacheCreationBreakdownDoesNotDoubleCount(t *testing.T) {
	body := `data: {"type":"message_start","message":{"usage":{"input_tokens":100,"output_tokens":1,"cache_creation_input_tokens":900,"cache_creation":{"ephemeral_5m_input_tokens":200,"ephemeral_1h_input_tokens":700}}}}` + "\n\n"
	got, ok := parseUsageTokensFromStream([]byte(body))
	if !ok || got.InputTokens != 1000 || got.CacheWriteInputTokens != 200 || got.CacheWrite1hInputTokens != 700 {
		t.Fatalf("expected 900 cache writes split by TTL, got %+v", got)
	}
}

func TestOpenAIProxyForwardsRequestedUsage(t *testing.T) {
	request := normalizeOpenAIRequest("/v1/chat/completions", map[string]interface{}{
		"messages":       []interface{}{map[string]interface{}{"role": "user", "content": "hello"}},
		"stream":         true,
		"stream_options": map[string]interface{}{"include_usage": true},
	}, "gpt-4o")
	payload, err := openAIChatCompletionsPayloadMap(request)
	options, _ := payload["stream_options"].(map[string]interface{})
	if err != nil || options["include_usage"] != true {
		t.Fatalf("expected include_usage to reach the upstream, got %v (err=%v)", options, err)
	}
}

func TestClaudeStreamBillingMergesCumulativeUsage(t *testing.T) {
	body := `data: {"type":"message_start","message":{"usage":{"input_tokens":20000,"cache_read_input_tokens":800,"output_tokens":1}}}` + "\n\n" +
		`data: {"type":"message_delta","usage":{"output_tokens":100}}` + "\n\n" +
		`data: {"type":"message_delta","usage":{"output_tokens":123}}` + "\n\n"
	want := usageTokenCounts{InputTokens: 20800, OutputTokens: 123, CachedInputTokens: 800, CacheReadInputTokens: 800}
	got, ok := parseUsageTokensFromStream([]byte(body))
	if !ok || got.InputTokens != want.InputTokens || got.OutputTokens != want.OutputTokens || got.CachedInputTokens != want.CachedInputTokens {
		t.Fatalf("proxy got %+v, want %+v", got, want)
	}
	_, got, ok, err := readServerChatStream(strings.NewReader(body), protocolClaude, nil)
	if err != nil || !ok || got.InputTokens != want.InputTokens || got.OutputTokens != want.OutputTokens || got.CachedInputTokens != want.CachedInputTokens {
		t.Fatalf("server chat got %+v (err=%v), want %+v", got, err, want)
	}
}
