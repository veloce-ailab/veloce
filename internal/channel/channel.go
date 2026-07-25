package channel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	communityservice "github.com/veloce-ailab/veloce/internal/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	settingMessageChannelEnabled = "message_channel_enabled"

	providerTelegram       = "telegram"
	providerDiscord        = "discord"
	providerQQ             = "qq"
	providerOneBot         = "onebot"
	providerWeixin         = "weixin"
	providerTencentChannel = "tencent_channel"

	messageChannelDefaultAgentKey = "default"

	defaultContextMessageCount = 12
	maxContextMessageCount     = 100
	maxWebhookPayloadBytes     = 1 << 20
)

type MessageChannelIntegration struct {
	ID                              uint       `gorm:"primaryKey" json:"id"`
	UserID                          uint       `gorm:"uniqueIndex:idx_message_channel_user_name;not null" json:"user_id"`
	User                            model.User `gorm:"foreignKey:UserID" json:"-"`
	Name                            string     `gorm:"uniqueIndex:idx_message_channel_user_name;size:120;not null" json:"name"`
	Provider                        string     `gorm:"index;size:180;not null" json:"provider"`
	BotToken                        string     `gorm:"type:text;not null" json:"-"`
	WebhookSecret                   string     `gorm:"uniqueIndex;size:80;not null" json:"-"`
	Enabled                         bool       `gorm:"default:true" json:"enabled"`
	DefaultDeviceID                 string     `gorm:"size:80" json:"default_device_id"`
	DefaultWorkspacePath            string     `gorm:"type:text" json:"default_workspace_path"`
	DefaultWorkspaceUnrestricted    bool       `gorm:"default:false" json:"default_workspace_unrestricted"`
	DefaultConnectorAutoApprove     bool       `gorm:"default:false" json:"default_connector_auto_approve"`
	DefaultConnectorCommandPrefixes string     `gorm:"type:text;not null;default:'[]'" json:"-"`
	DefaultUserChannelID            *uint      `gorm:"index" json:"default_user_channel_id,omitempty"`
	DefaultModel                    string     `gorm:"size:120" json:"default_model"`
	DefaultAgentID                  *uint      `gorm:"index" json:"default_agent_id,omitempty"`
	DefaultAgentKey                 string     `gorm:"size:80" json:"default_agent_key"`
	DefaultAgentGroupID             string     `gorm:"size:80" json:"default_agent_group_id"`
	DefaultSkillIDs                 string     `gorm:"type:text;not null" json:"-"`
	DefaultContextMessageCount      int        `gorm:"default:12" json:"default_context_message_count"`
	ReplyMode                       string     `gorm:"size:30;default:mention" json:"reply_mode"`
	TriggerMode                     string     `gorm:"size:30;default:mention" json:"trigger_mode"`
	SystemPrompt                    string     `gorm:"type:text;not null" json:"system_prompt"`
	GroupConfigs                    string     `gorm:"type:text;not null" json:"-"`
	AdvancedOptions                 string     `gorm:"type:text;not null" json:"-"`
	LastEventAt                     *time.Time `json:"last_event_at,omitempty"`
	CreatedAt                       time.Time  `json:"created_at"`
	UpdatedAt                       time.Time  `json:"updated_at"`
}

type MessageChannelMessage struct {
	ID               uint                      `gorm:"primaryKey" json:"id"`
	IntegrationID    uint                      `gorm:"index;not null" json:"integration_id"`
	Integration      MessageChannelIntegration `gorm:"foreignKey:IntegrationID" json:"-"`
	UserID           uint                      `gorm:"index;not null" json:"user_id"`
	Provider         string                    `gorm:"index;size:30;not null" json:"provider"`
	ExternalChatID   string                    `gorm:"index;size:160" json:"external_chat_id"`
	ExternalUserID   string                    `gorm:"size:160" json:"external_user_id"`
	ExternalUserName string                    `gorm:"size:200" json:"external_user_name"`
	ExternalMsgID    string                    `gorm:"size:160" json:"external_message_id"`
	Direction        string                    `gorm:"size:20;not null" json:"direction"`
	Status           string                    `gorm:"size:30;not null" json:"status"`
	Content          string                    `gorm:"type:text;not null" json:"content"`
	Payload          string                    `gorm:"type:text;not null" json:"-"`
	Error            string                    `gorm:"type:text;not null" json:"error,omitempty"`
	CreatedAt        time.Time                 `json:"created_at"`
}

type GroupConfig struct {
	ExternalID               string   `json:"external_id"`
	Name                     string   `json:"name"`
	Enabled                  bool     `json:"enabled"`
	DeviceID                 string   `json:"device_id"`
	WorkspacePath            string   `json:"workspace_path"`
	WorkspaceUnrestricted    bool     `json:"workspace_unrestricted"`
	ConnectorAutoApprove     bool     `json:"connector_auto_approve"`
	ConnectorCommandPrefixes []string `json:"connector_command_prefixes"`
	UserChannelID            *uint    `json:"user_channel_id,omitempty"`
	Model                    string   `json:"model"`
	AgentID                  *uint    `json:"agent_id,omitempty"`
	AgentKey                 string   `json:"agent_key"`
	AgentGroupID             string   `json:"agent_group_id"`
	SkillIDs                 []string `json:"skill_ids"`
	ContextMessageCount      int      `json:"context_message_count"`
	ReplyMode                string   `json:"reply_mode"`
	TriggerMode              string   `json:"trigger_mode"`
	SystemPromptOverride     string   `json:"system_prompt_override"`
}

type AdvancedOptions struct {
	Temperature              *float64 `json:"temperature,omitempty"`
	MaxTokens                int      `json:"max_tokens"`
	ReplyPrefix              string   `json:"reply_prefix"`
	ReplySuffix              string   `json:"reply_suffix"`
	Language                 string   `json:"language"`
	Timezone                 string   `json:"timezone"`
	MentionPolicy            string   `json:"mention_policy"`
	AttachmentMode           string   `json:"attachment_mode"`
	ThreadMode               string   `json:"thread_mode"`
	DeduplicationWindowSecs  int      `json:"deduplication_window_seconds"`
	ResponseTimeoutSecs      int      `json:"response_timeout_seconds"`
	ErrorFallback            string   `json:"error_fallback"`
	CustomProviderConfigJSON string   `json:"custom_provider_config_json"`
}

