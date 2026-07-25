package channel

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
	qrcode "github.com/skip2/go-qrcode"
)

const (
	weixinDefaultBaseURL          = "https://ilinkai.weixin.qq.com"
	weixinDefaultCDNBaseURL       = "https://novac2c.cdn.weixin.qq.com/c2c"
	weixinDefaultBotType          = "3"
	weixinDefaultLongPollTimeout  = 35 * time.Second
	weixinDefaultAPITimeout       = 15 * time.Second
	weixinMessageTypeUser         = 1
	weixinMessageTypeBot          = 2
	weixinMessageStateFinish      = 2
	weixinMessageItemTypeText     = 1
	weixinStaleTokenErrCode       = -14
	weixinSessionPauseDuration    = time.Hour
	weixinLoginSessionTTL         = 5 * time.Minute
	weixinQRCodeLongPollTimeout   = 35 * time.Second
	weixinQRCodeDataURLPixelWidth = 256
)

type weixinProvider struct{}

type weixinLoginSession struct {
	SessionKey string
	QRCode     string
	QRCodeURL  string
	StartedAt  time.Time
	BaseURL    string
}

type weixinQRCodeResponse struct {
	QRCode         string `json:"qrcode"`
	QRCodeContent  string `json:"qrcode_img_content"`
	QRCodeURLAlias string `json:"qrcode_url"`
}

type weixinStatusResponse struct {
	Status       string `json:"status"`
	BotToken     string `json:"bot_token"`
	BotID        string `json:"ilink_bot_id"`
	BaseURL      string `json:"baseurl"`
	UserID       string `json:"ilink_user_id"`
	RedirectHost string `json:"redirect_host"`
	ErrMsg       string `json:"errmsg"`
}

type weixinBaseInfo struct {
	ChannelVersion string `json:"channel_version,omitempty"`
	BotAgent       string `json:"bot_agent,omitempty"`
}

type weixinMessageItem struct {
	Type     int               `json:"type,omitempty"`
	TextItem *weixinTextItem   `json:"text_item,omitempty"`
	RefMsg   *weixinRefMessage `json:"ref_msg,omitempty"`
	Voice    *weixinVoiceItem  `json:"voice_item,omitempty"`
	Image    map[string]any    `json:"image_item,omitempty"`
	File     map[string]any    `json:"file_item,omitempty"`
	Video    map[string]any    `json:"video_item,omitempty"`
	Raw      map[string]any    `json:"-"`
}

type weixinTextItem struct {
	Text string `json:"text,omitempty"`
}

type weixinVoiceItem struct {
	Text string `json:"text,omitempty"`
}

type weixinRefMessage struct {
	Title       string             `json:"title,omitempty"`
	MessageItem *weixinMessageItem `json:"message_item,omitempty"`
}

type weixinMessage struct {
	Seq          int64               `json:"seq,omitempty"`
	MessageID    any                 `json:"message_id,omitempty"`
	FromUserID   string              `json:"from_user_id,omitempty"`
	ToUserID     string              `json:"to_user_id,omitempty"`
	ClientID     string              `json:"client_id,omitempty"`
	CreateTimeMS int64               `json:"create_time_ms,omitempty"`
	SessionID    string              `json:"session_id,omitempty"`
	GroupID      string              `json:"group_id,omitempty"`
	MessageType  int                 `json:"message_type,omitempty"`
	MessageState int                 `json:"message_state,omitempty"`
	ItemList     []weixinMessageItem `json:"item_list,omitempty"`
	ContextToken string              `json:"context_token,omitempty"`
	RunID        string              `json:"run_id,omitempty"`
}

type weixinGetUpdatesResponse struct {
	Ret                  int             `json:"ret,omitempty"`
	ErrCode              int             `json:"errcode,omitempty"`
	ErrMsg               string          `json:"errmsg,omitempty"`
	Messages             []weixinMessage `json:"msgs,omitempty"`
	GetUpdatesBuf        string          `json:"get_updates_buf,omitempty"`
	LongPollingTimeoutMS int             `json:"longpolling_timeout_ms,omitempty"`
}

