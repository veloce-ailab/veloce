package channel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gorilla/websocket"
)

const (
	qqGatewayOpcodeDispatch     = 0
	qqGatewayOpcodeHeartbeat    = 1
	qqGatewayOpcodeIdentify     = 2
	qqGatewayOpcodeHello        = 10
	qqGatewayOpcodeHeartbeatAck = 11

	messageChannelRuntimeSyncInterval = 30 * time.Second
)

var messageChannelRuntime = newMessageChannelRuntimeManager()

type messageChannelRuntimeManager struct {
	mu             sync.Mutex
	started        bool
	cancel         context.CancelFunc
	clients        map[uint]*qqWebSocketClient
	weixinClients  map[uint]*weixinLongPollClient
	tencentClients map[uint]*tencentChannelGatewayClient
}

type qqWebSocketClient struct {
	integrationID uint
	signature     string
	cancel        context.CancelFunc
	done          chan struct{}
}

type weixinLongPollClient struct {
	integrationID uint
	signature     string
	cancel        context.CancelFunc
	done          chan struct{}
}

type qqGatewayPayload struct {
	Op int             `json:"op"`
	S  int64           `json:"s,omitempty"`
	T  string          `json:"t,omitempty"`
	D  json.RawMessage `json:"d,omitempty"`
}

type qqAppAccessTokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   any    `json:"expires_in"`
}

type qqReadyEvent struct {
	SessionID string `json:"session_id"`
}

func newMessageChannelRuntimeManager() *messageChannelRuntimeManager {
	return &messageChannelRuntimeManager{
		clients:        map[uint]*qqWebSocketClient{},
		weixinClients:  map[uint]*weixinLongPollClient{},
		tencentClients: map[uint]*tencentChannelGatewayClient{},
	}
}

func startMessageChannelRuntime() {
	messageChannelRuntime.start()
}

func syncMessageChannelRuntime() {
	messageChannelRuntime.sync()
}

func (m *messageChannelRuntimeManager) start() {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.started = true
	m.cancel = cancel
	m.mu.Unlock()

	m.sync()
	go func() {
		ticker := time.NewTicker(messageChannelRuntimeSyncInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.sync()
			}
		}
	}()
}

func (m *messageChannelRuntimeManager) sync() {
	if !Enabled() {
		m.stopAll()
		return
	}
	var integrations []MessageChannelIntegration
	if err := model.DB.Where("provider = ? AND enabled = ?", providerQQ, true).Find(&integrations).Error; err != nil {
		log.Printf("message channel runtime sync failed: %v", err)
		return
	}

	want := map[uint]MessageChannelIntegration{}
	for _, integration := range integrations {
		config := providerConfig(integration)
		if normalizeQQConnectionMode(configString(config, "connection_mode", "mode")) != "websocket" {
			continue
		}
		want[integration.ID] = integration
	}

	m.mu.Lock()
	for id, client := range m.clients {
		if _, ok := want[id]; !ok {
			client.cancel()
			delete(m.clients, id)
		}
	}
	for id, integration := range want {
		signature := qqWebSocketSignature(integration)
		if client, ok := m.clients[id]; ok {
			if client.signature == signature {
				continue
			}
			client.cancel()
			delete(m.clients, id)
		}
		ctx, cancel := context.WithCancel(context.Background())
		client := &qqWebSocketClient{integrationID: id, signature: signature, cancel: cancel, done: make(chan struct{})}
		m.clients[id] = client
		go runQQWebSocketClient(ctx, client, integration)
	}
	m.mu.Unlock()

	m.syncWeixin()
	m.syncTencentChannel()
}

func qqWebSocketSignature(integration MessageChannelIntegration) string {
	config := providerConfig(integration)
	values := []string{
		configString(config, "connection_mode", "mode"),
		configString(config, "base_url", "api_base_url", "endpoint"),
		configString(config, "token_url", "access_token_url"),
		configString(config, "bot_id", "robot_id", "app_id", "appid"),
		configString(config, "bot_secret", "secret", "client_secret"),
		configString(config, "authorization", "auth_header"),
		configString(config, "intents"),
		strconv.FormatBool(integration.Enabled),
	}
	sum := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return hex.EncodeToString(sum[:])
}