type integrationInput struct {
	Name                            string          `json:"name"`
	Provider                        string          `json:"provider"`
	BotToken                        *string         `json:"bot_token"`
	Enabled                         *bool           `json:"enabled"`
	DefaultDeviceID                 string          `json:"default_device_id"`
	DefaultWorkspacePath            string          `json:"default_workspace_path"`
	DefaultWorkspaceUnrestricted    bool            `json:"default_workspace_unrestricted"`
	DefaultConnectorAutoApprove     bool            `json:"default_connector_auto_approve"`
	DefaultConnectorCommandPrefixes []string        `json:"default_connector_command_prefixes"`
	DefaultUserChannelID            *uint           `json:"default_user_channel_id"`
	DefaultModel                    string          `json:"default_model"`
	DefaultAgentID                  *uint           `json:"default_agent_id"`
	DefaultAgentKey                 string          `json:"default_agent_key"`
	DefaultAgentGroupID             string          `json:"default_agent_group_id"`
	DefaultSkillIDs                 []string        `json:"default_skill_ids"`
	DefaultContextMessageCount      *int            `json:"default_context_message_count"`
	ReplyMode                       string          `json:"reply_mode"`
	TriggerMode                     string          `json:"trigger_mode"`
	SystemPrompt                    string          `json:"system_prompt"`
	GroupConfigs                    []GroupConfig   `json:"group_configs"`
	AdvancedOptions                 AdvancedOptions `json:"advanced_options"`
}

type adminSettingsInput struct {
	Enabled *bool `json:"enabled"`
}

type adminSettingsResponse struct {
	Enabled bool `json:"enabled"`
}

type integrationResponse struct {
	ID                              uint            `json:"id"`
	Name                            string          `json:"name"`
	Provider                        string          `json:"provider"`
	Enabled                         bool            `json:"enabled"`
	BotTokenConfigured              bool            `json:"bot_token_configured"`
	BotTokenPreview                 string          `json:"bot_token_preview,omitempty"`
	WebhookSecret                   string          `json:"webhook_secret"`
	WebhookPath                     string          `json:"webhook_path"`
	DefaultDeviceID                 string          `json:"default_device_id"`
	DefaultWorkspacePath            string          `json:"default_workspace_path"`
	DefaultWorkspaceUnrestricted    bool            `json:"default_workspace_unrestricted"`
	DefaultConnectorAutoApprove     bool            `json:"default_connector_auto_approve"`
	DefaultConnectorCommandPrefixes []string        `json:"default_connector_command_prefixes"`
	DefaultUserChannelID            *uint           `json:"default_user_channel_id,omitempty"`
	DefaultModel                    string          `json:"default_model"`
	DefaultAgentID                  *uint           `json:"default_agent_id,omitempty"`
	DefaultAgentKey                 string          `json:"default_agent_key"`
	DefaultAgentGroupID             string          `json:"default_agent_group_id"`
	DefaultSkillIDs                 []string        `json:"default_skill_ids"`
	DefaultContextMessageCount      int             `json:"default_context_message_count"`
	ReplyMode                       string          `json:"reply_mode"`
	TriggerMode                     string          `json:"trigger_mode"`
	SystemPrompt                    string          `json:"system_prompt"`
	GroupConfigs                    []GroupConfig   `json:"group_configs"`
	AdvancedOptions                 AdvancedOptions `json:"advanced_options"`
	LastEventAt                     *time.Time      `json:"last_event_at,omitempty"`
	CreatedAt                       time.Time       `json:"created_at"`
	UpdatedAt                       time.Time       `json:"updated_at"`
}

type API struct{}

func InitFeatures() error {
	if err := model.DB.AutoMigrate(&MessageChannelIntegration{}, &MessageChannelMessage{}); err != nil {
		return err
	}
	startMessageChannelRuntime()
	return nil
}

func RegisterAdminRoutes(group *gin.RouterGroup) {
	api := &API{}
	group.GET("/message-channel/settings", api.getAdminSettings)
	group.PUT("/message-channel/settings", api.updateAdminSettings)
}

func RegisterUserRoutes(group *gin.RouterGroup) {
	api := &API{}
	group.GET("/message-channels/settings", api.getUserSettings)
	group.GET("/message-channels", api.listIntegrations)
	group.POST("/message-channels", api.createIntegration)
	group.PUT("/message-channels/:id", api.updateIntegration)
	group.POST("/message-channels/:id/qq/login/start", api.startQQLogin)
	group.POST("/message-channels/:id/qq/login/wait", api.waitQQLogin)
	group.POST("/message-channels/:id/weixin/login/start", api.startWeixinLogin)
	group.POST("/message-channels/:id/weixin/login/wait", api.waitWeixinLogin)
	group.POST("/message-channels/:id/tencent-channel/login/start", api.startTencentChannelLogin)
	group.POST("/message-channels/:id/tencent-channel/login/wait", api.waitTencentChannelLogin)
	group.POST("/message-channels/:id/tencent-channel/guilds", api.tencentChannelGuilds)
	group.POST("/message-channels/:id/tencent-channel/channels", api.tencentChannelChannels)
	group.POST("/message-channels/:id/tencent-channel/list-posts", api.tencentChannelListPosts)
	group.POST("/message-channels/:id/tencent-channel/get-post", api.tencentChannelGetPost)
	group.POST("/message-channels/:id/tencent-channel/get-comments", api.tencentChannelGetComments)
	group.POST("/message-channels/:id/tencent-channel/publish-post", api.tencentChannelPublishPost)
	group.POST("/message-channels/:id/tencent-channel/comment-post", api.tencentChannelCommentPost)
	group.POST("/message-channels/:id/tencent-channel/reply-comment", api.tencentChannelReplyComment)
	group.POST("/message-channels/:id/enable", api.enableIntegration)
	group.POST("/message-channels/:id/disable", api.disableIntegration)
	group.DELETE("/message-channels/:id", api.deleteIntegration)
	group.GET("/message-channels/:id/messages", api.listMessages)
}

func RegisterPublicRoutes(group *gin.RouterGroup) {
	api := &API{}
	group.POST("/message-channels/:provider/:id/webhook", api.receiveWebhook)
}

func Enabled() bool {
	return settingBool(settingMessageChannelEnabled, false)
}

func (api *API) getAdminSettings(c *gin.Context) {
	c.JSON(http.StatusOK, adminSettingsResponse{Enabled: Enabled()})
}

func (api *API) updateAdminSettings(c *gin.Context) {
	var input adminSettingsInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if input.Enabled != nil {
		if err := model.SetSystemSetting(settingMessageChannelEnabled, strconv.FormatBool(*input.Enabled)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update message channel settings"})
			return
		}
	}
	syncMessageChannelRuntime()
	c.JSON(http.StatusOK, adminSettingsResponse{Enabled: Enabled()})
}

func (api *API) getUserSettings(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":   Enabled(),
		"providers": supportedProvidersForUser(user.ID),
	})
}

func (api *API) listIntegrations(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if !requireEnabled(c) {
		return
	}
	var integrations []MessageChannelIntegration
	if err := model.DB.Where("user_id = ?", user.ID).Order("created_at ASC").Find(&integrations).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list message channels"})
		return
	}
	response := make([]integrationResponse, 0, len(integrations))
	for _, integration := range integrations {
		response = append(response, integrationToResponse(integration))
	}
	c.JSON(http.StatusOK, response)
}

