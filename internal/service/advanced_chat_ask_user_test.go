package service

import (
	"context"
	"encoding/json"
	"testing"
)

func TestHandleAskUserToolNormalizesPrompt(t *testing.T) {
	result, err := handleAskUserTool(context.Background(), AdvancedChatToolCallInput{
		UserID:    1,
		SessionID: "s1",
		Arguments: map[string]interface{}{
			"question":     "你希望优先支持哪个平台？",
			"multi_select": true,
			"options": []interface{}{
				map[string]interface{}{"label": "网页端", "description": "浏览器访问"},
				map[string]interface{}{"label": "桌面端"},
			},
		},
	})
	if err != nil {
		t.Fatalf("ask_user: %v", err)
	}
	var prompt askUserPrompt
	if err := json.Unmarshal([]byte(result), &prompt); err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if prompt.Question != "你希望优先支持哪个平台？" || len(prompt.Options) != 2 || !prompt.MultiSelect || !prompt.AllowCustom {
		t.Fatalf("unexpected prompt: %+v", prompt)
	}
	if prompt.Options[0].Label != "网页端" || prompt.Options[0].Description != "浏览器访问" {
		t.Fatalf("unexpected option: %+v", prompt.Options[0])
	}
	if prompt.Note == "" {
		t.Fatal("note must instruct the model to end the turn")
	}
}

func TestHandleAskUserToolValidation(t *testing.T) {
	ctx := context.Background()
	if _, err := handleAskUserTool(ctx, AdvancedChatToolCallInput{Arguments: map[string]interface{}{}}); err == nil {
		t.Fatal("missing question must be rejected")
	}
	if _, err := handleAskUserTool(ctx, AdvancedChatToolCallInput{Arguments: map[string]interface{}{
		"question":     "选一个",
		"allow_custom": false,
	}}); err == nil {
		t.Fatal("no options and no custom input must be rejected")
	}
	if _, err := handleAskUserTool(ctx, AdvancedChatToolCallInput{Arguments: map[string]interface{}{
		"question": "选一个",
		"options":  []interface{}{map[string]interface{}{"label": ""}},
	}}); err == nil {
		t.Fatal("empty option label must be rejected")
	}
}
