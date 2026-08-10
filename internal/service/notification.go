package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"gorm.io/gorm/clause"
)

const notificationScanInterval = 10 * time.Minute

type NotificationService struct{}

func NewNotificationService() *NotificationService { return &NotificationService{} }

func NotificationsEnabled() bool {
	return settingBool("notifications_enabled", false)
}

func (s *NotificationService) Start() {
	RegisterScheduledJob(ScheduledJob{
		Name:        "user_notifications",
		Description: "检查并发送用户余额、令牌和套餐通知",
		PrimaryOnly: true,
		Interval:    func() time.Duration { return notificationScanInterval },
		Enabled:     NotificationsEnabled,
		Run: func(ctx context.Context) (string, bool, error) {
			count, err := s.Run(ctx)
			return fmt.Sprintf("已发送 %d 条用户通知", count), count > 0, err
		},
	})
}

func (s *NotificationService) Run(ctx context.Context) (int, error) {
	if !NotificationsEnabled() {
		return 0, nil
	}
	now := time.Now()
	var settings []model.UserNotificationSettings
	if err := model.DB.Find(&settings).Error; err != nil {
		return 0, err
	}
	settingsByUser := make(map[uint]model.UserNotificationSettings, len(settings))
	for _, item := range settings {
		settingsByUser[item.UserID] = item
	}
	if len(settingsByUser) == 0 {
		return 0, nil
	}

	sent := 0
	for userID, setting := range settingsByUser {
		if setting.LowBalanceEnabled && setting.LowBalanceThreshold.IsPositive() {
			var user model.User
			if err := model.DB.First(&user, userID).Error; err == nil && user.Balance.LessThanOrEqual(setting.LowBalanceThreshold) {
				if s.deliver(ctx, user, setting, "low_balance:"+now.Format("2006-01-02"), "low_balance", "余额不足提醒", fmt.Sprintf("账户余额 %s 已低于您设置的阈值 %s。", user.Balance, setting.LowBalanceThreshold)) {
					sent++
				}
			}
		}
	}

	var keys []model.APIKey
	if err := model.DB.Preload("User").Where("expires_at IS NOT NULL AND expires_at <= ?", now).Find(&keys).Error; err != nil {
		return sent, err
	}
	for _, key := range keys {
		setting, ok := settingsByUser[key.UserID]
		if !ok || !setting.APIKeyExpiredEnabled {
			continue
		}
		if s.deliver(ctx, key.User, setting, fmt.Sprintf("api_key_expired:%d", key.ID), "api_key_expired", "令牌已过期", fmt.Sprintf("令牌“%s”已于 %s 过期。", key.Name, key.ExpiresAt.Format(time.RFC3339))) {
			sent++
		}
	}

	var subscriptions []UserSubscription
	if err := model.DB.Preload("Plan").Where("user_id IN ?", notificationUserIDs(settingsByUser)).Find(&subscriptions).Error; err != nil {
		return sent, err
	}
	for _, subscription := range subscriptions {
		setting := settingsByUser[subscription.UserID]
		var user model.User
		if err := model.DB.First(&user, subscription.UserID).Error; err != nil {
			continue
		}
		if setting.SubscriptionResetEnabled && subscription.LastResetAt != nil && subscription.LastResetAt.After(now.Add(-2*notificationScanInterval)) {
			key := fmt.Sprintf("subscription_reset:%d:%d", subscription.ID, subscription.LastResetAt.Unix())
			if s.deliver(ctx, user, setting, key, "subscription_reset", "套餐额度已重置", fmt.Sprintf("套餐“%s”的额度已重置为 %s。", subscription.Plan.Name, subscription.Balance)) {
				sent++
			}
		}
		if setting.SubscriptionExpiredEnabled && subscription.ActiveUntil != nil && !subscription.ActiveUntil.After(now) {
			if s.deliver(ctx, user, setting, fmt.Sprintf("subscription_expired:%d", subscription.ID), "subscription_expired", "套餐已过期", fmt.Sprintf("套餐“%s”已于 %s 过期。", subscription.Plan.Name, subscription.ActiveUntil.Format(time.RFC3339))) {
				sent++
			}
		}
	}
	return sent, nil
}

func notificationUserIDs(settings map[uint]model.UserNotificationSettings) []uint {
	ids := make([]uint, 0, len(settings))
	for id := range settings {
		ids = append(ids, id)
	}
	return ids
}

func (s *NotificationService) deliver(ctx context.Context, user model.User, settings model.UserNotificationSettings, eventKey, eventType, subject, body string) bool {
	if user.ID == 0 || notificationAlreadyDelivered(user.ID, eventKey) {
		return false
	}
	sent := false
	if settings.EmailEnabled && strings.TrimSpace(user.Email) != "" && smtpConfigured() {
		if err := sendMail(user.Email, subject, body); err == nil {
			sent = true
		}
	}
	if webhook := strings.TrimSpace(settings.WebhookURL); webhook != "" {
		if err := sendWebhookNotification(ctx, webhook, user, eventType, subject, body); err == nil {
			sent = true
		}
	}
	if !sent {
		return false
	}
	return model.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.UserNotificationDelivery{UserID: user.ID, EventKey: eventKey, EventType: eventType}).Error == nil
}

func notificationAlreadyDelivered(userID uint, eventKey string) bool {
	var count int64
	return model.DB.Model(&model.UserNotificationDelivery{}).Where("user_id = ? AND event_key = ?", userID, eventKey).Count(&count).Error == nil && count > 0
}

func sendWebhookNotification(ctx context.Context, endpoint string, user model.User, eventType, subject, body string) error {
	if err := ValidateConfiguredHTTPURL(endpoint); err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]interface{}{"event": eventType, "subject": subject, "message": body, "user": map[string]interface{}{"id": user.ID, "username": user.Username, "email": user.Email}, "sent_at": time.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: GuardedRedirectPolicy()}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
	}
	return nil
}
