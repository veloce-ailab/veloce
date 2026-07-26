package model

import (
	"time"

	"github.com/shopspring/decimal"
)

// User represents a system user
type User struct {
	ID            uint                  `gorm:"primaryKey" json:"id"`
	Username      string                `gorm:"uniqueIndex;size:100" json:"username"`
	Email         string                `gorm:"uniqueIndex;size:100" json:"email"`
	Phone         *string               `gorm:"uniqueIndex;size:32" json:"phone,omitempty"`
	OIDCSub       *string               `gorm:"column:oidc_sub;uniqueIndex;size:255" json:"oidc_sub,omitempty"`
	PasswordHash  string                `gorm:"size:255" json:"-"`
	EmailVerified bool                  `gorm:"default:false" json:"email_verified"`
	AvatarURL     string                `gorm:"size:255" json:"avatar_url"`
	Balance       decimal.Decimal       `gorm:"type:decimal(20,6);default:0" json:"balance"`
	GroupID       uint                  `json:"group_id"`
	Group         Group                 `gorm:"foreignKey:GroupID" json:"group"`
	Groups        []UserGroupMembership `gorm:"foreignKey:UserID" json:"groups,omitempty"`
	APIKey        string                `gorm:"uniqueIndex;size:100" json:"-"`
	ReferralCode  *string               `gorm:"uniqueIndex;size:32" json:"referral_code,omitempty"`
	ReferrerID    *uint                 `gorm:"index" json:"referrer_id,omitempty"`
	Referrer      *User                 `gorm:"foreignKey:ReferrerID;references:ID" json:"referrer,omitempty"`
	IsAdmin       bool                  `gorm:"default:false" json:"is_admin"`
	CreatedAt     time.Time             `json:"created_at"`
	UpdatedAt     time.Time             `json:"updated_at"`
}

// UserAvatar keeps uploaded avatar bytes separate from the frequently-read users table.
type UserAvatar struct {
	UserID    uint      `gorm:"primaryKey" json:"-"`
	MIMEType  string    `gorm:"size:40;not null" json:"-"`
	Data      []byte    `gorm:"not null" json:"-"`
	UpdatedAt time.Time `json:"-"`
}