var (
	weixinHTTPClient    = &http.Client{}
	weixinLoginMu       sync.Mutex
	weixinLoginSessions = map[string]*weixinLoginSession{}
	weixinPauseMu       sync.Mutex
	weixinPauseUntil    = map[uint]time.Time{}
)

func (weixinProvider) Name() string {
	return providerWeixin
}

func (weixinProvider) ExtractWebhookSummary(body []byte) webhookSummary {
	var msg weixinMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return webhookSummary{}
	}
	from := strings.TrimSpace(msg.FromUserID)
	content := strings.TrimSpace(weixinMessageText(msg.ItemList))
	return webhookSummary{
		ExternalChatID:    from,
		ExternalUserID:    from,
		ExternalUserName:  from,
		ExternalMessageID: stringID(msg.MessageID),
		Content:           content,
	}
}

func (weixinProvider) SendReply(ctx context.Context, integration MessageChannelIntegration, inbound MessageChannelMessage, content string) error {
	var current MessageChannelIntegration
	if err := model.DB.First(&current, integration.ID).Error; err == nil {
		integration = current
	}
	config := providerConfig(integration)
	baseURL := weixinBaseURL(config)
	token := strings.TrimSpace(integration.BotToken)
	if token == "" {
		return errors.New("weixin bot token is not configured")
	}
	to := strings.TrimSpace(inbound.ExternalChatID)
	if to == "" {
		return errors.New("weixin recipient is empty")
	}
	itemList := []map[string]any{}
	if strings.TrimSpace(content) != "" {
		itemList = append(itemList, map[string]any{
			"type": weixinMessageItemTypeText,
			"text_item": map[string]any{
				"text": content,
			},
		})
	}
	payload := map[string]any{
		"msg": map[string]any{
			"from_user_id":  "",
			"to_user_id":    to,
			"client_id":     weixinGenerateID("windypear-weixin"),
			"message_type":  weixinMessageTypeBot,
			"message_state": weixinMessageStateFinish,
			"item_list":     itemList,
			"context_token": weixinContextToken(config, to),
		},
		"base_info": weixinBuildBaseInfo(),
	}
	return weixinPostJSON(ctx, baseURL, "ilink/bot/sendmessage", token, payload, weixinDefaultAPITimeout, nil)
}

func (api *API) startWeixinLogin(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if !requireEnabled(c) {
		return
	}
	integration, ok := loadUserWeixinIntegration(c, user.ID)
	if !ok {
		return
	}
	config := providerConfig(integration)
	baseURL := weixinBaseURL(config)
	localTokens := []string{}
	if token := strings.TrimSpace(integration.BotToken); token != "" {
		localTokens = append(localTokens, token)
	}
	body := map[string]any{"local_token_list": localTokens}
	var qrResp weixinQRCodeResponse
	if err := weixinPostJSON(c.Request.Context(), weixinDefaultBaseURL, "ilink/bot/get_bot_qrcode?bot_type="+weixinDefaultBotType, "", body, weixinDefaultAPITimeout, &qrResp); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	qrCode := strings.TrimSpace(qrResp.QRCode)
	qrURL := strings.TrimSpace(qrResp.QRCodeContent)
	if qrURL == "" {
		qrURL = strings.TrimSpace(qrResp.QRCodeURLAlias)
	}
	if qrCode == "" || qrURL == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Weixin QR response is incomplete"})
		return
	}
	sessionKey := weixinGenerateID("weixin-login")
	weixinLoginMu.Lock()
	weixinPurgeExpiredLoginSessionsLocked()
	weixinLoginSessions[sessionKey] = &weixinLoginSession{
		SessionKey: sessionKey,
		QRCode:     qrCode,
		QRCodeURL:  qrURL,
		StartedAt:  time.Now(),
		BaseURL:    baseURL,
	}
	weixinLoginMu.Unlock()
	qrDataURL, _ := weixinQRCodeDataURL(qrURL)
	c.JSON(http.StatusOK, gin.H{
		"session_key": sessionKey,
		"qrcode_url":  qrURL,
		"qr_data_url": qrDataURL,
		"message":     "用手机微信扫描二维码，以继续连接。",
	})
}