func (m *messageChannelRuntimeManager) stopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, client := range m.clients {
		client.cancel()
		delete(m.clients, id)
	}
	for id, client := range m.weixinClients {
		client.cancel()
		delete(m.weixinClients, id)
	}
	for id, client := range m.tencentClients {
		client.cancel()
		delete(m.tencentClients, id)
	}
}

func runQQWebSocketClient(ctx context.Context, client *qqWebSocketClient, integration MessageChannelIntegration) {
	defer close(client.done)
	backoff := 3 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		var current MessageChannelIntegration
		if err := model.DB.Where("id = ? AND provider = ? AND enabled = ?", integration.ID, providerQQ, true).First(&current).Error; err != nil {
			return
		}
		config := providerConfig(current)
		if normalizeQQConnectionMode(configString(config, "connection_mode", "mode")) != "websocket" {
			return
		}
		if err := connectQQWebSocket(ctx, current, config); err != nil && ctx.Err() == nil {
			log.Printf("qq websocket channel %d disconnected: %v", current.ID, err)
		}
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

func connectQQWebSocket(ctx context.Context, integration MessageChannelIntegration, config map[string]interface{}) error {
	accessToken, err := qqAppAccessToken(ctx, config)
	if err != nil {
		return err
	}
	endpoint, err := qqWebSocketEndpoint(ctx, config, accessToken)
	if err != nil {
		return err
	}
	if endpoint == "" {
		return errors.New("qq websocket url is empty")
	}
	headers := http.Header{}
	headers.Set("Authorization", qqAccessTokenAuthorization(accessToken))
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, endpoint, headers)
	if err != nil {
		return err
	}
	defer conn.Close()

	identified := false
	seq := int64(0)
	heartbeatStop := make(chan struct{})
	defer close(heartbeatStop)
	heartbeatStarted := false

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var payload qqGatewayPayload
		if err := json.Unmarshal(data, &payload); err != nil {
			continue
		}
		if payload.S > 0 {
			seq = payload.S
		}
		switch payload.Op {
		case qqGatewayOpcodeHello:
			if interval := qqHeartbeatInterval(payload.D); interval > 0 {
				if !heartbeatStarted {
					startQQHeartbeat(ctx, conn, heartbeatStop, interval, &seq)
					heartbeatStarted = true
				}
			}
			if !identified {
				if err := sendQQIdentify(conn, accessToken, config); err != nil {
					return err
				}
				identified = true
			}
		case qqGatewayOpcodeDispatch:
			if strings.EqualFold(strings.TrimSpace(payload.T), "READY") {
				logQQReadyEvent(integration.ID, payload.D)
				continue
			}
			if !qqDispatchLooksLikeMessage(payload.T) {
				continue
			}
			if len(payload.D) == 0 || string(payload.D) == "null" {
				continue
			}
			if err := processMessageChannelPayload(ctx, integration, payload.D, nil); err != nil {
				log.Printf("qq websocket channel %d payload handling failed: %v", integration.ID, err)
			}
		}
	}
}

func startQQHeartbeat(ctx context.Context, conn *websocket.Conn, stop <-chan struct{}, interval time.Duration, seq *int64) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = conn.WriteJSON(map[string]interface{}{"op": qqGatewayOpcodeHeartbeat, "d": *seq})
			}
		}
	}()
}

func qqDispatchLooksLikeMessage(event string) bool {
	switch strings.ToUpper(strings.TrimSpace(event)) {
	case "AT_MESSAGE_CREATE", "MESSAGE_CREATE", "DIRECT_MESSAGE_CREATE", "GROUP_AT_MESSAGE_CREATE", "C2C_MESSAGE_CREATE":
		return true
	default:
		return false
	}
}

func logQQReadyEvent(integrationID uint, raw json.RawMessage) {
	var ready qqReadyEvent
	if len(raw) == 0 || json.Unmarshal(raw, &ready) != nil {
		log.Printf("qq websocket channel %d authenticated", integrationID)
		return
	}
	if strings.TrimSpace(ready.SessionID) == "" {
		log.Printf("qq websocket channel %d authenticated", integrationID)
		return
	}
	log.Printf("qq websocket channel %d authenticated session_id=%s", integrationID, ready.SessionID)
}