func (api *API) createIntegration(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if !requireEnabled(c) {
		return
	}
	var input integrationInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	integration, ok := integrationFromInput(c, input, MessageChannelIntegration{UserID: user.ID})
	if !ok {
		return
	}
	secret, err := newSecret()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create webhook secret"})
		return
	}
	integration.WebhookSecret = secret
	if err := model.DB.Create(&integration).Error; err != nil {
		if isUniqueError(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "Message channel name already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create message channel"})
		return
	}
	syncMessageChannelRuntime()
	c.JSON(http.StatusOK, integrationToResponse(integration))
}

func (api *API) updateIntegration(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if !requireEnabled(c) {
		return
	}
	var integration MessageChannelIntegration
	if err := model.DB.Where("id = ? AND user_id = ?", c.Param("id"), user.ID).First(&integration).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Message channel not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load message channel"})
		return
	}
	var input integrationInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	next, ok := integrationFromInput(c, input, integration)
	if !ok {
		return
	}
	if err := model.DB.Model(&integration).Updates(map[string]interface{}{
		"name":                               next.Name,
		"provider":                           next.Provider,
		"bot_token":                          next.BotToken,
		"enabled":                            next.Enabled,
		"default_device_id":                  next.DefaultDeviceID,
		"default_workspace_path":             next.DefaultWorkspacePath,
		"default_workspace_unrestricted":     next.DefaultWorkspaceUnrestricted,
		"default_connector_auto_approve":     next.DefaultConnectorAutoApprove,
		"default_connector_command_prefixes": next.DefaultConnectorCommandPrefixes,
		"default_user_channel_id":            next.DefaultUserChannelID,
		"default_model":                      next.DefaultModel,
		"default_agent_id":                   next.DefaultAgentID,
		"default_agent_key":                  next.DefaultAgentKey,
		"default_agent_group_id":             next.DefaultAgentGroupID,
		"default_skill_ids":                  next.DefaultSkillIDs,
		"default_context_message_count":      next.DefaultContextMessageCount,
		"reply_mode":                         next.ReplyMode,
		"trigger_mode":                       next.TriggerMode,
		"system_prompt":                      next.SystemPrompt,
		"group_configs":                      next.GroupConfigs,
		"advanced_options":                   next.AdvancedOptions,
	}).Error; err != nil {
		if isUniqueError(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "Message channel name already exists"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update message channel"})
		return
	}
	model.DB.First(&integration, integration.ID)
	syncMessageChannelRuntime()
	c.JSON(http.StatusOK, integrationToResponse(integration))
}

func (api *API) enableIntegration(c *gin.Context) {
	api.updateIntegrationEnabled(c, true)
}

func (api *API) disableIntegration(c *gin.Context) {
	api.updateIntegrationEnabled(c, false)
}

func (api *API) updateIntegrationEnabled(c *gin.Context, enabled bool) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if !requireEnabled(c) {
		return
	}
	var integration MessageChannelIntegration
	if err := model.DB.Where("id = ? AND user_id = ?", c.Param("id"), user.ID).First(&integration).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Message channel not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load message channel"})
		return
	}
	if err := model.DB.Model(&integration).Update("enabled", enabled).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update message channel"})
		return
	}
	integration.Enabled = enabled
	syncMessageChannelRuntime()
	c.JSON(http.StatusOK, integrationToResponse(integration))
}

func (api *API) deleteIntegration(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if !requireEnabled(c) {
		return
	}
	id := c.Param("id")
	if err := model.DB.Where("integration_id IN (?)", model.DB.Model(&MessageChannelIntegration{}).Select("id").Where("id = ? AND user_id = ?", id, user.ID)).Delete(&MessageChannelMessage{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete message channel messages"})
		return
	}
	if err := model.DB.Where("id = ? AND user_id = ?", id, user.ID).Delete(&MessageChannelIntegration{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete message channel"})
		return
	}
	syncMessageChannelRuntime()
	c.JSON(http.StatusOK, gin.H{"message": "Message channel deleted"})
}

func (api *API) listMessages(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if !requireEnabled(c) {
		return
	}
	var messages []MessageChannelMessage
	if err := model.DB.
		Where("integration_id = ? AND user_id = ?", c.Param("id"), user.ID).
		Order("created_at DESC").
		Limit(100).
		Find(&messages).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list message channel messages"})
		return
	}
	c.JSON(http.StatusOK, messages)
}

func (api *API) receiveWebhook(c *gin.Context) {
	if !Enabled() {
		c.JSON(http.StatusNotFound, gin.H{"error": "Message channel is disabled"})
		return
	}
	provider := resolveProvider(c.Param("provider"))
	if provider == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Unsupported message channel provider"})
		return
	}
	var integration MessageChannelIntegration
	if err := model.DB.Where("id = ? AND provider = ? AND enabled = ?", c.Param("id"), provider, true).First(&integration).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Message channel not found"})
		return
	}
	secret := strings.TrimSpace(c.Query("secret"))
	if secret == "" {
		secret = strings.TrimSpace(c.GetHeader("X-Message-Channel-Secret"))
	}
	if secret == "" || secret != integration.WebhookSecret {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid webhook secret"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxWebhookPayloadBytes))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid webhook payload"})
		return
	}
	if err := processMessageChannelPayload(c.Request.Context(), integration, body, c); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func processMessageChannelPayload(ctx context.Context, integration MessageChannelIntegration, body []byte, c *gin.Context) error {
	adapter, ok := providerFor(integration.Provider)
	if !ok {
		return errors.New("unsupported provider")
	}
	var summary webhookSummary
	if pluginAdapter, ok := adapter.(pluginChannelProvider); ok {
		method := "POST"
		headers := map[string]string{}
		if c != nil {
			method = c.Request.Method
			for key, values := range c.Request.Header {
				if len(headers) >= 50 {
					break
				}
				headers[key] = strings.Join(values, ", ")
			}
		}
		var err error
		summary, err = pluginChannelInboundSummary(ctx, pluginAdapter.descriptor, integration, body, method, headers)
		if err != nil {
			return err
		}
	} else {
		summary = adapter.ExtractWebhookSummary(body)
	}
	now := time.Now()
	message := MessageChannelMessage{
		IntegrationID:    integration.ID,
		UserID:           integration.UserID,
		Provider:         integration.Provider,
		ExternalChatID:   summary.ExternalChatID,
		ExternalUserID:   summary.ExternalUserID,
		ExternalUserName: trimMax(summary.ExternalUserName, 200),
		ExternalMsgID:    summary.ExternalMessageID,
		Direction:        "inbound",
		Status:           "received",
		Content:          summary.Content,
		Payload:          string(body),
		CreatedAt:        now,
	}
	if err := model.DB.Create(&message).Error; err != nil {
		return errors.New("failed to record message channel event")
	}
	_ = model.DB.Model(&integration).Update("last_event_at", now).Error
	if strings.TrimSpace(summary.Content) != "" {
		handleInboundMessage(ctx, c, integration, message)
	}
	return nil
}