func (api *API) waitWeixinLogin(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if !requireEnabled(c) {
		return
	}
	integration, ok := loadUserWeixinIntegration(c, user.ID)
	if !ok {
		return
	}
	var input struct {
		SessionKey string `json:"session_key"`
		TimeoutMS  int    `json:"timeout_ms"`
	}
	_ = c.ShouldBindJSON(&input)
	sessionKey := strings.TrimSpace(input.SessionKey)
	if sessionKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_key is required"})
		return
	}
	weixinLoginMu.Lock()
	session := weixinLoginSessions[sessionKey]
	if session == nil || time.Since(session.StartedAt) > weixinLoginSessionTTL {
		delete(weixinLoginSessions, sessionKey)
		weixinLoginMu.Unlock()
		c.JSON(http.StatusGone, gin.H{"connected": false, "status": "expired", "message": "二维码已过期，请重新生成。"})
		return
	}
	baseURL := session.BaseURL
	qrCode := session.QRCode
	weixinLoginMu.Unlock()

	timeout := weixinQRCodeLongPollTimeout
	if input.TimeoutMS > 0 && input.TimeoutMS < int((60*time.Second)/time.Millisecond) {
		timeout = time.Duration(input.TimeoutMS) * time.Millisecond
	}
	var statusResp weixinStatusResponse
	endpoint := "ilink/bot/get_qrcode_status?qrcode=" + url.QueryEscape(qrCode)
	if err := weixinGetJSON(c.Request.Context(), baseURL, endpoint, timeout, &statusResp); err != nil {
		c.JSON(http.StatusOK, gin.H{"connected": false, "status": "wait", "message": err.Error()})
		return
	}
	if statusResp.Status == "scaned_but_redirect" && strings.TrimSpace(statusResp.RedirectHost) != "" {
		nextBaseURL := "https://" + strings.TrimSpace(statusResp.RedirectHost)
		weixinLoginMu.Lock()
		if current := weixinLoginSessions[sessionKey]; current != nil {
			current.BaseURL = nextBaseURL
		}
		weixinLoginMu.Unlock()
		c.JSON(http.StatusOK, gin.H{"connected": false, "status": statusResp.Status, "message": "已扫码，正在切换微信服务节点。"})
		return
	}
	if statusResp.Status != "confirmed" {
		c.JSON(http.StatusOK, gin.H{
			"connected": false,
			"status":    statusResp.Status,
			"message":   weixinLoginStatusMessage(statusResp),
		})
		return
	}
	if strings.TrimSpace(statusResp.BotToken) == "" || strings.TrimSpace(statusResp.BotID) == "" {
		c.JSON(http.StatusBadGateway, gin.H{"connected": false, "status": statusResp.Status, "error": "Weixin login confirmed without token or bot id"})
		return
	}
	if err := saveWeixinLoginResult(integration, statusResp); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"connected": false, "status": statusResp.Status, "error": err.Error()})
		return
	}
	weixinLoginMu.Lock()
	delete(weixinLoginSessions, sessionKey)
	weixinLoginMu.Unlock()
	syncMessageChannelRuntime()
	c.JSON(http.StatusOK, gin.H{
		"connected":  true,
		"status":     "confirmed",
		"account_id": statusResp.BotID,
		"user_id":    statusResp.UserID,
		"message":    "已连接微信。",
	})
}

