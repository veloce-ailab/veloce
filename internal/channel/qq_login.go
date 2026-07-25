package channel

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
	qrcode "github.com/skip2/go-qrcode"
)

const (
	qqLoginSessionTTL         = 5 * time.Minute
	qqBindDefaultHost         = "q.qq.com"
	qqBindDefaultSource       = "veloce"
	qqBindDefaultTimeout      = 10 * time.Second
	qqBindStatusCompleted     = 2
	qqBindStatusExpired       = 3
	qqQRCodeDataURLPixelWidth = 256
)

type qqLoginSession struct {
	SessionKey string
	TaskID     string
	KeyBase64  string
	QRCodeURL  string
	StartedAt  time.Time
}

type qqBindTaskResponse struct {
	RetCode int    `json:"retcode"`
	Msg     string `json:"msg"`
	Data    struct {
		TaskID string `json:"task_id"`
	} `json:"data"`
}

type qqBindPollResponse struct {
	RetCode int    `json:"retcode"`
	Msg     string `json:"msg"`
	Data    struct {
		Status           int    `json:"status"`
		BotAppID         string `json:"bot_appid"`
		BotEncryptSecret string `json:"bot_encrypt_secret"`
	} `json:"data"`
}

type qqLoginWaitInput struct {
	SessionKey string `json:"session_key"`
}

var (
	qqLoginMu       sync.Mutex
	qqLoginSessions = map[string]*qqLoginSession{}
)

func (api *API) startQQLogin(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if !requireEnabled(c) {
		return
	}
	if _, ok := loadUserQQIntegration(c, user.ID); !ok {
		return
	}
	session, err := createQQLoginSession(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	qrDataURL := ""
	if png, err := qrcode.Encode(session.QRCodeURL, qrcode.Medium, qqQRCodeDataURLPixelWidth); err == nil {
		qrDataURL = "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	}
	c.JSON(http.StatusOK, gin.H{
		"session_key": session.SessionKey,
		"qrcode_url":  session.QRCodeURL,
		"qr_data_url": qrDataURL,
		"expires_in":  int(qqLoginSessionTTL.Seconds()),
		"status":      "waiting_scan",
	})
}

func (api *API) waitQQLogin(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if !requireEnabled(c) {
		return
	}
	integration, ok := loadUserQQIntegration(c, user.ID)
	if !ok {
		return
	}
	var input qqLoginWaitInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid login session"})
		return
	}
	session, ok := qqLoginSessionByKey(input.SessionKey)
	if !ok {
		c.JSON(http.StatusGone, gin.H{"connected": false, "status": "expired", "error": "QQ login session expired"})
		return
	}
	result, err := pollQQBindResult(c.Request.Context(), session.TaskID)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"connected": false, "status": "error", "error": err.Error()})
		return
	}
	switch result.Data.Status {
	case qqBindStatusCompleted:
		secret, err := decryptQQBotSecret(result.Data.BotEncryptSecret, session.KeyBase64)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"connected": false, "status": "error", "error": "Failed to decrypt QQ bot secret"})
			return
		}
		if strings.TrimSpace(result.Data.BotAppID) == "" || strings.TrimSpace(secret) == "" {
			c.JSON(http.StatusBadGateway, gin.H{"connected": false, "status": "error", "error": "QQ bind result is incomplete"})
			return
		}
		if err := saveQQLoginResult(integration, result.Data.BotAppID, secret); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"connected": false, "status": "error", "error": "Failed to save QQ bot credentials"})
			return
		}
		deleteQQLoginSession(session.SessionKey)
		syncMessageChannelRuntime()
		c.JSON(http.StatusOK, gin.H{"connected": true, "status": "connected", "app_id": result.Data.BotAppID})
	case qqBindStatusExpired:
		deleteQQLoginSession(session.SessionKey)
		c.JSON(http.StatusGone, gin.H{"connected": false, "status": "expired", "error": "QQ login QR code expired"})
	default:
		c.JSON(http.StatusOK, gin.H{"connected": false, "status": "waiting_scan"})
	}
}

func loadUserQQIntegration(c *gin.Context, userID uint) (MessageChannelIntegration, bool) {
	id := strings.TrimSpace(c.Param("id"))
	var integration MessageChannelIntegration
	if err := model.DB.Where("id = ? AND user_id = ? AND provider = ?", id, userID, providerQQ).First(&integration).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "QQ message channel not found"})
		return MessageChannelIntegration{}, false
	}
	return integration, true
}