func (api *API) handleInboundMessage(c *gin.Context, integration MessageChannelIntegration, inbound MessageChannelMessage) {
	handleInboundMessage(c.Request.Context(), c, integration, inbound)
}

func handleInboundMessage(ctx context.Context, c *gin.Context, integration MessageChannelIntegration, inbound MessageChannelMessage) {
	if strings.TrimSpace(inbound.ExternalChatID) == "" {
		_ = model.DB.Model(&inbound).Updates(map[string]interface{}{"status": "error", "error": "missing external chat id"}).Error
		return
	}
	resolved := resolveConfigForMessage(integration, inbound.ExternalChatID)
	if handled := handleMessageChannelApprovalReply(ctx, integration, inbound, resolved); handled {
		return
	}
	if !resolved.Enabled || normalizeMode(resolved.TriggerMode, integration.TriggerMode) == "manual" {
		_ = model.DB.Model(&inbound).Updates(map[string]interface{}{"status": "ignored"}).Error
		return
	}
	modelName := strings.TrimSpace(resolved.Model)
	if modelName == "" {
		modelName = strings.TrimSpace(integration.DefaultModel)
	}
	if modelName == "" {
		_ = model.DB.Model(&inbound).Updates(map[string]interface{}{"status": "waiting_model"}).Error
		return
	}
	var user model.User
	if err := model.DB.First(&user, integration.UserID).Error; err != nil {
		_ = model.DB.Model(&inbound).Updates(map[string]interface{}{"status": "error", "error": "user not found"}).Error
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var cancel context.CancelFunc
	ctx = context.Background()
	if resolved.AdvancedOptions.ResponseTimeoutSecs > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(resolved.AdvancedOptions.ResponseTimeoutSecs)*time.Second)
		defer cancel()
	}
	messages := historyMessages(integration.ID, inbound.ExternalChatID, inbound.ID, resolved.ContextMessageCount)
	messages = append(messages, communityservice.ChatExecutorMessage{Role: "user", Content: messageChannelUserMessageContent(inbound)})
	systemPrompt := buildSystemPrompt(integration.UserID, resolved, integration.SystemPrompt)
	if contextPrompt := messageChannelIdentitySystemPrompt(integration, inbound); contextPrompt != "" {
		systemPrompt = strings.TrimSpace(systemPrompt + "\n\n" + contextPrompt)
	}
	if integration.Provider == providerTencentChannel {
		systemPrompt = strings.TrimSpace(systemPrompt + "\n\n" + tencentChannelGatewaySystemPrompt(integration, inbound))
	}
	var replyContent string
	var completionErr error
	if strings.TrimSpace(resolved.DeviceID) != "" || strings.TrimSpace(resolved.WorkspacePath) != "" || strings.TrimSpace(resolved.AgentGroupID) != "" {
		result, err := communityservice.ExecuteMessageChannelAssistantCompletion(&user, communityservice.MessageChannelAssistantRequest{
			Context:                        ctx,
			ModelName:                      modelName,
			UserChannelID:                  resolved.userChannelID(),
			Messages:                       messages,
			System:                         systemPrompt,
			ConnectorDeviceID:              resolved.DeviceID,
			ConnectorWorkspacePath:         resolved.WorkspacePath,
			ConnectorWorkspaceUnrestricted: resolved.WorkspaceUnrestricted,
			ConnectorAutoApprove:           resolved.ConnectorAutoApprove,
			ConnectorCommandPrefixes:       resolved.ConnectorCommandPrefixes,
			AgentGroupID:                   resolved.AgentGroupID,
			MaxTokens:                      resolved.AdvancedOptions.MaxTokens,
			Temperature:                    resolved.AdvancedOptions.Temperature,
			RunID:                          messageChannelApprovalRunID(integration.ID, inbound.ExternalChatID),
			OnApprovalRequired: func(approvalCtx context.Context, approval communityservice.MessageChannelConnectorApproval) error {
				text := formatMessageChannelApprovalPrompt(approval)
				return sendProviderReplyForMessage(approvalCtx, integration, inbound, text)
			},
		})
		completionErr = err
		if result != nil {
			replyContent = result.Content
		}
	} else {
		result, err := communityservice.ExecuteServerChatCompletion(c, &user, communityservice.ChatExecutorRequest{
			Context:       ctx,
			ModelName:     modelName,
			UserChannelID: resolved.userChannelID(),
			Messages:      messages,
			System:        systemPrompt,
			MaxTokens:     resolved.AdvancedOptions.MaxTokens,
			Temperature:   resolved.AdvancedOptions.Temperature,
		})
		completionErr = err
		if result != nil {
			replyContent = result.Content
		}
	}
	if completionErr != nil {
		if fallback := strings.TrimSpace(resolved.AdvancedOptions.ErrorFallback); fallback != "" {
			_ = sendProviderReplyForMessage(ctx, integration, inbound, fallback)
		}
		_ = model.DB.Model(&inbound).Updates(map[string]interface{}{"status": "error", "error": completionErr.Error()}).Error
		return
	}
	reply := strings.TrimSpace(replyContent)
	if reply == "" {
		_ = model.DB.Model(&inbound).Updates(map[string]interface{}{"status": "completed"}).Error
		return
	}
	reply = resolved.AdvancedOptions.ReplyPrefix + reply + resolved.AdvancedOptions.ReplySuffix
	status := "sent"
	errText := ""
	if err := sendProviderReplyForMessage(ctx, integration, inbound, reply); err != nil {
		status = "send_failed"
		errText = err.Error()
	}
	outbound := MessageChannelMessage{
		IntegrationID:  integration.ID,
		UserID:         integration.UserID,
		Provider:       integration.Provider,
		ExternalChatID: inbound.ExternalChatID,
		Direction:      "outbound",
		Status:         status,
		Content:        reply,
		Error:          errText,
	}
	_ = model.DB.Create(&outbound).Error
	_ = model.DB.Model(&inbound).Updates(map[string]interface{}{"status": "completed"}).Error
}