func loadUserWeixinIntegration(c *gin.Context, userID uint) (MessageChannelIntegration, bool) {
	var integration MessageChannelIntegration
	if err := model.DB.Where("id = ? AND user_id = ? AND provider = ?", c.Param("id"), userID, providerWeixin).First(&integration).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Weixin message channel not found"})
		return MessageChannelIntegration{}, false
	}
	return integration, true
}

func saveWeixinLoginResult(integration MessageChannelIntegration, result weixinStatusResponse) error {
	options := decodeAdvancedOptions(integration.AdvancedOptions)
	config := providerConfigFromAdvancedOptions(options)
	if baseURL := strings.TrimSpace(result.BaseURL); baseURL != "" {
		config["base_url"] = baseURL
	}
	if _, ok := config["cdn_base_url"]; !ok {
		config["cdn_base_url"] = weixinDefaultCDNBaseURL
	}
	config["account_id"] = strings.TrimSpace(result.BotID)
	if userID := strings.TrimSpace(result.UserID); userID != "" {
		config["user_id"] = userID
	}
	config["login_status"] = "connected"
	options.CustomProviderConfigJSON = encodeProviderConfig(config)
	data, err := json.Marshal(options)
	if err != nil {
		return err
	}
	return model.DB.Model(&MessageChannelIntegration{}).Where("id = ?", integration.ID).Updates(map[string]any{
		"bot_token":        strings.TrimSpace(result.BotToken),
		"advanced_options": string(data),
		"enabled":          true,
	}).Error
}

func updateWeixinProviderConfig(integrationID uint, mutate func(map[string]any)) error {
	var integration MessageChannelIntegration
	if err := model.DB.First(&integration, integrationID).Error; err != nil {
		return err
	}
	options := decodeAdvancedOptions(integration.AdvancedOptions)
	config := providerConfigFromAdvancedOptions(options)
	mutate(config)
	options.CustomProviderConfigJSON = encodeProviderConfig(config)
	data, err := json.Marshal(options)
	if err != nil {
		return err
	}
	return model.DB.Model(&integration).Update("advanced_options", string(data)).Error
}

func weixinMessageText(items []weixinMessageItem) string {
	for _, item := range items {
		if item.Type == weixinMessageItemTypeText && item.TextItem != nil {
			text := strings.TrimSpace(item.TextItem.Text)
			if item.RefMsg == nil {
				return text
			}
			refText := strings.TrimSpace(item.RefMsg.Title)
			if item.RefMsg.MessageItem != nil {
				refText = strings.TrimSpace(strings.Join([]string{refText, weixinMessageText([]weixinMessageItem{*item.RefMsg.MessageItem})}, " "))
			}
			if refText == "" {
				return text
			}
			return "[引用: " + refText + "]\n" + text
		}
		if item.Type == 3 && item.Voice != nil && strings.TrimSpace(item.Voice.Text) != "" {
			return strings.TrimSpace(item.Voice.Text)
		}
	}
	return ""
}

func weixinBaseURL(config map[string]any) string {
	if baseURL := configString(config, "base_url", "api_base_url"); baseURL != "" {
		return baseURL
	}
	return weixinDefaultBaseURL
}

func weixinContextToken(config map[string]any, userID string) string {
	var tokens map[string]string
	if raw := configString(config, "context_tokens_json"); raw != "" {
		_ = json.Unmarshal([]byte(raw), &tokens)
	}
	if tokens != nil {
		if token := strings.TrimSpace(tokens[userID]); token != "" {
			return token
		}
	}
	return configString(config, "context_token")
}

func weixinBuildBaseInfo() weixinBaseInfo {
	return weixinBaseInfo{ChannelVersion: "windypear", BotAgent: "WindyPear/1.0"}
}

