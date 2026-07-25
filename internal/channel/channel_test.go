package channel

import (
	"strings"
	"testing"

	"github.com/veloce-ailab/veloce/internal/model"
	communityservice "github.com/veloce-ailab/veloce/internal/service"
)

func TestPluginChannelDescriptorAndWebhookSummary(t *testing.T) {
	plugin := model.Plugin{ID: "acme-plugin", Enabled: true, ManifestJSON: `{"channels":[{"id":"acme","name":"Acme Chat","inbound_action":"channel.inbound","send_action":"channel.send","config":{"fields":[{"name":"token","type":"secret"}]}}]}`}
	descriptor, ok := pluginChannelDescriptorFromPlugin(plugin, "acme")
	if !ok || descriptor.Provider != "plugin--acme-plugin--acme" || descriptor.InboundAction != "channel.inbound" {
		t.Fatalf("descriptor = %#v, ok=%v", descriptor, ok)
	}
	summary := pluginWebhookSummary(map[string]interface{}{"external_chat_id": "chat-1", "external_user_id": "user-1", "external_user_name": "Ada", "external_message_id": "message-1", "content": "hello"})
	if summary.ExternalChatID != "chat-1" || summary.ExternalUserID != "user-1" || summary.Content != "hello" {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestParseMessageChannelApprovalDecision(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
		ok   bool
	}{
		{name: "yes", text: "yes", want: true, ok: true},
		{name: "quoted yes", text: `"yes"`, want: true, ok: true},
		{name: "curly quoted yes", text: `“yes”`, want: true, ok: true},
		{name: "yes punctuation", text: "yes。", want: true, ok: true},
		{name: "reply yes", text: "回复 yes", want: true, ok: true},
		{name: "chinese approve", text: "同意", want: true, ok: true},
		{name: "no", text: "no", want: false, ok: true},
		{name: "quoted no", text: `"no"`, want: false, ok: true},
		{name: "chinese deny", text: "拒绝。", want: false, ok: true},
		{name: "unknown", text: "稍等", want: false, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseMessageChannelApprovalDecision(tt.text)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("parseMessageChannelApprovalDecision(%q) = (%v, %v), want (%v, %v)", tt.text, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestFormatMessageChannelApprovalPromptChinese(t *testing.T) {
	prompt := formatMessageChannelApprovalPrompt(communityservice.MessageChannelConnectorApproval{
		TaskID:        "task-1",
		DeviceName:    "dev",
		Action:        "run_command",
		WorkspacePath: "D:/dev/project",
		Arguments:     map[string]interface{}{"command": "go test ./..."},
	})
	for _, want := range []string{"连接器操作需要审批", "任务：task-1", "回复 yes 或 同意以批准", "回复 no 或 拒绝以拒绝"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("approval prompt missing %q:\n%s", want, prompt)
		}
	}
}