func integrationFromInput(c *gin.Context, input integrationInput, current MessageChannelIntegration) (MessageChannelIntegration, bool) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Message channel name is required"})
		return MessageChannelIntegration{}, false
	}
	if len([]rune(name)) > 120 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Message channel name is too long"})
		return MessageChannelIntegration{}, false
	}
	provider := resolveProvider(input.Provider)
	if provider == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Unsupported message channel provider"})
		return MessageChannelIntegration{}, false
	}
	if descriptor, ok := pluginChannelDescriptorFor(provider); ok && !communityservice.PluginEnabledForUser(descriptor.Plugin, current.UserID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "The plugin for this message channel is disabled"})
		return MessageChannelIntegration{}, false
	}
	advancedOptions := normalizeAdvancedOptions(input.AdvancedOptions)
	providerConfig := providerConfigFromAdvancedOptions(advancedOptions)
	botToken := current.BotToken
	if input.BotToken != nil {
		botToken = strings.TrimSpace(*input.BotToken)
	}
	if provider == providerQQ {
		mode := normalizeQQConnectionMode(configString(providerConfig, "connection_mode", "mode"))
		providerConfig["connection_mode"] = mode
		advancedOptions.CustomProviderConfigJSON = encodeProviderConfig(providerConfig)
	} else if provider == providerTencentChannel {
		if strings.TrimSpace(input.DefaultDeviceID) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Tencent channel gateway requires a connector device"})
			return MessageChannelIntegration{}, false
		}
		providerConfig = normalizeTencentChannelConfig(providerConfig)
		advancedOptions.CustomProviderConfigJSON = encodeProviderConfig(providerConfig)
	} else if !isPluginChannelProvider(provider) && provider != providerWeixin && botToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Bot token is required"})
		return MessageChannelIntegration{}, false
	}
	if len(botToken) > 4096 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Bot token is too long"})
		return MessageChannelIntegration{}, false
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	} else if current.ID != 0 {
		enabled = current.Enabled
	}
	contextCount := defaultContextMessageCount
	if current.DefaultContextMessageCount > 0 {
		contextCount = current.DefaultContextMessageCount
	}
	if input.DefaultContextMessageCount != nil {
		contextCount = *input.DefaultContextMessageCount
	}
	if contextCount < 0 || contextCount > maxContextMessageCount {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Context message count must be between 0 and 100"})
		return MessageChannelIntegration{}, false
	}
	replyMode := normalizeMode(input.ReplyMode, "mention")
	triggerMode := normalizeMode(input.TriggerMode, "mention")
	skillIDs, err := json.Marshal(uniqueStringList(input.DefaultSkillIDs, 20, 120))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to encode skills"})
		return MessageChannelIntegration{}, false
	}
	groupConfigs := normalizeGroupConfigs(input.GroupConfigs)
	groupConfigData, err := json.Marshal(groupConfigs)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to encode group configs"})
		return MessageChannelIntegration{}, false
	}
	advancedOptionsData, err := json.Marshal(advancedOptions)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to encode advanced options"})
		return MessageChannelIntegration{}, false
	}
	current.Name = name
	current.Provider = provider
	current.BotToken = botToken
	current.Enabled = enabled
	current.DefaultDeviceID = trimMax(input.DefaultDeviceID, 80)
	current.DefaultWorkspacePath = trimMax(input.DefaultWorkspacePath, 1000)
	current.DefaultWorkspaceUnrestricted = input.DefaultWorkspaceUnrestricted
	current.DefaultConnectorAutoApprove = input.DefaultConnectorAutoApprove
	current.DefaultConnectorCommandPrefixes = encodeStringList(input.DefaultConnectorCommandPrefixes, 20, 200)
	current.DefaultUserChannelID = input.DefaultUserChannelID
	current.DefaultModel = trimMax(input.DefaultModel, 120)
	current.DefaultAgentID = input.DefaultAgentID
	current.DefaultAgentKey = normalizeMessageChannelAgentKey(input.DefaultAgentKey)
	current.DefaultAgentGroupID = trimMax(input.DefaultAgentGroupID, 80)
	current.DefaultSkillIDs = string(skillIDs)
	current.DefaultContextMessageCount = contextCount
	current.ReplyMode = replyMode
	current.TriggerMode = triggerMode
	current.SystemPrompt = trimMax(input.SystemPrompt, 20000)
	current.GroupConfigs = string(groupConfigData)
	current.AdvancedOptions = string(advancedOptionsData)
	return current, true
}

func integrationToResponse(integration MessageChannelIntegration) integrationResponse {
	botTokenConfigured := strings.TrimSpace(integration.BotToken) != ""
	if integration.Provider == providerQQ {
		botTokenConfigured = qqCredentialsConfigured(providerConfig(integration), integration.BotToken)
	}
	return integrationResponse{
		ID:                              integration.ID,
		Name:                            integration.Name,
		Provider:                        integration.Provider,
		Enabled:                         integration.Enabled,
		BotTokenConfigured:              botTokenConfigured,
		BotTokenPreview:                 tokenPreview(integration.BotToken),
		WebhookSecret:                   integration.WebhookSecret,
		WebhookPath:                     messageChannelWebhookPath(integration),
		DefaultDeviceID:                 integration.DefaultDeviceID,
		DefaultWorkspacePath:            integration.DefaultWorkspacePath,
		DefaultWorkspaceUnrestricted:    integration.DefaultWorkspaceUnrestricted,
		DefaultConnectorAutoApprove:     integration.DefaultConnectorAutoApprove,
		DefaultConnectorCommandPrefixes: decodeStringList(integration.DefaultConnectorCommandPrefixes, 20),
		DefaultUserChannelID:            integration.DefaultUserChannelID,
		DefaultModel:                    integration.DefaultModel,
		DefaultAgentID:                  integration.DefaultAgentID,
		DefaultAgentKey:                 responseAgentKey(integration.DefaultAgentKey, integration.DefaultAgentID),
		DefaultAgentGroupID:             integration.DefaultAgentGroupID,
		DefaultSkillIDs:                 decodeSkillIDList(integration.DefaultSkillIDs),
		DefaultContextMessageCount:      integration.DefaultContextMessageCount,
		ReplyMode:                       integration.ReplyMode,
		TriggerMode:                     integration.TriggerMode,
		SystemPrompt:                    integration.SystemPrompt,
		GroupConfigs:                    decodeGroupConfigs(integration.GroupConfigs),
		AdvancedOptions:                 decodeAdvancedOptions(integration.AdvancedOptions),
		LastEventAt:                     integration.LastEventAt,
		CreatedAt:                       integration.CreatedAt,
		UpdatedAt:                       integration.UpdatedAt,
	}
}

type resolvedMessageConfig struct {
	Enabled                  bool
	DeviceID                 string
	WorkspacePath            string
	WorkspaceUnrestricted    bool
	ConnectorAutoApprove     bool
	ConnectorCommandPrefixes []string
	UserChannelID            *uint
	Model                    string
	AgentID                  *uint
	AgentKey                 string
	AgentGroupID             string
	SkillIDs                 []string
	ContextMessageCount      int
	ReplyMode                string
	TriggerMode              string
	SystemPrompt             string
	AdvancedOptions          AdvancedOptions
}

func (config resolvedMessageConfig) userChannelID() uint {
	if config.UserChannelID == nil {
		return 0
	}
	return *config.UserChannelID
}

func (config resolvedMessageConfig) systemPrompt(fallback string) string {
	if strings.TrimSpace(config.SystemPrompt) != "" {
		return config.SystemPrompt
	}
	return fallback
}