func weixinPostJSON(ctx context.Context, baseURL string, endpoint string, token string, payload any, timeout time.Duration, out any) error {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := weixinRequest(ctx, http.MethodPost, baseURL, endpoint, token, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return weixinDo(req, out)
}

func weixinGetJSON(ctx context.Context, baseURL string, endpoint string, timeout time.Duration, out any) error {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	req, err := weixinRequest(ctx, http.MethodGet, baseURL, endpoint, "", nil)
	if err != nil {
		return err
	}
	return weixinDo(req, out)
}

func weixinRequest(ctx context.Context, method string, baseURL string, endpoint string, token string, body io.Reader) (*http.Request, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	url := strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/" + strings.TrimLeft(endpoint, "/")
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("iLink-App-Id", "bot")
	req.Header.Set("iLink-App-ClientVersion", strconv.Itoa(1<<16))
	req.Header.Set("X-WECHAT-UIN", randomWechatUIN())
	if strings.TrimSpace(token) != "" {
		req.Header.Set("AuthorizationType", "ilink_bot_token")
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	}
	return req, nil
}

func weixinDo(req *http.Request, out any) error {
	resp, err := weixinHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return errors.New("weixin api returned status " + strconv.Itoa(resp.StatusCode) + ": " + strings.TrimSpace(string(body)))
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return err
		}
	}
	return nil
}

func randomWechatUIN() string {
	var raw [4]byte
	_, _ = rand.Read(raw[:])
	n := uint32(raw[0])<<24 | uint32(raw[1])<<16 | uint32(raw[2])<<8 | uint32(raw[3])
	return base64.StdEncoding.EncodeToString([]byte(strconv.FormatUint(uint64(n), 10)))
}

func weixinGenerateID(prefix string) string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return prefix + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return prefix + "-" + hex.EncodeToString(raw[:])
}

func weixinQRCodeDataURL(content string) (string, error) {
	png, err := qrcode.Encode(content, qrcode.Medium, weixinQRCodeDataURLPixelWidth)
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}

func weixinPurgeExpiredLoginSessionsLocked() {
	for key, session := range weixinLoginSessions {
		if session == nil || time.Since(session.StartedAt) > weixinLoginSessionTTL {
			delete(weixinLoginSessions, key)
		}
	}
}

func weixinLoginStatusMessage(resp weixinStatusResponse) string {
	switch strings.TrimSpace(resp.Status) {
	case "wait":
		return "等待扫码。"
	case "scaned":
		return "已扫码，请在手机微信确认。"
	case "need_verifycode":
		return "需要在手机微信完成数字验证。"
	case "verify_code_blocked":
		return "数字验证失败次数过多，请重新生成二维码。"
	case "expired":
		return "二维码已过期，请重新生成。"
	case "binded_redirect":
		return "此微信机器人已绑定。"
	default:
		if strings.TrimSpace(resp.ErrMsg) != "" {
			return resp.ErrMsg
		}
		return "等待微信确认。"
	}
}

func weixinSetPaused(integrationID uint) {
	weixinPauseMu.Lock()
	weixinPauseUntil[integrationID] = time.Now().Add(weixinSessionPauseDuration)
	weixinPauseMu.Unlock()
}

func weixinPauseRemaining(integrationID uint) time.Duration {
	weixinPauseMu.Lock()
	defer weixinPauseMu.Unlock()
	until := weixinPauseUntil[integrationID]
	if until.IsZero() || time.Now().After(until) {
		delete(weixinPauseUntil, integrationID)
		return 0
	}
	return time.Until(until)
}

func weixinRuntimeSignature(integration MessageChannelIntegration) string {
	config := providerConfig(integration)
	values := []string{
		integration.BotToken,
		weixinBaseURL(config),
		configString(config, "account_id"),
		strconv.FormatBool(integration.Enabled),
	}
	sum := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return hex.EncodeToString(sum[:])
}

func logWeixinRuntimeError(integrationID uint, err error) {
	if err != nil {
		log.Printf("weixin channel %d disconnected: %v", integrationID, err)
	}
}
