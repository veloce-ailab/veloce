package channel

import (
	"context"
	"fmt"
	"strings"
	"time"

	communityservice "github.com/veloce-ailab/veloce/internal/service"
)

type pluginChannelProvider struct{ descriptor pluginChannelDescriptor }

func (p pluginChannelProvider) Name() string { return p.descriptor.Provider }

// Plugin providers need the integration to expose its connection config, so
// inbound parsing is performed in processMessageChannelPayload instead.
func (p pluginChannelProvider) ExtractWebhookSummary([]byte) webhookSummary { return webhookSummary{} }

func (p pluginChannelProvider) SendReply(ctx context.Context, integration MessageChannelIntegration, inbound MessageChannelMessage, content string) error {
	if !communityservice.PluginEnabledForUser(p.descriptor.Plugin, integration.UserID) {
		return fmt.Errorf("plugin channel is disabled for this user")
	}
	_, err := communityservice.InvokePluginAction(ctx, p.descriptor.Plugin, integration.UserID, pluginChannelRequestID("send", integration.ID), p.descriptor.SendAction, map[string]interface{}{
		"channel":             pluginChannelConnection(integration, p.descriptor),
		"external_chat_id":    inbound.ExternalChatID,
		"external_user_id":    inbound.ExternalUserID,
		"external_message_id": inbound.ExternalMsgID,
		"inbound_payload":     inbound.Payload,
		"content":             content,
	})
	return err
}

func pluginChannelInboundSummary(ctx context.Context, descriptor pluginChannelDescriptor, integration MessageChannelIntegration, body []byte, method string, headers map[string]string) (webhookSummary, error) {
	if !communityservice.PluginEnabledForUser(descriptor.Plugin, integration.UserID) {
		return webhookSummary{}, fmt.Errorf("plugin channel is disabled for this user")
	}
	result, err := communityservice.InvokePluginAction(ctx, descriptor.Plugin, integration.UserID, pluginChannelRequestID("inbound", integration.ID), descriptor.InboundAction, map[string]interface{}{
		"channel": pluginChannelConnection(integration, descriptor),
		"method":  method, "headers": headers, "body": string(body),
	})
	if err != nil {
		return webhookSummary{}, err
	}
	return pluginWebhookSummary(result), nil
}

func pluginChannelConnection(integration MessageChannelIntegration, descriptor pluginChannelDescriptor) map[string]interface{} {
	return map[string]interface{}{
		"provider": descriptor.Provider, "type_id": descriptor.TypeID, "integration_id": integration.ID, "name": integration.Name,
		"config": providerConfig(integration), "bot_token": integration.BotToken,
	}
}

func pluginWebhookSummary(result map[string]interface{}) webhookSummary {
	return webhookSummary{
		ExternalChatID: stringValue(result["external_chat_id"]), ExternalUserID: stringValue(result["external_user_id"]),
		ExternalUserName: stringValue(result["external_user_name"]), ExternalMessageID: stringValue(result["external_message_id"]),
		Content: stringValue(result["content"]),
	}
}

func pluginChannelRequestID(kind string, integrationID uint) string {
	return fmt.Sprintf("channel-%s-%d-%d", kind, integrationID, time.Now().UnixNano())
}

func isPluginChannelProvider(provider string) bool {
	return pluginProviderPattern.MatchString(strings.TrimSpace(provider))
}