func createQQLoginSession(ctx context.Context) (*qqLoginSession, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	keyBase64 := base64.StdEncoding.EncodeToString(key)
	task, err := createQQBindTask(ctx, keyBase64)
	if err != nil {
		return nil, err
	}
	sessionKeyBytes := make([]byte, 16)
	if _, err := rand.Read(sessionKeyBytes); err != nil {
		return nil, err
	}
	session := &qqLoginSession{
		SessionKey: hex.EncodeToString(sessionKeyBytes),
		TaskID:     task.Data.TaskID,
		KeyBase64:  keyBase64,
		QRCodeURL:  buildQQConnectURL(task.Data.TaskID, qqBindDefaultSource),
		StartedAt:  time.Now(),
	}
	qqLoginMu.Lock()
	defer qqLoginMu.Unlock()
	qqLoginSessions[session.SessionKey] = session
	return session, nil
}

func createQQBindTask(ctx context.Context, keyBase64 string) (qqBindTaskResponse, error) {
	var result qqBindTaskResponse
	if err := postQQBindJSON(ctx, "https://"+qqBindDefaultHost+"/lite/create_bind_task", map[string]string{"key": keyBase64}, &result); err != nil {
		return result, err
	}
	if result.RetCode != 0 {
		return result, errors.New(firstNonEmptyQQText(result.Msg, "create_bind_task failed"))
	}
	if strings.TrimSpace(result.Data.TaskID) == "" {
		return result, errors.New("create_bind_task returned empty task id")
	}
	return result, nil
}

func pollQQBindResult(ctx context.Context, taskID string) (qqBindPollResponse, error) {
	var result qqBindPollResponse
	if err := postQQBindJSON(ctx, "https://"+qqBindDefaultHost+"/lite/poll_bind_result", map[string]string{"task_id": taskID}, &result); err != nil {
		return result, err
	}
	if result.RetCode != 0 {
		return result, errors.New(firstNonEmptyQQText(result.Msg, "poll_bind_result failed"))
	}
	return result, nil
}

func postQQBindJSON(ctx context.Context, endpoint string, payload interface{}, target interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	reqCtx, cancel := context.WithTimeout(ctx, qqBindDefaultTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := providerHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return errors.New("QQ bind service returned status " + resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, target)
}

func buildQQConnectURL(taskID string, source string) string {
	return "https://" + qqBindDefaultHost + "/qqbot/openclaw/connect.html?task_id=" + url.QueryEscape(taskID) + "&source=" + url.QueryEscape(source) + "&_wv=2"
}

func decryptQQBotSecret(encryptedBase64 string, keyBase64 string) (string, error) {
	key, err := base64.StdEncoding.DecodeString(keyBase64)
	if err != nil {
		return "", err
	}
	ciphertext, err := base64.StdEncoding.DecodeString(encryptedBase64)
	if err != nil {
		return "", err
	}
	if len(key) != 32 || len(ciphertext) < 12+16 {
		return "", errors.New("invalid QQ bot encrypted secret")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := ciphertext[:12]
	encrypted := ciphertext[12:]
	plain, err := aead.Open(nil, nonce, encrypted, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func saveQQLoginResult(integration MessageChannelIntegration, appID string, appSecret string) error {
	options := decodeAdvancedOptions(integration.AdvancedOptions)
	config := providerConfigFromAdvancedOptions(options)
	config["bot_id"] = strings.TrimSpace(appID)
	config["bot_secret"] = strings.TrimSpace(appSecret)
	config["login_status"] = "connected"
	if strings.TrimSpace(configString(config, "connection_mode", "mode")) == "" {
		config["connection_mode"] = "webhook"
	}
	options.CustomProviderConfigJSON = encodeProviderConfig(config)
	data, err := json.Marshal(options)
	if err != nil {
		return err
	}
	return model.DB.Model(&MessageChannelIntegration{}).Where("id = ?", integration.ID).Updates(map[string]interface{}{
		"bot_token":        "",
		"advanced_options": string(data),
		"enabled":          true,
	}).Error
}

func qqLoginSessionByKey(sessionKey string) (*qqLoginSession, bool) {
	qqLoginMu.Lock()
	defer qqLoginMu.Unlock()
	session := qqLoginSessions[strings.TrimSpace(sessionKey)]
	if session == nil {
		return nil, false
	}
	if time.Since(session.StartedAt) > qqLoginSessionTTL {
		delete(qqLoginSessions, session.SessionKey)
		return nil, false
	}
	return session, true
}

func deleteQQLoginSession(sessionKey string) {
	qqLoginMu.Lock()
	defer qqLoginMu.Unlock()
	delete(qqLoginSessions, strings.TrimSpace(sessionKey))
}

func firstNonEmptyQQText(values ...string) string {
	for _, value := range values {
		if text := strings.TrimSpace(value); text != "" {
			return text
		}
	}
	return ""
}