func buildSystemPrompt(userID uint, config resolvedMessageConfig, fallback string) string {
	parts := []string{}
	agentKey := strings.TrimSpace(config.AgentKey)
	if agentKey != "" || config.AgentID != nil {
		var agent struct {
			Name   string
			Prompt string
		}
		query := model.DB.Table("advanced_chat_agents").Select("name, prompt").Where("user_id = ?", userID)
		if agentKey == "default" {
			query = query.Where("stable_id = ?", agentKey)
		} else if agentKey != "" {
			if id, err := strconv.ParseUint(agentKey, 10, 64); err == nil && id > 0 {
				query = query.Where("id = ?", uint(id))
			} else {
				query = query.Where("stable_id = ?", agentKey)
			}
		} else {
			query = query.Where("id = ?", *config.AgentID)
		}
		if err := query.First(&agent).Error; err == nil && strings.TrimSpace(agent.Prompt) != "" {
			parts = append(parts, strings.TrimSpace(agent.Prompt))
		}
	}
	base := strings.TrimSpace(config.systemPrompt(fallback))
	if base != "" {
		parts = append(parts, base)
	}
	for _, skillID := range config.SkillIDs {
		var skill struct {
			ID          string
			Name        string
			Description string
			Source      string
		}
		if err := model.DB.Table("advanced_chat_packaged_skills").
			Select("id, name, description, source").
			Where("id = ? AND user_id = ? AND enabled = ?", skillID, userID, true).
			First(&skill).Error; err != nil {
			continue
		}
		label := strings.TrimSpace(skill.Name)
		if label == "" {
			label = strings.TrimSpace(skill.ID)
		}
		description := strings.TrimSpace(skill.Description)
		if description == "" {
			description = "No description."
		}
		parts = append(parts, "Available Skill: "+label+"\nID: "+strings.TrimSpace(skill.ID)+"\nDescription: "+description)
	}
	return strings.Join(parts, "\n\n")
}

func resolveConfigForMessage(integration MessageChannelIntegration, externalChatID string) resolvedMessageConfig {
	resolved := resolvedMessageConfig{
		Enabled:                  integration.Enabled,
		DeviceID:                 integration.DefaultDeviceID,
		WorkspacePath:            integration.DefaultWorkspacePath,
		WorkspaceUnrestricted:    integration.DefaultWorkspaceUnrestricted,
		ConnectorAutoApprove:     integration.DefaultConnectorAutoApprove,
		ConnectorCommandPrefixes: decodeStringList(integration.DefaultConnectorCommandPrefixes, 20),
		UserChannelID:            integration.DefaultUserChannelID,
		Model:                    integration.DefaultModel,
		AgentID:                  integration.DefaultAgentID,
		AgentKey:                 integration.DefaultAgentKey,
		AgentGroupID:             integration.DefaultAgentGroupID,
		SkillIDs:                 decodeSkillIDList(integration.DefaultSkillIDs),
		ContextMessageCount:      integration.DefaultContextMessageCount,
		ReplyMode:                integration.ReplyMode,
		TriggerMode:              integration.TriggerMode,
		SystemPrompt:             integration.SystemPrompt,
		AdvancedOptions:          decodeAdvancedOptions(integration.AdvancedOptions),
	}
	for _, group := range decodeGroupConfigs(integration.GroupConfigs) {
		if group.ExternalID != externalChatID {
			continue
		}
		resolved.Enabled = group.Enabled
		if group.DeviceID != "" {
			resolved.DeviceID = group.DeviceID
		}
		if group.WorkspacePath != "" {
			resolved.WorkspacePath = group.WorkspacePath
		}
		if group.WorkspaceUnrestricted {
			resolved.WorkspaceUnrestricted = true
			resolved.WorkspacePath = ""
		}
		if group.ConnectorAutoApprove {
			resolved.ConnectorAutoApprove = true
		}
		if len(group.ConnectorCommandPrefixes) > 0 {
			resolved.ConnectorCommandPrefixes = group.ConnectorCommandPrefixes
		}
		if group.UserChannelID != nil {
			resolved.UserChannelID = group.UserChannelID
		}
		if group.Model != "" {
			resolved.Model = group.Model
		}
		if group.AgentID != nil {
			resolved.AgentID = group.AgentID
		}
		if strings.TrimSpace(group.AgentKey) != "" {
			resolved.AgentKey = group.AgentKey
		}
		if strings.TrimSpace(group.AgentGroupID) != "" {
			resolved.AgentGroupID = group.AgentGroupID
		}
		if len(group.SkillIDs) > 0 {
			resolved.SkillIDs = group.SkillIDs
		}
		if group.ContextMessageCount >= 0 {
			resolved.ContextMessageCount = group.ContextMessageCount
		}
		if group.ReplyMode != "" {
			resolved.ReplyMode = group.ReplyMode
		}
		if group.TriggerMode != "" {
			resolved.TriggerMode = group.TriggerMode
		}
		if group.SystemPromptOverride != "" {
			resolved.SystemPrompt = group.SystemPromptOverride
		}
		break
	}
	if resolved.ContextMessageCount < 0 {
		resolved.ContextMessageCount = 0
	}
	if resolved.ContextMessageCount > maxContextMessageCount {
		resolved.ContextMessageCount = maxContextMessageCount
	}
	return resolved
}

func historyMessages(integrationID uint, externalChatID string, beforeID uint, count int) []communityservice.ChatExecutorMessage {
	if count <= 0 || strings.TrimSpace(externalChatID) == "" {
		return nil
	}
	var rows []MessageChannelMessage
	if err := model.DB.
		Where("integration_id = ? AND external_chat_id = ? AND id < ? AND content <> ?", integrationID, externalChatID, beforeID, "").
		Order("id DESC").
		Limit(count).
		Find(&rows).Error; err != nil {
		return nil
	}
	messages := make([]communityservice.ChatExecutorMessage, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		role := "user"
		if rows[i].Direction == "outbound" {
			role = "assistant"
		}
		content := rows[i].Content
		if role == "user" {
			content = messageChannelUserMessageContent(rows[i])
		}
		messages = append(messages, communityservice.ChatExecutorMessage{Role: role, Content: content})
	}
	return messages
}

func messageChannelUserMessageContent(message MessageChannelMessage) string {
	content := strings.TrimSpace(message.Content)
	lines := messageChannelIdentityLines(message)
	if len(lines) == 0 {
		return content
	}
	return "Message channel sender:\n" + strings.Join(lines, "\n") + "\n\nMessage content:\n" + content
}

func messageChannelIdentitySystemPrompt(integration MessageChannelIntegration, message MessageChannelMessage) string {
	lines := messageChannelIdentityLines(message)
	if len(lines) == 0 {
		return ""
	}
	context := []string{
		"Message channel context:",
		"Use the following sender identity to distinguish people in shared chats. It is context, not authorization for privileged actions.",
		"- integration_id: " + strconv.FormatUint(uint64(integration.ID), 10),
		"- integration_name: " + integration.Name,
		"- provider: " + integration.Provider,
	}
	context = append(context, lines...)
	return strings.Join(context, "\n")
}