func qqAppAccessToken(ctx context.Context, config map[string]interface{}) (string, error) {
	appID := configString(config, "bot_id", "robot_id", "app_id", "appid")
	clientSecret := configString(config, "bot_secret", "secret", "client_secret")
	if appID == "" || clientSecret == "" {
		return "", errors.New("qq app id and client secret are required")
	}
	tokenURL := configString(config, "token_url", "access_token_url")
	if tokenURL == "" {
		tokenURL = "https://bots.qq.com/app/getAppAccessToken"
	}
	body, err := json.Marshal(map[string]string{
		"appId":        appID,
		"clientSecret": clientSecret,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := providerHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return "", errors.New("qq access token endpoint returned status " + strconv.Itoa(resp.StatusCode))
	}
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var data qqAppAccessTokenResponse
	if err := json.Unmarshal(respBody, &data); err != nil {
		return "", err
	}
	token := strings.TrimSpace(data.AccessToken)
	if token == "" {
		return "", errors.New("qq access token response missing access_token")
	}
	return token, nil
}

func qqWebSocketEndpoint(ctx context.Context, config map[string]interface{}, accessToken string) (string, error) {
	baseURL := configString(config, "base_url", "api_base_url", "endpoint")
	if baseURL == "" {
		baseURL = "https://api.sgroup.qq.com"
	}
	gatewayURL := joinEndpoint(baseURL, "/gateway")
	if gatewayURL == "" {
		return "", errors.New("invalid qq gateway endpoint")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gatewayURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", qqAccessTokenAuthorization(accessToken))
	resp, err := providerHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		return "", errors.New("qq gateway returned status " + strconv.Itoa(resp.StatusCode))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var data map[string]interface{}
	if err := json.Unmarshal(body, &data); err != nil {
		return "", err
	}
	endpoint := stringValue(data["url"])
	if endpoint == "" {
		return "", errors.New("qq gateway response missing url")
	}
	log.Printf("qq websocket endpoint: %s", endpoint)
	return endpoint, nil
}

func sendQQIdentify(conn *websocket.Conn, accessToken string, config map[string]interface{}) error {
	intents := parseQQIntents(configString(config, "intents"))
	payload := map[string]interface{}{
		"op": qqGatewayOpcodeIdentify,
		"d": map[string]interface{}{
			"token":   qqAccessTokenAuthorization(accessToken),
			"intents": intents,
			"shard":   qqShard(config),
			"properties": map[string]string{
				"$os":      "linux",
				"$browser": "windypear",
				"$device":  "windypear",
			},
		},
	}
	return conn.WriteJSON(payload)
}

func qqAccessTokenAuthorization(accessToken string) string {
	return "QQBot " + strings.TrimSpace(accessToken)
}

func qqHeartbeatInterval(raw json.RawMessage) time.Duration {
	var data map[string]interface{}
	if len(raw) == 0 || json.Unmarshal(raw, &data) != nil {
		return 0
	}
	value := data["heartbeat_interval"]
	switch typed := value.(type) {
	case float64:
		return time.Duration(typed) * time.Millisecond
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return time.Duration(parsed) * time.Millisecond
		}
	}
	return 0
}

func parseQQIntents(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 513
	}
	if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
		return parsed
	}
	result := 0
	for _, part := range strings.Split(value, ",") {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case "guild_messages":
			result |= 1 << 9
		case "guild_message_reactions":
			result |= 1 << 10
		case "direct_message":
			result |= 1 << 12
		case "open_forum_event":
			result |= 1 << 18
		case "audio_action":
			result |= 1 << 29
		case "public_guild_messages":
			result |= 1 << 30
		case "group_and_c2c_event", "group_c2c", "group":
			result |= 1 << 25
		}
	}
	if result == 0 {
		return 513
	}
	return result
}

func qqShard(config map[string]interface{}) []int {
	index := configInt(config, "shard_index")
	total := configInt(config, "shard_total")
	if total <= 0 {
		total = 1
	}
	if index < 0 || index >= total {
		index = 0
	}
	return []int{index, total}
}

func configInt(config map[string]interface{}, key string) int {
	value, exists := config[key]
	if !exists {
		return 0
	}
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err == nil {
			return parsed
		}
	}
	return 0
}
