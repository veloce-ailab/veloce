package channel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	communityservice "github.com/veloce-ailab/veloce/internal/service"
	"github.com/gin-gonic/gin"
)

const (
	tencentChannelActionGatewayPoll  = "tencent_channel_gateway_poll"
	tencentChannelActionListPosts    = "tencent_channel_list_posts"
	tencentChannelActionGetPost      = "tencent_channel_get_post"
	tencentChannelActionGetComments  = "tencent_channel_get_comments"
	tencentChannelActionPublishPost  = "tencent_channel_publish_post"
	tencentChannelActionCommentPost  = "tencent_channel_comment_post"
	tencentChannelActionReplyComment = "tencent_channel_reply_comment"

	tencentChannelDefaultPollInterval = 30 * time.Second
)

type tencentChannelProvider struct{}

type tencentChannelGatewayClient struct {
	integrationID uint
	signature     string
	cancel        context.CancelFunc
	done          chan struct{}
}

type tencentChannelGatewayPollResult struct {
	Events     []json.RawMessage `json:"events"`
	Cursor     string            `json:"cursor,omitempty"`
	Watermark  string            `json:"watermark,omitempty"`
	AttachInfo string            `json:"attach_info,omitempty"`
}

func (tencentChannelProvider) Name() string {
	return providerTencentChannel
}

func (tencentChannelProvider) ExtractWebhookSummary(body []byte) webhookSummary {
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return webhookSummary{}
	}
	data := objectAt(payload, "data")
	if len(data) == 0 {
		data = objectAt(payload, "event")
	}
	if len(data) == 0 {
		data = payload
	}
	guildID := firstStringID(data["guild_id"], data["guildId"], payload["guild_id"])
	channelID := firstStringID(data["channel_id"], data["channelId"], payload["channel_id"])
	feedID := firstStringID(data["feed_id"], data["feedId"], payload["feed_id"])
	commentID := firstStringID(data["comment_id"], data["commentId"], payload["comment_id"])
	replyID := firstStringID(data["reply_id"], data["replyId"], payload["reply_id"])
	noticeID := firstStringID(data["notice_id"], data["noticeId"], data["id"], payload["notice_id"], payload["id"])
	ref := firstStringValue(data["ref"], data["notice_ref"], data["notice_number"], payload["ref"], payload["notice_ref"])
	content := firstStringValue(data["content"], data["text"], data["summary"], data["notice_text"], payload["content"], payload["text"])
	if content == "" {
		content = formatTencentChannelEventContent(data)
	}
	chatID := firstStringValue(data["external_chat_id"], payload["external_chat_id"])
	if chatID == "" {
		chatID = tencentChannelExternalChatID(guildID, channelID, feedID)
	}
	msgID := firstNonEmptyTencentText(noticeID, ref, replyID, commentID, feedID)
	return webhookSummary{
		ExternalChatID:    chatID,
		ExternalUserID:    firstStringID(data["author_id"], data["user_id"], data["tiny_id"], data["sender_id"]),
		ExternalUserName:  firstStringValue(data["author_name"], data["nick"], data["nickname"], data["sender_name"]),
		ExternalMessageID: msgID,
		Content:           strings.TrimSpace(content),
	}
}

func (tencentChannelProvider) SendReply(ctx context.Context, integration MessageChannelIntegration, inbound MessageChannelMessage, content string) error {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil
	}
	if strings.TrimSpace(integration.DefaultDeviceID) == "" {
		return errors.New("tencent channel gateway requires a connector device")
	}
	config := tencentChannelConfig(providerConfig(integration))
	action := tencentChannelActionCommentPost
	if configString(config, "reply_mode", "reply_action") == "reply" {
		action = tencentChannelActionReplyComment
	}
	payload := tencentChannelActionPayload(integration, inbound, content)
	if _, ok := payload["ref"]; !ok && action == tencentChannelActionReplyComment {
		if value := stringValue(payload["comment_id"]); value == "" {
			action = tencentChannelActionCommentPost
		}
	}
	_, err := communityservice.RunMessageChannelConnectorAction(
		ctx,
		integration.UserID,
		messageChannelApprovalRunID(integration.ID, inbound.ExternalChatID),
		integration.DefaultDeviceID,
		integration.DefaultWorkspacePath,
		integration.DefaultWorkspaceUnrestricted,
		action,
		payload,
		true,
		decodeStringList(integration.DefaultConnectorCommandPrefixes, 20),
	)
	return err
}

