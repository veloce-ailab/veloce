package model

import (
	"time"

	"github.com/shopspring/decimal"
)

type UserNotificationSettings struct {
	UserID                     uint            `gorm:"primaryKey" json:"user_id"`
	EmailEnabled               bool            `gorm:"default:true" json:"email_enabled"`
	WebhookURL                 string          `gorm:"size:1000" json:"webhook_url"`
	LowBalanceEnabled          bool            `gorm:"default:false" json:"low_balance_enabled"`
	LowBalanceThreshold        decimal.Decimal `gorm:"type:decimal(20,6);default:0" json:"low_balance_threshold"`
	APIKeyExpiredEnabled       bool            `gorm:"default:false" json:"api_key_expired_enabled"`
	SubscriptionResetEnabled   bool            `gorm:"default:false" json:"subscription_reset_enabled"`
	SubscriptionExpiredEnabled bool            `gorm:"default:false" json:"subscription_expired_enabled"`
	CreatedAt                  time.Time       `json:"created_at"`
	UpdatedAt                  time.Time       `json:"updated_at"`
}

type UserNotificationDelivery struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"uniqueIndex:idx_user_notification_delivery,priority:1;index" json:"user_id"`
	EventKey  string    `gorm:"uniqueIndex:idx_user_notification_delivery,priority:2;size:255" json:"event_key"`
	EventType string    `gorm:"size:64" json:"event_type"`
	CreatedAt time.Time `json:"created_at"`
}