// APIKey represents a user-owned API token.
type APIKey struct {
	ID                  uint            `gorm:"primaryKey" json:"id"`
	UserID              uint            `gorm:"index;not null" json:"user_id"`
	User                User            `gorm:"foreignKey:UserID" json:"-"`
	Name                string          `gorm:"size:100;not null" json:"name"`
	APIKey              string          `gorm:"column:api_key;size:100" json:"api_key,omitempty"`
	KeyHash             string          `gorm:"uniqueIndex;size:64;not null" json:"-"`
	KeyPrefix           string          `gorm:"size:16" json:"key_prefix"`
	AllowedModels       string          `gorm:"column:allowed_models;type:text" json:"-"`
	AllowedUserChannels string          `gorm:"column:allowed_user_channels;type:text" json:"-"`
	AllowedIPs          string          `gorm:"column:allowed_ips;type:text" json:"-"`
	QuotaLimit          decimal.Decimal `gorm:"type:decimal(20,10);default:0" json:"quota_limit"`
	Enabled             bool            `gorm:"default:true" json:"enabled"`
	LastUsedAt          *time.Time      `json:"last_used_at"`
	UsageResetAt        *time.Time      `json:"usage_reset_at"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

// CheckInRecord records a user's daily check-in reward.
type CheckInRecord struct {
	ID           uint            `gorm:"primaryKey" json:"id"`
	UserID       uint            `gorm:"uniqueIndex:idx_check_in_user_date;not null" json:"user_id"`
	User         User            `gorm:"foreignKey:UserID" json:"-"`
	CheckInDate  string          `gorm:"uniqueIndex:idx_check_in_user_date;size:10;not null" json:"check_in_date"`
	RewardAmount decimal.Decimal `gorm:"type:decimal(20,6);not null" json:"reward_amount"`
	StreakDays   int             `gorm:"default:1" json:"streak_days"`
	RewardKind   string          `gorm:"size:50" json:"reward_kind"`
	CreatedAt    time.Time       `json:"created_at"`
}

// PaymentOrder records a recharge order submitted through a payment provider.
type PaymentOrder struct {
	ID              uint            `gorm:"primaryKey" json:"id"`
	OrderNo         string          `gorm:"uniqueIndex;size:64;not null" json:"order_no"`
	UserID          uint            `gorm:"index;not null" json:"user_id"`
	User            User            `gorm:"foreignKey:UserID" json:"-"`
	Amount          decimal.Decimal `gorm:"type:decimal(20,6);not null" json:"amount"`
	RMBAmount       decimal.Decimal `gorm:"type:decimal(20,2);not null" json:"rmb_amount"`
	ExchangeRate    decimal.Decimal `gorm:"type:decimal(20,6);not null" json:"exchange_rate"`
	PaymentCurrency string          `gorm:"size:8" json:"payment_currency"`
	GatewayAmount   decimal.Decimal `gorm:"type:decimal(20,2)" json:"gateway_amount"`
	Method          string          `gorm:"size:32;not null" json:"method"`
	Status          string          `gorm:"index;size:32;not null" json:"status"`
	GatewayProvider string          `gorm:"size:32" json:"gateway_provider"`
	GatewayChannel  string          `gorm:"size:64" json:"gateway_channel"`
	GatewayTradeNo  string          `gorm:"size:128" json:"gateway_trade_no"`
	NotifyPayload   string          `gorm:"type:text" json:"-"`
	PaidAt          *time.Time      `json:"paid_at"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

// WalletTransaction is an immutable balance settlement record. Debit and
// credit are retained separately so a single atomic operation can represent a
// paid action and its reward without losing either side of the audit trail.
type WalletTransaction struct {
	ID             uint            `gorm:"primaryKey" json:"id"`
	UserID         uint            `gorm:"uniqueIndex:idx_wallet_transaction_idempotency,priority:1;index;not null" json:"user_id"`
	User           User            `gorm:"foreignKey:UserID" json:"-"`
	Source         string          `gorm:"uniqueIndex:idx_wallet_transaction_idempotency,priority:2;size:100;not null" json:"source"`
	IdempotencyKey string          `gorm:"uniqueIndex:idx_wallet_transaction_idempotency,priority:3;size:160;not null" json:"idempotency_key"`
	PluginID       string          `gorm:"index;size:80" json:"plugin_id,omitempty"`
	DebitAmount    decimal.Decimal `gorm:"type:decimal(20,6);not null;default:0" json:"debit_amount"`
	CreditAmount   decimal.Decimal `gorm:"type:decimal(20,6);not null;default:0" json:"credit_amount"`
	BalanceBefore  decimal.Decimal `gorm:"type:decimal(20,6);not null" json:"balance_before"`
	BalanceAfter   decimal.Decimal `gorm:"type:decimal(20,6);not null" json:"balance_after"`
	ReferenceType  string          `gorm:"index;size:80" json:"reference_type,omitempty"`
	ReferenceID    string          `gorm:"index;size:160" json:"reference_id,omitempty"`
	Description    string          `gorm:"size:500" json:"description,omitempty"`
	RequestHash    string          `gorm:"size:64;not null" json:"-"`
	MetadataJSON   string          `gorm:"type:text" json:"metadata_json,omitempty"`
	CreatedAt      time.Time       `gorm:"index" json:"created_at"`
}

// WalletLimitUsage records which configured participation limits were consumed
// by a wallet transaction. Rows are created in the same transaction as the
// balance settlement.
type WalletLimitUsage struct {
	ID                  uint      `gorm:"primaryKey" json:"id"`
	WalletTransactionID uint      `gorm:"uniqueIndex:idx_wallet_limit_usage_transaction,priority:1;not null" json:"wallet_transaction_id"`
	UserID              uint      `gorm:"index:idx_wallet_limit_usage_counter,priority:1;not null" json:"user_id"`
	Source              string    `gorm:"index:idx_wallet_limit_usage_counter,priority:2;size:100;not null" json:"source"`
	LimitKey            string    `gorm:"uniqueIndex:idx_wallet_limit_usage_transaction,priority:2;index:idx_wallet_limit_usage_counter,priority:3;size:160;not null" json:"limit_key"`
	CreatedAt           time.Time `gorm:"index" json:"created_at"`
}

// EmailVerificationCode stores short-lived codes for password registration.
type EmailVerificationCode struct {
	ID               uint       `gorm:"primaryKey" json:"id"`
	Email            string     `gorm:"index;size:100;not null" json:"email"`
	CodeHash         string     `gorm:"size:64;not null" json:"-"`
	Purpose          string     `gorm:"size:32;not null" json:"purpose"`
	HCaptchaVerified bool       `gorm:"default:false" json:"-"`
	ExpiresAt        time.Time  `gorm:"index" json:"expires_at"`
	UsedAt           *time.Time `json:"used_at"`
	CreatedAt        time.Time  `json:"created_at"`
}

// PhoneVerificationCode stores short-lived SMS codes for phone registration
// and phone binding.
type PhoneVerificationCode struct {
	ID               uint       `gorm:"primaryKey" json:"id"`
	Phone            string     `gorm:"index;size:32;not null" json:"phone"`
	CodeHash         string     `gorm:"size:64;not null" json:"-"`
	Purpose          string     `gorm:"size:32;not null" json:"purpose"`
	HCaptchaVerified bool       `gorm:"default:false" json:"-"`
	ExpiresAt        time.Time  `gorm:"index" json:"expires_at"`
	UsedAt           *time.Time `json:"used_at"`
	CreatedAt        time.Time  `json:"created_at"`
}

// OIDCBindRequest tracks a pending authenticated OIDC binding flow.
type OIDCBindRequest struct {
	State     string    `gorm:"primaryKey;size:128" json:"state"`
	UserID    uint      `gorm:"index;not null" json:"user_id"`
	ExpiresAt time.Time `gorm:"index" json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

// WebAuthnChallenge stores short-lived passkey registration/login challenges.
type WebAuthnChallenge struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Challenge string    `gorm:"uniqueIndex;size:128;not null" json:"challenge"`
	Purpose   string    `gorm:"index;size:32;not null" json:"purpose"`
	UserID    *uint     `gorm:"index" json:"user_id,omitempty"`
	RPID      string    `gorm:"size:255;not null" json:"rp_id"`
	Origin    string    `gorm:"size:500;not null" json:"origin"`
	ExpiresAt time.Time `gorm:"index" json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

// PasskeyCredential stores a WebAuthn credential bound to a user.
type PasskeyCredential struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	UserID        uint       `gorm:"index;not null" json:"user_id"`
	User          User       `gorm:"foreignKey:UserID" json:"-"`
	Name          string     `gorm:"size:100;not null" json:"name"`
	CredentialID  []byte     `gorm:"uniqueIndex;not null" json:"-"`
	PublicKeyCOSE []byte     `gorm:"not null" json:"-"`
	AAGUID        []byte     `json:"-"`
	SignCount     uint32     `json:"sign_count"`
	LastUsedAt    *time.Time `json:"last_used_at"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// UserChannel represents a user-facing logical channel.
type UserChannel struct {
	ID               uint                     `gorm:"primaryKey" json:"id"`
	Name             string                   `gorm:"uniqueIndex;size:100;not null" json:"name"`
	Description      string                   `gorm:"size:255" json:"description"`
	Multiplier       decimal.Decimal          `gorm:"type:decimal(10,4);default:1.0" json:"multiplier"`
	RoutingAlgorithm string                   `gorm:"size:32;default:priority" json:"routing_algorithm"`
	Enabled          bool                     `gorm:"default:true" json:"enabled"`
	CreatedAt        time.Time                `json:"created_at"`
	UpdatedAt        time.Time                `json:"updated_at"`
	Channels         []Channel                `gorm:"foreignKey:UserChannelID" json:"channels,omitempty"`
	AllowedGroups    []UserChannelGroupAccess `gorm:"foreignKey:UserChannelID" json:"allowed_groups,omitempty"`
}

// UserChannelGroupAccess restricts a user-facing channel to members of a group.
// A channel with no access records remains available to every group.
type UserChannelGroupAccess struct {
	ID            uint        `gorm:"primaryKey" json:"id"`
	UserChannelID uint        `gorm:"uniqueIndex:idx_user_channel_group_access;not null" json:"user_channel_id"`
	UserChannel   UserChannel `gorm:"foreignKey:UserChannelID" json:"-"`
	GroupID       uint        `gorm:"uniqueIndex:idx_user_channel_group_access;not null" json:"group_id"`
	Group         Group       `gorm:"foreignKey:GroupID" json:"group"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
}

// Group represents a user group with a billing multiplier
type Group struct {
	ID         uint            `gorm:"primaryKey" json:"id"`
	Name       string          `gorm:"uniqueIndex;size:50" json:"name"`
	Multiplier decimal.Decimal `gorm:"type:decimal(10,4);default:1.0" json:"multiplier"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

// UserGroupMembership assigns a user to a group, optionally for a limited time.
type UserGroupMembership struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	UserID    uint       `gorm:"uniqueIndex:idx_user_group_membership;not null" json:"user_id"`
	User      User       `gorm:"foreignKey:UserID" json:"-"`
	GroupID   uint       `gorm:"uniqueIndex:idx_user_group_membership;not null" json:"group_id"`
	Group     Group      `gorm:"foreignKey:GroupID" json:"group"`
	ExpiresAt *time.Time `json:"expires_at"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// ChannelGroupMultiplier overrides a group multiplier for an upstream channel.
type ChannelGroupMultiplier struct {
	ID         uint            `gorm:"primaryKey" json:"id"`
	ChannelID  uint            `gorm:"uniqueIndex:idx_channel_group_multiplier;not null" json:"channel_id"`
	Channel    Channel         `gorm:"foreignKey:ChannelID" json:"-"`
	GroupID    uint            `gorm:"uniqueIndex:idx_channel_group_multiplier;not null" json:"group_id"`
	Group      Group           `gorm:"foreignKey:GroupID" json:"group"`
	Multiplier decimal.Decimal `gorm:"type:decimal(10,4);default:1.0" json:"multiplier"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

// ModelGroupMultiplier overrides a group multiplier for a specific upstream model config.
type ModelGroupMultiplier struct {
	ID            uint            `gorm:"primaryKey" json:"id"`
	ModelConfigID uint            `gorm:"uniqueIndex:idx_model_group_multiplier;not null" json:"model_config_id"`
	ModelConfig   ModelConfig     `gorm:"foreignKey:ModelConfigID" json:"-"`
	GroupID       uint            `gorm:"uniqueIndex:idx_model_group_multiplier;not null" json:"group_id"`
	Group         Group           `gorm:"foreignKey:GroupID" json:"group"`
	Multiplier    decimal.Decimal `gorm:"type:decimal(10,4);default:1.0" json:"multiplier"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// ReferralCommissionLog records commission credited to a referrer from referred usage.
type ReferralCommissionLog struct {
	ID             uint            `gorm:"primaryKey" json:"id"`
	ReferrerID     uint            `gorm:"index;not null" json:"referrer_id"`
	Referrer       User            `gorm:"foreignKey:ReferrerID;references:ID;belongsTo:User" json:"-"`
	ReferredUserID uint            `gorm:"index;not null" json:"referred_user_id"`
	ReferredUser   User            `gorm:"foreignKey:ReferredUserID" json:"referred_user,omitempty"`
	TokenLogID     uint            `gorm:"uniqueIndex;not null" json:"token_log_id"`
	TokenLog       TokenLog        `gorm:"-" json:"-"`
	BaseCost       decimal.Decimal `gorm:"type:decimal(20,10);not null" json:"base_cost"`
	Rate           decimal.Decimal `gorm:"type:decimal(10,6);not null" json:"rate"`
	Amount         decimal.Decimal `gorm:"type:decimal(20,10);not null" json:"amount"`
	CreatedAt      time.Time       `json:"created_at"`
}

// Channel represents an upstream API provider
type Channel struct {
	ID                  uint                     `gorm:"primaryKey" json:"id"`
	UserChannelID       *uint                    `gorm:"index" json:"user_channel_id"`
	UserChannel         UserChannel              `gorm:"foreignKey:UserChannelID" json:"user_channel"`
	Name                string                   `gorm:"size:100" json:"name"`
	Type                string                   `gorm:"size:50" json:"type"` // openai, claude
	BaseURL             string                   `gorm:"size:255" json:"base_url"`
	APIKey              string                   `gorm:"size:255" json:"api_key"`
	PluginConfigJSON    string                   `gorm:"type:text" json:"plugin_config"`
	Multiplier          decimal.Decimal          `gorm:"type:decimal(10,4);default:1.0" json:"multiplier"`
	Priority            int                      `gorm:"default:1" json:"priority"`
	Weight              int                      `gorm:"default:1" json:"weight"`
	Enabled             bool                     `gorm:"default:true" json:"enabled"`
	PriceSyncEnabled    bool                     `json:"price_sync_enabled"`
	PriceSyncCron       string                   `gorm:"size:100" json:"price_sync_cron"`
	PriceSyncLastAt     *time.Time               `json:"price_sync_last_at"`
	ConsecutiveFailures int                      `gorm:"default:0" json:"consecutive_failures"`
	LastFailureAt       *time.Time               `json:"last_failure_at"`
	LastFailureReason   string                   `gorm:"size:500" json:"last_failure_reason"`
	AutoDisabledAt      *time.Time               `json:"auto_disabled_at"`
	AutoDisabledReason  string                   `gorm:"size:500" json:"auto_disabled_reason"`
	LastHealthCheckedAt *time.Time               `json:"last_health_checked_at"`
	LastHealthStatus    string                   `gorm:"size:20" json:"last_health_status"`
	CreatedAt           time.Time                `json:"created_at"`
	UpdatedAt           time.Time                `json:"updated_at"`
	Models              []ModelConfig            `gorm:"foreignKey:ChannelID" json:"models"`
	GroupMultipliers    []ChannelGroupMultiplier `gorm:"foreignKey:ChannelID" json:"group_multipliers,omitempty"`
}

// Model represents a global model identity. It is not bound to any upstream channel.
type Model struct {
	ID                          uint               `gorm:"primaryKey" json:"id"`
	ModelName                   string             `gorm:"uniqueIndex;size:100;not null" json:"model_name"`
	Provider                    string             `gorm:"size:50" json:"provider"` // e.g., openai, deepseek
	ProviderIconURL             string             `gorm:"size:255" json:"provider_icon_url"`
	QuotaType                   int                `gorm:"default:0" json:"quota_type"`                             // 0 = per token, 1 = per request/item
	InputPrice                  decimal.Decimal    `gorm:"type:decimal(20,10);default:0" json:"input_price"`        // price per 1M tokens
	OutputPrice                 decimal.Decimal    `gorm:"type:decimal(20,10);default:0" json:"output_price"`       // price per 1M tokens
	CachedInputPrice            decimal.Decimal    `gorm:"type:decimal(20,10);default:0" json:"cached_input_price"` // cached input price per 1M tokens
	CacheWriteInputPrice        decimal.Decimal    `gorm:"type:decimal(20,10);default:0" json:"cache_write_input_price"`
	CacheWrite1hInputPrice      decimal.Decimal    `gorm:"column:cache_write_1h_input_price;type:decimal(20,10);default:0" json:"cache_write_1h_input_price"`
	ImageInputPrice             decimal.Decimal    `gorm:"type:decimal(20,10);default:0" json:"image_input_price"`
	ImageOutputPrice            decimal.Decimal    `gorm:"type:decimal(20,10);default:0" json:"image_output_price"`
	AudioInputPrice             decimal.Decimal    `gorm:"type:decimal(20,10);default:0" json:"audio_input_price"`
	AudioOutputPrice            decimal.Decimal    `gorm:"type:decimal(20,10);default:0" json:"audio_output_price"`
	InputPriceTiers             PriceTierList      `gorm:"type:text" json:"input_price_tiers"`
	OutputPriceTiers            PriceTierList      `gorm:"type:text" json:"output_price_tiers"`
	CachedInputPriceTiers       PriceTierList      `gorm:"type:text" json:"cached_input_price_tiers"`
	CacheWriteInputPriceTiers   PriceTierList      `gorm:"type:text" json:"cache_write_input_price_tiers"`
	CacheWrite1hInputPriceTiers PriceTierList      `gorm:"column:cache_write_1h_input_price_tiers;type:text" json:"cache_write_1h_input_price_tiers"`
	ImageInputPriceTiers        PriceTierList      `gorm:"type:text" json:"image_input_price_tiers"`
	ImageOutputPriceTiers       PriceTierList      `gorm:"type:text" json:"image_output_price_tiers"`
	AudioInputPriceTiers        PriceTierList      `gorm:"type:text" json:"audio_input_price_tiers"`
	AudioOutputPriceTiers       PriceTierList      `gorm:"type:text" json:"audio_output_price_tiers"`
	VideoBillingConfig          VideoBillingConfig `gorm:"type:text" json:"video_billing_config"`
	Enabled                     bool               `gorm:"default:true" json:"enabled"`
	CreatedAt                   time.Time          `json:"created_at"`
	UpdatedAt                   time.Time          `json:"updated_at"`
	Configs                     []ModelConfig      `gorm:"foreignKey:ModelID" json:"configs,omitempty"`
}

// ModelConfig represents an upstream channel's configuration for a global model.
type ModelConfig struct {
	ID                uint                   `gorm:"primaryKey" json:"id"`
	ChannelID         uint                   `json:"channel_id"`
	Channel           Channel                `gorm:"foreignKey:ChannelID" json:"channel,omitempty"`
	ModelID           uint                   `gorm:"index" json:"model_id"`
	Model             Model                  `gorm:"foreignKey:ModelID" json:"model,omitempty"`
	UpstreamModelName string                 `gorm:"size:100" json:"upstream_model_name"`
	InputPrice        decimal.Decimal        `gorm:"type:decimal(20,10);default:0" json:"input_price"`  // legacy price per 1M tokens
	OutputPrice       decimal.Decimal        `gorm:"type:decimal(20,10);default:0" json:"output_price"` // legacy price per 1M tokens
	Enabled           bool                   `gorm:"default:true" json:"enabled"`
	CreatedAt         time.Time              `json:"created_at"`
	UpdatedAt         time.Time              `json:"updated_at"`
	GroupMultipliers  []ModelGroupMultiplier `gorm:"foreignKey:ModelConfigID" json:"group_multipliers,omitempty"`

	// Compatibility fields for API responses and legacy request payloads. They are stored on Model.
	ModelName       string `gorm:"-" json:"model_name,omitempty"`
	Provider        string `gorm:"-" json:"provider,omitempty"`
	ProviderIconURL string `gorm:"-" json:"provider_icon_url,omitempty"`
}

// StatusMonitor defines a node that can be checked and shown on the public status page.
type StatusMonitor struct {
	ID              uint          `gorm:"primaryKey" json:"id"`
	Name            string        `gorm:"size:100;not null" json:"name"`
	TargetURL       string        `gorm:"size:500;not null" json:"target_url"`
	CheckType       string        `gorm:"size:20;default:http" json:"check_type"`
	Method          string        `gorm:"size:10;default:GET" json:"method"`
	IntervalSeconds int           `gorm:"default:60" json:"interval_seconds"`
	RetentionHours  int           `gorm:"default:168" json:"retention_hours"`
	Enabled         bool          `gorm:"default:true" json:"enabled"`
	LastStatus      string        `gorm:"size:20;default:pending" json:"last_status"`
	LastLatencyMs   int           `json:"last_latency_ms"`
	LastStatusCode  int           `json:"last_status_code"`
	LastMessage     string        `gorm:"size:500" json:"last_message"`
	LastCheckedAt   *time.Time    `json:"last_checked_at"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
	Checks          []StatusCheck `gorm:"-" json:"checks,omitempty"`
}

// StatusCheck records a single status-monitor result.
type StatusCheck struct {
	ID         uint          `gorm:"primaryKey" json:"id"`
	MonitorID  uint          `gorm:"index;not null" json:"monitor_id"`
	Monitor    StatusMonitor `gorm:"-" json:"-"`
	Status     string        `gorm:"size:20;not null" json:"status"`
	LatencyMs  int           `json:"latency_ms"`
	StatusCode int           `json:"status_code"`
	Message    string        `gorm:"size:500" json:"message"`
	CheckedAt  time.Time     `gorm:"index" json:"checked_at"`
	CreatedAt  time.Time     `json:"created_at"`
}

// Announcement stores dashboard announcements shown to users.
type Announcement struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Title     string    `gorm:"size:120;not null" json:"title"`
	Content   string    `gorm:"type:text;not null" json:"content"`
	Enabled   bool      `gorm:"default:true" json:"enabled"`
	SortOrder int       `gorm:"default:0" json:"sort_order"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SystemSetting stores global UI and platform configuration.
type SystemSetting struct {
	Key       string    `gorm:"primaryKey;size:100" json:"key"`
	Value     string    `gorm:"type:text" json:"value"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Plugin stores an installed WASM plugin package.
type Plugin struct {
	ID               string    `gorm:"primaryKey;size:80" json:"id"`
	Name             string    `gorm:"size:120;not null" json:"name"`
	Version          string    `gorm:"size:50;not null" json:"version"`
	Description      string    `gorm:"type:text" json:"description"`
	Author           string    `gorm:"size:120" json:"author"`
	Enabled          bool      `gorm:"default:false" json:"enabled"`
	ManifestJSON     string    `gorm:"type:text;not null" json:"manifest_json"`
	PermissionsJSON  string    `gorm:"type:text" json:"permissions_json"`
	HooksJSON        string    `gorm:"type:text" json:"hooks_json"`
	FrontendJSON     string    `gorm:"type:text" json:"frontend_json"`
	SettingsJSON     string    `gorm:"type:text" json:"settings_json"`
	GlobalConfigJSON string    `gorm:"type:text" json:"-"`
	Path             string    `gorm:"size:500;not null" json:"path"`
	WASMPath         string    `gorm:"size:500" json:"wasm_path"`
	LastError        string    `gorm:"type:text" json:"last_error"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// UserPluginState stores a user's enabled state for an installed plugin.
type UserPluginState struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"uniqueIndex:idx_user_plugin_state;not null" json:"user_id"`
	User      User      `gorm:"foreignKey:UserID" json:"-"`
	PluginID  string    `gorm:"uniqueIndex:idx_user_plugin_state;size:80;not null" json:"plugin_id"`
	Plugin    Plugin    `gorm:"foreignKey:PluginID" json:"plugin"`
	Enabled   bool      `gorm:"default:false" json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// UserPluginConfig stores declarative plugin settings for a user.
type UserPluginConfig struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	UserID     uint      `gorm:"uniqueIndex:idx_user_plugin_config;not null" json:"user_id"`
	User       User      `gorm:"foreignKey:UserID" json:"-"`
	PluginID   string    `gorm:"uniqueIndex:idx_user_plugin_config;size:80;not null" json:"plugin_id"`
	Plugin     Plugin    `gorm:"foreignKey:PluginID" json:"plugin"`
	ConfigJSON string    `gorm:"type:text" json:"config_json"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// PluginKV stores plugin-owned key/value data.
type PluginKV struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    uint      `gorm:"uniqueIndex:idx_plugin_kv;not null" json:"user_id"`
	User      User      `gorm:"foreignKey:UserID" json:"-"`
	PluginID  string    `gorm:"uniqueIndex:idx_plugin_kv;size:80;not null" json:"plugin_id"`
	Plugin    Plugin    `gorm:"foreignKey:PluginID" json:"plugin"`
	Key       string    `gorm:"uniqueIndex:idx_plugin_kv;size:200;not null" json:"key"`
	ValueJSON string    `gorm:"type:text" json:"value_json"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// PluginLog records plugin runtime and management events.
type PluginLog struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    *uint     `gorm:"index" json:"user_id,omitempty"`
	User      User      `gorm:"-" json:"user,omitempty"`
	PluginID  string    `gorm:"index;size:80" json:"plugin_id"`
	Plugin    Plugin    `gorm:"-" json:"plugin"`
	Level     string    `gorm:"index;size:20;not null" json:"level"`
	Event     string    `gorm:"index;size:100;not null" json:"event"`
	Message   string    `gorm:"type:text" json:"message"`
	Metadata  string    `gorm:"type:text" json:"metadata,omitempty"`
	CreatedAt time.Time `gorm:"index" json:"created_at"`
}

// VideoTask tracks asynchronous video generation requests.
type VideoTask struct {
	ID                string          `gorm:"primaryKey;size:64" json:"id"`
	UserID            uint            `gorm:"index;not null" json:"user_id"`
	APIKeyID          *uint           `gorm:"index" json:"api_key_id,omitempty"`
	UserChannelID     *uint           `gorm:"index" json:"user_channel_id,omitempty"`
	ChannelID         uint            `gorm:"index;not null" json:"channel_id"`
	ModelConfigID     uint            `gorm:"index;not null" json:"model_config_id"`
	ModelName         string          `gorm:"size:100;not null" json:"model_name"`
	BillingModelName  string          `gorm:"size:100" json:"billing_model_name"`
	UpstreamTaskID    string          `gorm:"size:255" json:"upstream_task_id"`
	Status            string          `gorm:"index;size:32;not null" json:"status"`
	Cost              decimal.Decimal `gorm:"type:decimal(20,10);default:0" json:"cost"`
	RequestPayload    string          `gorm:"type:text" json:"-"`
	ResponsePayload   string          `gorm:"type:text" json:"-"`
	LastStatusPayload string          `gorm:"type:text" json:"-"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

// TokenLog records every request and its cost
type TokenLog struct {
	ID                      uint            `gorm:"primaryKey" json:"id"`
	UserID                  uint            `gorm:"index" json:"user_id"`
	APIKeyID                *uint           `gorm:"index" json:"api_key_id,omitempty"`
	UserChannelID           *uint           `gorm:"index" json:"user_channel_id,omitempty"`
	ChannelID               uint            `gorm:"index" json:"channel_id"`
	ModelConfigID           uint            `gorm:"index" json:"model_config_id"`
	ModelName               string          `gorm:"size:100" json:"model_name"`
	InputTokens             int             `json:"input_tokens"`
	OutputTokens            int             `json:"output_tokens"`
	CachedInputTokens       int             `gorm:"default:0" json:"cached_input_tokens"`
	CacheWriteInputTokens   int             `gorm:"default:0" json:"cache_write_input_tokens"`
	CacheWrite1hInputTokens int             `gorm:"column:cache_write_1h_input_tokens;default:0" json:"cache_write_1h_input_tokens"`
	ImageInputTokens        int             `gorm:"default:0" json:"image_input_tokens"`
	ImageOutputTokens       int             `gorm:"default:0" json:"image_output_tokens"`
	AudioInputTokens        int             `gorm:"default:0" json:"audio_input_tokens"`
	AudioOutputTokens       int             `gorm:"default:0" json:"audio_output_tokens"`
	ResponseTimeMs          int64           `gorm:"default:0" json:"response_time_ms"`
	FirstResponseTimeMs     int64           `gorm:"default:0" json:"first_response_time_ms"`
	BaseCost                decimal.Decimal `gorm:"type:decimal(20,10);default:0" json:"base_cost"`
	GroupMultiplier         decimal.Decimal `gorm:"type:decimal(20,10);default:1" json:"group_multiplier"`
	UserChannelMultiplier   decimal.Decimal `gorm:"type:decimal(20,10);default:1" json:"user_channel_multiplier"`
	InputPrice              decimal.Decimal `gorm:"type:decimal(20,10);default:0" json:"input_price"`
	OutputPrice             decimal.Decimal `gorm:"type:decimal(20,10);default:0" json:"output_price"`
	CachedInputPrice        decimal.Decimal `gorm:"type:decimal(20,10);default:0" json:"cached_input_price"`
	CacheWriteInputPrice    decimal.Decimal `gorm:"type:decimal(20,10);default:0" json:"cache_write_input_price"`
	CacheWrite1hInputPrice  decimal.Decimal `gorm:"type:decimal(20,10);default:0" json:"cache_write_1h_input_price"`
	PricingFormula          string          `gorm:"type:text" json:"pricing_formula"`
	Cost                    decimal.Decimal `gorm:"type:decimal(20,10)" json:"cost"`
	IP                      string          `gorm:"size:45" json:"ip"`
	UserAgent               string          `json:"user_agent"`
	CreatedAt               time.Time       `json:"created_at"`
}

// AuditLog records API access, login attempts, admin changes, and background system events.
type AuditLog struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	LogType    string    `gorm:"index;size:32;not null" json:"log_type"`
	Action     string    `gorm:"index;size:100;not null" json:"action"`
	Resource   string    `gorm:"index;size:255" json:"resource"`
	UserID     *uint     `gorm:"index" json:"user_id,omitempty"`
	User       User      `gorm:"-" json:"user,omitempty"`
	APIKeyID   *uint     `gorm:"index" json:"api_key_id,omitempty"`
	Method     string    `gorm:"size:12" json:"method"`
	Path       string    `gorm:"index;size:255" json:"path"`
	Query      string    `gorm:"size:1000" json:"query,omitempty"`
	StatusCode int       `gorm:"index" json:"status_code"`
	IPAddress  string    `gorm:"size:45" json:"ip_address"`
	UserAgent  string    `gorm:"size:500" json:"user_agent"`
	Message    string    `gorm:"size:1000" json:"message"`
	Metadata   string    `gorm:"type:text" json:"metadata,omitempty"`
	DurationMs int64     `json:"duration_ms"`
	CreatedAt  time.Time `gorm:"index" json:"created_at"`
}