func (api *API) tencentChannelListPosts(c *gin.Context) {
	api.runTencentChannelGatewayAction(c, tencentChannelActionListPosts)
}

func (api *API) tencentChannelGetPost(c *gin.Context) {
	api.runTencentChannelGatewayAction(c, tencentChannelActionGetPost)
}

func (api *API) tencentChannelGetComments(c *gin.Context) {
	api.runTencentChannelGatewayAction(c, tencentChannelActionGetComments)
}

func (api *API) tencentChannelPublishPost(c *gin.Context) {
	api.runTencentChannelGatewayAction(c, tencentChannelActionPublishPost)
}

func (api *API) tencentChannelCommentPost(c *gin.Context) {
	api.runTencentChannelGatewayAction(c, tencentChannelActionCommentPost)
}

func (api *API) tencentChannelReplyComment(c *gin.Context) {
	api.runTencentChannelGatewayAction(c, tencentChannelActionReplyComment)
}

func (api *API) runTencentChannelGatewayAction(c *gin.Context, action string) {
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
	var input map[string]interface{}
	if c.Request.Body != nil {
		if err := c.ShouldBindJSON(&input); err != nil && !errors.Is(err, io.EOF) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid JSON body"})
			return
		}
	}
	payload := tencentChannelConfiguredPayload(integration, input)
	result, err := communityservice.RunMessageChannelConnectorAction(
		c.Request.Context(),
		integration.UserID,
		messageChannelApprovalRunID(integration.ID, "gateway"),
		integration.DefaultDeviceID,
		integration.DefaultWorkspacePath,
		integration.DefaultWorkspaceUnrestricted,
		action,
		payload,
		true,
		decodeStringList(integration.DefaultConnectorCommandPrefixes, 20),
	)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"result": parseTencentChannelActionResult(result)})
}

func loadUserTencentChannelIntegration(c *gin.Context, userID uint) (MessageChannelIntegration, bool) {
	id, err := strconv.ParseUint(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid message channel id"})
		return MessageChannelIntegration{}, false
	}
	var integration MessageChannelIntegration
	if err := model.DB.Where("id = ? AND user_id = ? AND provider = ?", uint(id), userID, providerTencentChannel).First(&integration).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Tencent channel gateway not found"})
		return MessageChannelIntegration{}, false
	}
	if strings.TrimSpace(integration.DefaultDeviceID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Tencent channel gateway requires a connector device"})
		return MessageChannelIntegration{}, false
	}
	return integration, true
}