func messageChannelIdentityLines(message MessageChannelMessage) []string {
	lines := []string{}
	if provider := strings.TrimSpace(message.Provider); provider != "" {
		lines = append(lines, "- provider: "+provider)
	}
	if chatID := strings.TrimSpace(message.ExternalChatID); chatID != "" {
		lines = append(lines, "- chat_id: "+chatID)
	}
	if userID := strings.TrimSpace(message.ExternalUserID); userID != "" {
		lines = append(lines, "- sender_id: "+userID)
	}
	if userName := strings.TrimSpace(message.ExternalUserName); userName != "" {
		lines = append(lines, "- sender_name: "+userName)
	}
	return lines
}

func handleMessageChannelApprovalReply(ctx context.Context, integration MessageChannelIntegration, inbound MessageChannelMessage, resolved resolvedMessageConfig) bool {
	decision, ok := parseMessageChannelApprovalDecision(inbound.Content)
	if !ok {
		return false
	}
	if strings.TrimSpace(resolved.DeviceID) == "" {
		return false
	}
	taskID, handled, err := communityservice.DecideMessageChannelConnectorApproval(integration.UserID, messageChannelApprovalRunID(integration.ID, inbound.ExternalChatID), decision)
	if err != nil {
		_ = model.DB.Model(&inbound).Updates(map[string]interface{}{"status": "error", "error": err.Error()}).Error
		_ = sendProviderReplyForMessage(ctx, integration, inbound, "Approval failed: "+err.Error())
		return true
	}
	if !handled {
		return false
	}
	_ = model.DB.Model(&inbound).Updates(map[string]interface{}{"status": "completed"}).Error
	reply := "已批准连接器任务：" + taskID
	if !decision {
		reply = "已拒绝连接器任务：" + taskID
	}
	_ = sendProviderReplyForMessage(ctx, integration, inbound, reply)
	return true
}

func parseMessageChannelApprovalDecision(content string) (bool, bool) {
	text := normalizeMessageChannelApprovalToken(content)
	switch text {
	case "yes", "y", "ok", "approve", "approved", "allow", "allowed", "同意", "允许", "批准", "确认", "是", "好", "可以":
		return true, true
	case "no", "n", "deny", "denied", "reject", "rejected", "disallow", "拒绝", "不", "否", "不行", "取消":
		return false, true
	default:
		return false, false
	}
}

func normalizeMessageChannelApprovalToken(content string) string {
	text := strings.ToLower(strings.TrimSpace(content))
	text = strings.NewReplacer(
		"“", "\"",
		"”", "\"",
		"‘", "'",
		"’", "'",
		"：", ":",
		"，", ",",
		"。", ".",
		"！", "!",
		"？", "?",
	).Replace(text)
	text = strings.Trim(text, " \t\r\n\"'`.,!?:;，。！？；：、()[]{}<>")
	fields := strings.Fields(text)
	candidates := []string{text}
	if len(fields) > 0 {
		candidates = append(candidates, fields[len(fields)-1])
	}
	for i := len(candidates) - 1; i >= 0; i-- {
		token := strings.Trim(candidates[i], " \t\r\n\"'`.,!?:;，。！？；：、()[]{}<>")
		token = strings.TrimPrefix(token, "@")
		token = strings.Trim(token, " \t\r\n\"'`.,!?:;，。！？；：、()[]{}<>")
		if token != "" {
			return token
		}
	}
	return ""
}

func messageChannelApprovalRunID(integrationID uint, externalChatID string) string {
	chatID := strings.TrimSpace(externalChatID)
	if chatID == "" {
		chatID = "unknown"
	}
	return "msgch-" + strconv.FormatUint(uint64(integrationID), 10) + "-" + chatID
}

func formatMessageChannelApprovalPrompt(approval communityservice.MessageChannelConnectorApproval) string {
	workspace := strings.TrimSpace(approval.WorkspacePath)
	if approval.Unrestricted || workspace == "" {
		workspace = "不限制"
	}
	return strings.TrimSpace("连接器操作需要审批。\n" +
		"任务：" + approval.TaskID + "\n" +
		"设备：" + approval.DeviceName + "\n" +
		"操作：" + approval.Action + "\n" +
		"工作目录：" + workspace + "\n" +
		"参数：\n" + communityservice.MessageChannelApprovalArgumentsPreview(approval.Arguments) + "\n\n" +
		"回复 yes 或 同意以批准；回复 no 或 拒绝以拒绝。")
}

func normalizeGroupConfigs(input []GroupConfig) []GroupConfig {
	if len(input) > 100 {
		input = input[:100]
	}
	configs := make([]GroupConfig, 0, len(input))
	seen := map[string]struct{}{}
	for _, item := range input {
		externalID := trimMax(item.ExternalID, 160)
		if externalID == "" {
			continue
		}
		if _, exists := seen[externalID]; exists {
			continue
		}
		seen[externalID] = struct{}{}
		contextCount := item.ContextMessageCount
		if contextCount < 0 {
			contextCount = 0
		}
		if contextCount > maxContextMessageCount {
			contextCount = maxContextMessageCount
		}
		configs = append(configs, GroupConfig{
			ExternalID:               externalID,
			Name:                     trimMax(item.Name, 120),
			Enabled:                  item.Enabled,
			DeviceID:                 trimMax(item.DeviceID, 80),
			WorkspacePath:            trimMax(item.WorkspacePath, 1000),
			WorkspaceUnrestricted:    item.WorkspaceUnrestricted,
			ConnectorAutoApprove:     item.ConnectorAutoApprove,
			ConnectorCommandPrefixes: decodeStringList(encodeStringList(item.ConnectorCommandPrefixes, 20, 200), 20),
			UserChannelID:            item.UserChannelID,
			Model:                    trimMax(item.Model, 120),
			AgentID:                  item.AgentID,
			AgentKey:                 normalizeMessageChannelAgentKey(item.AgentKey),
			AgentGroupID:             trimMax(item.AgentGroupID, 80),
			SkillIDs:                 uniqueStringList(item.SkillIDs, 20, 120),
			ContextMessageCount:      contextCount,
			ReplyMode:                normalizeMode(item.ReplyMode, ""),
			TriggerMode:              normalizeMode(item.TriggerMode, ""),
			SystemPromptOverride:     trimMax(item.SystemPromptOverride, 20000),
		})
	}
	return configs
}

func normalizeMessageChannelAgentKey(value string) string {
	value = trimMax(value, 80)
	if strings.EqualFold(value, messageChannelDefaultAgentKey) {
		return messageChannelDefaultAgentKey
	}
	return value
}

func responseAgentKey(value string, fallbackID *uint) string {
	if key := normalizeMessageChannelAgentKey(value); key != "" {
		return key
	}
	if fallbackID != nil && *fallbackID > 0 {
		return strconv.FormatUint(uint64(*fallbackID), 10)
	}
	return ""
}

