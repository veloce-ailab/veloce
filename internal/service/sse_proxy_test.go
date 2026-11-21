package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestMessagesStreamingPayloadAndToolHistory(t *testing.T) {
	messages := []interface{}{
		map[string]interface{}{"role": "assistant", "content": []interface{}{map[string]interface{}{"type": "tool_use", "id": "call1", "name": "lookup", "input": map[string]interface{}{"q": "hi"}}}},
		map[string]interface{}{"role": "user", "content": []interface{}{map[string]interface{}{"type": "tool_result", "tool_use_id": "call1", "content": ""}}},
	}
	req := normalizeClaudeRequest(map[string]interface{}{"messages": messages, "stream": true, "tools": []interface{}{map[string]interface{}{"name": "lookup", "input_schema": map[string]interface{}{"type": "object"}}}}, "model")
	claude, err := claudeMessagesPayloadMap(req)
	if err != nil || claude["stream"] != true {
		t.Fatalf("Messages stream lost: %v %v", claude, err)
	}
	chat, err := openAIChatCompletionsPayloadMap(req)
	if err != nil {
		t.Fatal(err)
	}
	chatHistory := chat["messages"].([]map[string]interface{})
	if len(chatHistory) != 2 || chatHistory[1]["role"] != "tool" || chatHistory[1]["tool_call_id"] != "call1" {
		t.Fatalf("tool history lost: %v", chatHistory)
	}
	responses, err := openAIResponsesPayloadMap(req)
	if err != nil {
		t.Fatal(err)
	}
	input := responses["input"].([]map[string]interface{})
	if len(input) != 2 || input[1]["type"] != "function_call_output" || input[1]["output"] != "" {
		t.Fatalf("empty tool output lost: %v", input)
	}
	// Also exercise Chat's native ResponsesInput creation, not only fallback.
	req = normalizeOpenAIRequest("/v1/chat/completions", map[string]interface{}{"messages": []interface{}{map[string]interface{}{"role": "tool", "tool_call_id": "call1", "content": ""}}}, "model")
	responses, err = openAIResponsesPayloadMap(req)
	if err != nil {
		t.Fatal(err)
	}
	input = responses["input"].([]map[string]interface{})
	if len(input) != 1 || input[0]["output"] != "" {
		t.Fatalf("native history lost empty result: %v", input)
	}
}

func TestConvertedSSEHeadersDiscardOriginalEntityFraming(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Length": []string{"99"}, "Content-Encoding": []string{"gzip"}, "Etag": []string{"abc"}, "X-Request-Id": []string{"request1"}}}
	writeConvertedStreamHeaders(c, resp)
	if c.Writer.Header().Get("Content-Length") != "" || c.Writer.Header().Get("Content-Encoding") != "" || c.Writer.Header().Get("Etag") != "" {
		t.Fatalf("stale entity framing: %v", c.Writer.Header())
	}
	if c.Writer.Header().Get("X-Request-Id") != "request1" || c.Writer.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatalf("stream metadata missing: %v", c.Writer.Header())
	}
}