func (m *messageChannelRuntimeManager) syncTencentChannel() {
	if !Enabled() {
		return
	}
	var integrations []MessageChannelIntegration
	if err := model.DB.Where("provider = ? AND enabled = ?", providerTencentChannel, true).Find(&integrations).Error; err != nil {
		log.Printf("tencent channel gateway sync failed: %v", err)
		return
	}
	want := map[uint]MessageChannelIntegration{}
	for _, integration := range integrations {
		config := tencentChannelConfig(providerConfig(integration))
		if !boolConfig(config, "gateway_enabled", true) {
			continue
		}
		if strings.TrimSpace(integration.DefaultDeviceID) == "" {
			continue
		}
		want[integration.ID] = integration
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for id, client := range m.tencentClients {
		if _, ok := want[id]; !ok {
			client.cancel()
			delete(m.tencentClients, id)
		}
	}
	for id, integration := range want {
		signature := tencentChannelGatewaySignature(integration)
		if client, ok := m.tencentClients[id]; ok {
			if client.signature == signature {
				continue
			}
			client.cancel()
			delete(m.tencentClients, id)
		}
		ctx, cancel := context.WithCancel(context.Background())
		client := &tencentChannelGatewayClient{integrationID: id, signature: signature, cancel: cancel, done: make(chan struct{})}
		m.tencentClients[id] = client
		go runTencentChannelGatewayClient(ctx, client, integration)
	}
}

func runTencentChannelGatewayClient(ctx context.Context, client *tencentChannelGatewayClient, integration MessageChannelIntegration) {
	defer close(client.done)
	backoff := 2 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		nextPoll, err := pollTencentChannelGatewayOnce(ctx, integration)
		if err == nil {
			backoff = 2 * time.Second
			if nextPoll <= 0 {
				nextPoll = tencentChannelDefaultPollInterval
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(nextPoll):
			}
			continue
		}
		if ctx.Err() != nil {
			return
		}
		log.Printf("tencent channel gateway %d poll failed: %v", integration.ID, err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func pollTencentChannelGatewayOnce(ctx context.Context, integration MessageChannelIntegration) (time.Duration, error) {
	var current MessageChannelIntegration
	if err := model.DB.Where("id = ? AND provider = ? AND enabled = ?", integration.ID, providerTencentChannel, true).First(&current).Error; err != nil {
		return 0, err
	}
	if strings.TrimSpace(current.DefaultDeviceID) == "" {
		return 0, context.Canceled
	}
	config := tencentChannelConfig(providerConfig(current))
	if !boolConfig(config, "gateway_enabled", true) {
		return 0, context.Canceled
	}
	interval := time.Duration(intConfig(config, "poll_interval_seconds", int(tencentChannelDefaultPollInterval/time.Second), 10, 3600)) * time.Second
	result, err := runTencentChannelCLI(ctx, current, tencentChannelNoticePollCommand(config), 60)
	if err != nil {
		return interval, err
	}
	parsed := parseTencentChannelGatewayPollResult(result)
	parsed.Events = filterTencentChannelPolledEvents(parsed.Events, config)
	seenKeys := tencentChannelSeenNoticeKeys(config)
	newEvents, nextSeenKeys := filterTencentChannelNewEvents(parsed.Events, seenKeys)
	if len(seenKeys) == 0 && !boolConfig(config, "poll_replay_existing", false) {
		newEvents = nil
	}
	for _, event := range newEvents {
		if len(event) == 0 {
			continue
		}
		if err := processMessageChannelPayload(ctx, current, event, nil); err != nil {
			log.Printf("tencent channel %d payload handling failed: %v", current.ID, err)
		}
	}
	if parsed.Cursor != "" || parsed.Watermark != "" || parsed.AttachInfo != "" || len(nextSeenKeys) > 0 {
		_ = updateTencentChannelProviderConfig(current.ID, func(config map[string]interface{}) {
			if parsed.Cursor != "" {
				config["cursor"] = parsed.Cursor
			}
			if parsed.Watermark != "" {
				config["watermark"] = parsed.Watermark
			}
			if parsed.AttachInfo != "" {
				config["notice_attach_info"] = parsed.AttachInfo
			}
			if len(nextSeenKeys) > 0 {
				config["notice_seen_keys"] = nextSeenKeys
			}
		})
	}
	return interval, nil
}

func filterTencentChannelPolledEvents(events []json.RawMessage, config map[string]interface{}) []json.RawMessage {
	if boolConfig(config, "poll_posts", false) {
		return events
	}
	if !boolConfig(config, "poll_mentions", true) {
		return nil
	}
	filtered := make([]json.RawMessage, 0, len(events))
	for _, event := range events {
		var payload map[string]interface{}
		if json.Unmarshal(event, &payload) != nil {
			continue
		}
		noticeType := strings.ToLower(firstStringValue(payload["type"], payload["notice_type"], payload["event_type"], payload["summary"], payload["content"]))
		if strings.Contains(noticeType, "@") || strings.Contains(noticeType, "mention") {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

func tencentChannelNoticePollCommand(config map[string]interface{}) string {
	parts := []string{
		"tencent-channel-cli feed get-notices",
		"--page-num " + strconv.Itoa(intConfig(config, "max_events", 20, 1, 100)),
		"--json",
	}
	if guildID := configString(config, "guild_id"); regexp.MustCompile(`^\d+$`).MatchString(guildID) {
		parts = append(parts, "--guild-id "+guildID)
	}
	return strings.Join(parts, " ")
}

func tencentChannelGatewayPollPayload(config map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"guild_id":      configString(config, "guild_id"),
		"channel_id":    configString(config, "channel_id"),
		"get_type":      intConfig(config, "default_get_type", 2, 1, 2),
		"poll_mentions": boolConfig(config, "poll_mentions", true),
		"poll_posts":    boolConfig(config, "poll_posts", false),
		"cursor":        configString(config, "cursor"),
		"watermark":     configString(config, "watermark"),
		"cli_profile":   configString(config, "cli_profile"),
		"max_events":    intConfig(config, "max_events", 20, 1, 100),
		"json":          true,
		"cli_flags":     []string{"--json"},
	}
}

func tencentChannelConfiguredPayload(integration MessageChannelIntegration, input map[string]interface{}) map[string]interface{} {
	config := tencentChannelConfig(providerConfig(integration))
	payload := map[string]interface{}{
		"guild_id":    configString(config, "guild_id"),
		"channel_id":  configString(config, "channel_id"),
		"get_type":    intConfig(config, "default_get_type", 2, 1, 2),
		"cli_profile": configString(config, "cli_profile"),
		"json":        true,
		"cli_flags":   []string{"--json"},
	}
	for key, value := range input {
		if strings.TrimSpace(key) == "" {
			continue
		}
		payload[key] = value
	}
	for key, value := range payload {
		if text, ok := value.(string); ok && strings.TrimSpace(text) == "" {
			delete(payload, key)
		}
	}
	return payload
}

func parseTencentChannelActionResult(text string) interface{} {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	var value interface{}
	if err := json.Unmarshal([]byte(text), &value); err == nil {
		return value
	}
	payload := parseTencentChannelCLIJSON(text)
	if _, ok := payload["raw"]; !ok {
		if data, ok := payload["data"]; ok {
			return data
		}
		return payload
	}
	return text
}

func parseTencentChannelGatewayPollResult(text string) tencentChannelGatewayPollResult {
	text = strings.TrimSpace(text)
	if text == "" {
		return tencentChannelGatewayPollResult{}
	}
	var result tencentChannelGatewayPollResult
	if err := json.Unmarshal([]byte(text), &result); err == nil && tencentChannelPollResultHasData(result) {
		return result
	}
	var events []json.RawMessage
	if err := json.Unmarshal([]byte(text), &events); err == nil {
		return tencentChannelGatewayPollResult{Events: events}
	}
	payload := parseTencentChannelCLIJSON(text)
	if _, ok := payload["raw"]; ok {
		return tencentChannelGatewayPollResult{}
	}
	if parsed := parseTencentChannelGatewayPollValue(payload); tencentChannelPollResultHasData(parsed) {
		return parsed
	}
	if data, ok := payload["data"]; ok {
		return parseTencentChannelGatewayPollValue(data)
	}
	return tencentChannelGatewayPollResult{}
}

func parseTencentChannelGatewayPollValue(value interface{}) tencentChannelGatewayPollResult {
	data, err := json.Marshal(value)
	if err != nil {
		return tencentChannelGatewayPollResult{}
	}
	var result tencentChannelGatewayPollResult
	if err := json.Unmarshal(data, &result); err == nil && (len(result.Events) > 0 || result.Cursor != "" || result.Watermark != "") {
		return result
	}
	var events []json.RawMessage
	if err := json.Unmarshal(data, &events); err == nil {
		return tencentChannelGatewayPollResult{Events: events}
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(data, &payload); err == nil {
		return parseTencentChannelNoticePollPayload(payload)
	}
	return tencentChannelGatewayPollResult{}
}

func tencentChannelPollResultHasData(result tencentChannelGatewayPollResult) bool {
	return len(result.Events) > 0 || result.Cursor != "" || result.Watermark != "" || result.AttachInfo != ""
}

func parseTencentChannelNoticePollPayload(payload map[string]interface{}) tencentChannelGatewayPollResult {
	notices, ok := payload["notices"].([]interface{})
	if !ok {
		return tencentChannelGatewayPollResult{}
	}
	result := tencentChannelGatewayPollResult{
		AttachInfo: firstStringValue(payload["attach_info"], payload["attachInfo"]),
	}
	for _, notice := range notices {
		noticeMap, ok := notice.(map[string]interface{})
		if !ok {
			continue
		}
		event := map[string]interface{}{}
		for key, value := range noticeMap {
			event[key] = value
		}
		event["event_type"] = "notice"
		event["source"] = providerTencentChannel
		if summary := firstStringValue(event["summary"], event["content"], event["text"]); summary != "" {
			event["content"] = summary
		}
		if noticeID := tencentChannelNoticeEventKey(event); noticeID != "" {
			event["notice_id"] = noticeID
		}
		data, err := json.Marshal(event)
		if err == nil {
			result.Events = append(result.Events, data)
		}
	}
	return result
}

func filterTencentChannelNewEvents(events []json.RawMessage, seenKeys []string) ([]json.RawMessage, []string) {
	seen := map[string]struct{}{}
	for _, key := range seenKeys {
		key = strings.TrimSpace(key)
		if key != "" {
			seen[key] = struct{}{}
		}
	}
	nextKeys := make([]string, 0, len(events)+len(seenKeys))
	newEvents := []json.RawMessage{}
	for _, event := range events {
		key := tencentChannelNoticeRawEventKey(event)
		if key == "" {
			newEvents = append(newEvents, event)
			continue
		}
		if _, ok := seen[key]; !ok {
			newEvents = append(newEvents, event)
		}
		nextKeys = append(nextKeys, key)
	}
	for _, key := range seenKeys {
		if len(nextKeys) >= 200 {
			break
		}
		if _, ok := seen[key]; ok && !tencentChannelStringSliceContains(nextKeys, key) {
			nextKeys = append(nextKeys, key)
		}
	}
	return newEvents, nextKeys
}

func tencentChannelSeenNoticeKeys(config map[string]interface{}) []string {
	value, ok := config["notice_seen_keys"]
	if !ok || value == nil {
		return nil
	}
	switch typed := value.(type) {
	case []string:
		return typed
	case []interface{}:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" && text != "<nil>" {
				result = append(result, text)
			}
		}
		return result
	case string:
		return decodeStringList(typed, 200)
	default:
		return nil
	}
}

func tencentChannelNoticeRawEventKey(raw json.RawMessage) string {
	var event map[string]interface{}
	if json.Unmarshal(raw, &event) != nil {
		return ""
	}
	return tencentChannelNoticeEventKey(event)
}

func tencentChannelNoticeEventKey(event map[string]interface{}) string {
	if id := firstStringValue(event["notice_id"], event["noticeId"], event["id"]); id != "" {
		return id
	}
	parts := []string{}
	for _, value := range []string{
		firstStringValue(event["create_time"], event["createTime"], event["time"]),
		firstStringValue(event["type"], event["event_type"]),
		firstStringID(event["guild_id"], event["guildId"]),
		firstStringID(event["feed_id"], event["feedId"]),
		firstStringValue(event["summary"], event["content"], event["text"]),
	} {
		if value != "" {
			parts = append(parts, value)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "|")
}

func tencentChannelStringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func tencentChannelActionPayload(integration MessageChannelIntegration, inbound MessageChannelMessage, content string) map[string]interface{} {
	config := tencentChannelConfig(providerConfig(integration))
	payload := map[string]interface{}{
		"content":     content,
		"guild_id":    configString(config, "guild_id"),
		"channel_id":  configString(config, "channel_id"),
		"cli_profile": configString(config, "cli_profile"),
		"json":        true,
		"cli_flags":   []string{"--json"},
	}
	var raw map[string]interface{}
	if json.Unmarshal([]byte(inbound.Payload), &raw) == nil {
		data := objectAt(raw, "data")
		if len(data) == 0 {
			data = objectAt(raw, "event")
		}
		if len(data) == 0 {
			data = raw
		}
		copyTencentChannelPayloadField(payload, data, "ref", "ref", "notice_ref", "notice_number")
		copyTencentChannelPayloadField(payload, data, "guild_id", "guild_id", "guildId")
		copyTencentChannelPayloadField(payload, data, "channel_id", "channel_id", "channelId")
		copyTencentChannelPayloadField(payload, data, "feed_id", "feed_id", "feedId")
		copyTencentChannelPayloadField(payload, data, "feed_create_time", "feed_create_time", "feedCreateTime")
		copyTencentChannelPayloadField(payload, data, "comment_id", "comment_id", "commentId")
		copyTencentChannelPayloadField(payload, data, "comment_author_id", "comment_author_id", "commentAuthorId")
		copyTencentChannelPayloadField(payload, data, "comment_create_time", "comment_create_time", "commentCreateTime")
		copyTencentChannelPayloadField(payload, data, "reply_id", "reply_id", "replyId")
		copyTencentChannelPayloadField(payload, data, "target_user_id", "target_user_id", "targetUserId")
	}
	for key, value := range payload {
		if text, ok := value.(string); ok && strings.TrimSpace(text) == "" {
			delete(payload, key)
		}
	}
	return payload
}

func copyTencentChannelPayloadField(target map[string]interface{}, source map[string]interface{}, targetKey string, keys ...string) {
	for _, key := range keys {
		if value := firstStringID(source[key]); value != "" {
			target[targetKey] = value
			return
		}
		if value := stringValue(source[key]); value != "" {
			target[targetKey] = value
			return
		}
	}
}

func formatTencentChannelEventContent(data map[string]interface{}) string {
	eventType := firstStringValue(data["type"], data["event_type"], data["notice_type"])
	title := firstStringValue(data["title"], data["feed_title"])
	author := firstStringValue(data["author_name"], data["nick"], data["nickname"])
	parts := []string{}
	if eventType != "" {
		parts = append(parts, "event="+eventType)
	}
	if title != "" {
		parts = append(parts, "title="+title)
	}
	if author != "" {
		parts = append(parts, "author="+author)
	}
	if len(parts) == 0 {
		return "Tencent Channel event"
	}
	return "Tencent Channel event: " + strings.Join(parts, ", ")
}

func tencentChannelExternalChatID(guildID string, channelID string, feedID string) string {
	parts := []string{}
	if guildID != "" {
		parts = append(parts, "guild:"+guildID)
	}
	if channelID != "" {
		parts = append(parts, "channel:"+channelID)
	}
	if feedID != "" {
		parts = append(parts, "feed:"+feedID)
	}
	if len(parts) == 0 {
		return "tencent_channel"
	}
	return strings.Join(parts, ":")
}

func tencentChannelGatewaySystemPrompt(integration MessageChannelIntegration, inbound MessageChannelMessage) string {
	config := tencentChannelConfig(providerConfig(integration))
	lines := []string{
		"Tencent Channel gateway context:",
		"- This is a post/comment based message channel, not an instant-message chat.",
		"- When the incoming event is a mention or post/comment event, answer with the exact text that should be posted back.",
		"- The gateway will publish your final answer as a Tencent Channel comment or comment reply; do not include implementation notes.",
		"- Do not claim that you have posted anything unless the gateway returns success.",
	}
	if guildID := configString(config, "guild_id"); guildID != "" {
		lines = append(lines, "- default_guild_id: "+guildID)
	}
	if channelID := configString(config, "channel_id"); channelID != "" {
		lines = append(lines, "- default_channel_id: "+channelID)
	}
	if ref := tencentChannelPayloadRef(inbound.Payload); ref != "" {
		lines = append(lines, "- source_ref: "+ref)
	}
	return strings.Join(lines, "\n")
}

func tencentChannelPayloadRef(rawPayload string) string {
	var raw map[string]interface{}
	if json.Unmarshal([]byte(rawPayload), &raw) != nil {
		return ""
	}
	data := objectAt(raw, "data")
	if len(data) == 0 {
		data = objectAt(raw, "event")
	}
	if len(data) == 0 {
		data = raw
	}
	return firstStringValue(data["ref"], data["notice_ref"], data["notice_number"])
}

func normalizeTencentChannelConfig(config map[string]interface{}) map[string]interface{} {
	next := map[string]interface{}{}
	for key, value := range config {
		next[key] = value
	}
	next["gateway_enabled"] = boolConfig(next, "gateway_enabled", true)
	next["poll_mentions"] = boolConfig(next, "poll_mentions", true)
	next["poll_posts"] = boolConfig(next, "poll_posts", false)
	next["auto_reply_mentions"] = boolConfig(next, "auto_reply_mentions", true)
	next["default_get_type"] = intConfig(next, "default_get_type", 2, 1, 2)
	next["poll_interval_seconds"] = intConfig(next, "poll_interval_seconds", int(tencentChannelDefaultPollInterval/time.Second), 10, 3600)
	next["max_events"] = intConfig(next, "max_events", 20, 1, 100)
	replyMode := strings.ToLower(configString(next, "reply_mode", "reply_action"))
	switch replyMode {
	case "reply":
		next["reply_mode"] = "reply"
	default:
		next["reply_mode"] = "comment"
	}
	return next
}

func tencentChannelConfig(config map[string]interface{}) map[string]interface{} {
	return normalizeTencentChannelConfig(config)
}

func updateTencentChannelProviderConfig(integrationID uint, mutate func(map[string]interface{})) error {
	var integration MessageChannelIntegration
	if err := model.DB.First(&integration, integrationID).Error; err != nil {
		return err
	}
	options := decodeAdvancedOptions(integration.AdvancedOptions)
	config := providerConfigFromAdvancedOptions(options)
	mutate(config)
	options.CustomProviderConfigJSON = encodeProviderConfig(normalizeTencentChannelConfig(config))
	data, err := json.Marshal(options)
	if err != nil {
		return err
	}
	return model.DB.Model(&integration).Update("advanced_options", string(data)).Error
}

func tencentChannelGatewaySignature(integration MessageChannelIntegration) string {
	config := tencentChannelConfig(providerConfig(integration))
	values := []string{
		integration.DefaultDeviceID,
		integration.DefaultWorkspacePath,
		strconv.FormatBool(integration.DefaultWorkspaceUnrestricted),
		configString(config, "guild_id"),
		configString(config, "channel_id"),
		configString(config, "cli_profile"),
		tencentChannelConfigString(config, "gateway_enabled"),
		tencentChannelConfigString(config, "poll_mentions"),
		tencentChannelConfigString(config, "poll_posts"),
		tencentChannelConfigString(config, "poll_interval_seconds"),
		tencentChannelConfigString(config, "max_events"),
	}
	sum := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return hex.EncodeToString(sum[:])
}

func firstNonEmptyTencentText(values ...string) string {
	for _, value := range values {
		if text := strings.TrimSpace(value); text != "" {
			return text
		}
	}
	return ""
}

func tencentChannelConfigString(config map[string]interface{}, key string) string {
	value, ok := config[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case bool:
		return strconv.FormatBool(typed)
	case int:
		return strconv.Itoa(typed)
	case float64:
		return strconv.Itoa(int(typed))
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func boolConfig(config map[string]interface{}, key string, fallback bool) bool {
	switch typed := config[key].(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "true", "yes", "on", "enabled":
			return true
		case "0", "false", "no", "off", "disabled":
			return false
		}
	case float64:
		return typed != 0
	case int:
		return typed != 0
	}
	return fallback
}

func intConfig(config map[string]interface{}, key string, fallback int, min int, max int) int {
	value := fallback
	switch typed := config[key].(type) {
	case float64:
		value = int(typed)
	case int:
		value = typed
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			value = parsed
		}
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