func normalizeAdvancedOptions(input AdvancedOptions) AdvancedOptions {
	var temperature *float64
	if input.Temperature != nil {
		value := *input.Temperature
		if value < 0 {
			value = 0
		}
		if value > 2 {
			value = 2
		}
		temperature = &value
	}
	maxTokens := input.MaxTokens
	if maxTokens < 0 {
		maxTokens = 0
	}
	if maxTokens > 200000 {
		maxTokens = 200000
	}
	return AdvancedOptions{
		Temperature:              temperature,
		MaxTokens:                maxTokens,
		ReplyPrefix:              trimMax(input.ReplyPrefix, 1000),
		ReplySuffix:              trimMax(input.ReplySuffix, 1000),
		Language:                 trimMax(input.Language, 80),
		Timezone:                 trimMax(input.Timezone, 80),
		MentionPolicy:            trimMax(input.MentionPolicy, 80),
		AttachmentMode:           trimMax(input.AttachmentMode, 80),
		ThreadMode:               trimMax(input.ThreadMode, 80),
		DeduplicationWindowSecs:  clampInt(input.DeduplicationWindowSecs, 0, 86400),
		ResponseTimeoutSecs:      clampInt(input.ResponseTimeoutSecs, 0, 600),
		ErrorFallback:            trimMax(input.ErrorFallback, 2000),
		CustomProviderConfigJSON: trimMax(input.CustomProviderConfigJSON, 20000),
	}
}

func providerConfigFromAdvancedOptions(options AdvancedOptions) map[string]interface{} {
	raw := strings.TrimSpace(options.CustomProviderConfigJSON)
	if raw == "" {
		return map[string]interface{}{}
	}
	var config map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return map[string]interface{}{}
	}
	return config
}

func encodeProviderConfig(config map[string]interface{}) string {
	cleaned := map[string]interface{}{}
	for key, value := range config {
		name := strings.TrimSpace(key)
		if name == "" {
			continue
		}
		if text, ok := value.(string); ok {
			text = strings.TrimSpace(text)
			if text == "" {
				continue
			}
			cleaned[name] = text
			continue
		}
		if value != nil {
			cleaned[name] = value
		}
	}
	if len(cleaned) == 0 {
		return ""
	}
	data, err := json.Marshal(cleaned)
	if err != nil {
		return ""
	}
	return string(data)
}

func messageChannelWebhookPath(integration MessageChannelIntegration) string {
	if integration.Provider == providerQQ {
		config := providerConfig(integration)
		if normalizeQQConnectionMode(configString(config, "connection_mode", "mode")) == "websocket" {
			return ""
		}
	}
	if integration.Provider == providerWeixin {
		return ""
	}
	if integration.Provider == providerTencentChannel {
		return ""
	}
	return "/api/message-channels/" + integration.Provider + "/" + strconv.FormatUint(uint64(integration.ID), 10) + "/webhook?secret=" + integration.WebhookSecret
}

func normalizeQQConnectionMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "websocket", "ws":
		return "websocket"
	default:
		return "webhook"
	}
}

func qqCredentialsConfigured(config map[string]interface{}, botToken string) bool {
	if configString(config, "authorization", "auth_header") != "" {
		return true
	}
	if strings.TrimSpace(botToken) != "" {
		return true
	}
	return configString(config, "bot_id", "robot_id", "app_id", "appid") != "" &&
		configString(config, "bot_secret", "secret", "client_secret") != ""
}

func decodeAdvancedOptions(raw string) AdvancedOptions {
	var options AdvancedOptions
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &options) != nil {
		return AdvancedOptions{}
	}
	return normalizeAdvancedOptions(options)
}

func requireEnabled(c *gin.Context) bool {
	if Enabled() {
		return true
	}
	c.JSON(http.StatusForbidden, gin.H{"error": "Message channel is disabled"})
	return false
}

func clampInt(value int, min int, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func currentUser(c *gin.Context) (*model.User, bool) {
	value, exists := c.Get("user")
	if !exists {
		return nil, false
	}
	user, ok := value.(*model.User)
	return user, ok && user != nil && user.ID != 0
}

func normalizeProvider(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case providerTelegram:
		return providerTelegram
	case providerDiscord:
		return providerDiscord
	case providerQQ, "qq-official", "qq_official":
		return providerQQ
	case providerOneBot, "one-bot", "one_bot":
		return providerOneBot
	case providerWeixin, "wechat", "we-chat", "we_chat":
		return providerWeixin
	case providerTencentChannel, "tencent-channel", "qq-channel", "qq_channel":
		return providerTencentChannel
	default:
		return ""
	}
}

func resolveProvider(value string) string {
	if provider := normalizeProvider(value); provider != "" {
		return provider
	}
	if descriptor, ok := pluginChannelDescriptorFor(strings.TrimSpace(value)); ok {
		return descriptor.Provider
	}
	return ""
}

func normalizeMode(value, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "always", "mention", "direct", "command", "manual":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return fallback
	}
}

func trimMax(value string, max int) string {
	value = strings.TrimSpace(value)
	if len([]rune(value)) <= max {
		return value
	}
	return string([]rune(value)[:max])
}

func encodeStringList(values []string, max int, maxRunes int) string {
	encoded, err := json.Marshal(uniqueStringList(values, max, maxRunes))
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

func decodeStringList(raw string, max int) []string {
	var values []string
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &values) != nil {
		return []string{}
	}
	return uniqueStringList(values, max, 200)
}

func decodeSkillIDList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{}
	}
	var stringsValue []string
	if err := json.Unmarshal([]byte(raw), &stringsValue); err == nil {
		return uniqueStringList(stringsValue, 20, 120)
	}
	var numbersValue []uint
	if err := json.Unmarshal([]byte(raw), &numbersValue); err != nil {
		return []string{}
	}
	values := make([]string, 0, len(numbersValue))
	for _, value := range numbersValue {
		if value > 0 {
			values = append(values, strconv.FormatUint(uint64(value), 10))
		}
	}
	return uniqueStringList(values, 20, 120)
}

func uniqueStringList(values []string, max int, maxRunes int) []string {
	if max <= 0 {
		max = len(values)
	}
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, raw := range values {
		value := trimMax(raw, maxRunes)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
		if len(result) >= max {
			break
		}
	}
	return result
}

func decodeGroupConfigs(raw string) []GroupConfig {
	var values []GroupConfig
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &values) != nil {
		return []GroupConfig{}
	}
	return normalizeGroupConfigs(values)
}

func tokenPreview(token string) string {
	token = strings.TrimSpace(token)
	if len(token) <= 8 {
		if token == "" {
			return ""
		}
		return "configured"
	}
	return token[:4] + "..." + token[len(token)-4:]
}

func newSecret() (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func settingBool(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(model.GetSystemSetting(key, strconv.FormatBool(fallback))))
	switch value {
	case "1", "true", "yes", "on", "enabled":
		return true
	case "0", "false", "no", "off", "disabled":
		return false
	default:
		return fallback
	}
}

func isUniqueError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint") || strings.Contains(message, "duplicate")
}
