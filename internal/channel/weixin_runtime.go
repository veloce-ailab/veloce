package channel

import (
	"context"
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
)

func (m *messageChannelRuntimeManager) syncWeixin() {
	if !Enabled() {
		return
	}
	var integrations []MessageChannelIntegration
	if err := model.DB.Where("provider = ? AND enabled = ? AND bot_token <> ?", providerWeixin, true, "").Find(&integrations).Error; err != nil {
		log.Printf("weixin message channel runtime sync failed: %v", err)
		return
	}
	want := map[uint]MessageChannelIntegration{}
	for _, integration := range integrations {
		if strings.TrimSpace(integration.BotToken) == "" {
			continue
		}
		want[integration.ID] = integration
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for id, client := range m.weixinClients {
		if _, ok := want[id]; !ok {
			client.cancel()
			delete(m.weixinClients, id)
		}
	}
	for id, integration := range want {
		signature := weixinRuntimeSignature(integration)
		if client, ok := m.weixinClients[id]; ok {
			if client.signature == signature {
				continue
			}
			client.cancel()
			delete(m.weixinClients, id)
		}
		ctx, cancel := context.WithCancel(context.Background())
		client := &weixinLongPollClient{integrationID: id, signature: signature, cancel: cancel, done: make(chan struct{})}
		m.weixinClients[id] = client
		go runWeixinLongPollClient(ctx, client, integration)
	}
}

func runWeixinLongPollClient(ctx context.Context, client *weixinLongPollClient, integration MessageChannelIntegration) {
	defer close(client.done)
	backoff := 2 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if remaining := weixinPauseRemaining(integration.ID); remaining > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(remaining):
			}
			continue
		}
		err := pollWeixinOnce(ctx, integration)
		if err == nil {
			backoff = 2 * time.Second
			continue
		}
		if ctx.Err() != nil {
			return
		}
		logWeixinRuntimeError(integration.ID, err)
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

func pollWeixinOnce(ctx context.Context, integration MessageChannelIntegration) error {
	var current MessageChannelIntegration
	if err := model.DB.Where("id = ? AND provider = ? AND enabled = ?", integration.ID, providerWeixin, true).First(&current).Error; err != nil {
		return err
	}
	if strings.TrimSpace(current.BotToken) == "" {
		return context.Canceled
	}
	config := providerConfig(current)
	baseURL := weixinBaseURL(config)
	getUpdatesBuf := configString(config, "get_updates_buf")
	timeout := weixinDefaultLongPollTimeout
	if raw := configString(config, "long_poll_timeout_ms"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			timeout = time.Duration(parsed) * time.Millisecond
		}
	}
	body := map[string]any{
		"get_updates_buf": getUpdatesBuf,
		"base_info":       weixinBuildBaseInfo(),
	}
	var resp weixinGetUpdatesResponse
	if err := weixinPostJSON(ctx, baseURL, "ilink/bot/getupdates", current.BotToken, body, timeout+5*time.Second, &resp); err != nil {
		return err
	}
	if resp.Ret == weixinStaleTokenErrCode || resp.ErrCode == weixinStaleTokenErrCode {
		weixinSetPaused(current.ID)
		return nil
	}
	if (resp.Ret != 0) || (resp.ErrCode != 0) {
		return &weixinAPIError{Ret: resp.Ret, ErrCode: resp.ErrCode, ErrMsg: resp.ErrMsg}
	}
	if strings.TrimSpace(resp.GetUpdatesBuf) != "" && resp.GetUpdatesBuf != getUpdatesBuf {
		_ = updateWeixinProviderConfig(current.ID, func(config map[string]any) {
			config["get_updates_buf"] = resp.GetUpdatesBuf
		})
	}
	for _, msg := range resp.Messages {
		if strings.TrimSpace(msg.FromUserID) == "" {
			continue
		}
		if strings.TrimSpace(msg.ContextToken) != "" {
			_ = updateWeixinProviderConfig(current.ID, func(config map[string]any) {
				tokens := map[string]string{}
				if raw := configString(config, "context_tokens_json"); raw != "" {
					_ = json.Unmarshal([]byte(raw), &tokens)
				}
				tokens[msg.FromUserID] = msg.ContextToken
				if data, err := json.Marshal(tokens); err == nil {
					config["context_tokens_json"] = string(data)
				}
				config["context_token"] = msg.ContextToken
			})
		}
		raw, err := json.Marshal(msg)
		if err != nil {
			continue
		}
		if err := processMessageChannelPayload(ctx, current, raw, nil); err != nil {
			log.Printf("weixin channel %d payload handling failed: %v", current.ID, err)
		}
		now := time.Now()
		_ = model.DB.Model(&current).Update("last_event_at", now).Error
	}
	return nil
}

type weixinAPIError struct {
	Ret     int
	ErrCode int
	ErrMsg  string
}

func (err *weixinAPIError) Error() string {
	return "weixin getupdates failed: ret=" + strconv.Itoa(err.Ret) + " errcode=" + strconv.Itoa(err.ErrCode) + " errmsg=" + err.ErrMsg
}
