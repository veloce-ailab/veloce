package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/veloce-ailab/veloce/internal/model"
)

func TestPrepareServerChatRequestIncludesImageParts(t *testing.T) {
	channel := &model.Channel{APIKey: "test-key", BaseURL: "https://upstream.example"}
	request := ChatExecutorRequest{
		Messages: []ChatExecutorMessage{{
			Role:    "user",
			Content: "describe this image",
			Parts: []ChatExecutorContentPart{
				{Type: "text", Text: "describe this image"},
				{Type: "image", MIMEType: "image/png", Data: "YWJj"},
			},
		}},
	}

	t.Run("openai chat", func(t *testing.T) {
		prepared, err := prepareServerOpenAIChatRequest(channel, "gpt-4o-mini", request)
		if err != nil {
			t.Fatal(err)
		}
		body := decodePreparedBody(t, prepared.Body)
		messages := body["messages"].([]interface{})
		content := messages[0].(map[string]interface{})["content"].([]interface{})
		image := content[1].(map[string]interface{})
		if image["type"] != "image_url" {
			t.Fatalf("image part type = %v, want image_url", image["type"])
		}
		imageURL := image["image_url"].(map[string]interface{})["url"].(string)
		if !strings.HasPrefix(imageURL, "data:image/png;base64,YWJj") {
			t.Fatalf("image URL = %q, want data URL", imageURL)
		}
	})

	t.Run("openai responses", func(t *testing.T) {
		prepared, err := prepareServerOpenAIResponsesRequest(channel, "gpt-4o-mini", request)
		if err != nil {
			t.Fatal(err)
		}
		body := decodePreparedBody(t, prepared.Body)
		input := body["input"].([]interface{})
		content := input[0].(map[string]interface{})["content"].([]interface{})
		image := content[1].(map[string]interface{})
		if image["type"] != "input_image" || image["image_url"] != "data:image/png;base64,YWJj" {
			t.Fatalf("image part = %#v, want Responses input_image data URL", image)
		}
	})

	t.Run("claude", func(t *testing.T) {
		prepared, err := prepareServerClaudeMessagesRequest(channel, "claude-3-5-sonnet", request)
		if err != nil {
			t.Fatal(err)
		}
		body := decodePreparedBody(t, prepared.Body)
		messages := body["messages"].([]interface{})
		content := messages[0].(map[string]interface{})["content"].([]interface{})
		image := content[1].(map[string]interface{})
		source := image["source"].(map[string]interface{})
		if image["type"] != "image" || source["media_type"] != "image/png" || source["data"] != "YWJj" {
			t.Fatalf("image block = %#v, want Claude base64 image block", image)
		}
	})

	t.Run("gemini", func(t *testing.T) {
		prepared, err := prepareServerGeminiGenerateContentRequest(channel, "gemini-2.0-flash", request)
		if err != nil {
			t.Fatal(err)
		}
		body := decodePreparedBody(t, prepared.Body)
		contents := body["contents"].([]interface{})
		parts := contents[0].(map[string]interface{})["parts"].([]interface{})
		inlineData := parts[1].(map[string]interface{})["inlineData"].(map[string]interface{})
		if inlineData["mimeType"] != "image/png" || inlineData["data"] != "YWJj" {
			t.Fatalf("inlineData = %#v, want Gemini inline image data", inlineData)
		}
	})
}

func TestReadServerChatStreamKeepsPartialContentOnCallbackError(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"
	result, _, usageOK, err := readServerChatStream(strings.NewReader(stream), protocolOpenAI, func(string) error {
		return errTestStreamCallback
	})
	if err == nil {
		t.Fatal("readServerChatStream error = nil, want callback error")
	}
	if result.Content != "hello" {
		t.Fatalf("partial content = %q, want hello", result.Content)
	}
	if !billableStreamPartial(result, usageOK) {
		t.Fatal("partial stream result should be billable")
	}
}

func decodePreparedBody(t *testing.T, body []byte) map[string]interface{} {
	t.Helper()
	var decoded map[string]interface{}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

var errTestStreamCallback = &ChatExecutorError{Status: 499, Message: "stream callback failed"}
