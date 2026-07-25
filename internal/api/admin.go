package api

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/veloce-ailab/veloce/internal/service"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// SystemAPI handles global platform configuration.
type SystemAPI struct{}

const (
	chatPageModeBasic            = "basic"
	chatPageModeAdvanced         = "advanced"
	authAgreementNotice          = "notice"
	authAgreementCheckbox        = "checkbox"
	configurationExportVersion   = 1
	configurationSectionSettings = "system_settings"
	configurationSectionChannels = "channels"
	configurationSectionModels   = "model_data"
	configurationSectionPrices   = "model_prices"
)

var configurationSections = map[string]struct{}{
	configurationSectionSettings: {},
	configurationSectionChannels: {},
	configurationSectionModels:   {},
	configurationSectionPrices:   {},
}

var sensitiveSystemSettingKeys = map[string]struct{}{
	"hcaptcha_secret":                     {},
	"smtp_password":                       {},
	"oidc_client_secret":                  {},
	"payment_yipay_key":                   {},
	"payment_openpayment_key":             {},
	"payment_wechat_private_key":          {},
	"payment_wechat_platform_certificate": {},
	"payment_wechat_api_v3_key":           {},
	"payment_alipay_private_key":          {},
	"payment_alipay_public_key":           {},
	"payment_paypal_client_secret":        {},
	"payment_stripe_secret_key":           {},
	"payment_stripe_webhook_secret":       {},
}

var sensitivePaymentChannelConfigKeys = map[string]struct{}{
	"key":                         {},
	"openpayment_key":             {},
	"wechat_private_key":          {},
	"wechat_platform_certificate": {},
	"wechat_api_v3_key":           {},
	"alipay_private_key":          {},
	"alipay_public_key":           {},
	"paypal_client_secret":        {},
	"stripe_secret_key":           {},
	"stripe_webhook_secret":       {},
}

type configurationExportRequest struct {
	Sections []string `json:"sections"`
}

// configurationExport is intentionally limited to operational configuration.
// It excludes users, API keys, balances, orders, logs, and every other
// business record.
type configurationExport struct {
	Version        int                        `json:"version"`
	ExportedAt     time.Time                  `json:"exported_at"`
	Sections       []string                   `json:"sections"`
	SystemSettings []model.SystemSetting      `json:"system_settings,omitempty"`
	UserChannels   []configurationUserChannel `json:"user_channels,omitempty"`
	Channels       []configurationChannel     `json:"channels,omitempty"`
	Models         []configurationModel       `json:"models,omitempty"`
	ModelPrices    []configurationModelPrice  `json:"model_prices,omitempty"`
	ModelConfigs   []configurationModelConfig `json:"model_configs,omitempty"`
}

type configurationUserChannel struct {
	Name             string          `json:"name"`
	Description      string          `json:"description"`
	Multiplier       decimal.Decimal `json:"multiplier"`
	RoutingAlgorithm string          `json:"routing_algorithm"`
	Enabled          bool            `json:"enabled"`
}

type configurationChannel struct {
	Name             string          `json:"name"`
	Type             string          `json:"type"`
	BaseURL          string          `json:"base_url"`
	APIKey           string          `json:"api_key"`
	PluginConfigJSON string          `json:"plugin_config"`
	UserChannelName  string          `json:"user_channel_name,omitempty"`
	Multiplier       decimal.Decimal `json:"multiplier"`
	Priority         int             `json:"priority"`
	Weight           int             `json:"weight"`
	Enabled          bool            `json:"enabled"`
	PriceSyncEnabled bool            `json:"price_sync_enabled"`
	PriceSyncCron    string          `json:"price_sync_cron"`
}

type configurationModel struct {
	ModelName       string `json:"model_name"`
	Provider        string `json:"provider"`
	ProviderIconURL string `json:"provider_icon_url"`
	Enabled         bool   `json:"enabled"`
}

type configurationModelPrice struct {
	ModelName                   string                   `json:"model_name"`
	QuotaType                   int                      `json:"quota_type"`
	InputPrice                  decimal.Decimal          `json:"input_price"`
	OutputPrice                 decimal.Decimal          `json:"output_price"`
	CachedInputPrice            decimal.Decimal          `json:"cached_input_price"`
	CacheWriteInputPrice        decimal.Decimal          `json:"cache_write_input_price"`
	CacheWrite1hInputPrice      decimal.Decimal          `json:"cache_write_1h_input_price"`
	ImageInputPrice             decimal.Decimal          `json:"image_input_price"`
	ImageOutputPrice            decimal.Decimal          `json:"image_output_price"`
	AudioInputPrice             decimal.Decimal          `json:"audio_input_price"`
	AudioOutputPrice            decimal.Decimal          `json:"audio_output_price"`
	InputPriceTiers             model.PriceTierList      `json:"input_price_tiers"`
	OutputPriceTiers            model.PriceTierList      `json:"output_price_tiers"`
	CachedInputPriceTiers       model.PriceTierList      `json:"cached_input_price_tiers"`
	CacheWriteInputPriceTiers   model.PriceTierList      `json:"cache_write_input_price_tiers"`
	CacheWrite1hInputPriceTiers model.PriceTierList      `json:"cache_write_1h_input_price_tiers"`
	ImageInputPriceTiers        model.PriceTierList      `json:"image_input_price_tiers"`
	ImageOutputPriceTiers       model.PriceTierList      `json:"image_output_price_tiers"`
	AudioInputPriceTiers        model.PriceTierList      `json:"audio_input_price_tiers"`
	AudioOutputPriceTiers       model.PriceTierList      `json:"audio_output_price_tiers"`
	VideoBillingConfig          model.VideoBillingConfig `json:"video_billing_config"`
}

type configurationModelConfig struct {
	ChannelName       string          `json:"channel_name"`
	ChannelBaseURL    string          `json:"channel_base_url"`
	ModelName         string          `json:"model_name"`
	UpstreamModelName string          `json:"upstream_model_name"`
	InputPrice        decimal.Decimal `json:"input_price"`
	OutputPrice       decimal.Decimal `json:"output_price"`
	Enabled           bool            `json:"enabled"`
}

type systemSettingsResponse struct {
	Edition                              string `json:"edition"`
	SystemMode                           string `json:"system_mode"`
	EnterpriseFeaturesEnabled            bool   `json:"enterprise_features_enabled"`
	SiteName                             string `json:"site_name"`
	BaseURL                              string `json:"base_url"`
	IconURL                              string `json:"icon_url"`
	FooterText                           string `json:"footer_text"`
	AboutHTML                            string `json:"about_html"`
	HomeIframeURL                        string `json:"home_iframe_url"`
	PrivacyPolicy                        string `json:"privacy_policy"`
	Terms                                string `json:"terms"`
	PrivacyPolicyURL                     string `json:"privacy_policy_url"`
	TermsURL                             string `json:"terms_url"`
	AuthAgreementMode                    string `json:"auth_agreement_mode"`
	Announcement                         string `json:"announcement"`
	TopNavEnabled                        bool   `json:"top_nav_enabled"`
	TopNavItems                          string `json:"top_nav_items"`
	PageLayouts                          string `json:"page_layouts"`
	ThemeLightBackground                 string `json:"theme_light_background"`
	ThemeLightForeground                 string `json:"theme_light_foreground"`
	ThemeLightCard                       string `json:"theme_light_card"`
	ThemeLightCardForeground             string `json:"theme_light_card_foreground"`
	ThemeLightPrimary                    string `json:"theme_light_primary"`
	ThemeLightPrimaryForeground          string `json:"theme_light_primary_foreground"`
	ThemeLightSecondary                  string `json:"theme_light_secondary"`
	ThemeLightSecondaryForeground        string `json:"theme_light_secondary_foreground"`
	ThemeLightAccent                     string `json:"theme_light_accent"`
	ThemeLightAccentForeground           string `json:"theme_light_accent_foreground"`
	ThemeLightMuted                      string `json:"theme_light_muted"`
	ThemeLightMutedForeground            string `json:"theme_light_muted_foreground"`
	ThemeLightBorder                     string `json:"theme_light_border"`
	ThemeDarkBackground                  string `json:"theme_dark_background"`
	ThemeDarkForeground                  string `json:"theme_dark_foreground"`
	ThemeDarkCard                        string `json:"theme_dark_card"`
	ThemeDarkCardForeground              string `json:"theme_dark_card_foreground"`
	ThemeDarkPrimary                     string `json:"theme_dark_primary"`
	ThemeDarkPrimaryForeground           string `json:"theme_dark_primary_foreground"`
	ThemeDarkSecondary                   string `json:"theme_dark_secondary"`
	ThemeDarkSecondaryForeground         string `json:"theme_dark_secondary_foreground"`
	ThemeDarkAccent                      string `json:"theme_dark_accent"`
	ThemeDarkAccentForeground            string `json:"theme_dark_accent_foreground"`
	ThemeDarkMuted                       string `json:"theme_dark_muted"`
	ThemeDarkMutedForeground             string `json:"theme_dark_muted_foreground"`
	ThemeDarkBorder                      string `json:"theme_dark_border"`
	ThemeBackgroundImage                 string `json:"theme_background_image"`
	ThemeCustomCSS                       string `json:"theme_custom_css"`
	SidebarDashboardEnabled              bool   `json:"sidebar_dashboard_enabled"`
	SidebarUsageEnabled                  bool   `json:"sidebar_usage_enabled"`
	SidebarWalletEnabled                 bool   `json:"sidebar_wallet_enabled"`
	SidebarDataBoardEnabled              bool   `json:"sidebar_data_board_enabled"`
	SidebarAPIKeysEnabled                bool   `json:"sidebar_api_keys_enabled"`
	SidebarChatEnabled                   bool   `json:"sidebar_chat_enabled"`
	SidebarImagesEnabled                 bool   `json:"sidebar_images_enabled"`
	SidebarSettingsEnabled               bool   `json:"sidebar_settings_enabled"`
	SidebarSystemEnabled                 bool   `json:"sidebar_system_enabled"`
	SidebarAdminOverviewEnabled          bool   `json:"sidebar_admin_overview_enabled"`
	SidebarChannelsEnabled               bool   `json:"sidebar_channels_enabled"`
	SidebarModelsEnabled                 bool   `json:"sidebar_models_enabled"`
	SidebarUsersEnabled                  bool   `json:"sidebar_users_enabled"`
	ChatPageMode                         string `json:"chat_page_mode"`
	MessageChannelEnabled                bool   `json:"message_channel_enabled"`
	ReferralEnabled                      bool   `json:"referral_enabled"`
	ReferralCommissionRate               string `json:"referral_commission_rate"`
	GroupMultiplierMode                  string `json:"group_multiplier_mode"`
	PricingEndpointEnabled               bool   `json:"pricing_endpoint_enabled"`
	StatusMonitorEnabled                 bool   `json:"status_monitor_enabled"`
	ReliabilityAutoDisableEnabled        bool   `json:"reliability_auto_disable_enabled"`
	ReliabilityDisableAfterFailures      string `json:"reliability_disable_after_failures"`
	ReliabilityAutoDetectUpstreamEnabled bool   `json:"reliability_auto_detect_upstream_enabled"`
	ReliabilityAutoDetectIntervalSeconds string `json:"reliability_auto_detect_interval_seconds"`
	ReliabilityAutoDetectTimeoutSeconds  string `json:"reliability_auto_detect_timeout_seconds"`
	ReliabilityAutoRecoverEnabled        bool   `json:"reliability_auto_recover_enabled"`
	ReliabilityRecoveryAfterSeconds      string `json:"reliability_recovery_after_seconds"`
	LogRetentionAPIDays                  string `json:"log_retention_api_days"`
	LogRetentionLoginDays                string `json:"log_retention_login_days"`
	LogRetentionAdminDays                string `json:"log_retention_admin_days"`
	LogRetentionSystemDays               string `json:"log_retention_system_days"`
	LogRetentionTokenDays                string `json:"log_retention_token_days"`
	LogRetentionCleanupIntervalHours     string `json:"log_retention_cleanup_interval_hours"`
	LogStorageMode                       string `json:"log_storage_mode"`
	LogRetentionDays                     string `json:"log_retention_days"`
	CheckInEnabled                       bool   `json:"checkin_enabled"`
	CheckInDailyReward                   string `json:"checkin_daily_reward"`
	CheckInTimezone                      string `json:"checkin_timezone"`
	CheckInStreakEnabled                 bool   `json:"checkin_streak_enabled"`
	CheckInStreakCycleDays               string `json:"checkin_streak_cycle_days"`
	CheckInStreakRewards                 string `json:"checkin_streak_rewards"`
	CheckInRandomEnabled                 bool   `json:"checkin_random_enabled"`
	CheckInRandomMin                     string `json:"checkin_random_min"`
	CheckInRandomMax                     string `json:"checkin_random_max"`
	PaymentEnabled                       bool   `json:"payment_enabled"`
	PaymentCurrencyDisplayName           string `json:"payment_currency_display_name"`
	PaymentUSDToRMBRate                  string `json:"payment_usd_to_rmb_rate"`
	PaymentMinRechargeAmount             string `json:"payment_min_recharge_amount"`
	PaymentRechargePresets               string `json:"payment_recharge_presets"`
	PaymentMethods                       string `json:"payment_methods"`
	PaymentGatewayProvider               string `json:"payment_gateway_provider"`
	PaymentChannels                      string `json:"payment_channels"`
	PaymentYipayGatewayURL               string `json:"payment_yipay_gateway_url,omitempty"`
	PaymentYipayPID                      string `json:"payment_yipay_pid,omitempty"`
	PaymentYipayKey                      string `json:"payment_yipay_key,omitempty"`
	PaymentYipayNotifyURL                string `json:"payment_yipay_notify_url,omitempty"`
	PaymentYipayReturnURL                string `json:"payment_yipay_return_url,omitempty"`
	PaymentOpenPaymentBaseURL            string `json:"payment_openpayment_base_url,omitempty"`
	PaymentOpenPaymentConfigURL          string `json:"payment_openpayment_config_url,omitempty"`
	PaymentOpenPaymentMerchantID         string `json:"payment_openpayment_merchant_id,omitempty"`
	PaymentOpenPaymentKey                string `json:"payment_openpayment_key,omitempty"`
	PaymentOpenPaymentNotifyURL          string `json:"payment_openpayment_notify_url,omitempty"`
	PaymentOpenPaymentReturnURL          string `json:"payment_openpayment_return_url,omitempty"`
	PaymentOfficialCurrency              string `json:"payment_official_currency,omitempty"`
	PaymentWeChatMchID                   string `json:"payment_wechat_mch_id,omitempty"`
	PaymentWeChatAppID                   string `json:"payment_wechat_app_id,omitempty"`
	PaymentWeChatSerialNo                string `json:"payment_wechat_serial_no,omitempty"`
	PaymentWeChatPrivateKey              string `json:"payment_wechat_private_key,omitempty"`
	PaymentWeChatPlatformCertificate     string `json:"payment_wechat_platform_certificate,omitempty"`
	PaymentWeChatAPIV3Key                string `json:"payment_wechat_api_v3_key,omitempty"`
	PaymentAlipayAppID                   string `json:"payment_alipay_app_id,omitempty"`
	PaymentAlipayPrivateKey              string `json:"payment_alipay_private_key,omitempty"`
	PaymentAlipayPublicKey               string `json:"payment_alipay_public_key,omitempty"`
	PaymentAlipayGatewayURL              string `json:"payment_alipay_gateway_url,omitempty"`
	PaymentPayPalClientID                string `json:"payment_paypal_client_id,omitempty"`
	PaymentPayPalClientSecret            string `json:"payment_paypal_client_secret,omitempty"`
	PaymentPayPalBaseURL                 string `json:"payment_paypal_base_url,omitempty"`
	PaymentPayPalWebhookID               string `json:"payment_paypal_webhook_id,omitempty"`
	PaymentStripeSecretKey               string `json:"payment_stripe_secret_key,omitempty"`
	PaymentStripeWebhookSecret           string `json:"payment_stripe_webhook_secret,omitempty"`
	RateLimitEnabled                     bool   `json:"rate_limit_enabled"`
	RateLimitRequestsPerMinute           string `json:"rate_limit_requests_per_minute"`
	RateLimitBurst                       string `json:"rate_limit_burst"`
	SensitiveFilterEnabled               bool   `json:"sensitive_filter_enabled"`
	SensitiveWords                       string `json:"sensitive_words,omitempty"`
	SensitiveFilterScope                 string `json:"sensitive_filter_scope"`
	SSRFProtectionEnabled                bool   `json:"ssrf_protection_enabled"`
	SSRFAllowPrivateNetworks             bool   `json:"ssrf_allow_private_networks"`
	SSRFAllowedHosts                     string `json:"ssrf_allowed_hosts,omitempty"`
	OIDCEnabled                          bool   `json:"oidc_enabled"`
	PasskeyEnabled                       bool   `json:"passkey_enabled"`
	PasswordLoginEnabled                 bool   `json:"password_login_enabled"`
	PasswordRegistrationEnabled          bool   `json:"password_registration_enabled"`
	TokenAPIEnabled                      bool   `json:"token_api_enabled"`
	PasswordHCaptchaEnabled              bool   `json:"password_hcaptcha_enabled"`
	HCaptchaSiteKey                      string `json:"hcaptcha_site_key"`
	HCaptchaSecret                       string `json:"hcaptcha_secret,omitempty"`
	EmailVerificationRequired            bool   `json:"email_verification_required"`
	RegistrationEmailSuffixes            string `json:"registration_email_suffixes"`
	RegistrationEmailRouting             string `json:"registration_email_routing"`
	SMTPHost                             string `json:"smtp_host,omitempty"`
	SMTPPort                             string `json:"smtp_port,omitempty"`
	SMTPUsername                         string `json:"smtp_username,omitempty"`
	SMTPPassword                         string `json:"smtp_password,omitempty"`
	SMTPFrom                             string `json:"smtp_from,omitempty"`
	OIDCIssuer                           string `json:"oidc_issuer,omitempty"`
	OIDCClientID                         string `json:"oidc_client_id,omitempty"`
	OIDCClientSecret                     string `json:"oidc_client_secret,omitempty"`
	OIDCRedirectURL                      string `json:"oidc_redirect_url,omitempty"`
	OAuthProviders                       string `json:"oauth_providers,omitempty"`
	AutoUpdateEnabled                    bool   `json:"auto_update_enabled"`
	AutoUpdateIntervalHours              string `json:"auto_update_interval_hours"`
	RedisEnabled                         bool   `json:"redis_enabled"`
	RedisAddress                         string `json:"redis_address"`
	RedisUsername                        string `json:"redis_username"`
	RedisPasswordSet                     bool   `json:"redis_password_set"`
	RedisDatabase                        string `json:"redis_database"`
	RedisTLSEnabled                      bool   `json:"redis_tls_enabled"`
}

type systemSettingsInput struct {
	SystemMode                           *string `json:"system_mode"`
	SiteName                             *string `json:"site_name"`
	BaseURL                              *string `json:"base_url"`
	IconURL                              *string `json:"icon_url"`
	FooterText                           *string `json:"footer_text"`
	AboutHTML                            *string `json:"about_html"`
	HomeIframeURL                        *string `json:"home_iframe_url"`
	PrivacyPolicy                        *string `json:"privacy_policy"`
	Terms                                *string `json:"terms"`
	PrivacyPolicyURL                     *string `json:"privacy_policy_url"`
	TermsURL                             *string `json:"terms_url"`
	AuthAgreementMode                    *string `json:"auth_agreement_mode"`
	Announcement                         *string `json:"announcement"`
	TopNavEnabled                        *bool   `json:"top_nav_enabled"`
	TopNavItems                          *string `json:"top_nav_items"`
	PageLayouts                          *string `json:"page_layouts"`
	ThemeLightBackground                 *string `json:"theme_light_background"`
	ThemeLightForeground                 *string `json:"theme_light_foreground"`
	ThemeLightCard                       *string `json:"theme_light_card"`
	ThemeLightCardForeground             *string `json:"theme_light_card_foreground"`
	ThemeLightPrimary                    *string `json:"theme_light_primary"`
	ThemeLightPrimaryForeground          *string `json:"theme_light_primary_foreground"`
	ThemeLightSecondary                  *string `json:"theme_light_secondary"`
	ThemeLightSecondaryForeground        *string `json:"theme_light_secondary_foreground"`
	ThemeLightAccent                     *string `json:"theme_light_accent"`
	ThemeLightAccentForeground           *string `json:"theme_light_accent_foreground"`
	ThemeLightMuted                      *string `json:"theme_light_muted"`
	ThemeLightMutedForeground            *string `json:"theme_light_muted_foreground"`
	ThemeLightBorder                     *string `json:"theme_light_border"`
	ThemeDarkBackground                  *string `json:"theme_dark_background"`
	ThemeDarkForeground                  *string `json:"theme_dark_foreground"`
	ThemeDarkCard                        *string `json:"theme_dark_card"`
	ThemeDarkCardForeground              *string `json:"theme_dark_card_foreground"`
	ThemeDarkPrimary                     *string `json:"theme_dark_primary"`
	ThemeDarkPrimaryForeground           *string `json:"theme_dark_primary_foreground"`
	ThemeDarkSecondary                   *string `json:"theme_dark_secondary"`
	ThemeDarkSecondaryForeground         *string `json:"theme_dark_secondary_foreground"`
	ThemeDarkAccent                      *string `json:"theme_dark_accent"`
	ThemeDarkAccentForeground            *string `json:"theme_dark_accent_foreground"`
	ThemeDarkMuted                       *string `json:"theme_dark_muted"`
	ThemeDarkMutedForeground             *string `json:"theme_dark_muted_foreground"`
	ThemeDarkBorder                      *string `json:"theme_dark_border"`
	ThemeBackgroundImage                 *string `json:"theme_background_image"`
	ThemeCustomCSS                       *string `json:"theme_custom_css"`
	SidebarDashboardEnabled              *bool   `json:"sidebar_dashboard_enabled"`
	SidebarUsageEnabled                  *bool   `json:"sidebar_usage_enabled"`
	SidebarWalletEnabled                 *bool   `json:"sidebar_wallet_enabled"`
	SidebarDataBoardEnabled              *bool   `json:"sidebar_data_board_enabled"`
	SidebarAPIKeysEnabled                *bool   `json:"sidebar_api_keys_enabled"`
	SidebarChatEnabled                   *bool   `json:"sidebar_chat_enabled"`
	SidebarImagesEnabled                 *bool   `json:"sidebar_images_enabled"`
	SidebarSettingsEnabled               *bool   `json:"sidebar_settings_enabled"`
	SidebarSystemEnabled                 *bool   `json:"sidebar_system_enabled"`
	SidebarAdminOverviewEnabled          *bool   `json:"sidebar_admin_overview_enabled"`
	SidebarChannelsEnabled               *bool   `json:"sidebar_channels_enabled"`
	SidebarModelsEnabled                 *bool   `json:"sidebar_models_enabled"`
	SidebarUsersEnabled                  *bool   `json:"sidebar_users_enabled"`
	ChatPageMode                         *string `json:"chat_page_mode"`
	MessageChannelEnabled                *bool   `json:"message_channel_enabled"`
	ReferralEnabled                      *bool   `json:"referral_enabled"`
	ReferralCommissionRate               *string `json:"referral_commission_rate"`
	GroupMultiplierMode                  *string `json:"group_multiplier_mode"`
	PricingEndpointEnabled               *bool   `json:"pricing_endpoint_enabled"`
	StatusMonitorEnabled                 *bool   `json:"status_monitor_enabled"`
	ReliabilityAutoDisableEnabled        *bool   `json:"reliability_auto_disable_enabled"`
	ReliabilityDisableAfterFailures      *string `json:"reliability_disable_after_failures"`
	ReliabilityAutoDetectUpstreamEnabled *bool   `json:"reliability_auto_detect_upstream_enabled"`
	ReliabilityAutoDetectIntervalSeconds *string `json:"reliability_auto_detect_interval_seconds"`
	ReliabilityAutoDetectTimeoutSeconds  *string `json:"reliability_auto_detect_timeout_seconds"`
	ReliabilityAutoRecoverEnabled        *bool   `json:"reliability_auto_recover_enabled"`
	ReliabilityRecoveryAfterSeconds      *string `json:"reliability_recovery_after_seconds"`
	LogRetentionAPIDays                  *string `json:"log_retention_api_days"`
	LogRetentionLoginDays                *string `json:"log_retention_login_days"`
	LogRetentionAdminDays                *string `json:"log_retention_admin_days"`
	LogRetentionSystemDays               *string `json:"log_retention_system_days"`
	LogRetentionTokenDays                *string `json:"log_retention_token_days"`
	LogRetentionCleanupIntervalHours     *string `json:"log_retention_cleanup_interval_hours"`
	LogStorageMode                       *string `json:"log_storage_mode"`
	LogRetentionDays                     *string `json:"log_retention_days"`
	CheckInEnabled                       *bool   `json:"checkin_enabled"`
	CheckInDailyReward                   *string `json:"checkin_daily_reward"`
	CheckInTimezone                      *string `json:"checkin_timezone"`
	CheckInStreakEnabled                 *bool   `json:"checkin_streak_enabled"`
	CheckInStreakCycleDays               *string `json:"checkin_streak_cycle_days"`
	CheckInStreakRewards                 *string `json:"checkin_streak_rewards"`
	CheckInRandomEnabled                 *bool   `json:"checkin_random_enabled"`
	CheckInRandomMin                     *string `json:"checkin_random_min"`
	CheckInRandomMax                     *string `json:"checkin_random_max"`
	PaymentEnabled                       *bool   `json:"payment_enabled"`
	PaymentCurrencyDisplayName           *string `json:"payment_currency_display_name"`
	PaymentUSDToRMBRate                  *string `json:"payment_usd_to_rmb_rate"`
	PaymentMinRechargeAmount             *string `json:"payment_min_recharge_amount"`
	PaymentRechargePresets               *string `json:"payment_recharge_presets"`
	PaymentMethods                       *string `json:"payment_methods"`
	PaymentGatewayProvider               *string `json:"payment_gateway_provider"`
	PaymentChannels                      *string `json:"payment_channels"`
	PaymentYipayGatewayURL               *string `json:"payment_yipay_gateway_url"`
	PaymentYipayPID                      *string `json:"payment_yipay_pid"`
	PaymentYipayKey                      *string `json:"payment_yipay_key"`
	PaymentYipayNotifyURL                *string `json:"payment_yipay_notify_url"`
	PaymentYipayReturnURL                *string `json:"payment_yipay_return_url"`
	PaymentOpenPaymentBaseURL            *string `json:"payment_openpayment_base_url"`
	PaymentOpenPaymentConfigURL          *string `json:"payment_openpayment_config_url"`
	PaymentOpenPaymentMerchantID         *string `json:"payment_openpayment_merchant_id"`
	PaymentOpenPaymentKey                *string `json:"payment_openpayment_key"`
	PaymentOpenPaymentNotifyURL          *string `json:"payment_openpayment_notify_url"`
	PaymentOpenPaymentReturnURL          *string `json:"payment_openpayment_return_url"`
	PaymentOfficialCurrency              *string `json:"payment_official_currency"`
	PaymentWeChatMchID                   *string `json:"payment_wechat_mch_id"`
	PaymentWeChatAppID                   *string `json:"payment_wechat_app_id"`
	PaymentWeChatSerialNo                *string `json:"payment_wechat_serial_no"`
	PaymentWeChatPrivateKey              *string `json:"payment_wechat_private_key"`
	PaymentWeChatPlatformCertificate     *string `json:"payment_wechat_platform_certificate"`
	PaymentWeChatAPIV3Key                *string `json:"payment_wechat_api_v3_key"`
	PaymentAlipayAppID                   *string `json:"payment_alipay_app_id"`
	PaymentAlipayPrivateKey              *string `json:"payment_alipay_private_key"`
	PaymentAlipayPublicKey               *string `json:"payment_alipay_public_key"`
	PaymentAlipayGatewayURL              *string `json:"payment_alipay_gateway_url"`
	PaymentPayPalClientID                *string `json:"payment_paypal_client_id"`
	PaymentPayPalClientSecret            *string `json:"payment_paypal_client_secret"`
	PaymentPayPalBaseURL                 *string `json:"payment_paypal_base_url"`
	PaymentPayPalWebhookID               *string `json:"payment_paypal_webhook_id"`
	PaymentStripeSecretKey               *string `json:"payment_stripe_secret_key"`
	PaymentStripeWebhookSecret           *string `json:"payment_stripe_webhook_secret"`
	RateLimitEnabled                     *bool   `json:"rate_limit_enabled"`
	RateLimitRequestsPerMinute           *string `json:"rate_limit_requests_per_minute"`
	RateLimitBurst                       *string `json:"rate_limit_burst"`
	SensitiveFilterEnabled               *bool   `json:"sensitive_filter_enabled"`
	SensitiveWords                       *string `json:"sensitive_words"`
	SensitiveFilterScope                 *string `json:"sensitive_filter_scope"`
	SSRFProtectionEnabled                *bool   `json:"ssrf_protection_enabled"`
	SSRFAllowPrivateNetworks             *bool   `json:"ssrf_allow_private_networks"`
	SSRFAllowedHosts                     *string `json:"ssrf_allowed_hosts"`
	OIDCEnabled                          *bool   `json:"oidc_enabled"`
	PasskeyEnabled                       *bool   `json:"passkey_enabled"`
	PasswordLoginEnabled                 *bool   `json:"password_login_enabled"`
	PasswordRegistrationEnabled          *bool   `json:"password_registration_enabled"`
	TokenAPIEnabled                      *bool   `json:"token_api_enabled"`
	PasswordHCaptchaEnabled              *bool   `json:"password_hcaptcha_enabled"`
	HCaptchaSiteKey                      *string `json:"hcaptcha_site_key"`
	HCaptchaSecret                       *string `json:"hcaptcha_secret"`
	EmailVerificationRequired            *bool   `json:"email_verification_required"`
	RegistrationEmailSuffixes            *string `json:"registration_email_suffixes"`
	RegistrationEmailRouting             *string `json:"registration_email_routing"`
	SMTPHost                             *string `json:"smtp_host"`
	SMTPPort                             *string `json:"smtp_port"`
	SMTPUsername                         *string `json:"smtp_username"`
	SMTPPassword                         *string `json:"smtp_password"`
	SMTPFrom                             *string `json:"smtp_from"`
	OIDCIssuer                           *string `json:"oidc_issuer"`
	OIDCClientID                         *string `json:"oidc_client_id"`
	OIDCClientSecret                     *string `json:"oidc_client_secret"`
	OIDCRedirectURL                      *string `json:"oidc_redirect_url"`
	OAuthProviders                       *string `json:"oauth_providers"`
	AutoUpdateEnabled                    *bool   `json:"auto_update_enabled"`
	AutoUpdateIntervalHours              *string `json:"auto_update_interval_hours"`
	RedisEnabled                         *bool   `json:"redis_enabled"`
	RedisAddress                         *string `json:"redis_address"`
	RedisUsername                        *string `json:"redis_username"`
	RedisPassword                        *string `json:"redis_password"`
	RedisPasswordClear                   *bool   `json:"redis_password_clear"`
	RedisDatabase                        *string `json:"redis_database"`
	RedisTLSEnabled                      *bool   `json:"redis_tls_enabled"`
}

func (api *SystemAPI) PublicSettings(c *gin.Context) {
	c.JSON(http.StatusOK, currentPublicSystemSettings())
}

func (api *SystemAPI) GetSettings(c *gin.Context) {
	c.JSON(http.StatusOK, currentAdminSystemSettings())
}

func (api *SystemAPI) GetAutoUpdateStatus(c *gin.Context) {
	c.JSON(http.StatusOK, service.CurrentAutoUpdateStatus())
}

func (api *SystemAPI) CheckForUpdate(c *gin.Context) {
	updater := service.NewAutoUpdateService()
	status, err := updater.CheckNow(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error(), "status": status})
		return
	}
	c.JSON(http.StatusOK, status)
}

func (api *SystemAPI) StartAutoUpdate(c *gin.Context) {
	status, err := service.StartManualAutoUpdate()
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "status": status})
		return
	}
	c.JSON(http.StatusAccepted, status)
}

func (api *SystemAPI) Restart(c *gin.Context) {
	var input struct {
		Confirmation string `json:"confirmation"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || input.Confirmation != "确认重启" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请输入“确认重启”以继续"})
		return
	}
	if err := service.RequestApplicationRestart(); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"message": "服务正在重启"})
}

func (api *SystemAPI) DeleteLogs(c *gin.Context) {
	deleted, err := model.DeleteLogs()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete logs"})
		return
	}
	var userID *uint
	if user, ok := currentUser(c); ok {
		userID = &user.ID
	}
	service.RecordAuditLog(service.AuditLogInput{LogType: service.AuditLogTypeSystem, Action: "logs_deleted", Resource: "log_databases", UserID: userID, Method: c.Request.Method, Path: c.Request.URL.Path, StatusCode: http.StatusOK, IPAddress: c.ClientIP(), UserAgent: c.Request.UserAgent(), Metadata: `{"rows":` + strconv.FormatInt(deleted, 10) + `}`})
	c.JSON(http.StatusOK, gin.H{"deleted": deleted})
}

func (api *SystemAPI) ExportConfiguration(c *gin.Context) {
	var input configurationExportRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid export request"})
		return
	}
	sections, err := normalizeConfigurationSections(input.Sections)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	export, err := buildConfigurationExport(sections)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to export configuration"})
		return
	}
	payload, err := json.MarshalIndent(export, "", "  ")
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to encode configuration export"})
		return
	}
	c.Header("Content-Disposition", `attachment; filename="flai-configuration-`+time.Now().Format("20060102-150405")+`.json"`)
	c.Data(http.StatusOK, "application/json; charset=utf-8", payload)
}

func (api *SystemAPI) ImportConfiguration(c *gin.Context) {
	var input configurationExport
	decoder := json.NewDecoder(io.LimitReader(c.Request.Body, 20<<20))
	if err := decoder.Decode(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid configuration file"})
		return
	}
	if input.Version != configurationExportVersion {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Unsupported configuration export version"})
		return
	}
	sections, err := normalizeConfigurationSections(input.Sections)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := importConfiguration(input, sections); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"imported_sections": input.Sections})
}

func normalizeConfigurationSections(values []string) (map[string]bool, error) {
	if len(values) == 0 {
		return nil, errors.New("Select at least one configuration section")
	}
	sections := make(map[string]bool, len(values))
	for _, value := range values {
		section := strings.ToLower(strings.TrimSpace(value))
		if _, ok := configurationSections[section]; !ok {
			return nil, errors.New("Unsupported configuration section")
		}
		sections[section] = true
	}
	return sections, nil
}

func buildConfigurationExport(sections map[string]bool) (configurationExport, error) {
	selected := make([]string, 0, len(sections))
	for _, section := range []string{configurationSectionSettings, configurationSectionChannels, configurationSectionModels, configurationSectionPrices} {
		if sections[section] {
			selected = append(selected, section)
		}
	}
	export := configurationExport{Version: configurationExportVersion, ExportedAt: time.Now().UTC(), Sections: selected}
	if sections[configurationSectionSettings] {
		if err := model.DB.Order("key ASC").Find(&export.SystemSettings).Error; err != nil {
			return configurationExport{}, err
		}
	}
	if sections[configurationSectionChannels] {
		var userChannels []model.UserChannel
		var channels []model.Channel
		if err := model.DB.Order("name ASC").Find(&userChannels).Error; err != nil {
			return configurationExport{}, err
		}
		if err := model.DB.Preload("UserChannel").Order("name ASC, base_url ASC").Find(&channels).Error; err != nil {
			return configurationExport{}, err
		}
		export.UserChannels = make([]configurationUserChannel, 0, len(userChannels))
		for _, channel := range userChannels {
			export.UserChannels = append(export.UserChannels, configurationUserChannel{Name: channel.Name, Description: channel.Description, Multiplier: channel.Multiplier, RoutingAlgorithm: channel.RoutingAlgorithm, Enabled: channel.Enabled})
		}
		export.Channels = make([]configurationChannel, 0, len(channels))
		for _, channel := range channels {
			userChannelName := ""
			if channel.UserChannelID != nil {
				userChannelName = channel.UserChannel.Name
			}
			export.Channels = append(export.Channels, configurationChannel{Name: channel.Name, Type: channel.Type, BaseURL: channel.BaseURL, APIKey: channel.APIKey, PluginConfigJSON: channel.PluginConfigJSON, UserChannelName: userChannelName, Multiplier: channel.Multiplier, Priority: channel.Priority, Weight: channel.Weight, Enabled: channel.Enabled, PriceSyncEnabled: channel.PriceSyncEnabled, PriceSyncCron: channel.PriceSyncCron})
		}
	}
	if sections[configurationSectionModels] || sections[configurationSectionPrices] {
		var models []model.Model
		if err := model.DB.Order("model_name ASC").Find(&models).Error; err != nil {
			return configurationExport{}, err
		}
		if sections[configurationSectionModels] {
			export.Models = make([]configurationModel, 0, len(models))
			for _, item := range models {
				export.Models = append(export.Models, configurationModel{ModelName: item.ModelName, Provider: item.Provider, ProviderIconURL: item.ProviderIconURL, Enabled: item.Enabled})
			}
		}
		if sections[configurationSectionPrices] {
			export.ModelPrices = make([]configurationModelPrice, 0, len(models))
			for _, item := range models {
				export.ModelPrices = append(export.ModelPrices, configurationModelPrice{ModelName: item.ModelName, QuotaType: item.QuotaType, InputPrice: item.InputPrice, OutputPrice: item.OutputPrice, CachedInputPrice: item.CachedInputPrice, CacheWriteInputPrice: item.CacheWriteInputPrice, CacheWrite1hInputPrice: item.CacheWrite1hInputPrice, ImageInputPrice: item.ImageInputPrice, ImageOutputPrice: item.ImageOutputPrice, AudioInputPrice: item.AudioInputPrice, AudioOutputPrice: item.AudioOutputPrice, InputPriceTiers: item.InputPriceTiers, OutputPriceTiers: item.OutputPriceTiers, CachedInputPriceTiers: item.CachedInputPriceTiers, CacheWriteInputPriceTiers: item.CacheWriteInputPriceTiers, CacheWrite1hInputPriceTiers: item.CacheWrite1hInputPriceTiers, ImageInputPriceTiers: item.ImageInputPriceTiers, ImageOutputPriceTiers: item.ImageOutputPriceTiers, AudioInputPriceTiers: item.AudioInputPriceTiers, AudioOutputPriceTiers: item.AudioOutputPriceTiers, VideoBillingConfig: item.VideoBillingConfig})
			}
			var configs []model.ModelConfig
			if err := model.DB.Preload("Channel").Preload("Model").Order("channel_id ASC, model_id ASC, upstream_model_name ASC").Find(&configs).Error; err != nil {
				return configurationExport{}, err
			}
			export.ModelConfigs = make([]configurationModelConfig, 0, len(configs))
			for _, item := range configs {
				export.ModelConfigs = append(export.ModelConfigs, configurationModelConfig{ChannelName: item.Channel.Name, ChannelBaseURL: item.Channel.BaseURL, ModelName: item.Model.ModelName, UpstreamModelName: item.UpstreamModelName, InputPrice: item.InputPrice, OutputPrice: item.OutputPrice, Enabled: item.Enabled})
			}
		}
	}
	return export, nil
}

func importConfiguration(input configurationExport, sections map[string]bool) error {
	return model.DB.Transaction(func(tx *gorm.DB) error {
		if sections[configurationSectionSettings] {
			for _, entry := range input.SystemSettings {
				entry.Key = strings.TrimSpace(entry.Key)
				if entry.Key == "" {
					return errors.New("System setting key is required")
				}
				if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"})}).Create(&model.SystemSetting{Key: entry.Key, Value: entry.Value}).Error; err != nil {
					return err
				}
			}
		}
		if sections[configurationSectionChannels] {
			if err := importConfigurationChannels(tx, input.UserChannels, input.Channels); err != nil {
				return err
			}
		}
		if sections[configurationSectionModels] {
			if err := importConfigurationModels(tx, input.Models); err != nil {
				return err
			}
		}
		if sections[configurationSectionPrices] {
			if err := importConfigurationPrices(tx, input.ModelPrices, input.ModelConfigs); err != nil {
				return err
			}
		}
		return nil
	})
}

func importConfigurationChannels(tx *gorm.DB, userChannels []configurationUserChannel, channels []configurationChannel) error {
	userChannelIDs := map[string]uint{}
	for _, item := range userChannels {
		item.Name = strings.TrimSpace(item.Name)
		if item.Name == "" {
			return errors.New("User channel name is required")
		}
		var existing model.UserChannel
		err := tx.Where("name = ?", item.Name).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			existing = model.UserChannel{Name: item.Name, Description: item.Description, Multiplier: item.Multiplier, RoutingAlgorithm: item.RoutingAlgorithm, Enabled: item.Enabled}
			if err := tx.Create(&existing).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if err := tx.Model(&existing).Updates(map[string]interface{}{"description": item.Description, "multiplier": item.Multiplier, "routing_algorithm": item.RoutingAlgorithm, "enabled": item.Enabled}).Error; err != nil {
			return err
		}
		userChannelIDs[item.Name] = existing.ID
	}
	for _, item := range channels {
		item.Name = strings.TrimSpace(item.Name)
		item.BaseURL = strings.TrimSpace(item.BaseURL)
		if item.Name == "" || item.BaseURL == "" {
			return errors.New("Channel name and base URL are required")
		}
		if err := service.ValidateConfiguredHTTPURL(item.BaseURL); err != nil {
			return errors.New("Channel has an unsafe or invalid base URL")
		}
		item.PriceSyncCron = service.NormalizePriceSyncCron(item.PriceSyncCron)
		if err := service.ValidatePriceSyncCron(item.PriceSyncCron); err != nil {
			return err
		}
		var userChannelID *uint
		if item.UserChannelName = strings.TrimSpace(item.UserChannelName); item.UserChannelName != "" {
			id, ok := userChannelIDs[item.UserChannelName]
			if !ok {
				var userChannel model.UserChannel
				if err := tx.Where("name = ?", item.UserChannelName).First(&userChannel).Error; err != nil {
					return errors.New("Referenced user channel was not found")
				}
				id = userChannel.ID
			}
			userChannelID = &id
		}
		candidate := model.Channel{Type: item.Type, PluginConfigJSON: item.PluginConfigJSON}
		if err := service.ValidatePluginUpstreamChannel(candidate); err != nil {
			return err
		}
		values := map[string]interface{}{"type": item.Type, "api_key": item.APIKey, "plugin_config_json": item.PluginConfigJSON, "user_channel_id": userChannelID, "multiplier": item.Multiplier, "priority": item.Priority, "weight": item.Weight, "enabled": item.Enabled, "price_sync_enabled": item.PriceSyncEnabled, "price_sync_cron": item.PriceSyncCron}
		var existing model.Channel
		err := tx.Where("name = ? AND base_url = ?", item.Name, item.BaseURL).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			existing = model.Channel{Name: item.Name, Type: item.Type, BaseURL: item.BaseURL, APIKey: item.APIKey, PluginConfigJSON: item.PluginConfigJSON, UserChannelID: userChannelID, Multiplier: item.Multiplier, Priority: item.Priority, Weight: item.Weight, Enabled: item.Enabled, PriceSyncEnabled: item.PriceSyncEnabled, PriceSyncCron: item.PriceSyncCron}
			if err := tx.Create(&existing).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if err := tx.Model(&existing).Updates(values).Error; err != nil {
			return err
		}
	}
	return nil
}

func importConfigurationModels(tx *gorm.DB, models []configurationModel) error {
	for _, item := range models {
		item.ModelName = strings.TrimSpace(item.ModelName)
		if item.ModelName == "" {
			return errors.New("Model name is required")
		}
		values := map[string]interface{}{"provider": item.Provider, "provider_icon_url": item.ProviderIconURL, "enabled": item.Enabled}
		var existing model.Model
		err := tx.Where("model_name = ?", item.ModelName).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := tx.Create(&model.Model{ModelName: item.ModelName, Provider: item.Provider, ProviderIconURL: item.ProviderIconURL, Enabled: item.Enabled}).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if err := tx.Model(&existing).Updates(values).Error; err != nil {
			return err
		}
	}
	return nil
}

func importConfigurationPrices(tx *gorm.DB, prices []configurationModelPrice, configs []configurationModelConfig) error {
	models := map[string]model.Model{}
	for _, item := range prices {
		item.ModelName = strings.TrimSpace(item.ModelName)
		if item.ModelName == "" {
			return errors.New("Model price requires a model name")
		}
		var existing model.Model
		if err := tx.Where("model_name = ?", item.ModelName).First(&existing).Error; err != nil {
			return errors.New("Model price references a model that was not found")
		}
		existing.QuotaType = item.QuotaType
		existing.InputPrice = item.InputPrice
		existing.OutputPrice = item.OutputPrice
		existing.CachedInputPrice = item.CachedInputPrice
		existing.CacheWriteInputPrice = item.CacheWriteInputPrice
		existing.CacheWrite1hInputPrice = item.CacheWrite1hInputPrice
		existing.ImageInputPrice = item.ImageInputPrice
		existing.ImageOutputPrice = item.ImageOutputPrice
		existing.AudioInputPrice = item.AudioInputPrice
		existing.AudioOutputPrice = item.AudioOutputPrice
		existing.InputPriceTiers = item.InputPriceTiers
		existing.OutputPriceTiers = item.OutputPriceTiers
		existing.CachedInputPriceTiers = item.CachedInputPriceTiers
		existing.CacheWriteInputPriceTiers = item.CacheWriteInputPriceTiers
		existing.CacheWrite1hInputPriceTiers = item.CacheWrite1hInputPriceTiers
		existing.ImageInputPriceTiers = item.ImageInputPriceTiers
		existing.ImageOutputPriceTiers = item.ImageOutputPriceTiers
		existing.AudioInputPriceTiers = item.AudioInputPriceTiers
		existing.AudioOutputPriceTiers = item.AudioOutputPriceTiers
		existing.VideoBillingConfig = item.VideoBillingConfig
		if err := tx.Save(&existing).Error; err != nil {
			return err
		}
		models[item.ModelName] = existing
	}
	for _, item := range configs {
		item.ChannelName = strings.TrimSpace(item.ChannelName)
		item.ChannelBaseURL = strings.TrimSpace(item.ChannelBaseURL)
		item.ModelName = strings.TrimSpace(item.ModelName)
		if item.ChannelName == "" || item.ChannelBaseURL == "" || item.ModelName == "" {
			return errors.New("Model configuration is missing a channel or model reference")
		}
		var channel model.Channel
		if err := tx.Where("name = ? AND base_url = ?", item.ChannelName, item.ChannelBaseURL).First(&channel).Error; err != nil {
			return errors.New("Model configuration references a channel that was not found")
		}
		modelItem, ok := models[item.ModelName]
		if !ok {
			if err := tx.Where("model_name = ?", item.ModelName).First(&modelItem).Error; err != nil {
				return errors.New("Model configuration references a model that was not found")
			}
		}
		var existing model.ModelConfig
		err := tx.Where("channel_id = ? AND model_id = ? AND upstream_model_name = ?", channel.ID, modelItem.ID, item.UpstreamModelName).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			existing = model.ModelConfig{ChannelID: channel.ID, ModelID: modelItem.ID, UpstreamModelName: item.UpstreamModelName, InputPrice: item.InputPrice, OutputPrice: item.OutputPrice, Enabled: item.Enabled}
			if err := tx.Create(&existing).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if err := tx.Model(&existing).Updates(map[string]interface{}{"input_price": item.InputPrice, "output_price": item.OutputPrice, "enabled": item.Enabled}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (api *SystemAPI) UpdateSettings(c *gin.Context) {
	var input systemSettingsInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var systemMode string
	if input.SystemMode != nil {
		systemMode = service.NormalizeSystemMode(*input.SystemMode)
	}

	var chatPageMode string
	if input.ChatPageMode != nil {
		chatPageMode = normalizeChatPageMode(*input.ChatPageMode)
		if chatPageMode == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid chat page mode"})
			return
		}
	}
	if input.MessageChannelEnabled != nil && *input.MessageChannelEnabled && service.CurrentEdition() != "premium" {
		c.JSON(http.StatusForbidden, gin.H{"error": "Message channel requires premium edition"})
		return
	}
	if input.PaymentEnabled != nil && *input.PaymentEnabled && !PaymentFeatureEnabled() {
		c.JSON(http.StatusForbidden, gin.H{"error": "Payment requires premium edition"})
		return
	}

	var authAgreementMode string
	if input.AuthAgreementMode != nil {
		authAgreementMode = normalizeAuthAgreementMode(*input.AuthAgreementMode)
		if authAgreementMode == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid auth agreement mode"})
			return
		}
	}

	var autoUpdateIntervalHours *string
	if input.AutoUpdateIntervalHours != nil {
		interval, err := strconv.Atoi(strings.TrimSpace(*input.AutoUpdateIntervalHours))
		if err != nil || interval < 1 || interval > 168 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Automatic update interval must be between 1 and 168 hours"})
			return
		}
		normalized := strconv.Itoa(interval)
		autoUpdateIntervalHours = &normalized
	}

	var logStorageMode *string
	if input.LogStorageMode != nil {
		mode := strings.ToLower(strings.TrimSpace(*input.LogStorageMode))
		if mode != model.LogStorageSingle && mode != model.LogStorageDaily {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Log storage mode must be single or daily"})
			return
		}
		logStorageMode = &mode
	}
	var logRetentionDays *string
	if input.LogRetentionDays != nil {
		days, err := strconv.Atoi(strings.TrimSpace(*input.LogRetentionDays))
		if err != nil || days < 1 || days > 3650 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Log retention must be between 1 and 3650 days"})
			return
		}
		normalized := strconv.Itoa(days)
		logRetentionDays = &normalized
	}

	var redisDatabase *string
	if input.RedisDatabase != nil {
		database, err := strconv.Atoi(strings.TrimSpace(*input.RedisDatabase))
		if err != nil || database < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Redis database must be a non-negative integer"})
			return
		}
		normalized := strconv.Itoa(database)
		redisDatabase = &normalized
	}
	if input.RedisEnabled != nil && *input.RedisEnabled {
		address := settingString("redis_address", "127.0.0.1:6379")
		if input.RedisAddress != nil {
			address = strings.TrimSpace(*input.RedisAddress)
		}
		if address == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Redis address is required when Redis is enabled"})
			return
		}
	}

	var oauthProvidersValue *string
	if input.OAuthProviders != nil {
		normalized, err := service.NormalizeOAuthProvidersJSON(*input.OAuthProviders)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		preserved := preserveOAuthProviderSecrets(normalized, settingString("oauth_providers", "[]"))
		oauthProvidersValue = &preserved
	}

	if input.SiteName != nil {
		siteName := strings.TrimSpace(*input.SiteName)
		if siteName == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Site name is required"})
			return
		}
		if len([]rune(siteName)) > 80 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Site name is too long"})
			return
		}
		if err := model.SetSystemSetting("site_name", siteName); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update system settings"})
			return
		}
	}
	if input.SystemMode != nil {
		previousSystemMode := service.CurrentSystemMode()
		if err := model.SetSystemSetting("system_mode", systemMode); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update system settings"})
			return
		}
		if systemMode == service.SystemModeEnterprise {
			if err := model.EnsureEnterpriseTenantForExistingUsers(model.DB); err != nil {
				_ = model.SetSystemSetting("system_mode", previousSystemMode)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to initialize enterprise mode"})
				return
			}
		}
	}

	stringSettings := map[string]*string{
		"base_url":                                 input.BaseURL,
		"icon_url":                                 input.IconURL,
		"footer_text":                              input.FooterText,
		"about_html":                               input.AboutHTML,
		"home_iframe_url":                          input.HomeIframeURL,
		"privacy_policy":                           input.PrivacyPolicy,
		"terms":                                    input.Terms,
		"privacy_policy_url":                       input.PrivacyPolicyURL,
		"terms_url":                                input.TermsURL,
		"announcement":                             input.Announcement,
		"top_nav_items":                            input.TopNavItems,
		"page_layouts":                             input.PageLayouts,
		"theme_light_background":                   input.ThemeLightBackground,
		"theme_light_foreground":                   input.ThemeLightForeground,
		"theme_light_card":                         input.ThemeLightCard,
		"theme_light_card_foreground":              input.ThemeLightCardForeground,
		"theme_light_primary":                      input.ThemeLightPrimary,
		"theme_light_primary_foreground":           input.ThemeLightPrimaryForeground,
		"theme_light_secondary":                    input.ThemeLightSecondary,
		"theme_light_secondary_foreground":         input.ThemeLightSecondaryForeground,
		"theme_light_accent":                       input.ThemeLightAccent,
		"theme_light_accent_foreground":            input.ThemeLightAccentForeground,
		"theme_light_muted":                        input.ThemeLightMuted,
		"theme_light_muted_foreground":             input.ThemeLightMutedForeground,
		"theme_light_border":                       input.ThemeLightBorder,
		"theme_dark_background":                    input.ThemeDarkBackground,
		"theme_dark_foreground":                    input.ThemeDarkForeground,
		"theme_dark_card":                          input.ThemeDarkCard,
		"theme_dark_card_foreground":               input.ThemeDarkCardForeground,
		"theme_dark_primary":                       input.ThemeDarkPrimary,
		"theme_dark_primary_foreground":            input.ThemeDarkPrimaryForeground,
		"theme_dark_secondary":                     input.ThemeDarkSecondary,
		"theme_dark_secondary_foreground":          input.ThemeDarkSecondaryForeground,
		"theme_dark_accent":                        input.ThemeDarkAccent,
		"theme_dark_accent_foreground":             input.ThemeDarkAccentForeground,
		"theme_dark_muted":                         input.ThemeDarkMuted,
		"theme_dark_muted_foreground":              input.ThemeDarkMutedForeground,
		"theme_dark_border":                        input.ThemeDarkBorder,
		"theme_background_image":                   input.ThemeBackgroundImage,
		"theme_custom_css":                         input.ThemeCustomCSS,
		"oidc_issuer":                              input.OIDCIssuer,
		"oidc_client_id":                           input.OIDCClientID,
		"oidc_client_secret":                       input.OIDCClientSecret,
		"oidc_redirect_url":                        input.OIDCRedirectURL,
		"oauth_providers":                          oauthProvidersValue,
		"auto_update_interval_hours":               autoUpdateIntervalHours,
		"referral_commission_rate":                 input.ReferralCommissionRate,
		"group_multiplier_mode":                    input.GroupMultiplierMode,
		"reliability_disable_after_failures":       input.ReliabilityDisableAfterFailures,
		"reliability_auto_detect_interval_seconds": input.ReliabilityAutoDetectIntervalSeconds,
		"reliability_auto_detect_timeout_seconds":  input.ReliabilityAutoDetectTimeoutSeconds,
		"reliability_recovery_after_seconds":       input.ReliabilityRecoveryAfterSeconds,
		"log_retention_api_days":                   input.LogRetentionAPIDays,
		"log_retention_login_days":                 input.LogRetentionLoginDays,
		"log_retention_admin_days":                 input.LogRetentionAdminDays,
		"log_retention_system_days":                input.LogRetentionSystemDays,
		"log_retention_token_days":                 input.LogRetentionTokenDays,
		"log_retention_cleanup_interval_hours":     input.LogRetentionCleanupIntervalHours,
		"log_storage_mode":                         logStorageMode,
		"log_retention_days":                       logRetentionDays,
		"checkin_daily_reward":                     input.CheckInDailyReward,
		"checkin_timezone":                         input.CheckInTimezone,
		"checkin_streak_cycle_days":                input.CheckInStreakCycleDays,
		"checkin_streak_rewards":                   input.CheckInStreakRewards,
		"checkin_random_min":                       input.CheckInRandomMin,
		"checkin_random_max":                       input.CheckInRandomMax,
		"payment_currency_display_name":            input.PaymentCurrencyDisplayName,
		"payment_usd_to_rmb_rate":                  input.PaymentUSDToRMBRate,
		"payment_min_recharge_amount":              input.PaymentMinRechargeAmount,
		"payment_recharge_presets":                 input.PaymentRechargePresets,
		"payment_methods":                          input.PaymentMethods,
		"payment_gateway_provider":                 input.PaymentGatewayProvider,
		"payment_channels":                         input.PaymentChannels,
		"payment_yipay_gateway_url":                input.PaymentYipayGatewayURL,
		"payment_yipay_pid":                        input.PaymentYipayPID,
		"payment_yipay_key":                        input.PaymentYipayKey,
		"payment_yipay_notify_url":                 input.PaymentYipayNotifyURL,
		"payment_yipay_return_url":                 input.PaymentYipayReturnURL,
		"payment_openpayment_base_url":             input.PaymentOpenPaymentBaseURL,
		"payment_openpayment_config_url":           input.PaymentOpenPaymentConfigURL,
		"payment_openpayment_merchant_id":          input.PaymentOpenPaymentMerchantID,
		"payment_openpayment_key":                  input.PaymentOpenPaymentKey,
		"payment_official_currency":                input.PaymentOfficialCurrency,
		"payment_wechat_mch_id":                    input.PaymentWeChatMchID,
		"payment_wechat_app_id":                    input.PaymentWeChatAppID,
		"payment_wechat_serial_no":                 input.PaymentWeChatSerialNo,
		"payment_wechat_private_key":               input.PaymentWeChatPrivateKey,
		"payment_wechat_platform_certificate":      input.PaymentWeChatPlatformCertificate,
		"payment_wechat_api_v3_key":                input.PaymentWeChatAPIV3Key,
		"payment_alipay_app_id":                    input.PaymentAlipayAppID,
		"payment_alipay_private_key":               input.PaymentAlipayPrivateKey,
		"payment_alipay_public_key":                input.PaymentAlipayPublicKey,
		"payment_alipay_gateway_url":               input.PaymentAlipayGatewayURL,
		"payment_paypal_client_id":                 input.PaymentPayPalClientID,
		"payment_paypal_client_secret":             input.PaymentPayPalClientSecret,
		"payment_paypal_base_url":                  input.PaymentPayPalBaseURL,
		"payment_paypal_webhook_id":                input.PaymentPayPalWebhookID,
		"payment_stripe_secret_key":                input.PaymentStripeSecretKey,
		"payment_stripe_webhook_secret":            input.PaymentStripeWebhookSecret,
		"rate_limit_requests_per_minute":           input.RateLimitRequestsPerMinute,
		"rate_limit_burst":                         input.RateLimitBurst,
		"sensitive_words":                          input.SensitiveWords,
		"sensitive_filter_scope":                   input.SensitiveFilterScope,
		"ssrf_allowed_hosts":                       input.SSRFAllowedHosts,
		"hcaptcha_site_key":                        input.HCaptchaSiteKey,
		"hcaptcha_secret":                          input.HCaptchaSecret,
		"smtp_host":                                input.SMTPHost,
		"smtp_port":                                input.SMTPPort,
		"smtp_username":                            input.SMTPUsername,
		"smtp_password":                            input.SMTPPassword,
		"smtp_from":                                input.SMTPFrom,
		"redis_address":                            input.RedisAddress,
		"redis_username":                           input.RedisUsername,
		"redis_database":                           redisDatabase,
	}
	for key, value := range stringSettings {
		if value == nil {
			continue
		}
		trimmed := strings.TrimSpace(*value)
		if isSensitiveSystemSetting(key) && trimmed == "" {
			continue
		}
		if key == "payment_channels" {
			trimmed = preservePaymentChannelSecrets(trimmed, settingString("payment_channels", "[]"))
		}
		if err := model.SetSystemSetting(key, trimmed); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update system settings"})
			return
		}
	}

	if input.ChatPageMode != nil {
		if err := model.SetSystemSetting("chat_page_mode", chatPageMode); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update system settings"})
			return
		}
	}
	if input.AuthAgreementMode != nil {
		if err := model.SetSystemSetting("auth_agreement_mode", authAgreementMode); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update system settings"})
			return
		}
	}

	boolSettings := map[string]*bool{
		"top_nav_enabled":                          input.TopNavEnabled,
		"sidebar_dashboard_enabled":                input.SidebarDashboardEnabled,
		"sidebar_usage_enabled":                    input.SidebarUsageEnabled,
		"sidebar_wallet_enabled":                   input.SidebarWalletEnabled,
		"sidebar_data_board_enabled":               input.SidebarDataBoardEnabled,
		"sidebar_api_keys_enabled":                 input.SidebarAPIKeysEnabled,
		"sidebar_chat_enabled":                     input.SidebarChatEnabled,
		"sidebar_images_enabled":                   input.SidebarImagesEnabled,
		"sidebar_settings_enabled":                 input.SidebarSettingsEnabled,
		"sidebar_system_enabled":                   input.SidebarSystemEnabled,
		"sidebar_admin_overview_enabled":           input.SidebarAdminOverviewEnabled,
		"sidebar_channels_enabled":                 input.SidebarChannelsEnabled,
		"sidebar_models_enabled":                   input.SidebarModelsEnabled,
		"sidebar_users_enabled":                    input.SidebarUsersEnabled,
		"message_channel_enabled":                  input.MessageChannelEnabled,
		"referral_enabled":                         input.ReferralEnabled,
		"pricing_endpoint_enabled":                 input.PricingEndpointEnabled,
		"status_monitor_enabled":                   input.StatusMonitorEnabled,
		"reliability_auto_disable_enabled":         input.ReliabilityAutoDisableEnabled,
		"reliability_auto_detect_upstream_enabled": input.ReliabilityAutoDetectUpstreamEnabled,
		"reliability_auto_recover_enabled":         input.ReliabilityAutoRecoverEnabled,
		"checkin_enabled":                          input.CheckInEnabled,
		"checkin_streak_enabled":                   input.CheckInStreakEnabled,
		"checkin_random_enabled":                   input.CheckInRandomEnabled,
		"payment_enabled":                          input.PaymentEnabled,
		"rate_limit_enabled":                       input.RateLimitEnabled,
		"sensitive_filter_enabled":                 input.SensitiveFilterEnabled,
		"ssrf_protection_enabled":                  input.SSRFProtectionEnabled,
		"ssrf_allow_private_networks":              input.SSRFAllowPrivateNetworks,
		"oidc_enabled":                             input.OIDCEnabled,
		"passkey_enabled":                          input.PasskeyEnabled,
		"password_login_enabled":                   input.PasswordLoginEnabled,
		"password_registration_enabled":            input.PasswordRegistrationEnabled,
		"token_api_enabled":                        input.TokenAPIEnabled,
		"password_hcaptcha_enabled":                input.PasswordHCaptchaEnabled,
		"email_verification_required":              input.EmailVerificationRequired,
		"auto_update_enabled":                      input.AutoUpdateEnabled,
		"redis_enabled":                            input.RedisEnabled,
		"redis_tls_enabled":                        input.RedisTLSEnabled,
	}
	if input.RedisPasswordClear != nil && *input.RedisPasswordClear {
		if err := model.SetSystemSetting("redis_password", ""); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update Redis password"})
			return
		}
	} else if input.RedisPassword != nil && *input.RedisPassword != "" {
		if err := model.SetSystemSetting("redis_password", *input.RedisPassword); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update Redis password"})
			return
		}
	}
	for key, value := range map[string]*string{"registration_email_suffixes": input.RegistrationEmailSuffixes, "registration_email_routing": input.RegistrationEmailRouting} {
		if value != nil {
			if err := model.SetSystemSetting(key, strings.TrimSpace(*value)); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update registration rules"})
				return
			}
		}
	}
	for key, value := range boolSettings {
		if value == nil {
			continue
		}
		if err := model.SetSystemSetting(key, strconv.FormatBool(*value)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update system settings"})
			return
		}
	}

	c.JSON(http.StatusOK, currentAdminSystemSettings())
}

func currentPublicSystemSettings() systemSettingsResponse {
	return systemSettingsResponse{
		Edition:                              service.CurrentEdition(),
		SystemMode:                           service.CurrentSystemMode(),
		EnterpriseFeaturesEnabled:            service.EnterpriseFeaturesEnabled(),
		SiteName:                             settingString("site_name", "flai"),
		BaseURL:                              settingString("base_url", ""),
		IconURL:                              settingString("icon_url", ""),
		FooterText:                           settingString("footer_text", ""),
		AboutHTML:                            settingString("about_html", ""),
		HomeIframeURL:                        settingString("home_iframe_url", ""),
		PrivacyPolicy:                        settingString("privacy_policy", ""),
		Terms:                                settingString("terms", ""),
		PrivacyPolicyURL:                     settingString("privacy_policy_url", ""),
		TermsURL:                             settingString("terms_url", ""),
		AuthAgreementMode:                    currentAuthAgreementMode(),
		Announcement:                         settingString("announcement", ""),
		TopNavEnabled:                        settingBool("top_nav_enabled", false),
		TopNavItems:                          settingString("top_nav_items", ""),
		PageLayouts:                          settingString("page_layouts", "{}"),
		ThemeLightBackground:                 settingString("theme_light_background", "#ffffff"),
		ThemeLightForeground:                 settingString("theme_light_foreground", "#020817"),
		ThemeLightCard:                       settingString("theme_light_card", "#ffffff"),
		ThemeLightCardForeground:             settingString("theme_light_card_foreground", "#020817"),
		ThemeLightPrimary:                    settingString("theme_light_primary", "#0f172a"),
		ThemeLightPrimaryForeground:          settingString("theme_light_primary_foreground", "#f8fafc"),
		ThemeLightSecondary:                  settingString("theme_light_secondary", "#f1f5f9"),
		ThemeLightSecondaryForeground:        settingString("theme_light_secondary_foreground", "#0f172a"),
		ThemeLightAccent:                     settingString("theme_light_accent", "#f1f5f9"),
		ThemeLightAccentForeground:           settingString("theme_light_accent_foreground", "#0f172a"),
		ThemeLightMuted:                      settingString("theme_light_muted", "#f1f5f9"),
		ThemeLightMutedForeground:            settingString("theme_light_muted_foreground", "#64748b"),
		ThemeLightBorder:                     settingString("theme_light_border", "#e2e8f0"),
		ThemeDarkBackground:                  settingString("theme_dark_background", "#020817"),
		ThemeDarkForeground:                  settingString("theme_dark_foreground", "#f8fafc"),
		ThemeDarkCard:                        settingString("theme_dark_card", "#020817"),
		ThemeDarkCardForeground:              settingString("theme_dark_card_foreground", "#f8fafc"),
		ThemeDarkPrimary:                     settingString("theme_dark_primary", "#f8fafc"),
		ThemeDarkPrimaryForeground:           settingString("theme_dark_primary_foreground", "#0f172a"),
		ThemeDarkSecondary:                   settingString("theme_dark_secondary", "#1e293b"),
		ThemeDarkSecondaryForeground:         settingString("theme_dark_secondary_foreground", "#f8fafc"),
		ThemeDarkAccent:                      settingString("theme_dark_accent", "#1e293b"),
		ThemeDarkAccentForeground:            settingString("theme_dark_accent_foreground", "#f8fafc"),
		ThemeDarkMuted:                       settingString("theme_dark_muted", "#1e293b"),
		ThemeDarkMutedForeground:             settingString("theme_dark_muted_foreground", "#94a3b8"),
		ThemeDarkBorder:                      settingString("theme_dark_border", "#1e293b"),
		ThemeBackgroundImage:                 settingString("theme_background_image", ""),
		ThemeCustomCSS:                       settingString("theme_custom_css", ""),
		SidebarDashboardEnabled:              settingBool("sidebar_dashboard_enabled", true),
		SidebarUsageEnabled:                  settingBool("sidebar_usage_enabled", true),
		SidebarWalletEnabled:                 settingBool("sidebar_wallet_enabled", true),
		SidebarDataBoardEnabled:              settingBool("sidebar_data_board_enabled", true),
		SidebarAPIKeysEnabled:                settingBool("sidebar_api_keys_enabled", true),
		SidebarChatEnabled:                   settingBool("sidebar_chat_enabled", true),
		SidebarImagesEnabled:                 settingBool("sidebar_images_enabled", true),
		SidebarSettingsEnabled:               settingBool("sidebar_settings_enabled", true),
		SidebarSystemEnabled:                 settingBool("sidebar_system_enabled", true),
		SidebarAdminOverviewEnabled:          settingBool("sidebar_admin_overview_enabled", true),
		SidebarChannelsEnabled:               settingBool("sidebar_channels_enabled", true),
		SidebarModelsEnabled:                 settingBool("sidebar_models_enabled", true),
		SidebarUsersEnabled:                  settingBool("sidebar_users_enabled", true),
		ChatPageMode:                         currentChatPageMode(),
		MessageChannelEnabled:                currentMessageChannelEnabled(),
		ReferralEnabled:                      settingBool("referral_enabled", false),
		ReferralCommissionRate:               settingString("referral_commission_rate", "0"),
		GroupMultiplierMode:                  settingString("group_multiplier_mode", "min"),
		PricingEndpointEnabled:               settingBool("pricing_endpoint_enabled", false),
		StatusMonitorEnabled:                 settingBool("status_monitor_enabled", false),
		ReliabilityAutoDisableEnabled:        settingBool("reliability_auto_disable_enabled", false),
		ReliabilityDisableAfterFailures:      settingString("reliability_disable_after_failures", "3"),
		ReliabilityAutoDetectUpstreamEnabled: settingBool("reliability_auto_detect_upstream_enabled", false),
		ReliabilityAutoDetectIntervalSeconds: settingString("reliability_auto_detect_interval_seconds", "300"),
		ReliabilityAutoDetectTimeoutSeconds:  settingString("reliability_auto_detect_timeout_seconds", "10"),
		ReliabilityAutoRecoverEnabled:        settingBool("reliability_auto_recover_enabled", false),
		ReliabilityRecoveryAfterSeconds:      settingString("reliability_recovery_after_seconds", "1800"),
		LogRetentionAPIDays:                  settingString("log_retention_api_days", "0"),
		LogRetentionLoginDays:                settingString("log_retention_login_days", "0"),
		LogRetentionAdminDays:                settingString("log_retention_admin_days", "0"),
		LogRetentionSystemDays:               settingString("log_retention_system_days", "0"),
		LogRetentionTokenDays:                settingString("log_retention_token_days", "0"),
		LogRetentionCleanupIntervalHours:     settingString("log_retention_cleanup_interval_hours", "24"),
		LogStorageMode:                       settingString("log_storage_mode", model.LogStorageSingle),
		LogRetentionDays:                     settingString("log_retention_days", "30"),
		CheckInEnabled:                       settingBool("checkin_enabled", false),
		CheckInDailyReward:                   settingString("checkin_daily_reward", "0"),
		CheckInTimezone:                      settingString("checkin_timezone", "Asia/Shanghai"),
		CheckInStreakEnabled:                 settingBool("checkin_streak_enabled", false),
		CheckInStreakCycleDays:               settingString("checkin_streak_cycle_days", "7"),
		CheckInStreakRewards:                 settingString("checkin_streak_rewards", "{}"),
		CheckInRandomEnabled:                 settingBool("checkin_random_enabled", false),
		CheckInRandomMin:                     settingString("checkin_random_min", "0"),
		CheckInRandomMax:                     settingString("checkin_random_max", "0"),
		PaymentEnabled:                       !service.PersonalModeEnabled() && PaymentFeatureEnabled() && settingBool("payment_enabled", false),
		PaymentCurrencyDisplayName:           settingString("payment_currency_display_name", "$"),
		PaymentUSDToRMBRate:                  settingString("payment_usd_to_rmb_rate", "7.20"),
		PaymentMinRechargeAmount:             settingString("payment_min_recharge_amount", "1"),
		PaymentRechargePresets:               settingString("payment_recharge_presets", "[\"5\",\"10\",\"20\",\"50\",\"100\"]"),
		PaymentMethods:                       settingString("payment_methods", "[\"alipay\",\"wxpay\"]"),
		PaymentGatewayProvider:               normalizePaymentProvider(settingString("payment_gateway_provider", paymentProviderYipay)),
		RateLimitEnabled:                     settingBool("rate_limit_enabled", true),
		RateLimitRequestsPerMinute:           settingString("rate_limit_requests_per_minute", "60"),
		RateLimitBurst:                       settingString("rate_limit_burst", "10"),
		SensitiveFilterEnabled:               settingBool("sensitive_filter_enabled", false),
		SensitiveFilterScope:                 settingString("sensitive_filter_scope", "request"),
		SSRFProtectionEnabled:                settingBool("ssrf_protection_enabled", true),
		SSRFAllowPrivateNetworks:             settingBool("ssrf_allow_private_networks", false),
		OIDCEnabled:                          settingBool("oidc_enabled", false),
		OAuthProviders:                       publicOAuthProvidersJSON(),
		PasskeyEnabled:                       settingBool("passkey_enabled", false),
		PasswordLoginEnabled:                 settingBool("password_login_enabled", true),
		PasswordRegistrationEnabled:          settingBool("password_registration_enabled", true),
		TokenAPIEnabled:                      settingBool("token_api_enabled", true),
		PasswordHCaptchaEnabled:              settingBool("password_hcaptcha_enabled", false),
		HCaptchaSiteKey:                      settingString("hcaptcha_site_key", ""),
		EmailVerificationRequired:            settingBool("email_verification_required", false),
		RegistrationEmailSuffixes:            settingString("registration_email_suffixes", ""),
		RegistrationEmailRouting:             settingString("registration_email_routing", "[]"),
		AutoUpdateEnabled:                    settingBool("auto_update_enabled", false),
		AutoUpdateIntervalHours:              settingString("auto_update_interval_hours", "24"),
	}
}

func currentAdminSystemSettings() systemSettingsResponse {
	settings := currentPublicSystemSettings()
	settings.OIDCIssuer = settingString("oidc_issuer", "")
	settings.OIDCClientID = settingString("oidc_client_id", "")
	settings.OIDCRedirectURL = settingString("oidc_redirect_url", "")
	settings.OAuthProviders = redactOAuthProviderSecrets(settingString("oauth_providers", "[]"))
	settings.SMTPHost = settingString("smtp_host", "")
	settings.SMTPPort = settingString("smtp_port", "587")
	settings.SMTPUsername = settingString("smtp_username", "")
	settings.SMTPFrom = settingString("smtp_from", "")
	settings.SSRFAllowedHosts = settingString("ssrf_allowed_hosts", "")
	settings.PaymentYipayGatewayURL = settingString("payment_yipay_gateway_url", "")
	settings.PaymentChannels = redactPaymentChannelSecrets(settingString("payment_channels", "[]"))
	settings.PaymentYipayPID = settingString("payment_yipay_pid", "")
	settings.PaymentYipayNotifyURL = settingString("payment_yipay_notify_url", "")
	settings.PaymentYipayReturnURL = settingString("payment_yipay_return_url", "")
	settings.PaymentOpenPaymentBaseURL = settingString("payment_openpayment_base_url", "")
	settings.PaymentOpenPaymentConfigURL = settingString("payment_openpayment_config_url", "")
	settings.PaymentOpenPaymentMerchantID = settingString("payment_openpayment_merchant_id", "")
	settings.PaymentOpenPaymentNotifyURL = callbackURLFromBaseURL(settings.BaseURL, "/api/payment/openpayment/notify")
	settings.PaymentOpenPaymentReturnURL = callbackURLFromBaseURL(settings.BaseURL, "/api/payment/openpayment/return")
	settings.PaymentOfficialCurrency = settingString("payment_official_currency", "CNY")
	settings.PaymentWeChatMchID = settingString("payment_wechat_mch_id", "")
	settings.PaymentWeChatAppID = settingString("payment_wechat_app_id", "")
	settings.PaymentWeChatSerialNo = settingString("payment_wechat_serial_no", "")
	settings.PaymentAlipayAppID = settingString("payment_alipay_app_id", "")
	settings.PaymentAlipayGatewayURL = settingString("payment_alipay_gateway_url", "https://openapi.alipay.com/gateway.do")
	settings.PaymentPayPalClientID = settingString("payment_paypal_client_id", "")
	settings.PaymentPayPalBaseURL = settingString("payment_paypal_base_url", "https://api-m.sandbox.paypal.com")
	settings.PaymentPayPalWebhookID = settingString("payment_paypal_webhook_id", "")
	settings.RedisEnabled = settingBool("redis_enabled", false)
	settings.RedisAddress = settingString("redis_address", "127.0.0.1:6379")
	settings.RedisUsername = settingString("redis_username", "")
	settings.RedisPasswordSet = settingString("redis_password", "") != ""
	settings.RedisDatabase = settingString("redis_database", "0")
	settings.RedisTLSEnabled = settingBool("redis_tls_enabled", false)
	return settings
}

func isSensitiveSystemSetting(key string) bool {
	_, ok := sensitiveSystemSettingKeys[key]
	return ok
}

func redactOAuthProviderSecrets(raw string) string {
	var providers []map[string]any
	if err := json.Unmarshal([]byte(raw), &providers); err != nil {
		return "[]"
	}
	for index := range providers {
		delete(providers[index], "client_secret")
	}
	encoded, err := json.Marshal(providers)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

func preserveOAuthProviderSecrets(updatedRaw, existingRaw string) string {
	var updated, existing []service.OAuthProviderConfig
	if err := json.Unmarshal([]byte(updatedRaw), &updated); err != nil {
		return updatedRaw
	}
	if err := json.Unmarshal([]byte(existingRaw), &existing); err != nil {
		return updatedRaw
	}
	existingSecrets := make(map[string]string, len(existing))
	for _, provider := range existing {
		existingSecrets[provider.Key] = provider.ClientSecret
	}
	for index := range updated {
		if strings.TrimSpace(updated[index].ClientSecret) == "" {
			updated[index].ClientSecret = existingSecrets[updated[index].Key]
		}
	}
	encoded, err := json.Marshal(updated)
	if err != nil {
		return updatedRaw
	}
	return string(encoded)
}

func redactPaymentChannelSecrets(raw string) string {
	var channels []storedPaymentChannel
	if err := json.Unmarshal([]byte(raw), &channels); err != nil {
		return "[]"
	}
	for index := range channels {
		for key := range sensitivePaymentChannelConfigKeys {
			delete(channels[index].Config, key)
		}
	}
	encoded, err := json.Marshal(channels)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

func preservePaymentChannelSecrets(updatedRaw, existingRaw string) string {
	var updated, existing []storedPaymentChannel
	if err := json.Unmarshal([]byte(updatedRaw), &updated); err != nil {
		return updatedRaw
	}
	if err := json.Unmarshal([]byte(existingRaw), &existing); err != nil {
		return updatedRaw
	}
	existingByID := make(map[string]storedPaymentChannel, len(existing))
	for _, channel := range existing {
		existingByID[channel.ID] = channel
	}
	for index := range updated {
		existingChannel, found := existingByID[updated[index].ID]
		if !found {
			continue
		}
		if updated[index].Config == nil {
			updated[index].Config = map[string]string{}
		}
		for key := range sensitivePaymentChannelConfigKeys {
			if strings.TrimSpace(updated[index].Config[key]) == "" {
				if value := existingChannel.Config[key]; value != "" {
					updated[index].Config[key] = value
				}
			}
		}
	}
	encoded, err := json.Marshal(updated)
	if err != nil {
		return updatedRaw
	}
	return string(encoded)
}

type publicOAuthProvider struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	LoginURL    string `json:"login_url"`
	CallbackURL string `json:"callback_url"`
}

func publicOAuthProvidersJSON() string {
	providers := service.EnabledOAuthProviderConfigs()
	publicProviders := make([]publicOAuthProvider, 0, len(providers))
	for _, provider := range providers {
		if provider.Key == "" || provider.Name == "" {
			continue
		}
		publicProviders = append(publicProviders, publicOAuthProvider{
			Key:         provider.Key,
			Name:        provider.Name,
			LoginURL:    "/auth/oauth/" + provider.Key + "/login",
			CallbackURL: service.OAuthCallbackURL(provider),
		})
	}
	body, err := json.Marshal(publicProviders)
	if err != nil {
		return "[]"
	}
	return string(body)
}

func currentChatPageMode() string {
	mode := normalizeChatPageMode(settingString("chat_page_mode", chatPageModeAdvanced))
	if mode == "" {
		return chatPageModeAdvanced
	}
	return mode
}

func currentMessageChannelEnabled() bool {
	return service.CurrentEdition() == "premium" && settingBool("message_channel_enabled", false)
}

func currentAuthAgreementMode() string {
	mode := normalizeAuthAgreementMode(settingString("auth_agreement_mode", authAgreementNotice))
	if mode == "" {
		return authAgreementNotice
	}
	return mode
}

func RequireAuthAgreementAccepted(accepted bool) error {
	if !authAgreementRequired() || accepted {
		return nil
	}
	return errors.New("agreement confirmation is required")
}

func authAgreementRequired() bool {
	if currentAuthAgreementMode() != authAgreementCheckbox {
		return false
	}
	return strings.TrimSpace(settingString("privacy_policy", "")) != "" ||
		strings.TrimSpace(settingString("terms", "")) != "" ||
		strings.TrimSpace(settingString("privacy_policy_url", "")) != "" ||
		strings.TrimSpace(settingString("terms_url", "")) != ""
}

func normalizeChatPageMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", chatPageModeBasic:
		return chatPageModeBasic
	case chatPageModeAdvanced:
		return chatPageModeAdvanced
	default:
		return ""
	}
}

func normalizeAuthAgreementMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", authAgreementNotice:
		return authAgreementNotice
	case authAgreementCheckbox:
		return authAgreementCheckbox
	default:
		return ""
	}
}

func ParseAgreementAccepted(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on", "accepted":
		return true
	default:
		return false
	}
}

func callbackURLFromBaseURL(baseURL, path string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return ""
	}
	return baseURL + path
}

func settingString(key, fallback string) string {
	return model.GetSystemSetting(key, fallback)
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

// AnnouncementAPI handles user-facing announcements.
type AnnouncementAPI struct{}

type announcementInput struct {
	Title     string `json:"title"`
	Content   string `json:"content"`
	Enabled   *bool  `json:"enabled"`
	SortOrder int    `json:"sort_order"`
}

func (api *AnnouncementAPI) PublicList(c *gin.Context) {
	var announcements []model.Announcement
	if err := model.DB.Where("enabled = ?", true).
		Order("sort_order ASC").
		Order("created_at DESC").
		Find(&announcements).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list announcements"})
		return
	}
	c.JSON(http.StatusOK, announcements)
}

func (api *AnnouncementAPI) List(c *gin.Context) {
	var announcements []model.Announcement
	if err := model.DB.Order("sort_order ASC").Order("created_at DESC").Find(&announcements).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list announcements"})
		return
	}
	c.JSON(http.StatusOK, announcements)
}

func (api *AnnouncementAPI) Create(c *gin.Context) {
	announcement, ok := bindAnnouncementInput(c)
	if !ok {
		return
	}
	if err := model.DB.Create(&announcement).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create announcement"})
		return
	}
	c.JSON(http.StatusOK, announcement)
}

func (api *AnnouncementAPI) Update(c *gin.Context) {
	var announcement model.Announcement
	if err := model.DB.First(&announcement, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Announcement not found"})
		return
	}
	next, ok := bindAnnouncementInput(c)
	if !ok {
		return
	}
	announcement.Title = next.Title
	announcement.Content = next.Content
	announcement.Enabled = next.Enabled
	announcement.SortOrder = next.SortOrder
	if err := model.DB.Save(&announcement).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update announcement"})
		return
	}
	c.JSON(http.StatusOK, announcement)
}

func (api *AnnouncementAPI) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 0)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid announcement id"})
		return
	}
	if err := model.DB.Delete(&model.Announcement{}, uint(id)).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete announcement"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Announcement deleted"})
}

func bindAnnouncementInput(c *gin.Context) (model.Announcement, bool) {
	var input announcementInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return model.Announcement{}, false
	}
	title := strings.TrimSpace(input.Title)
	content := strings.TrimSpace(input.Content)
	if title == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Announcement title is required"})
		return model.Announcement{}, false
	}
	if len([]rune(title)) > 120 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Announcement title is too long"})
		return model.Announcement{}, false
	}
	if content == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Announcement content is required"})
		return model.Announcement{}, false
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	return model.Announcement{
		Title:     title,
		Content:   content,
		Enabled:   enabled,
		SortOrder: input.SortOrder,
	}, true
}

// StatusMonitorAPI handles public status-monitor configuration and output.
type StatusMonitorAPI struct {
	StatusService *service.StatusService
}

type statusMonitorInput struct {
	Name            string `json:"name"`
	TargetURL       string `json:"target_url"`
	CheckType       string `json:"check_type"`
	Method          string `json:"method"`
	IntervalSeconds int    `json:"interval_seconds"`
	RetentionHours  int    `json:"retention_hours"`
	Enabled         *bool  `json:"enabled"`
}

type statusCheckResponse struct {
	ID         uint      `json:"id,omitempty"`
	Status     string    `json:"status"`
	LatencyMs  int       `json:"latency_ms"`
	StatusCode int       `json:"status_code,omitempty"`
	Message    string    `json:"message,omitempty"`
	CheckedAt  time.Time `json:"checked_at"`
}

type statusMonitorAdminResponse struct {
	ID              uint                  `json:"id"`
	Name            string                `json:"name"`
	TargetURL       string                `json:"target_url"`
	CheckType       string                `json:"check_type"`
	Method          string                `json:"method"`
	IntervalSeconds int                   `json:"interval_seconds"`
	RetentionHours  int                   `json:"retention_hours"`
	Enabled         bool                  `json:"enabled"`
	LastStatus      string                `json:"last_status"`
	LastLatencyMs   int                   `json:"last_latency_ms"`
	LastStatusCode  int                   `json:"last_status_code"`
	LastMessage     string                `json:"last_message"`
	LastCheckedAt   *time.Time            `json:"last_checked_at"`
	RecentChecks    []statusCheckResponse `json:"recent_checks"`
	CreatedAt       time.Time             `json:"created_at"`
	UpdatedAt       time.Time             `json:"updated_at"`
}

type publicStatusResponse struct {
	Enabled     bool                          `json:"enabled"`
	GeneratedAt time.Time                     `json:"generated_at"`
	Monitors    []publicStatusMonitorResponse `json:"monitors"`
}

type publicStatusMonitorResponse struct {
	ID            uint                  `json:"id"`
	Name          string                `json:"name"`
	Status        string                `json:"status"`
	LatencyMs     int                   `json:"latency_ms"`
	LastCheckedAt *time.Time            `json:"last_checked_at"`
	Uptime        float64               `json:"uptime"`
	RecentChecks  []statusCheckResponse `json:"recent_checks"`
}

func (api *StatusMonitorAPI) PublicStatus(c *gin.Context) {
	if !settingBool("status_monitor_enabled", false) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Not found"})
		return
	}

	var monitors []model.StatusMonitor
	if err := model.DB.Where("enabled = ?", true).Order("name ASC").Find(&monitors).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load status"})
		return
	}
	checksByMonitor := recentStatusChecks(monitors, 60)

	response := publicStatusResponse{
		Enabled:     true,
		GeneratedAt: time.Now(),
		Monitors:    make([]publicStatusMonitorResponse, 0, len(monitors)),
	}
	for _, monitor := range monitors {
		checks := publicStatusChecks(checksByMonitor[monitor.ID])
		response.Monitors = append(response.Monitors, publicStatusMonitorResponse{
			ID:            monitor.ID,
			Name:          monitor.Name,
			Status:        firstNonEmptyString(monitor.LastStatus, service.StatusPending),
			LatencyMs:     monitor.LastLatencyMs,
			LastCheckedAt: monitor.LastCheckedAt,
			Uptime:        uptimePercent(checks),
			RecentChecks:  checks,
		})
	}
	c.JSON(http.StatusOK, response)
}

func (api *StatusMonitorAPI) List(c *gin.Context) {
	var monitors []model.StatusMonitor
	if err := model.DB.Order("name ASC").Find(&monitors).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list status monitors"})
		return
	}
	checksByMonitor := recentStatusChecks(monitors, 30)
	response := make([]statusMonitorAdminResponse, 0, len(monitors))
	for _, monitor := range monitors {
		response = append(response, toStatusMonitorAdminResponse(monitor, checksByMonitor[monitor.ID]))
	}
	c.JSON(http.StatusOK, response)
}

func (api *StatusMonitorAPI) Create(c *gin.Context) {
	var input statusMonitorInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	monitor, err := statusMonitorFromInput(input, nil)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := model.DB.Create(&monitor).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create status monitor"})
		return
	}
	c.JSON(http.StatusOK, toStatusMonitorAdminResponse(monitor, nil))
}

func (api *StatusMonitorAPI) Update(c *gin.Context) {
	var monitor model.StatusMonitor
	if err := model.DB.First(&monitor, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Status monitor not found"})
		return
	}
	var input statusMonitorInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	updated, err := statusMonitorFromInput(input, &monitor)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	updated.ID = monitor.ID
	if err := model.DB.Save(&updated).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update status monitor"})
		return
	}
	checksByMonitor := recentStatusChecks([]model.StatusMonitor{updated}, 30)
	c.JSON(http.StatusOK, toStatusMonitorAdminResponse(updated, checksByMonitor[updated.ID]))
}

func (api *StatusMonitorAPI) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 0)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid status monitor id"})
		return
	}
	if err := model.DeleteStatusChecksForMonitor(uint(id)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete status monitor logs"})
		return
	}
	if err := model.DB.Delete(&model.StatusMonitor{}, uint(id)).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete status monitor"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Status monitor deleted"})
}

func (api *StatusMonitorAPI) CheckNow(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 0)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid status monitor id"})
		return
	}
	statusService := api.StatusService
	if statusService == nil {
		statusService = service.NewStatusService()
	}
	check, err := statusService.CheckMonitorByID(c.Request.Context(), uint(id))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check status monitor"})
		return
	}
	c.JSON(http.StatusOK, toStatusCheckResponse(check))
}

func statusMonitorFromInput(input statusMonitorInput, existing *model.StatusMonitor) (model.StatusMonitor, error) {
	monitor := model.StatusMonitor{
		CheckType:       service.StatusCheckHTTP,
		Method:          http.MethodGet,
		IntervalSeconds: 60,
		RetentionHours:  168,
		Enabled:         true,
		LastStatus:      service.StatusPending,
	}
	if existing != nil {
		monitor = *existing
	}

	monitor.Name = strings.TrimSpace(input.Name)
	monitor.TargetURL = strings.TrimSpace(input.TargetURL)
	if monitor.Name == "" {
		return model.StatusMonitor{}, errors.New("Name is required")
	}
	if monitor.TargetURL == "" {
		return model.StatusMonitor{}, errors.New("Target URL is required")
	}

	monitor.CheckType = strings.ToLower(strings.TrimSpace(input.CheckType))
	if monitor.CheckType != service.StatusCheckTCP {
		monitor.CheckType = service.StatusCheckHTTP
	}
	if err := service.ValidateConfiguredStatusTarget(monitor.TargetURL, monitor.CheckType); err != nil {
		return model.StatusMonitor{}, errors.New("Unsafe or invalid target URL")
	}
	monitor.Method = strings.ToUpper(strings.TrimSpace(input.Method))
	if monitor.Method != http.MethodHead {
		monitor.Method = http.MethodGet
	}
	monitor.IntervalSeconds = clampInt(input.IntervalSeconds, 10, 86400, 60)
	monitor.RetentionHours = clampInt(input.RetentionHours, 1, 8760, 168)
	if input.Enabled != nil {
		monitor.Enabled = *input.Enabled
	}
	if strings.TrimSpace(monitor.LastStatus) == "" {
		monitor.LastStatus = service.StatusPending
	}
	return monitor, nil
}

func toStatusMonitorAdminResponse(monitor model.StatusMonitor, checks []statusCheckResponse) statusMonitorAdminResponse {
	return statusMonitorAdminResponse{
		ID:              monitor.ID,
		Name:            monitor.Name,
		TargetURL:       monitor.TargetURL,
		CheckType:       monitor.CheckType,
		Method:          monitor.Method,
		IntervalSeconds: monitor.IntervalSeconds,
		RetentionHours:  monitor.RetentionHours,
		Enabled:         monitor.Enabled,
		LastStatus:      firstNonEmptyString(monitor.LastStatus, service.StatusPending),
		LastLatencyMs:   monitor.LastLatencyMs,
		LastStatusCode:  monitor.LastStatusCode,
		LastMessage:     monitor.LastMessage,
		LastCheckedAt:   monitor.LastCheckedAt,
		RecentChecks:    checks,
		CreatedAt:       monitor.CreatedAt,
		UpdatedAt:       monitor.UpdatedAt,
	}
}

func recentStatusChecks(monitors []model.StatusMonitor, limitPerMonitor int) map[uint][]statusCheckResponse {
	checksByMonitor := map[uint][]statusCheckResponse{}
	if len(monitors) == 0 || limitPerMonitor <= 0 {
		return checksByMonitor
	}

	monitorIDs := make([]uint, 0, len(monitors))
	for _, monitor := range monitors {
		monitorIDs = append(monitorIDs, monitor.ID)
	}
	checks, err := model.RecentStatusChecks(monitorIDs, limitPerMonitor)
	if err != nil {
		return checksByMonitor
	}
	for _, check := range checks {
		if len(checksByMonitor[check.MonitorID]) >= limitPerMonitor {
			continue
		}
		checksByMonitor[check.MonitorID] = append(checksByMonitor[check.MonitorID], toStatusCheckResponse(check))
	}
	for monitorID, monitorChecks := range checksByMonitor {
		for left, right := 0, len(monitorChecks)-1; left < right; left, right = left+1, right-1 {
			monitorChecks[left], monitorChecks[right] = monitorChecks[right], monitorChecks[left]
		}
		checksByMonitor[monitorID] = monitorChecks
	}
	return checksByMonitor
}

func toStatusCheckResponse(check model.StatusCheck) statusCheckResponse {
	return statusCheckResponse{
		ID:         check.ID,
		Status:     check.Status,
		LatencyMs:  check.LatencyMs,
		StatusCode: check.StatusCode,
		Message:    check.Message,
		CheckedAt:  check.CheckedAt,
	}
}

func publicStatusChecks(checks []statusCheckResponse) []statusCheckResponse {
	publicChecks := make([]statusCheckResponse, 0, len(checks))
	for _, check := range checks {
		publicChecks = append(publicChecks, statusCheckResponse{
			Status:    check.Status,
			LatencyMs: check.LatencyMs,
			CheckedAt: check.CheckedAt,
		})
	}
	return publicChecks
}

func uptimePercent(checks []statusCheckResponse) float64 {
	if len(checks) == 0 {
		return 0
	}
	up := 0
	for _, check := range checks {
		if check.Status == service.StatusUp {
			up++
		}
	}
	return float64(up) / float64(len(checks)) * 100
}

func clampInt(value int, min int, max int, fallback int) int {
	if value == 0 {
		value = fallback
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

// ChannelAPI handles management of upstream channels
type ChannelAPI struct {
	SyncService *service.SyncService
}

type groupMultiplierInput struct {
	GroupID    uint            `json:"group_id"`
	Multiplier decimal.Decimal `json:"multiplier"`
}

func (api *ChannelAPI) List(c *gin.Context) {
	var channels []model.Channel
	model.DB.Preload("UserChannel").Preload("Models").Preload("GroupMultipliers.Group").Find(&channels)
	c.JSON(http.StatusOK, channels)
}

func (api *ChannelAPI) Create(c *gin.Context) {
	var channel model.Channel
	if err := c.ShouldBindJSON(&channel); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := service.ValidatePluginUpstreamChannel(channel); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := service.ValidateConfiguredHTTPURL(channel.BaseURL); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Unsafe or invalid base URL"})
		return
	}
	if strings.TrimSpace(channel.PriceSyncCron) == "" {
		channel.PriceSyncEnabled = true
	}
	channel.PriceSyncCron = service.NormalizePriceSyncCron(channel.PriceSyncCron)
	if err := service.ValidatePriceSyncCron(channel.PriceSyncCron); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := model.DB.Create(&channel).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, channel)
}

func (api *ChannelAPI) Update(c *gin.Context) {
	id := c.Param("id")
	var channel model.Channel
	if err := model.DB.First(&channel, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Channel not found"})
		return
	}
	channelID := channel.ID
	wasEnabled := channel.Enabled
	consecutiveFailures := channel.ConsecutiveFailures
	lastFailureAt := channel.LastFailureAt
	lastFailureReason := channel.LastFailureReason
	autoDisabledAt := channel.AutoDisabledAt
	autoDisabledReason := channel.AutoDisabledReason
	lastHealthCheckedAt := channel.LastHealthCheckedAt
	lastHealthStatus := channel.LastHealthStatus
	if err := c.ShouldBindJSON(&channel); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := service.ValidatePluginUpstreamChannel(channel); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := service.ValidateConfiguredHTTPURL(channel.BaseURL); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Unsafe or invalid base URL"})
		return
	}
	channel.PriceSyncCron = service.NormalizePriceSyncCron(channel.PriceSyncCron)
	if err := service.ValidatePriceSyncCron(channel.PriceSyncCron); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	channel.ID = channelID
	channel.ConsecutiveFailures = consecutiveFailures
	channel.LastFailureAt = lastFailureAt
	channel.LastFailureReason = lastFailureReason
	channel.AutoDisabledAt = autoDisabledAt
	channel.AutoDisabledReason = autoDisabledReason
	channel.LastHealthCheckedAt = lastHealthCheckedAt
	channel.LastHealthStatus = lastHealthStatus
	if !wasEnabled && channel.Enabled {
		channel.ConsecutiveFailures = 0
		channel.LastFailureAt = nil
		channel.LastFailureReason = ""
		channel.AutoDisabledAt = nil
		channel.AutoDisabledReason = ""
		channel.LastHealthStatus = "pending"
	}
	if err := model.DB.Save(&channel).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, channel)
}

func (api *ChannelAPI) Delete(c *gin.Context) {
	id := c.Param("id")
	if err := model.DB.Delete(&model.Channel{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Channel deleted"})
}

func (api *ChannelAPI) SetGroupMultipliers(c *gin.Context) {
	var channel model.Channel
	if err := model.DB.First(&channel, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Channel not found"})
		return
	}
	var input []groupMultiplierInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("channel_id = ?", channel.ID).Delete(&model.ChannelGroupMultiplier{}).Error; err != nil {
			return err
		}
		for _, item := range input {
			if item.GroupID == 0 || item.Multiplier.IsZero() {
				continue
			}
			if err := tx.Create(&model.ChannelGroupMultiplier{
				ChannelID:  channel.ID,
				GroupID:    item.GroupID,
				Multiplier: item.Multiplier,
			}).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	model.DB.Preload("GroupMultipliers.Group").First(&channel, channel.ID)
	c.JSON(http.StatusOK, channel.GroupMultipliers)
}

func (api *ChannelAPI) Sync(c *gin.Context) {
	results := api.SyncService.SyncAll()
	c.JSON(http.StatusOK, gin.H{"message": "Sync finished", "results": results})
}

// ModelAPI handles model configuration for upstream channels.
type ModelAPI struct {
	SyncService *service.SyncService
}

type modelSyncInput struct {
	ChannelIDs []uint `json:"channel_ids"`
}

type modelSyncPreviewInput struct {
	ChannelID uint   `json:"channel_id"`
	Format    string `json:"format"`
	Path      string `json:"path"`
}

type modelSyncBrowserPreviewInput struct {
	ChannelID uint            `json:"channel_id"`
	Source    string          `json:"source"`
	Payload   json.RawMessage `json:"payload"`
}

type modelSyncApplyInput struct {
	ChannelID uint                    `json:"channel_id"`
	Models    []service.ModelSyncItem `json:"models"`
}

type modelConfigInput struct {
	ChannelID                   uint                     `json:"channel_id"`
	ModelID                     uint                     `json:"model_id"`
	ModelName                   string                   `json:"model_name"`
	UpstreamModelName           string                   `json:"upstream_model_name"`
	Provider                    string                   `json:"provider"`
	ProviderIconURL             string                   `json:"provider_icon_url"`
	QuotaType                   int                      `json:"quota_type"`
	InputPrice                  decimal.Decimal          `json:"input_price"`
	OutputPrice                 decimal.Decimal          `json:"output_price"`
	CachedInputPrice            decimal.Decimal          `json:"cached_input_price"`
	CacheWriteInputPrice        decimal.Decimal          `json:"cache_write_input_price"`
	CacheWrite1hInputPrice      decimal.Decimal          `json:"cache_write_1h_input_price"`
	ImageInputPrice             decimal.Decimal          `json:"image_input_price"`
	ImageOutputPrice            decimal.Decimal          `json:"image_output_price"`
	AudioInputPrice             decimal.Decimal          `json:"audio_input_price"`
	AudioOutputPrice            decimal.Decimal          `json:"audio_output_price"`
	InputPriceTiers             model.PriceTierList      `json:"input_price_tiers"`
	OutputPriceTiers            model.PriceTierList      `json:"output_price_tiers"`
	CachedInputPriceTiers       model.PriceTierList      `json:"cached_input_price_tiers"`
	CacheWriteInputPriceTiers   model.PriceTierList      `json:"cache_write_input_price_tiers"`
	CacheWrite1hInputPriceTiers model.PriceTierList      `json:"cache_write_1h_input_price_tiers"`
	ImageInputPriceTiers        model.PriceTierList      `json:"image_input_price_tiers"`
	ImageOutputPriceTiers       model.PriceTierList      `json:"image_output_price_tiers"`
	AudioInputPriceTiers        model.PriceTierList      `json:"audio_input_price_tiers"`
	AudioOutputPriceTiers       model.PriceTierList      `json:"audio_output_price_tiers"`
	VideoBillingConfig          model.VideoBillingConfig `json:"video_billing_config"`
	Enabled                     *bool                    `json:"enabled"`
}

type publicModelCatalogItem struct {
	ModelName                   string                   `json:"model_name"`
	Description                 string                   `json:"description,omitempty"`
	Provider                    string                   `json:"provider"`
	ProviderName                string                   `json:"provider_name"`
	ProviderIconURL             string                   `json:"provider_icon_url"`
	IsMetaModel                 bool                     `json:"is_meta_model"`
	MetaBillingMode             string                   `json:"meta_billing_mode,omitempty"`
	QuotaType                   int                      `json:"quota_type"`
	InputPrice                  decimal.Decimal          `json:"input_price"`
	OutputPrice                 decimal.Decimal          `json:"output_price"`
	CachedInputPrice            decimal.Decimal          `json:"cached_input_price"`
	CacheWriteInputPrice        decimal.Decimal          `json:"cache_write_input_price"`
	CacheWrite1hInputPrice      decimal.Decimal          `json:"cache_write_1h_input_price"`
	ImageInputPrice             decimal.Decimal          `json:"image_input_price"`
	ImageOutputPrice            decimal.Decimal          `json:"image_output_price"`
	AudioInputPrice             decimal.Decimal          `json:"audio_input_price"`
	AudioOutputPrice            decimal.Decimal          `json:"audio_output_price"`
	InputPriceTiers             model.PriceTierList      `json:"input_price_tiers"`
	OutputPriceTiers            model.PriceTierList      `json:"output_price_tiers"`
	CachedInputPriceTiers       model.PriceTierList      `json:"cached_input_price_tiers"`
	CacheWriteInputPriceTiers   model.PriceTierList      `json:"cache_write_input_price_tiers"`
	CacheWrite1hInputPriceTiers model.PriceTierList      `json:"cache_write_1h_input_price_tiers"`
	ImageInputPriceTiers        model.PriceTierList      `json:"image_input_price_tiers"`
	ImageOutputPriceTiers       model.PriceTierList      `json:"image_output_price_tiers"`
	AudioInputPriceTiers        model.PriceTierList      `json:"audio_input_price_tiers"`
	AudioOutputPriceTiers       model.PriceTierList      `json:"audio_output_price_tiers"`
	VideoBillingConfig          model.VideoBillingConfig `json:"video_billing_config"`
	UserChannels                []publicModelUserChannel `json:"user_channels"`
	ReferencedModels            []publicModelCatalogItem `json:"referenced_models,omitempty"`
}

type publicModelUserChannel struct {
	ID                                   uint                     `json:"id"`
	Name                                 string                   `json:"name"`
	Description                          string                   `json:"description"`
	Multiplier                           decimal.Decimal          `json:"multiplier"`
	QuotaType                            int                      `json:"quota_type"`
	InputPrice                           decimal.Decimal          `json:"input_price"`
	OutputPrice                          decimal.Decimal          `json:"output_price"`
	CachedInputPrice                     decimal.Decimal          `json:"cached_input_price"`
	CacheWriteInputPrice                 decimal.Decimal          `json:"cache_write_input_price"`
	CacheWrite1hInputPrice               decimal.Decimal          `json:"cache_write_1h_input_price"`
	ImageInputPrice                      decimal.Decimal          `json:"image_input_price"`
	ImageOutputPrice                     decimal.Decimal          `json:"image_output_price"`
	AudioInputPrice                      decimal.Decimal          `json:"audio_input_price"`
	AudioOutputPrice                     decimal.Decimal          `json:"audio_output_price"`
	InputPriceTiers                      model.PriceTierList      `json:"input_price_tiers"`
	OutputPriceTiers                     model.PriceTierList      `json:"output_price_tiers"`
	CachedInputPriceTiers                model.PriceTierList      `json:"cached_input_price_tiers"`
	CacheWriteInputPriceTiers            model.PriceTierList      `json:"cache_write_input_price_tiers"`
	CacheWrite1hInputPriceTiers          model.PriceTierList      `json:"cache_write_1h_input_price_tiers"`
	ImageInputPriceTiers                 model.PriceTierList      `json:"image_input_price_tiers"`
	ImageOutputPriceTiers                model.PriceTierList      `json:"image_output_price_tiers"`
	AudioInputPriceTiers                 model.PriceTierList      `json:"audio_input_price_tiers"`
	AudioOutputPriceTiers                model.PriceTierList      `json:"audio_output_price_tiers"`
	EffectiveInputPrice                  decimal.Decimal          `json:"effective_input_price"`
	EffectiveOutputPrice                 decimal.Decimal          `json:"effective_output_price"`
	EffectiveCachedInputPrice            decimal.Decimal          `json:"effective_cached_input_price"`
	EffectiveCacheWriteInputPrice        decimal.Decimal          `json:"effective_cache_write_input_price"`
	EffectiveCacheWrite1hInputPrice      decimal.Decimal          `json:"effective_cache_write_1h_input_price"`
	EffectiveImageInputPrice             decimal.Decimal          `json:"effective_image_input_price"`
	EffectiveImageOutputPrice            decimal.Decimal          `json:"effective_image_output_price"`
	EffectiveAudioInputPrice             decimal.Decimal          `json:"effective_audio_input_price"`
	EffectiveAudioOutputPrice            decimal.Decimal          `json:"effective_audio_output_price"`
	EffectiveInputPriceTiers             model.PriceTierList      `json:"effective_input_price_tiers"`
	EffectiveOutputPriceTiers            model.PriceTierList      `json:"effective_output_price_tiers"`
	EffectiveCachedInputPriceTiers       model.PriceTierList      `json:"effective_cached_input_price_tiers"`
	EffectiveCacheWriteInputPriceTiers   model.PriceTierList      `json:"effective_cache_write_input_price_tiers"`
	EffectiveCacheWrite1hInputPriceTiers model.PriceTierList      `json:"effective_cache_write_1h_input_price_tiers"`
	EffectiveImageInputPriceTiers        model.PriceTierList      `json:"effective_image_input_price_tiers"`
	EffectiveImageOutputPriceTiers       model.PriceTierList      `json:"effective_image_output_price_tiers"`
	EffectiveAudioInputPriceTiers        model.PriceTierList      `json:"effective_audio_input_price_tiers"`
	EffectiveAudioOutputPriceTiers       model.PriceTierList      `json:"effective_audio_output_price_tiers"`
	VideoBillingConfig                   model.VideoBillingConfig `json:"video_billing_config"`
	EffectiveVideoBillingConfig          model.VideoBillingConfig `json:"effective_video_billing_config"`
}

type publicModelCatalogAggregate struct {
	publicModelCatalogItem
	userChannelMap map[uint]*publicModelUserChannel
}

type pricingResponse struct {
	Success bool        `json:"success"`
	Message string      `json:"message"`
	Data    pricingData `json:"data"`
}

type pricingData struct {
	Unit                            string                              `json:"unit"`
	ModelRatio                      map[string]decimal.Decimal          `json:"model_ratio"`
	CompletionRatio                 map[string]decimal.Decimal          `json:"completion_ratio"`
	InputPrice                      map[string]decimal.Decimal          `json:"input_price"`
	OutputPrice                     map[string]decimal.Decimal          `json:"output_price"`
	CachedInputPrice                map[string]decimal.Decimal          `json:"cached_input_price"`
	CacheReadInputPrice             map[string]decimal.Decimal          `json:"cache_read_input_price"`
	CacheWriteInputPrice            map[string]decimal.Decimal          `json:"cache_write_input_price"`
	CacheWrite1hInputPrice          map[string]decimal.Decimal          `json:"cache_write_1h_input_price"`
	ImageInputPrice                 map[string]decimal.Decimal          `json:"image_input_price"`
	ImageOutputPrice                map[string]decimal.Decimal          `json:"image_output_price"`
	AudioInputPrice                 map[string]decimal.Decimal          `json:"audio_input_price"`
	AudioOutputPrice                map[string]decimal.Decimal          `json:"audio_output_price"`
	QuotaType                       map[string]int                      `json:"quota_type"`
	InputPriceTiers                 map[string]model.PriceTierList      `json:"input_price_tiers"`
	OutputPriceTiers                map[string]model.PriceTierList      `json:"output_price_tiers"`
	CachedInputPriceTiers           map[string]model.PriceTierList      `json:"cached_input_price_tiers"`
	CacheReadInputPriceTiers        map[string]model.PriceTierList      `json:"cache_read_input_price_tiers"`
	CacheWriteInputPriceTiers       map[string]model.PriceTierList      `json:"cache_write_input_price_tiers"`
	CacheWrite1hInputPriceTiers     map[string]model.PriceTierList      `json:"cache_write_1h_input_price_tiers"`
	ImageInputPriceTiers            map[string]model.PriceTierList      `json:"image_input_price_tiers"`
	ImageOutputPriceTiers           map[string]model.PriceTierList      `json:"image_output_price_tiers"`
	AudioInputPriceTiers            map[string]model.PriceTierList      `json:"audio_input_price_tiers"`
	AudioOutputPriceTiers           map[string]model.PriceTierList      `json:"audio_output_price_tiers"`
	VideoBillingConfig              map[string]model.VideoBillingConfig `json:"video_billing_config"`
	BaseInputPrice                  map[string]decimal.Decimal          `json:"base_input_price"`
	BaseOutputPrice                 map[string]decimal.Decimal          `json:"base_output_price"`
	BaseCachedInputPrice            map[string]decimal.Decimal          `json:"base_cached_input_price"`
	BaseCacheReadInputPrice         map[string]decimal.Decimal          `json:"base_cache_read_input_price"`
	BaseCacheWriteInputPrice        map[string]decimal.Decimal          `json:"base_cache_write_input_price"`
	BaseCacheWrite1hInputPrice      map[string]decimal.Decimal          `json:"base_cache_write_1h_input_price"`
	BaseImageInputPrice             map[string]decimal.Decimal          `json:"base_image_input_price"`
	BaseImageOutputPrice            map[string]decimal.Decimal          `json:"base_image_output_price"`
	BaseAudioInputPrice             map[string]decimal.Decimal          `json:"base_audio_input_price"`
	BaseAudioOutputPrice            map[string]decimal.Decimal          `json:"base_audio_output_price"`
	BaseQuotaType                   map[string]int                      `json:"base_quota_type"`
	BaseInputPriceTiers             map[string]model.PriceTierList      `json:"base_input_price_tiers"`
	BaseOutputPriceTiers            map[string]model.PriceTierList      `json:"base_output_price_tiers"`
	BaseCachedInputPriceTiers       map[string]model.PriceTierList      `json:"base_cached_input_price_tiers"`
	BaseCacheReadInputPriceTiers    map[string]model.PriceTierList      `json:"base_cache_read_input_price_tiers"`
	BaseCacheWriteInputPriceTiers   map[string]model.PriceTierList      `json:"base_cache_write_input_price_tiers"`
	BaseCacheWrite1hInputPriceTiers map[string]model.PriceTierList      `json:"base_cache_write_1h_input_price_tiers"`
	BaseImageInputPriceTiers        map[string]model.PriceTierList      `json:"base_image_input_price_tiers"`
	BaseImageOutputPriceTiers       map[string]model.PriceTierList      `json:"base_image_output_price_tiers"`
	BaseAudioInputPriceTiers        map[string]model.PriceTierList      `json:"base_audio_input_price_tiers"`
	BaseAudioOutputPriceTiers       map[string]model.PriceTierList      `json:"base_audio_output_price_tiers"`
	BaseVideoBillingConfig          map[string]model.VideoBillingConfig `json:"base_video_billing_config"`
	UserChannelPrices               map[string]pricingUserChannelPrice  `json:"user_channel_prices"`
}

type pricingUserChannelPrice struct {
	ID                          uint                                `json:"id"`
	Name                        string                              `json:"name"`
	Description                 string                              `json:"description"`
	Multiplier                  decimal.Decimal                     `json:"multiplier"`
	QuotaType                   map[string]int                      `json:"quota_type"`
	ModelRatio                  map[string]decimal.Decimal          `json:"model_ratio"`
	CompletionRatio             map[string]decimal.Decimal          `json:"completion_ratio"`
	InputPrice                  map[string]decimal.Decimal          `json:"input_price"`
	OutputPrice                 map[string]decimal.Decimal          `json:"output_price"`
	CachedInputPrice            map[string]decimal.Decimal          `json:"cached_input_price"`
	CacheReadInputPrice         map[string]decimal.Decimal          `json:"cache_read_input_price"`
	CacheWriteInputPrice        map[string]decimal.Decimal          `json:"cache_write_input_price"`
	CacheWrite1hInputPrice      map[string]decimal.Decimal          `json:"cache_write_1h_input_price"`
	ImageInputPrice             map[string]decimal.Decimal          `json:"image_input_price"`
	ImageOutputPrice            map[string]decimal.Decimal          `json:"image_output_price"`
	AudioInputPrice             map[string]decimal.Decimal          `json:"audio_input_price"`
	AudioOutputPrice            map[string]decimal.Decimal          `json:"audio_output_price"`
	InputPriceTiers             map[string]model.PriceTierList      `json:"input_price_tiers"`
	OutputPriceTiers            map[string]model.PriceTierList      `json:"output_price_tiers"`
	CachedInputPriceTiers       map[string]model.PriceTierList      `json:"cached_input_price_tiers"`
	CacheReadInputPriceTiers    map[string]model.PriceTierList      `json:"cache_read_input_price_tiers"`
	CacheWriteInputPriceTiers   map[string]model.PriceTierList      `json:"cache_write_input_price_tiers"`
	CacheWrite1hInputPriceTiers map[string]model.PriceTierList      `json:"cache_write_1h_input_price_tiers"`
	ImageInputPriceTiers        map[string]model.PriceTierList      `json:"image_input_price_tiers"`
	ImageOutputPriceTiers       map[string]model.PriceTierList      `json:"image_output_price_tiers"`
	AudioInputPriceTiers        map[string]model.PriceTierList      `json:"audio_input_price_tiers"`
	AudioOutputPriceTiers       map[string]model.PriceTierList      `json:"audio_output_price_tiers"`
	VideoBillingConfig          map[string]model.VideoBillingConfig `json:"video_billing_config"`
}

func (api *ModelAPI) PublicCatalog(c *gin.Context) {
	var configs []model.ModelConfig
	if err := model.DB.
		Preload("Channel.UserChannel").
		Preload("Model").
		Where("enabled = ?", true).
		Find(&configs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list models"})
		return
	}

	catalog := map[string]*publicModelCatalogAggregate{}
	for _, config := range configs {
		if !config.Channel.Enabled || config.Channel.UserChannelID == nil || !config.Channel.UserChannel.Enabled || !config.Model.Enabled {
			continue
		}
		modelName := strings.TrimSpace(config.Model.ModelName)
		if modelName == "" {
			continue
		}
		quotaType := config.Model.QuotaType
		inputPrice := config.Model.InputPrice
		outputPrice := config.Model.OutputPrice
		cachedInputPrice := config.Model.CachedInputPrice
		cacheWriteInputPrice := config.Model.CacheWriteInputPrice
		cacheWrite1hInputPrice := config.Model.CacheWrite1hInputPrice
		imageInputPrice := config.Model.ImageInputPrice
		imageOutputPrice := config.Model.ImageOutputPrice
		audioInputPrice := config.Model.AudioInputPrice
		audioOutputPrice := config.Model.AudioOutputPrice
		inputPriceTiers := model.NormalizePriceTiers(config.Model.InputPriceTiers)
		outputPriceTiers := model.NormalizePriceTiers(config.Model.OutputPriceTiers)
		cachedInputPriceTiers := model.NormalizePriceTiers(config.Model.CachedInputPriceTiers)
		cacheWriteInputPriceTiers := model.NormalizePriceTiers(config.Model.CacheWriteInputPriceTiers)
		cacheWrite1hInputPriceTiers := model.NormalizePriceTiers(config.Model.CacheWrite1hInputPriceTiers)
		imageInputPriceTiers := model.NormalizePriceTiers(config.Model.ImageInputPriceTiers)
		imageOutputPriceTiers := model.NormalizePriceTiers(config.Model.ImageOutputPriceTiers)
		audioInputPriceTiers := model.NormalizePriceTiers(config.Model.AudioInputPriceTiers)
		audioOutputPriceTiers := model.NormalizePriceTiers(config.Model.AudioOutputPriceTiers)
		videoBillingConfig := model.NormalizeVideoBillingConfig(config.Model.VideoBillingConfig)

		provider := service.ResolveModelProvider(modelName, config.Model.Provider, config.Model.ProviderIconURL)
		item, exists := catalog[modelName]
		if !exists {
			item = &publicModelCatalogAggregate{
				publicModelCatalogItem: publicModelCatalogItem{
					ModelName:                   modelName,
					Provider:                    provider.ID,
					ProviderName:                provider.Name,
					ProviderIconURL:             provider.IconURL,
					QuotaType:                   quotaType,
					InputPrice:                  inputPrice,
					OutputPrice:                 outputPrice,
					CachedInputPrice:            cachedInputPrice,
					CacheWriteInputPrice:        cacheWriteInputPrice,
					CacheWrite1hInputPrice:      cacheWrite1hInputPrice,
					ImageInputPrice:             imageInputPrice,
					ImageOutputPrice:            imageOutputPrice,
					AudioInputPrice:             audioInputPrice,
					AudioOutputPrice:            audioOutputPrice,
					InputPriceTiers:             inputPriceTiers,
					OutputPriceTiers:            outputPriceTiers,
					CachedInputPriceTiers:       cachedInputPriceTiers,
					CacheWriteInputPriceTiers:   cacheWriteInputPriceTiers,
					CacheWrite1hInputPriceTiers: cacheWrite1hInputPriceTiers,
					ImageInputPriceTiers:        imageInputPriceTiers,
					ImageOutputPriceTiers:       imageOutputPriceTiers,
					AudioInputPriceTiers:        audioInputPriceTiers,
					AudioOutputPriceTiers:       audioOutputPriceTiers,
					VideoBillingConfig:          videoBillingConfig,
					UserChannels:                []publicModelUserChannel{},
				},
				userChannelMap: map[uint]*publicModelUserChannel{},
			}
			catalog[modelName] = item
		}
		if inputPrice.LessThan(item.InputPrice) {
			item.InputPrice = inputPrice
			item.InputPriceTiers = inputPriceTiers
		}
		if outputPrice.LessThan(item.OutputPrice) {
			item.OutputPrice = outputPrice
			item.OutputPriceTiers = outputPriceTiers
		}
		if cachedInputPrice.LessThan(item.CachedInputPrice) {
			item.CachedInputPrice = cachedInputPrice
			item.CachedInputPriceTiers = cachedInputPriceTiers
		}
		if cacheWriteInputPrice.LessThan(item.CacheWriteInputPrice) {
			item.CacheWriteInputPrice = cacheWriteInputPrice
			item.CacheWriteInputPriceTiers = cacheWriteInputPriceTiers
		}
		if cacheWrite1hInputPrice.LessThan(item.CacheWrite1hInputPrice) {
			item.CacheWrite1hInputPrice = cacheWrite1hInputPrice
			item.CacheWrite1hInputPriceTiers = cacheWrite1hInputPriceTiers
		}
		if imageInputPrice.LessThan(item.ImageInputPrice) {
			item.ImageInputPrice = imageInputPrice
			item.ImageInputPriceTiers = imageInputPriceTiers
		}
		if imageOutputPrice.LessThan(item.ImageOutputPrice) {
			item.ImageOutputPrice = imageOutputPrice
			item.ImageOutputPriceTiers = imageOutputPriceTiers
		}
		if audioInputPrice.LessThan(item.AudioInputPrice) {
			item.AudioInputPrice = audioInputPrice
			item.AudioInputPriceTiers = audioInputPriceTiers
		}
		if audioOutputPrice.LessThan(item.AudioOutputPrice) {
			item.AudioOutputPrice = audioOutputPrice
			item.AudioOutputPriceTiers = audioOutputPriceTiers
		}
		if item.Provider == "custom" && provider.ID != "custom" {
			item.Provider = provider.ID
			item.ProviderName = provider.Name
			item.ProviderIconURL = provider.IconURL
		}

		userChannel := config.Channel.UserChannel
		effectiveInput := inputPrice.Mul(userChannel.Multiplier)
		effectiveOutput := outputPrice.Mul(userChannel.Multiplier)
		effectiveCachedInput := cachedInputPrice.Mul(userChannel.Multiplier)
		effectiveCacheWriteInput := cacheWriteInputPrice.Mul(userChannel.Multiplier)
		effectiveCacheWrite1hInput := cacheWrite1hInputPrice.Mul(userChannel.Multiplier)
		effectiveImageInput := imageInputPrice.Mul(userChannel.Multiplier)
		effectiveImageOutput := imageOutputPrice.Mul(userChannel.Multiplier)
		effectiveAudioInput := audioInputPrice.Mul(userChannel.Multiplier)
		effectiveAudioOutput := audioOutputPrice.Mul(userChannel.Multiplier)
		effectiveInputTiers := model.MultiplyPriceTiers(inputPriceTiers, userChannel.Multiplier)
		effectiveOutputTiers := model.MultiplyPriceTiers(outputPriceTiers, userChannel.Multiplier)
		effectiveCachedInputTiers := model.MultiplyPriceTiers(cachedInputPriceTiers, userChannel.Multiplier)
		effectiveCacheWriteInputTiers := model.MultiplyPriceTiers(cacheWriteInputPriceTiers, userChannel.Multiplier)
		effectiveCacheWrite1hInputTiers := model.MultiplyPriceTiers(cacheWrite1hInputPriceTiers, userChannel.Multiplier)
		effectiveImageInputTiers := model.MultiplyPriceTiers(imageInputPriceTiers, userChannel.Multiplier)
		effectiveImageOutputTiers := model.MultiplyPriceTiers(imageOutputPriceTiers, userChannel.Multiplier)
		effectiveAudioInputTiers := model.MultiplyPriceTiers(audioInputPriceTiers, userChannel.Multiplier)
		effectiveAudioOutputTiers := model.MultiplyPriceTiers(audioOutputPriceTiers, userChannel.Multiplier)
		effectiveVideoBillingConfig := model.MultiplyVideoBillingConfig(videoBillingConfig, userChannel.Multiplier)
		channelItem, channelExists := item.userChannelMap[userChannel.ID]
		if !channelExists {
			channelItem = &publicModelUserChannel{
				ID:                                   userChannel.ID,
				Name:                                 userChannel.Name,
				Description:                          userChannel.Description,
				Multiplier:                           userChannel.Multiplier,
				QuotaType:                            quotaType,
				InputPrice:                           inputPrice,
				OutputPrice:                          outputPrice,
				CachedInputPrice:                     cachedInputPrice,
				CacheWriteInputPrice:                 cacheWriteInputPrice,
				CacheWrite1hInputPrice:               cacheWrite1hInputPrice,
				ImageInputPrice:                      imageInputPrice,
				ImageOutputPrice:                     imageOutputPrice,
				AudioInputPrice:                      audioInputPrice,
				AudioOutputPrice:                     audioOutputPrice,
				InputPriceTiers:                      inputPriceTiers,
				OutputPriceTiers:                     outputPriceTiers,
				CachedInputPriceTiers:                cachedInputPriceTiers,
				CacheWriteInputPriceTiers:            cacheWriteInputPriceTiers,
				CacheWrite1hInputPriceTiers:          cacheWrite1hInputPriceTiers,
				ImageInputPriceTiers:                 imageInputPriceTiers,
				ImageOutputPriceTiers:                imageOutputPriceTiers,
				AudioInputPriceTiers:                 audioInputPriceTiers,
				AudioOutputPriceTiers:                audioOutputPriceTiers,
				EffectiveInputPrice:                  effectiveInput,
				EffectiveOutputPrice:                 effectiveOutput,
				EffectiveCachedInputPrice:            effectiveCachedInput,
				EffectiveCacheWriteInputPrice:        effectiveCacheWriteInput,
				EffectiveCacheWrite1hInputPrice:      effectiveCacheWrite1hInput,
				EffectiveImageInputPrice:             effectiveImageInput,
				EffectiveImageOutputPrice:            effectiveImageOutput,
				EffectiveAudioInputPrice:             effectiveAudioInput,
				EffectiveAudioOutputPrice:            effectiveAudioOutput,
				EffectiveInputPriceTiers:             effectiveInputTiers,
				EffectiveOutputPriceTiers:            effectiveOutputTiers,
				EffectiveCachedInputPriceTiers:       effectiveCachedInputTiers,
				EffectiveCacheWriteInputPriceTiers:   effectiveCacheWriteInputTiers,
				EffectiveCacheWrite1hInputPriceTiers: effectiveCacheWrite1hInputTiers,
				EffectiveImageInputPriceTiers:        effectiveImageInputTiers,
				EffectiveImageOutputPriceTiers:       effectiveImageOutputTiers,
				EffectiveAudioInputPriceTiers:        effectiveAudioInputTiers,
				EffectiveAudioOutputPriceTiers:       effectiveAudioOutputTiers,
				VideoBillingConfig:                   videoBillingConfig,
				EffectiveVideoBillingConfig:          effectiveVideoBillingConfig,
			}
			item.userChannelMap[userChannel.ID] = channelItem
			continue
		}
		if effectiveInput.LessThan(channelItem.EffectiveInputPrice) {
			channelItem.InputPrice = inputPrice
			channelItem.InputPriceTiers = inputPriceTiers
			channelItem.EffectiveInputPrice = effectiveInput
			channelItem.EffectiveInputPriceTiers = effectiveInputTiers
		}
		if effectiveOutput.LessThan(channelItem.EffectiveOutputPrice) {
			channelItem.OutputPrice = outputPrice
			channelItem.OutputPriceTiers = outputPriceTiers
			channelItem.EffectiveOutputPrice = effectiveOutput
			channelItem.EffectiveOutputPriceTiers = effectiveOutputTiers
		}
		if effectiveCachedInput.LessThan(channelItem.EffectiveCachedInputPrice) {
			channelItem.CachedInputPrice = cachedInputPrice
			channelItem.CachedInputPriceTiers = cachedInputPriceTiers
			channelItem.EffectiveCachedInputPrice = effectiveCachedInput
			channelItem.EffectiveCachedInputPriceTiers = effectiveCachedInputTiers
		}
		if effectiveCacheWriteInput.LessThan(channelItem.EffectiveCacheWriteInputPrice) {
			channelItem.CacheWriteInputPrice = cacheWriteInputPrice
			channelItem.CacheWriteInputPriceTiers = cacheWriteInputPriceTiers
			channelItem.EffectiveCacheWriteInputPrice = effectiveCacheWriteInput
			channelItem.EffectiveCacheWriteInputPriceTiers = effectiveCacheWriteInputTiers
		}
		if effectiveCacheWrite1hInput.LessThan(channelItem.EffectiveCacheWrite1hInputPrice) {
			channelItem.CacheWrite1hInputPrice = cacheWrite1hInputPrice
			channelItem.CacheWrite1hInputPriceTiers = cacheWrite1hInputPriceTiers
			channelItem.EffectiveCacheWrite1hInputPrice = effectiveCacheWrite1hInput
			channelItem.EffectiveCacheWrite1hInputPriceTiers = effectiveCacheWrite1hInputTiers
		}
		if effectiveImageInput.LessThan(channelItem.EffectiveImageInputPrice) {
			channelItem.ImageInputPrice = imageInputPrice
			channelItem.ImageInputPriceTiers = imageInputPriceTiers
			channelItem.EffectiveImageInputPrice = effectiveImageInput
			channelItem.EffectiveImageInputPriceTiers = effectiveImageInputTiers
		}
		if effectiveImageOutput.LessThan(channelItem.EffectiveImageOutputPrice) {
			channelItem.ImageOutputPrice = imageOutputPrice
			channelItem.ImageOutputPriceTiers = imageOutputPriceTiers
			channelItem.EffectiveImageOutputPrice = effectiveImageOutput
			channelItem.EffectiveImageOutputPriceTiers = effectiveImageOutputTiers
		}
		if effectiveAudioInput.LessThan(channelItem.EffectiveAudioInputPrice) {
			channelItem.AudioInputPrice = audioInputPrice
			channelItem.AudioInputPriceTiers = audioInputPriceTiers
			channelItem.EffectiveAudioInputPrice = effectiveAudioInput
			channelItem.EffectiveAudioInputPriceTiers = effectiveAudioInputTiers
		}
		if effectiveAudioOutput.LessThan(channelItem.EffectiveAudioOutputPrice) {
			channelItem.AudioOutputPrice = audioOutputPrice
			channelItem.AudioOutputPriceTiers = audioOutputPriceTiers
			channelItem.EffectiveAudioOutputPrice = effectiveAudioOutput
			channelItem.EffectiveAudioOutputPriceTiers = effectiveAudioOutputTiers
		}
	}

	if metaModels, err := service.ListMetaModelCatalog(c); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list meta models"})
		return
	} else {
		addMetaModelCatalogItems(catalog, metaModels)
	}

	response := make([]publicModelCatalogItem, 0, len(catalog))
	for _, item := range catalog {
		response = append(response, catalogItemWithChannels(item))
	}
	sort.Slice(response, func(i, j int) bool {
		return response[i].ModelName < response[j].ModelName
	})

	c.JSON(http.StatusOK, response)
}

func catalogItemWithChannels(item *publicModelCatalogAggregate) publicModelCatalogItem {
	if item == nil {
		return publicModelCatalogItem{}
	}
	channels := make([]publicModelUserChannel, 0, len(item.userChannelMap))
	for _, channel := range item.userChannelMap {
		channels = append(channels, *channel)
	}
	sort.Slice(channels, func(i, j int) bool {
		return channels[i].Name < channels[j].Name
	})
	next := item.publicModelCatalogItem
	next.UserChannels = channels
	return next
}

func addMetaModelCatalogItems(catalog map[string]*publicModelCatalogAggregate, metaModels []service.MetaModelCatalogItem) {
	for _, meta := range metaModels {
		name := strings.TrimSpace(meta.Name)
		if name == "" {
			continue
		}
		referenced := referencedPublicCatalogItems(catalog, meta.ReferencedModels)
		if len(referenced) == 0 {
			continue
		}
		item := &publicModelCatalogAggregate{
			publicModelCatalogItem: publicModelCatalogItem{
				ModelName:       name,
				Description:     strings.TrimSpace(meta.Description),
				Provider:        firstNonEmptyString(strings.TrimSpace(meta.Provider), "meta"),
				ProviderName:    firstNonEmptyString(strings.TrimSpace(meta.ProviderName), "Meta Module"),
				ProviderIconURL: strings.TrimSpace(meta.ProviderIconURL),
				IsMetaModel:     true,
				MetaBillingMode: strings.TrimSpace(meta.BillingMode),
				UserChannels:    []publicModelUserChannel{},
			},
			userChannelMap: map[uint]*publicModelUserChannel{},
		}
		if meta.ExposeReferencedModels {
			item.ReferencedModels = referenced
		}
		if item.MetaBillingMode == "meta" {
			item.InputPrice = meta.InputPrice
			item.OutputPrice = meta.OutputPrice
			item.CachedInputPrice = meta.CachedInputPrice
			item.userChannelMap = metaBillingUserChannels(referenced, meta)
		} else {
			applyActualBillingMetaPrices(item, referenced)
			item.userChannelMap = actualBillingUserChannels(referenced)
		}
		catalog[name] = item
	}
}

func referencedPublicCatalogItems(catalog map[string]*publicModelCatalogAggregate, modelNames []string) []publicModelCatalogItem {
	items := make([]publicModelCatalogItem, 0, len(modelNames))
	seen := map[string]struct{}{}
	for _, modelName := range modelNames {
		modelName = strings.TrimSpace(modelName)
		if modelName == "" {
			continue
		}
		if _, exists := seen[modelName]; exists {
			continue
		}
		seen[modelName] = struct{}{}
		if item, exists := catalog[modelName]; exists {
			ref := catalogItemWithChannels(item)
			ref.ReferencedModels = nil
			items = append(items, ref)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].ModelName < items[j].ModelName
	})
	return items
}

func applyActualBillingMetaPrices(item *publicModelCatalogAggregate, referenced []publicModelCatalogItem) {
	for index, ref := range referenced {
		if index == 0 || ref.InputPrice.LessThan(item.InputPrice) {
			item.InputPrice = ref.InputPrice
			item.InputPriceTiers = ref.InputPriceTiers
		}
		if index == 0 || ref.OutputPrice.LessThan(item.OutputPrice) {
			item.OutputPrice = ref.OutputPrice
			item.OutputPriceTiers = ref.OutputPriceTiers
		}
		if index == 0 || ref.CachedInputPrice.LessThan(item.CachedInputPrice) {
			item.CachedInputPrice = ref.CachedInputPrice
			item.CachedInputPriceTiers = ref.CachedInputPriceTiers
		}
	}
}

func actualBillingUserChannels(referenced []publicModelCatalogItem) map[uint]*publicModelUserChannel {
	channels := map[uint]*publicModelUserChannel{}
	for _, ref := range referenced {
		for _, channel := range ref.UserChannels {
			existing := channels[channel.ID]
			if existing == nil {
				copy := channel
				channels[channel.ID] = &copy
				continue
			}
			if channel.EffectiveInputPrice.LessThan(existing.EffectiveInputPrice) {
				existing.InputPrice = channel.InputPrice
				existing.InputPriceTiers = channel.InputPriceTiers
				existing.EffectiveInputPrice = channel.EffectiveInputPrice
				existing.EffectiveInputPriceTiers = channel.EffectiveInputPriceTiers
			}
			if channel.EffectiveOutputPrice.LessThan(existing.EffectiveOutputPrice) {
				existing.OutputPrice = channel.OutputPrice
				existing.OutputPriceTiers = channel.OutputPriceTiers
				existing.EffectiveOutputPrice = channel.EffectiveOutputPrice
				existing.EffectiveOutputPriceTiers = channel.EffectiveOutputPriceTiers
			}
			if channel.EffectiveCachedInputPrice.LessThan(existing.EffectiveCachedInputPrice) {
				existing.CachedInputPrice = channel.CachedInputPrice
				existing.CachedInputPriceTiers = channel.CachedInputPriceTiers
				existing.EffectiveCachedInputPrice = channel.EffectiveCachedInputPrice
				existing.EffectiveCachedInputPriceTiers = channel.EffectiveCachedInputPriceTiers
			}
		}
	}
	return channels
}

func metaBillingUserChannels(referenced []publicModelCatalogItem, meta service.MetaModelCatalogItem) map[uint]*publicModelUserChannel {
	channels := map[uint]*publicModelUserChannel{}
	for _, ref := range referenced {
		for _, channel := range ref.UserChannels {
			if _, exists := channels[channel.ID]; exists {
				continue
			}
			channels[channel.ID] = &publicModelUserChannel{
				ID:                        channel.ID,
				Name:                      channel.Name,
				Description:               channel.Description,
				Multiplier:                channel.Multiplier,
				InputPrice:                meta.InputPrice,
				OutputPrice:               meta.OutputPrice,
				CachedInputPrice:          meta.CachedInputPrice,
				EffectiveInputPrice:       meta.InputPrice.Mul(channel.Multiplier),
				EffectiveOutputPrice:      meta.OutputPrice.Mul(channel.Multiplier),
				EffectiveCachedInputPrice: meta.CachedInputPrice.Mul(channel.Multiplier),
			}
		}
	}
	return channels
}

func (api *ModelAPI) Pricing(c *gin.Context) {
	if !settingBool("pricing_endpoint_enabled", false) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Not found"})
		return
	}

	var selectedUserChannelID uint64
	if value := strings.TrimSpace(c.Query("user_channel_id")); value != "" {
		parsed, err := strconv.ParseUint(value, 10, 0)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid user_channel_id"})
			return
		}
		selectedUserChannelID = parsed
	}

	var configs []model.ModelConfig
	if err := model.DB.
		Preload("Channel.UserChannel").
		Preload("Model").
		Where("enabled = ?", true).
		Find(&configs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load pricing"})
		return
	}

	data := pricingData{
		Unit:                            "per_1m_tokens",
		ModelRatio:                      map[string]decimal.Decimal{},
		CompletionRatio:                 map[string]decimal.Decimal{},
		InputPrice:                      map[string]decimal.Decimal{},
		OutputPrice:                     map[string]decimal.Decimal{},
		CachedInputPrice:                map[string]decimal.Decimal{},
		CacheReadInputPrice:             map[string]decimal.Decimal{},
		CacheWriteInputPrice:            map[string]decimal.Decimal{},
		CacheWrite1hInputPrice:          map[string]decimal.Decimal{},
		ImageInputPrice:                 map[string]decimal.Decimal{},
		ImageOutputPrice:                map[string]decimal.Decimal{},
		AudioInputPrice:                 map[string]decimal.Decimal{},
		AudioOutputPrice:                map[string]decimal.Decimal{},
		QuotaType:                       map[string]int{},
		InputPriceTiers:                 map[string]model.PriceTierList{},
		OutputPriceTiers:                map[string]model.PriceTierList{},
		CachedInputPriceTiers:           map[string]model.PriceTierList{},
		CacheReadInputPriceTiers:        map[string]model.PriceTierList{},
		CacheWriteInputPriceTiers:       map[string]model.PriceTierList{},
		CacheWrite1hInputPriceTiers:     map[string]model.PriceTierList{},
		ImageInputPriceTiers:            map[string]model.PriceTierList{},
		ImageOutputPriceTiers:           map[string]model.PriceTierList{},
		AudioInputPriceTiers:            map[string]model.PriceTierList{},
		AudioOutputPriceTiers:           map[string]model.PriceTierList{},
		VideoBillingConfig:              map[string]model.VideoBillingConfig{},
		BaseInputPrice:                  map[string]decimal.Decimal{},
		BaseOutputPrice:                 map[string]decimal.Decimal{},
		BaseCachedInputPrice:            map[string]decimal.Decimal{},
		BaseCacheReadInputPrice:         map[string]decimal.Decimal{},
		BaseCacheWriteInputPrice:        map[string]decimal.Decimal{},
		BaseCacheWrite1hInputPrice:      map[string]decimal.Decimal{},
		BaseImageInputPrice:             map[string]decimal.Decimal{},
		BaseImageOutputPrice:            map[string]decimal.Decimal{},
		BaseAudioInputPrice:             map[string]decimal.Decimal{},
		BaseAudioOutputPrice:            map[string]decimal.Decimal{},
		BaseQuotaType:                   map[string]int{},
		BaseInputPriceTiers:             map[string]model.PriceTierList{},
		BaseOutputPriceTiers:            map[string]model.PriceTierList{},
		BaseCachedInputPriceTiers:       map[string]model.PriceTierList{},
		BaseCacheReadInputPriceTiers:    map[string]model.PriceTierList{},
		BaseCacheWriteInputPriceTiers:   map[string]model.PriceTierList{},
		BaseCacheWrite1hInputPriceTiers: map[string]model.PriceTierList{},
		BaseImageInputPriceTiers:        map[string]model.PriceTierList{},
		BaseImageOutputPriceTiers:       map[string]model.PriceTierList{},
		BaseAudioInputPriceTiers:        map[string]model.PriceTierList{},
		BaseAudioOutputPriceTiers:       map[string]model.PriceTierList{},
		BaseVideoBillingConfig:          map[string]model.VideoBillingConfig{},
		UserChannelPrices:               map[string]pricingUserChannelPrice{},
	}
	channelPrices := map[uint]*pricingUserChannelPrice{}

	for _, config := range configs {
		if !config.Channel.Enabled || config.Channel.UserChannelID == nil || !config.Channel.UserChannel.Enabled || !config.Model.Enabled {
			continue
		}
		userChannel := config.Channel.UserChannel
		if selectedUserChannelID != 0 && uint64(userChannel.ID) != selectedUserChannelID {
			continue
		}
		modelName := strings.TrimSpace(config.Model.ModelName)
		if modelName == "" {
			continue
		}

		baseInput := config.Model.InputPrice
		baseOutput := config.Model.OutputPrice
		baseCachedInput := config.Model.CachedInputPrice
		baseCacheWriteInput := config.Model.CacheWriteInputPrice
		baseCacheWrite1hInput := config.Model.CacheWrite1hInputPrice
		baseImageInput := config.Model.ImageInputPrice
		baseImageOutput := config.Model.ImageOutputPrice
		baseAudioInput := config.Model.AudioInputPrice
		baseAudioOutput := config.Model.AudioOutputPrice
		baseQuotaType := config.Model.QuotaType
		baseVideoBillingConfig := model.NormalizeVideoBillingConfig(config.Model.VideoBillingConfig)
		baseInputTiers := model.NormalizePriceTiers(config.Model.InputPriceTiers)
		baseOutputTiers := model.NormalizePriceTiers(config.Model.OutputPriceTiers)
		baseCachedInputTiers := model.NormalizePriceTiers(config.Model.CachedInputPriceTiers)
		baseCacheWriteInputTiers := model.NormalizePriceTiers(config.Model.CacheWriteInputPriceTiers)
		baseCacheWrite1hInputTiers := model.NormalizePriceTiers(config.Model.CacheWrite1hInputPriceTiers)
		baseImageInputTiers := model.NormalizePriceTiers(config.Model.ImageInputPriceTiers)
		baseImageOutputTiers := model.NormalizePriceTiers(config.Model.ImageOutputPriceTiers)
		baseAudioInputTiers := model.NormalizePriceTiers(config.Model.AudioInputPriceTiers)
		baseAudioOutputTiers := model.NormalizePriceTiers(config.Model.AudioOutputPriceTiers)
		effectiveInput := baseInput.Mul(userChannel.Multiplier)
		effectiveOutput := baseOutput.Mul(userChannel.Multiplier)
		effectiveCachedInput := baseCachedInput.Mul(userChannel.Multiplier)
		effectiveCacheWriteInput := baseCacheWriteInput.Mul(userChannel.Multiplier)
		effectiveCacheWrite1hInput := baseCacheWrite1hInput.Mul(userChannel.Multiplier)
		effectiveImageInput := baseImageInput.Mul(userChannel.Multiplier)
		effectiveImageOutput := baseImageOutput.Mul(userChannel.Multiplier)
		effectiveAudioInput := baseAudioInput.Mul(userChannel.Multiplier)
		effectiveAudioOutput := baseAudioOutput.Mul(userChannel.Multiplier)
		effectiveVideoBillingConfig := model.MultiplyVideoBillingConfig(baseVideoBillingConfig, userChannel.Multiplier)
		effectiveInputTiers := model.MultiplyPriceTiers(baseInputTiers, userChannel.Multiplier)
		effectiveOutputTiers := model.MultiplyPriceTiers(baseOutputTiers, userChannel.Multiplier)
		effectiveCachedInputTiers := model.MultiplyPriceTiers(baseCachedInputTiers, userChannel.Multiplier)
		effectiveCacheWriteInputTiers := model.MultiplyPriceTiers(baseCacheWriteInputTiers, userChannel.Multiplier)
		effectiveCacheWrite1hInputTiers := model.MultiplyPriceTiers(baseCacheWrite1hInputTiers, userChannel.Multiplier)
		effectiveImageInputTiers := model.MultiplyPriceTiers(baseImageInputTiers, userChannel.Multiplier)
		effectiveImageOutputTiers := model.MultiplyPriceTiers(baseImageOutputTiers, userChannel.Multiplier)
		effectiveAudioInputTiers := model.MultiplyPriceTiers(baseAudioInputTiers, userChannel.Multiplier)
		effectiveAudioOutputTiers := model.MultiplyPriceTiers(baseAudioOutputTiers, userChannel.Multiplier)

		baseInput = exposedPricingDecimal(baseInput, baseQuotaType)
		baseOutput = exposedPricingDecimal(baseOutput, baseQuotaType)
		baseCachedInput = exposedPricingDecimal(baseCachedInput, baseQuotaType)
		baseCacheWriteInput = exposedPricingDecimal(baseCacheWriteInput, baseQuotaType)
		baseCacheWrite1hInput = exposedPricingDecimal(baseCacheWrite1hInput, baseQuotaType)
		baseImageInput = exposedPricingDecimal(baseImageInput, baseQuotaType)
		baseImageOutput = exposedPricingDecimal(baseImageOutput, baseQuotaType)
		baseAudioInput = exposedPricingDecimal(baseAudioInput, baseQuotaType)
		baseAudioOutput = exposedPricingDecimal(baseAudioOutput, baseQuotaType)
		baseInputTiers = exposedPricingTiers(baseInputTiers, baseQuotaType)
		baseOutputTiers = exposedPricingTiers(baseOutputTiers, baseQuotaType)
		baseCachedInputTiers = exposedPricingTiers(baseCachedInputTiers, baseQuotaType)
		baseCacheWriteInputTiers = exposedPricingTiers(baseCacheWriteInputTiers, baseQuotaType)
		baseCacheWrite1hInputTiers = exposedPricingTiers(baseCacheWrite1hInputTiers, baseQuotaType)
		baseImageInputTiers = exposedPricingTiers(baseImageInputTiers, baseQuotaType)
		baseImageOutputTiers = exposedPricingTiers(baseImageOutputTiers, baseQuotaType)
		baseAudioInputTiers = exposedPricingTiers(baseAudioInputTiers, baseQuotaType)
		baseAudioOutputTiers = exposedPricingTiers(baseAudioOutputTiers, baseQuotaType)
		effectiveInput = exposedPricingDecimal(effectiveInput, baseQuotaType)
		effectiveOutput = exposedPricingDecimal(effectiveOutput, baseQuotaType)
		effectiveCachedInput = exposedPricingDecimal(effectiveCachedInput, baseQuotaType)
		effectiveCacheWriteInput = exposedPricingDecimal(effectiveCacheWriteInput, baseQuotaType)
		effectiveCacheWrite1hInput = exposedPricingDecimal(effectiveCacheWrite1hInput, baseQuotaType)
		effectiveImageInput = exposedPricingDecimal(effectiveImageInput, baseQuotaType)
		effectiveImageOutput = exposedPricingDecimal(effectiveImageOutput, baseQuotaType)
		effectiveAudioInput = exposedPricingDecimal(effectiveAudioInput, baseQuotaType)
		effectiveAudioOutput = exposedPricingDecimal(effectiveAudioOutput, baseQuotaType)
		effectiveInputTiers = exposedPricingTiers(effectiveInputTiers, baseQuotaType)
		effectiveOutputTiers = exposedPricingTiers(effectiveOutputTiers, baseQuotaType)
		effectiveCachedInputTiers = exposedPricingTiers(effectiveCachedInputTiers, baseQuotaType)
		effectiveCacheWriteInputTiers = exposedPricingTiers(effectiveCacheWriteInputTiers, baseQuotaType)
		effectiveCacheWrite1hInputTiers = exposedPricingTiers(effectiveCacheWrite1hInputTiers, baseQuotaType)
		effectiveImageInputTiers = exposedPricingTiers(effectiveImageInputTiers, baseQuotaType)
		effectiveImageOutputTiers = exposedPricingTiers(effectiveImageOutputTiers, baseQuotaType)
		effectiveAudioInputTiers = exposedPricingTiers(effectiveAudioInputTiers, baseQuotaType)
		effectiveAudioOutputTiers = exposedPricingTiers(effectiveAudioOutputTiers, baseQuotaType)

		setMinDecimalWithTiers(data.BaseInputPrice, data.BaseInputPriceTiers, modelName, baseInput, baseInputTiers)
		setMinDecimalWithTiers(data.BaseOutputPrice, data.BaseOutputPriceTiers, modelName, baseOutput, baseOutputTiers)
		setMinDecimalWithTiers(data.BaseCachedInputPrice, data.BaseCachedInputPriceTiers, modelName, baseCachedInput, baseCachedInputTiers)
		setMinDecimalWithTiers(data.BaseCacheReadInputPrice, data.BaseCacheReadInputPriceTiers, modelName, baseCachedInput, baseCachedInputTiers)
		setMinDecimalWithTiers(data.BaseCacheWriteInputPrice, data.BaseCacheWriteInputPriceTiers, modelName, baseCacheWriteInput, baseCacheWriteInputTiers)
		setMinDecimalWithTiers(data.BaseCacheWrite1hInputPrice, data.BaseCacheWrite1hInputPriceTiers, modelName, baseCacheWrite1hInput, baseCacheWrite1hInputTiers)
		setMinDecimalWithTiers(data.BaseImageInputPrice, data.BaseImageInputPriceTiers, modelName, baseImageInput, baseImageInputTiers)
		setMinDecimalWithTiers(data.BaseImageOutputPrice, data.BaseImageOutputPriceTiers, modelName, baseImageOutput, baseImageOutputTiers)
		setMinDecimalWithTiers(data.BaseAudioInputPrice, data.BaseAudioInputPriceTiers, modelName, baseAudioInput, baseAudioInputTiers)
		setMinDecimalWithTiers(data.BaseAudioOutputPrice, data.BaseAudioOutputPriceTiers, modelName, baseAudioOutput, baseAudioOutputTiers)
		data.BaseVideoBillingConfig[modelName] = baseVideoBillingConfig
		data.BaseQuotaType[modelName] = baseQuotaType
		setMinDecimalWithTiers(data.InputPrice, data.InputPriceTiers, modelName, effectiveInput, effectiveInputTiers)
		setMinDecimalWithTiers(data.OutputPrice, data.OutputPriceTiers, modelName, effectiveOutput, effectiveOutputTiers)
		setMinDecimalWithTiers(data.CachedInputPrice, data.CachedInputPriceTiers, modelName, effectiveCachedInput, effectiveCachedInputTiers)
		setMinDecimalWithTiers(data.CacheReadInputPrice, data.CacheReadInputPriceTiers, modelName, effectiveCachedInput, effectiveCachedInputTiers)
		setMinDecimalWithTiers(data.CacheWriteInputPrice, data.CacheWriteInputPriceTiers, modelName, effectiveCacheWriteInput, effectiveCacheWriteInputTiers)
		setMinDecimalWithTiers(data.CacheWrite1hInputPrice, data.CacheWrite1hInputPriceTiers, modelName, effectiveCacheWrite1hInput, effectiveCacheWrite1hInputTiers)
		setMinDecimalWithTiers(data.ImageInputPrice, data.ImageInputPriceTiers, modelName, effectiveImageInput, effectiveImageInputTiers)
		setMinDecimalWithTiers(data.ImageOutputPrice, data.ImageOutputPriceTiers, modelName, effectiveImageOutput, effectiveImageOutputTiers)
		setMinDecimalWithTiers(data.AudioInputPrice, data.AudioInputPriceTiers, modelName, effectiveAudioInput, effectiveAudioInputTiers)
		setMinDecimalWithTiers(data.AudioOutputPrice, data.AudioOutputPriceTiers, modelName, effectiveAudioOutput, effectiveAudioOutputTiers)
		data.VideoBillingConfig[modelName] = effectiveVideoBillingConfig
		data.QuotaType[modelName] = baseQuotaType

		channelPrice, exists := channelPrices[userChannel.ID]
		if !exists {
			channelPrice = &pricingUserChannelPrice{
				ID:                          userChannel.ID,
				Name:                        userChannel.Name,
				Description:                 userChannel.Description,
				Multiplier:                  userChannel.Multiplier,
				QuotaType:                   map[string]int{},
				ModelRatio:                  map[string]decimal.Decimal{},
				CompletionRatio:             map[string]decimal.Decimal{},
				InputPrice:                  map[string]decimal.Decimal{},
				OutputPrice:                 map[string]decimal.Decimal{},
				CachedInputPrice:            map[string]decimal.Decimal{},
				CacheReadInputPrice:         map[string]decimal.Decimal{},
				CacheWriteInputPrice:        map[string]decimal.Decimal{},
				CacheWrite1hInputPrice:      map[string]decimal.Decimal{},
				ImageInputPrice:             map[string]decimal.Decimal{},
				ImageOutputPrice:            map[string]decimal.Decimal{},
				AudioInputPrice:             map[string]decimal.Decimal{},
				AudioOutputPrice:            map[string]decimal.Decimal{},
				InputPriceTiers:             map[string]model.PriceTierList{},
				OutputPriceTiers:            map[string]model.PriceTierList{},
				CachedInputPriceTiers:       map[string]model.PriceTierList{},
				CacheReadInputPriceTiers:    map[string]model.PriceTierList{},
				CacheWriteInputPriceTiers:   map[string]model.PriceTierList{},
				CacheWrite1hInputPriceTiers: map[string]model.PriceTierList{},
				ImageInputPriceTiers:        map[string]model.PriceTierList{},
				ImageOutputPriceTiers:       map[string]model.PriceTierList{},
				AudioInputPriceTiers:        map[string]model.PriceTierList{},
				AudioOutputPriceTiers:       map[string]model.PriceTierList{},
				VideoBillingConfig:          map[string]model.VideoBillingConfig{},
			}
			channelPrices[userChannel.ID] = channelPrice
		}
		setMinDecimalWithTiers(channelPrice.InputPrice, channelPrice.InputPriceTiers, modelName, effectiveInput, effectiveInputTiers)
		setMinDecimalWithTiers(channelPrice.OutputPrice, channelPrice.OutputPriceTiers, modelName, effectiveOutput, effectiveOutputTiers)
		setMinDecimalWithTiers(channelPrice.CachedInputPrice, channelPrice.CachedInputPriceTiers, modelName, effectiveCachedInput, effectiveCachedInputTiers)
		setMinDecimalWithTiers(channelPrice.CacheReadInputPrice, channelPrice.CacheReadInputPriceTiers, modelName, effectiveCachedInput, effectiveCachedInputTiers)
		setMinDecimalWithTiers(channelPrice.CacheWriteInputPrice, channelPrice.CacheWriteInputPriceTiers, modelName, effectiveCacheWriteInput, effectiveCacheWriteInputTiers)
		setMinDecimalWithTiers(channelPrice.CacheWrite1hInputPrice, channelPrice.CacheWrite1hInputPriceTiers, modelName, effectiveCacheWrite1hInput, effectiveCacheWrite1hInputTiers)
		setMinDecimalWithTiers(channelPrice.ImageInputPrice, channelPrice.ImageInputPriceTiers, modelName, effectiveImageInput, effectiveImageInputTiers)
		setMinDecimalWithTiers(channelPrice.ImageOutputPrice, channelPrice.ImageOutputPriceTiers, modelName, effectiveImageOutput, effectiveImageOutputTiers)
		setMinDecimalWithTiers(channelPrice.AudioInputPrice, channelPrice.AudioInputPriceTiers, modelName, effectiveAudioInput, effectiveAudioInputTiers)
		setMinDecimalWithTiers(channelPrice.AudioOutputPrice, channelPrice.AudioOutputPriceTiers, modelName, effectiveAudioOutput, effectiveAudioOutputTiers)
		channelPrice.VideoBillingConfig[modelName] = effectiveVideoBillingConfig
		channelPrice.QuotaType[modelName] = baseQuotaType
	}

	data.ModelRatio = copyDecimalMap(data.InputPrice)
	data.CompletionRatio = completionRatios(data.InputPrice, data.OutputPrice)
	for _, channelPrice := range channelPrices {
		channelPrice.ModelRatio = copyDecimalMap(channelPrice.InputPrice)
		channelPrice.CompletionRatio = completionRatios(channelPrice.InputPrice, channelPrice.OutputPrice)
		data.UserChannelPrices[channelPrice.Name] = *channelPrice
	}

	c.JSON(http.StatusOK, pricingResponse{
		Success: true,
		Message: "",
		Data:    data,
	})
}

func setMinDecimal(values map[string]decimal.Decimal, key string, value decimal.Decimal) {
	if existing, exists := values[key]; !exists || value.LessThan(existing) {
		values[key] = value
	}
}

func setMinDecimalWithTiers(values map[string]decimal.Decimal, tiers map[string]model.PriceTierList, key string, value decimal.Decimal, priceTiers model.PriceTierList) {
	if existing, exists := values[key]; !exists || value.LessThan(existing) {
		values[key] = value
		tiers[key] = model.NormalizePriceTiers(priceTiers)
	}
}

func exposedPricingDecimal(value decimal.Decimal, quotaType int) decimal.Decimal {
	normalizedQuotaType := normalizeQuotaType(quotaType)
	if normalizedQuotaType == 1 || normalizedQuotaType == model.QuotaTypeVideoResolutionDuration {
		return value
	}
	return value.Div(decimal.NewFromInt(2))
}

func exposedPricingTiers(tiers model.PriceTierList, quotaType int) model.PriceTierList {
	normalizedQuotaType := normalizeQuotaType(quotaType)
	if normalizedQuotaType == 1 || normalizedQuotaType == model.QuotaTypeVideoResolutionDuration {
		return model.NormalizePriceTiers(tiers)
	}
	return model.MultiplyPriceTiers(tiers, decimal.RequireFromString("0.5"))
}

func copyDecimalMap(values map[string]decimal.Decimal) map[string]decimal.Decimal {
	copied := make(map[string]decimal.Decimal, len(values))
	for key, value := range values {
		copied[key] = value
	}
	return copied
}

func completionRatios(inputPrices map[string]decimal.Decimal, outputPrices map[string]decimal.Decimal) map[string]decimal.Decimal {
	ratios := make(map[string]decimal.Decimal, len(outputPrices))
	for modelName, outputPrice := range outputPrices {
		inputPrice := inputPrices[modelName]
		if inputPrice.GreaterThan(decimal.Zero) {
			ratios[modelName] = outputPrice.Div(inputPrice)
			continue
		}
		if outputPrice.GreaterThan(decimal.Zero) {
			ratios[modelName] = outputPrice
			continue
		}
		ratios[modelName] = decimal.NewFromInt(1)
	}
	return ratios
}

func (api *ModelAPI) List(c *gin.Context) {
	var models []model.Model
	if err := model.DB.Order("model_name ASC").Find(&models).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list models"})
		return
	}
	c.JSON(http.StatusOK, models)
}

func (api *ModelAPI) Sync(c *gin.Context) {
	var input modelSyncInput
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	results, err := api.SyncService.SyncChannels(input.ChannelIDs)
	if err != nil {
		log.Printf("HTTP 500 %s %s model sync failed: channel_ids=%v error=%v", c.Request.Method, c.Request.URL.String(), input.ChannelIDs, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"results": results})
}

func (api *ModelAPI) PreviewSync(c *gin.Context) {
	var input modelSyncPreviewInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if input.ChannelID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Channel is required"})
		return
	}

	preview, err := api.SyncService.PreviewChannelModels(input.ChannelID, service.ModelSyncOptions{
		Format: input.Format,
		Path:   input.Path,
	})
	if err != nil {
		log.Printf(
			"HTTP 500 %s %s model sync preview failed: channel_id=%d format=%q path=%q error=%v",
			c.Request.Method,
			c.Request.URL.String(),
			input.ChannelID,
			input.Format,
			input.Path,
			err,
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, preview)
}

func (api *ModelAPI) PreviewSyncFromBrowser(c *gin.Context) {
	var input modelSyncBrowserPreviewInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if input.ChannelID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Channel is required"})
		return
	}
	if len(input.Payload) == 0 || strings.EqualFold(strings.TrimSpace(string(input.Payload)), "null") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Payload is required"})
		return
	}

	preview, err := api.SyncService.PreviewChannelModelsFromBody(input.ChannelID, input.Source, input.Payload)
	if err != nil {
		log.Printf(
			"HTTP 500 %s %s browser model sync preview failed: channel_id=%d source=%q payload_bytes=%d error=%v",
			c.Request.Method,
			c.Request.URL.String(),
			input.ChannelID,
			input.Source,
			len(input.Payload),
			err,
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, preview)
}

func (api *ModelAPI) ApplySync(c *gin.Context) {
	var input modelSyncApplyInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if input.ChannelID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Channel is required"})
		return
	}
	if len(input.Models) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No models selected"})
		return
	}

	result, err := api.SyncService.ApplyChannelModels(input.ChannelID, input.Models)
	if err != nil {
		log.Printf(
			"HTTP 500 %s %s model sync apply failed: channel_id=%d model_count=%d result=%+v error=%v",
			c.Request.Method,
			c.Request.URL.String(),
			input.ChannelID,
			len(input.Models),
			result,
			err,
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "result": result})
		return
	}
	c.JSON(http.StatusOK, gin.H{"results": []service.ChannelSyncResult{result}})
}

func (api *ModelAPI) PreviewPriceSync(c *gin.Context) {
	var input modelSyncPreviewInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if input.ChannelID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Channel is required"})
		return
	}

	preview, err := api.SyncService.PreviewGlobalModelPrices(input.ChannelID, service.ModelSyncOptions{
		Format: input.Format,
		Path:   input.Path,
	})
	if err != nil {
		log.Printf(
			"HTTP 500 %s %s model price sync preview failed: channel_id=%d format=%q path=%q error=%v",
			c.Request.Method,
			c.Request.URL.String(),
			input.ChannelID,
			input.Format,
			input.Path,
			err,
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, preview)
}

func (api *ModelAPI) PreviewPriceSyncFromBrowser(c *gin.Context) {
	var input modelSyncBrowserPreviewInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if input.ChannelID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Channel is required"})
		return
	}
	if len(input.Payload) == 0 || strings.EqualFold(strings.TrimSpace(string(input.Payload)), "null") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Payload is required"})
		return
	}

	preview, err := api.SyncService.PreviewGlobalModelPricesFromBody(input.ChannelID, input.Source, input.Payload)
	if err != nil {
		log.Printf(
			"HTTP 500 %s %s browser model price sync preview failed: channel_id=%d source=%q payload_bytes=%d error=%v",
			c.Request.Method,
			c.Request.URL.String(),
			input.ChannelID,
			input.Source,
			len(input.Payload),
			err,
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, preview)
}

func (api *ModelAPI) ApplyPriceSync(c *gin.Context) {
	var input modelSyncApplyInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if input.ChannelID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Channel is required"})
		return
	}
	if len(input.Models) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No models selected"})
		return
	}

	result, err := api.SyncService.ApplyGlobalModelPrices(input.ChannelID, input.Models)
	if err != nil {
		log.Printf(
			"HTTP 500 %s %s model price sync apply failed: channel_id=%d model_count=%d result=%+v error=%v",
			c.Request.Method,
			c.Request.URL.String(),
			input.ChannelID,
			len(input.Models),
			result,
			err,
		)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "result": result})
		return
	}
	c.JSON(http.StatusOK, gin.H{"results": []service.ChannelSyncResult{result}})
}

func (api *ModelAPI) ListChannelModels(c *gin.Context) {
	channelID, err := strconv.ParseUint(c.Param("id"), 10, 0)
	if err != nil || channelID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid channel id"})
		return
	}
	var configs []model.ModelConfig
	if err := model.DB.
		Preload("Model").
		Preload("GroupMultipliers.Group").
		Where("channel_id = ?", uint(channelID)).
		Order("model_id ASC, upstream_model_name ASC").
		Find(&configs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list channel models"})
		return
	}
	hydrateModelConfigResponses(configs)
	c.JSON(http.StatusOK, configs)
}

func (api *ModelAPI) CreateChannelModel(c *gin.Context) {
	channelID, err := strconv.ParseUint(c.Param("id"), 10, 0)
	if err != nil || channelID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid channel id"})
		return
	}
	var input modelConfigInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	input.ChannelID = uint(channelID)
	config, err := modelConfigFromInput(input, nil)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := model.DB.Create(&config).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	model.DB.Preload("Model").First(&config, config.ID)
	hydrateModelConfigResponse(&config)
	c.JSON(http.StatusOK, config)
}

func (api *ModelAPI) UpdateChannelModel(c *gin.Context) {
	var config model.ModelConfig
	if err := model.DB.First(&config, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Model config not found"})
		return
	}
	var input modelConfigInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	updatedConfig, err := modelConfigFromInput(input, &config)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	updatedConfig.ID = config.ID
	config = updatedConfig
	if err := model.DB.Save(&config).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	model.DB.Preload("Model").First(&config, config.ID)
	hydrateModelConfigResponse(&config)
	c.JSON(http.StatusOK, config)
}

func (api *ModelAPI) DeleteChannelModel(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 0)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid model config id"})
		return
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("model_config_id = ?", uint(id)).Delete(&model.ModelGroupMultiplier{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.ModelConfig{}, uint(id)).Error
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Model config deleted"})
}

func (api *ModelAPI) Create(c *gin.Context) {
	var input modelConfigInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	globalModel, err := modelFromInput(input, nil)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := model.DB.Create(&globalModel).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, globalModel)
}

func (api *ModelAPI) Update(c *gin.Context) {
	var globalModel model.Model
	if err := model.DB.First(&globalModel, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Model not found"})
		return
	}

	var input modelConfigInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	updatedModel, err := modelFromInput(input, &globalModel)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	updatedModel.ID = globalModel.ID
	if err := model.DB.Save(&updatedModel).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, updatedModel)
}

func (api *ModelAPI) Delete(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 0)
	if err != nil || id == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid model id"})
		return
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		var configs []model.ModelConfig
		if err := tx.Where("model_id = ?", uint(id)).Find(&configs).Error; err != nil {
			return err
		}
		configIDs := make([]uint, 0, len(configs))
		for _, config := range configs {
			configIDs = append(configIDs, config.ID)
		}
		if len(configIDs) > 0 {
			if err := tx.Where("model_config_id IN ?", configIDs).Delete(&model.ModelGroupMultiplier{}).Error; err != nil {
				return err
			}
			if err := tx.Where("id IN ?", configIDs).Delete(&model.ModelConfig{}).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&model.Model{}, uint(id)).Error
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Model deleted"})
}

func (api *ModelAPI) SetGroupMultipliers(c *gin.Context) {
	var modelConfig model.ModelConfig
	if err := model.DB.First(&modelConfig, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Model config not found"})
		return
	}
	var input []groupMultiplierInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("model_config_id = ?", modelConfig.ID).Delete(&model.ModelGroupMultiplier{}).Error; err != nil {
			return err
		}
		for _, item := range input {
			if item.GroupID == 0 || item.Multiplier.IsZero() {
				continue
			}
			if err := tx.Create(&model.ModelGroupMultiplier{
				ModelConfigID: modelConfig.ID,
				GroupID:       item.GroupID,
				Multiplier:    item.Multiplier,
			}).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	model.DB.Preload("GroupMultipliers.Group").First(&modelConfig, modelConfig.ID)
	c.JSON(http.StatusOK, modelConfig.GroupMultipliers)
}

func modelFromInput(input modelConfigInput, existing *model.Model) (model.Model, error) {
	modelName := strings.TrimSpace(input.ModelName)
	if modelName == "" && existing == nil {
		return model.Model{}, errors.New("Model is required")
	}

	globalModel := model.Model{}
	if existing != nil {
		globalModel = *existing
	}
	if modelName != "" {
		globalModel.ModelName = modelName
	}

	providerInput := strings.TrimSpace(input.Provider)
	iconURLInput := strings.TrimSpace(input.ProviderIconURL)
	provider := service.ResolveModelProvider(globalModel.ModelName, providerInput, iconURLInput)
	if providerInput != "" || strings.TrimSpace(globalModel.Provider) == "" {
		globalModel.Provider = provider.ID
	}
	if providerInput != "" {
		// An explicitly selected provider makes an empty icon URL an intentional clear.
		globalModel.ProviderIconURL = iconURLInput
	} else if iconURLInput != "" || strings.TrimSpace(globalModel.ProviderIconURL) == "" {
		globalModel.ProviderIconURL = provider.IconURL
	}
	globalModel.QuotaType = normalizeQuotaType(input.QuotaType)
	globalModel.InputPrice = input.InputPrice
	globalModel.OutputPrice = input.OutputPrice
	globalModel.CachedInputPrice = input.CachedInputPrice
	globalModel.CacheWriteInputPrice = input.CacheWriteInputPrice
	globalModel.CacheWrite1hInputPrice = input.CacheWrite1hInputPrice
	globalModel.ImageInputPrice = input.ImageInputPrice
	globalModel.ImageOutputPrice = input.ImageOutputPrice
	globalModel.AudioInputPrice = input.AudioInputPrice
	globalModel.AudioOutputPrice = input.AudioOutputPrice
	globalModel.InputPriceTiers = model.NormalizePriceTiers(input.InputPriceTiers)
	globalModel.OutputPriceTiers = model.NormalizePriceTiers(input.OutputPriceTiers)
	globalModel.CachedInputPriceTiers = model.NormalizePriceTiers(input.CachedInputPriceTiers)
	globalModel.CacheWriteInputPriceTiers = model.NormalizePriceTiers(input.CacheWriteInputPriceTiers)
	globalModel.CacheWrite1hInputPriceTiers = model.NormalizePriceTiers(input.CacheWrite1hInputPriceTiers)
	globalModel.ImageInputPriceTiers = model.NormalizePriceTiers(input.ImageInputPriceTiers)
	globalModel.ImageOutputPriceTiers = model.NormalizePriceTiers(input.ImageOutputPriceTiers)
	globalModel.AudioInputPriceTiers = model.NormalizePriceTiers(input.AudioInputPriceTiers)
	globalModel.AudioOutputPriceTiers = model.NormalizePriceTiers(input.AudioOutputPriceTiers)
	globalModel.VideoBillingConfig = model.NormalizeVideoBillingConfig(input.VideoBillingConfig)
	if globalModel.QuotaType == model.QuotaTypeVideoResolutionDuration {
		if err := validateVideoBillingConfig(globalModel.VideoBillingConfig); err != nil {
			return model.Model{}, err
		}
	}
	if input.Enabled != nil {
		globalModel.Enabled = *input.Enabled
	} else if existing == nil {
		globalModel.Enabled = true
	}
	return globalModel, nil
}

func normalizeQuotaType(value int) int {
	switch value {
	case 1:
		return 1
	case model.QuotaTypeVideoResolutionDuration:
		return model.QuotaTypeVideoResolutionDuration
	}
	return 0
}

func validateVideoBillingConfig(config model.VideoBillingConfig) error {
	config = model.NormalizeVideoBillingConfig(config)
	if len(config.Resolutions) == 0 {
		return errors.New("Video billing requires at least one resolution price")
	}
	for _, resolution := range config.Resolutions {
		if len(resolution.Durations) == 0 && resolution.DurationUnitPrice.LessThanOrEqual(decimal.Zero) {
			return errors.New("Video billing requires each resolution to have duration prices or a duration unit price")
		}
	}
	return nil
}

func modelConfigFromInput(input modelConfigInput, existing *model.ModelConfig) (model.ModelConfig, error) {
	if input.ChannelID == 0 && existing == nil {
		return model.ModelConfig{}, errors.New("Channel is required")
	}

	globalModel, err := globalModelFromInput(input, existing)
	if err != nil {
		return model.ModelConfig{}, err
	}

	config := model.ModelConfig{}
	if existing != nil {
		config = *existing
	}
	if input.ChannelID != 0 {
		config.ChannelID = input.ChannelID
	}
	config.ModelID = globalModel.ID
	config.UpstreamModelName = strings.TrimSpace(input.UpstreamModelName)
	if config.UpstreamModelName == "" {
		config.UpstreamModelName = globalModel.ModelName
	}
	if input.Enabled != nil {
		config.Enabled = *input.Enabled
	} else if existing == nil {
		config.Enabled = true
	}
	return config, nil
}

func globalModelFromInput(input modelConfigInput, existing *model.ModelConfig) (model.Model, error) {
	modelName := strings.TrimSpace(input.ModelName)
	modelID := input.ModelID
	if modelID == 0 && existing != nil {
		modelID = existing.ModelID
	}
	if modelName == "" && modelID == 0 {
		return model.Model{}, errors.New("Model is required")
	}

	var globalModel model.Model
	if modelID != 0 {
		if err := model.DB.First(&globalModel, modelID).Error; err != nil {
			return model.Model{}, err
		}
		if modelName == "" {
			modelName = globalModel.ModelName
		}
		if modelName != "" {
			globalModel.ModelName = modelName
		}
	} else {
		err := model.DB.Where(&model.Model{ModelName: modelName}).First(&globalModel).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			globalModel = model.Model{ModelName: modelName, Enabled: true}
		} else if err != nil {
			return model.Model{}, err
		}
	}

	providerInput := strings.TrimSpace(input.Provider)
	iconURLInput := strings.TrimSpace(input.ProviderIconURL)
	provider := service.ResolveModelProvider(modelName, providerInput, iconURLInput)
	if strings.TrimSpace(globalModel.Provider) == "" || providerInput != "" {
		globalModel.Provider = provider.ID
	}
	if providerInput != "" {
		// An explicitly selected provider makes an empty icon URL an intentional clear.
		globalModel.ProviderIconURL = iconURLInput
	} else if strings.TrimSpace(globalModel.ProviderIconURL) == "" || iconURLInput != "" {
		globalModel.ProviderIconURL = provider.IconURL
	}
	if !globalModel.Enabled {
		globalModel.Enabled = true
	}

	if globalModel.ID == 0 {
		if err := model.DB.Create(&globalModel).Error; err != nil {
			return model.Model{}, err
		}
		return globalModel, nil
	}
	if err := model.DB.Save(&globalModel).Error; err != nil {
		return model.Model{}, err
	}
	return globalModel, nil
}

func hydrateModelConfigResponses(configs []model.ModelConfig) {
	for index := range configs {
		hydrateModelConfigResponse(&configs[index])
	}
}

func hydrateModelConfigResponse(config *model.ModelConfig) {
	if config == nil || config.Model.ID == 0 {
		return
	}
	config.ModelName = config.Model.ModelName
	config.Provider = config.Model.Provider
	config.ProviderIconURL = config.Model.ProviderIconURL
	if strings.TrimSpace(config.UpstreamModelName) == "" {
		config.UpstreamModelName = config.Model.ModelName
	}
}

// UserChannelAPI handles user-facing logical channels.
type UserChannelAPI struct{}

type userChannelCatalogItem struct {
	ID                  uint                                `json:"id"`
	Name                string                              `json:"name"`
	Description         string                              `json:"description"`
	Enabled             bool                                `json:"enabled"`
	Models              []string                            `json:"models"`
	ModelIcons          map[string]string                   `json:"model_icons"`
	VideoBillingConfigs map[string]model.VideoBillingConfig `json:"video_billing_configs"`
}

func (api *UserChannelAPI) List(c *gin.Context) {
	var channels []model.UserChannel
	model.DB.Preload("Channels.Models.Model").Preload("AllowedGroups.Group").Find(&channels)
	c.JSON(http.StatusOK, channels)
}

func (api *UserChannelAPI) Create(c *gin.Context) {
	var channel model.UserChannel
	if err := c.ShouldBindJSON(&channel); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	channel.RoutingAlgorithm = service.RoutingAlgorithm(channel.RoutingAlgorithm)
	if err := model.DB.Create(&channel).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, channel)
}

func (api *UserChannelAPI) Update(c *gin.Context) {
	id := c.Param("id")
	var channel model.UserChannel
	if err := model.DB.First(&channel, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User channel not found"})
		return
	}
	channelID := channel.ID
	if err := c.ShouldBindJSON(&channel); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	channel.ID = channelID
	channel.RoutingAlgorithm = service.RoutingAlgorithm(channel.RoutingAlgorithm)
	if err := model.DB.Save(&channel).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, channel)
}

func (api *UserChannelAPI) Delete(c *gin.Context) {
	id := c.Param("id")
	if err := model.DB.Delete(&model.UserChannel{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "User channel deleted"})
}

func (api *UserChannelAPI) SetAllowedGroups(c *gin.Context) {
	var channel model.UserChannel
	if err := model.DB.First(&channel, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User channel not found"})
		return
	}
	var input struct {
		GroupIDs []uint `json:"group_ids"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	groupIDs := uniquePositiveUintIDs(input.GroupIDs)
	if len(groupIDs) > 0 {
		var count int64
		if err := model.DB.Model(&model.Group{}).Where("id IN ?", groupIDs).Count(&count).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to validate groups"})
			return
		}
		if count != int64(len(groupIDs)) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "One or more groups do not exist"})
			return
		}
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_channel_id = ?", channel.ID).Delete(&model.UserChannelGroupAccess{}).Error; err != nil {
			return err
		}
		for _, groupID := range groupIDs {
			if err := tx.Create(&model.UserChannelGroupAccess{UserChannelID: channel.ID, GroupID: groupID}).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update allowed groups"})
		return
	}
	if err := model.DB.Preload("AllowedGroups.Group").First(&channel, channel.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load allowed groups"})
		return
	}
	c.JSON(http.StatusOK, channel.AllowedGroups)
}

func uniquePositiveUintIDs(values []uint) []uint {
	seen := make(map[uint]struct{}, len(values))
	result := make([]uint, 0, len(values))
	for _, value := range values {
		if value == 0 {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (api *UserChannelAPI) Catalog(c *gin.Context) {
	var channels []model.UserChannel
	if err := model.DB.
		Preload("Channels", "enabled = ?", true).
		Preload("Channels.Models", "enabled = ?", true).
		Preload("Channels.Models.Model", "enabled = ?", true).
		Where("enabled = ?", true).
		Order("name ASC").
		Find(&channels).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load user channel catalog"})
		return
	}

	response := make([]userChannelCatalogItem, 0, len(channels))
	for _, channel := range channels {
		modelSet := map[string]struct{}{}
		modelIcons := map[string]string{}
		videoBillingConfigs := map[string]model.VideoBillingConfig{}
		for _, upstream := range channel.Channels {
			for _, modelConfig := range upstream.Models {
				modelName := strings.TrimSpace(modelConfig.Model.ModelName)
				if modelName != "" {
					modelSet[modelName] = struct{}{}
					if iconURL := strings.TrimSpace(modelConfig.Model.ProviderIconURL); iconURL != "" {
						modelIcons[modelName] = iconURL
					}
					if modelConfig.Model.QuotaType == model.QuotaTypeVideoResolutionDuration {
						videoBillingConfigs[modelName] = model.MultiplyVideoBillingConfig(
							modelConfig.Model.VideoBillingConfig,
							channel.Multiplier,
						)
					}
				}
			}
		}
		models := make([]string, 0, len(modelSet))
		for modelName := range modelSet {
			models = append(models, modelName)
		}
		sort.Strings(models)
		response = append(response, userChannelCatalogItem{
			ID:                  channel.ID,
			Name:                channel.Name,
			Description:         channel.Description,
			Enabled:             channel.Enabled,
			Models:              models,
			ModelIcons:          modelIcons,
			VideoBillingConfigs: videoBillingConfigs,
		})
	}

	c.JSON(http.StatusOK, response)
}

// GroupAPI handles user group management
type GroupAPI struct{}

func (api *GroupAPI) List(c *gin.Context) {
	var groups []model.Group
	model.DB.Order("name ASC").Find(&groups)
	c.JSON(http.StatusOK, groups)
}

func (api *GroupAPI) Create(c *gin.Context) {
	var group model.Group
	if err := c.ShouldBindJSON(&group); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	group.Name = strings.TrimSpace(group.Name)
	if group.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Group name is required"})
		return
	}
	if group.Multiplier.IsZero() {
		group.Multiplier = decimal.NewFromInt(1)
	}
	if err := model.DB.Create(&group).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, group)
}

func (api *GroupAPI) Update(c *gin.Context) {
	id := c.Param("id")
	var group model.Group
	if err := model.DB.First(&group, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Group not found"})
		return
	}
	groupID := group.ID
	oldName := group.Name
	if err := c.ShouldBindJSON(&group); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	group.Name = strings.TrimSpace(group.Name)
	if group.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Group name is required"})
		return
	}
	if strings.EqualFold(oldName, "user") && !strings.EqualFold(group.Name, "user") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Default user group cannot be renamed"})
		return
	}
	if group.Multiplier.IsZero() {
		group.Multiplier = decimal.NewFromInt(1)
	}
	group.ID = groupID
	if err := model.DB.Save(&group).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, group)
}

func (api *GroupAPI) Delete(c *gin.Context) {
	id := c.Param("id")
	var group model.Group
	if err := model.DB.First(&group, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Group not found"})
		return
	}
	if strings.EqualFold(group.Name, "user") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Default user group cannot be deleted"})
		return
	}
	if groupInUse(group.ID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Group is in use"})
		return
	}
	if err := model.DB.Delete(&group).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Group deleted"})
}

func groupInUse(groupID uint) bool {
	checks := []struct {
		table string
		field string
	}{
		{"users", "group_id"},
		{"user_group_memberships", "group_id"},
		{"channel_group_multipliers", "group_id"},
		{"model_group_multipliers", "group_id"},
		{"redeem_codes", "group_id"},
	}
	for _, check := range checks {
		var count int64
		if err := model.DB.Table(check.table).Where(check.field+" = ?", groupID).Count(&count).Error; err == nil && count > 0 {
			return true
		}
	}
	return false
}

// ReferralAPI handles referral code and commission views.
type ReferralAPI struct{}

type referralMeResponse struct {
	Enabled         bool                          `json:"enabled"`
	Code            string                        `json:"code"`
	Link            string                        `json:"link"`
	CommissionRate  string                        `json:"commission_rate"`
	InviteCount     int64                         `json:"invite_count"`
	TotalCommission decimal.Decimal               `json:"total_commission"`
	RecentLogs      []model.ReferralCommissionLog `json:"recent_logs"`
}

func (api *ReferralAPI) GetMine(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var refreshed model.User
	if err := model.DB.First(&refreshed, user.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load user"})
		return
	}
	code, err := ensureReferralCode(&refreshed)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to prepare referral code"})
		return
	}

	var inviteCount int64
	model.DB.Model(&model.User{}).Where("referrer_id = ?", refreshed.ID).Count(&inviteCount)
	var totalCommission decimal.Decimal
	model.DB.Model(&model.ReferralCommissionLog{}).
		Where("referrer_id = ?", refreshed.ID).
		Select("COALESCE(SUM(amount), 0)").
		Row().
		Scan(&totalCommission)
	var logs []model.ReferralCommissionLog
	model.DB.Preload("ReferredUser").
		Where("referrer_id = ?", refreshed.ID).
		Order("created_at DESC").
		Limit(50).
		Find(&logs)

	c.JSON(http.StatusOK, referralMeResponse{
		Enabled:         settingBool("referral_enabled", false),
		Code:            code,
		Link:            referralLink(c, code),
		CommissionRate:  settingString("referral_commission_rate", "0"),
		InviteCount:     inviteCount,
		TotalCommission: totalCommission,
		RecentLogs:      logs,
	})
}

func (api *ReferralAPI) ListCommissions(c *gin.Context) {
	var logs []model.ReferralCommissionLog
	if err := model.DB.Preload("Referrer").Preload("ReferredUser").
		Order("created_at DESC").
		Limit(200).
		Find(&logs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list referral commissions"})
		return
	}
	c.JSON(http.StatusOK, logs)
}

// UserAPI handles user management
type UserAPI struct {
	AuthService *service.AuthService
}

type apiKeyResponse struct {
	ID                  uint             `json:"id"`
	Name                string           `json:"name"`
	APIKey              string           `json:"api_key"`
	KeyPrefix           string           `json:"key_prefix"`
	AllowedModels       []string         `json:"allowed_models"`
	AllowedUserChannels []uint           `json:"allowed_user_channels"`
	AllowedIPs          []string         `json:"allowed_ips"`
	QuotaLimit          decimal.Decimal  `json:"quota_limit"`
	QuotaRemaining      *decimal.Decimal `json:"quota_remaining,omitempty"`
	Enabled             bool             `json:"enabled"`
	Usage               usageStats       `json:"usage"`
	LastUsedAt          *time.Time       `json:"last_used_at"`
	UsageResetAt        *time.Time       `json:"usage_reset_at"`
	CreatedAt           time.Time        `json:"created_at"`
	UpdatedAt           time.Time        `json:"updated_at"`
}

type usageStats struct {
	RequestCount      int64           `json:"request_count"`
	InputTokens       int64           `json:"input_tokens"`
	OutputTokens      int64           `json:"output_tokens"`
	CachedInputTokens int64           `json:"cached_input_tokens"`
	TotalTokens       int64           `json:"total_tokens"`
	TotalCost         decimal.Decimal `json:"total_cost"`
}

type apiKeyInput struct {
	Name                string           `json:"name"`
	AllowedModels       []string         `json:"allowed_models"`
	AllowedUserChannels []uint           `json:"allowed_user_channels"`
	AllowedIPs          []string         `json:"allowed_ips"`
	QuotaLimit          *decimal.Decimal `json:"quota_limit"`
	Enabled             *bool            `json:"enabled"`
}

const maxUserAvatarBytes int64 = 2 << 20

func (api *UserAPI) GetMe(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var response model.User
	if err := loadUserForResponse(user.ID).First(&response, user.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load user"})
		return
	}
	c.JSON(http.StatusOK, response)
}

func (api *UserAPI) UploadAvatar(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	// Keep a malformed multipart body from consuming the general request limit.
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxUserAvatarBytes+(1<<20))
	if err := c.Request.ParseMultipartForm(maxUserAvatarBytes); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Avatar upload is invalid or too large"})
		return
	}
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Avatar file is required"})
		return
	}
	defer file.Close()
	if header.Size > maxUserAvatarBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "Avatar must not exceed 2 MB"})
		return
	}
	data, err := io.ReadAll(io.LimitReader(file, maxUserAvatarBytes+1))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to read avatar"})
		return
	}
	if len(data) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Avatar file is empty"})
		return
	}
	if int64(len(data)) > maxUserAvatarBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "Avatar must not exceed 2 MB"})
		return
	}
	mimeType := http.DetectContentType(data)
	if !isAllowedAvatarMIMEType(mimeType) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Avatar must be a JPEG, PNG, GIF, or WebP image"})
		return
	}

	now := time.Now().UTC()
	avatarURL := "/api/avatars/" + strconv.FormatUint(uint64(user.ID), 10) + "?v=" + strconv.FormatInt(now.UnixNano(), 10)
	avatar := model.UserAvatar{UserID: user.ID, MIMEType: mimeType, Data: data, UpdatedAt: now}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "user_id"}},
			DoUpdates: clause.Assignments(map[string]interface{}{
				"mime_type":  mimeType,
				"data":       data,
				"updated_at": now,
			}),
		}).Create(&avatar).Error; err != nil {
			return err
		}
		return tx.Model(&model.User{}).Where("id = ?", user.ID).Update("avatar_url", avatarURL).Error
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save avatar"})
		return
	}

	var response model.User
	if err := loadUserForResponse(user.ID).First(&response, user.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Avatar saved but failed to load user"})
		return
	}
	c.JSON(http.StatusOK, response)
}

func (api *UserAPI) GetAvatar(c *gin.Context) {
	userID, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || userID == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Avatar not found"})
		return
	}
	var avatar model.UserAvatar
	if err := model.DB.First(&avatar, uint(userID)).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.Status(http.StatusNotFound)
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load avatar"})
		return
	}
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, avatar.MIMEType, avatar.Data)
}

func isAllowedAvatarMIMEType(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func (api *UserAPI) PasswordChangeMethod(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	var response model.User
	if err := model.DB.First(&response, user.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load user"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"method":       service.PasswordChangeMethod(),
		"email":        response.Email,
		"password_set": strings.TrimSpace(response.PasswordHash) != "",
	})
}

func (api *UserAPI) SendPasswordChangeEmailCode(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if api.AuthService == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Authentication service is unavailable"})
		return
	}
	if err := api.AuthService.SendPasswordChangeEmailCode(user.ID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Verification code sent"})
}

func (api *UserAPI) ChangePassword(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if api.AuthService == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Authentication service is unavailable"})
		return
	}

	var input struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
		EmailCode       string `json:"email_code"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := api.AuthService.ChangePassword(service.ChangePasswordInput{
		UserID:          user.ID,
		CurrentPassword: input.CurrentPassword,
		NewPassword:     input.NewPassword,
		EmailCode:       input.EmailCode,
	}); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Password updated"})
}

func (api *UserAPI) RotateAPIKey(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	raw, hash, err := service.GenerateAPIKey()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate API key"})
		return
	}

	keyID := strings.TrimSpace(c.Param("id"))
	if keyID != "" {
		var apiKey model.APIKey
		if err := model.DB.Where("id = ? AND user_id = ?", keyID, user.ID).First(&apiKey).Error; err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "API key not found"})
			return
		}
		updates := map[string]interface{}{
			"api_key":      raw,
			"key_hash":     hash,
			"key_prefix":   service.APIKeyPrefix(raw),
			"last_used_at": nil,
		}
		if err := model.DB.Model(&apiKey).Updates(updates).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to rotate API key"})
			return
		}
		if err := model.DB.First(&apiKey, apiKey.ID).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reload API key"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"api_key": raw,
			"key":     toAPIKeyResponse(apiKey),
		})
		return
	}

	apiKey := model.APIKey{
		UserID:    user.ID,
		Name:      "Default key",
		APIKey:    raw,
		KeyHash:   hash,
		KeyPrefix: service.APIKeyPrefix(raw),
		Enabled:   true,
	}
	if err := model.DB.Create(&apiKey).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to rotate API key"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"api_key": raw,
		"key":     toAPIKeyResponse(apiKey),
	})
}

func (api *UserAPI) ResetAPIKeyUsage(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	var apiKey model.APIKey
	if err := model.DB.Where("id = ? AND user_id = ?", c.Param("id"), user.ID).First(&apiKey).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "API key not found"})
		return
	}

	now := time.Now()
	if err := model.DB.Model(&apiKey).Update("usage_reset_at", &now).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reset API key usage"})
		return
	}
	apiKey.UsageResetAt = &now
	c.JSON(http.StatusOK, toAPIKeyResponse(apiKey))
}

func (api *UserAPI) ListAPIKeys(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	var keys []model.APIKey
	if err := model.DB.Where("user_id = ?", user.ID).Order("created_at DESC").Find(&keys).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list API keys"})
		return
	}

	response := make([]apiKeyResponse, 0, len(keys))
	for _, key := range keys {
		response = append(response, toAPIKeyResponse(key))
	}
	c.JSON(http.StatusOK, response)
}

func (api *UserAPI) CreateAPIKey(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	var input apiKeyInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	userChannelID, err := validateAPIKeyUserChannel(input.AllowedUserChannels)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	quotaLimit, err := validateAPIKeyQuotaLimit(input.QuotaLimit)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	raw, hash, err := service.GenerateAPIKey()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate API key"})
		return
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}

	apiKey := model.APIKey{
		UserID:              user.ID,
		Name:                firstNonEmptyString(input.Name, "API key"),
		APIKey:              raw,
		KeyHash:             hash,
		KeyPrefix:           service.APIKeyPrefix(raw),
		AllowedModels:       service.JoinList(input.AllowedModels),
		AllowedUserChannels: service.JoinUintList([]uint{userChannelID}),
		AllowedIPs:          service.JoinList(input.AllowedIPs),
		QuotaLimit:          quotaLimit,
		Enabled:             enabled,
	}
	if err := model.DB.Create(&apiKey).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create API key"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"api_key": raw,
		"key":     toAPIKeyResponse(apiKey),
	})
}

func (api *UserAPI) UpdateAPIKey(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	var apiKey model.APIKey
	if err := model.DB.Where("id = ? AND user_id = ?", c.Param("id"), user.ID).First(&apiKey).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "API key not found"})
		return
	}

	var input apiKeyInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	userChannelID, err := validateAPIKeyUserChannel(input.AllowedUserChannels)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	updates := map[string]interface{}{
		"name":                  strings.TrimSpace(input.Name),
		"allowed_models":        service.JoinList(input.AllowedModels),
		"allowed_user_channels": service.JoinUintList([]uint{userChannelID}),
		"allowed_ips":           service.JoinList(input.AllowedIPs),
	}
	if input.QuotaLimit != nil {
		quotaLimit, err := validateAPIKeyQuotaLimit(input.QuotaLimit)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		updates["quota_limit"] = quotaLimit
	}
	if input.Enabled != nil {
		updates["enabled"] = *input.Enabled
	}

	if err := model.DB.Model(&apiKey).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update API key"})
		return
	}
	if err := model.DB.First(&apiKey, apiKey.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to reload API key"})
		return
	}
	c.JSON(http.StatusOK, toAPIKeyResponse(apiKey))
}

func (api *UserAPI) DeleteAPIKey(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if err := model.DB.Where("id = ? AND user_id = ?", c.Param("id"), user.ID).Delete(&model.APIKey{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete API key"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "API key deleted"})
}

func (api *UserAPI) List(c *gin.Context) {
	var users []model.User
	loadUserForResponse(0).Order("created_at DESC").Find(&users)
	c.JSON(http.StatusOK, users)
}

func (api *UserAPI) Update(c *gin.Context) {
	id := c.Param("id")
	var user model.User
	if err := model.DB.First(&user, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	var input struct {
		Balance *decimal.Decimal `json:"balance"`
		GroupID *uint            `json:"group_id"`
		IsAdmin *bool            `json:"is_admin"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	updates := map[string]interface{}{}
	if input.Balance != nil {
		updates["balance"] = *input.Balance
	}
	if input.GroupID != nil {
		if *input.GroupID == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Group is required"})
			return
		}
		var group model.Group
		if err := model.DB.First(&group, *input.GroupID).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Group not found"})
			return
		}
		updates["group_id"] = *input.GroupID
	}
	if input.IsAdmin != nil {
		updates["is_admin"] = *input.IsAdmin
	}
	if len(updates) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No fields to update"})
		return
	}
	oldGroupID := user.GroupID
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&user).Updates(updates).Error; err != nil {
			return err
		}
		if input.GroupID != nil {
			if oldGroupID != 0 && oldGroupID != *input.GroupID {
				if err := tx.Where("user_id = ? AND group_id = ? AND expires_at IS NULL", user.ID, oldGroupID).
					Delete(&model.UserGroupMembership{}).Error; err != nil {
					return err
				}
			}
			if err := tx.Where(&model.UserGroupMembership{UserID: user.ID, GroupID: *input.GroupID}).
				FirstOrCreate(&model.UserGroupMembership{UserID: user.ID, GroupID: *input.GroupID}).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if err := loadUserForResponse(user.ID).First(&user, user.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, user)
}

func (api *UserAPI) Delete(c *gin.Context) {
	id := c.Param("id")
	if err := model.DB.Delete(&model.User{}, id).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "User deleted"})
}

func loadUserForResponse(userID uint) *gorm.DB {
	query := model.DB.
		Preload("Group").
		Preload("Groups.Group").
		Preload("Referrer")
	if userID != 0 {
		query = query.Where("id = ?", userID)
	}
	return query
}

// StatsAPI handles usage statistics
type StatsAPI struct{}

type userChannelUsageItem struct {
	ID                uint            `json:"id"`
	Name              string          `json:"name"`
	Description       string          `json:"description"`
	RoutingAlgorithm  string          `json:"routing_algorithm"`
	Multiplier        decimal.Decimal `json:"multiplier"`
	RequestCount      int64           `json:"request_count"`
	InputTokens       int64           `json:"input_tokens"`
	OutputTokens      int64           `json:"output_tokens"`
	CachedInputTokens int64           `json:"cached_input_tokens"`
	TotalTokens       int64           `json:"total_tokens"`
	TotalCost         decimal.Decimal `json:"total_cost"`
}

type upstreamChannelUsageItem struct {
	ID                uint            `json:"id"`
	Name              string          `json:"name"`
	Type              string          `json:"type"`
	UserChannelID     *uint           `json:"user_channel_id"`
	UserChannelName   string          `json:"user_channel_name"`
	Priority          int             `json:"priority"`
	Weight            int             `json:"weight"`
	RequestCount      int64           `json:"request_count"`
	InputTokens       int64           `json:"input_tokens"`
	OutputTokens      int64           `json:"output_tokens"`
	CachedInputTokens int64           `json:"cached_input_tokens"`
	TotalTokens       int64           `json:"total_tokens"`
	TotalCost         decimal.Decimal `json:"total_cost"`
}

func (api *StatsAPI) GetLogs(c *gin.Context) {
	filter, err := tokenLogFilterFromRequest(c, nil)
	if writePaginationError(c, err) {
		return
	}
	if !wantsPaginatedResponse(c) {
		logs, _, err := model.ListTokenLogs(filter, 0, 100)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load usage logs"})
			return
		}
		c.JSON(http.StatusOK, logs)
		return
	}

	page, pageSize := parsePagination(c)
	logs, total, err := model.ListTokenLogs(filter, (page-1)*pageSize, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load usage logs"})
		return
	}
	c.JSON(http.StatusOK, paginatedResponse{Items: logs, Total: total, Page: page, PageSize: pageSize})
}

type auditLogUserResponse struct {
	ID       uint   `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
}

type auditLogResponse struct {
	ID         uint                  `json:"id"`
	LogType    string                `json:"log_type"`
	Action     string                `json:"action"`
	Resource   string                `json:"resource"`
	UserID     *uint                 `json:"user_id,omitempty"`
	User       *auditLogUserResponse `json:"user,omitempty"`
	APIKeyID   *uint                 `json:"api_key_id,omitempty"`
	Method     string                `json:"method"`
	Path       string                `json:"path"`
	Query      string                `json:"query,omitempty"`
	StatusCode int                   `json:"status_code"`
	IPAddress  string                `json:"ip_address"`
	UserAgent  string                `json:"user_agent"`
	Message    string                `json:"message"`
	Metadata   string                `json:"metadata,omitempty"`
	DurationMs int64                 `json:"duration_ms"`
	CreatedAt  time.Time             `json:"created_at"`
}

func (api *StatsAPI) GetAuditLogs(c *gin.Context) {
	filter, err := auditLogFilterFromRequest(c)
	if writePaginationError(c, err) {
		return
	}

	page, pageSize := parsePagination(c)
	logs, total, err := model.ListAuditLogs(filter, (page-1)*pageSize, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load audit logs"})
		return
	}
	users := auditLogUsers(logs)
	items := make([]auditLogResponse, 0, len(logs))
	for _, logItem := range logs {
		items = append(items, auditLogToResponse(logItem, users))
	}
	c.JSON(http.StatusOK, paginatedResponse{Items: items, Total: total, Page: page, PageSize: pageSize})
}

func auditLogToResponse(logItem model.AuditLog, users map[uint]model.User) auditLogResponse {
	item := auditLogResponse{
		ID:         logItem.ID,
		LogType:    logItem.LogType,
		Action:     logItem.Action,
		Resource:   logItem.Resource,
		UserID:     logItem.UserID,
		APIKeyID:   logItem.APIKeyID,
		Method:     logItem.Method,
		Path:       logItem.Path,
		Query:      logItem.Query,
		StatusCode: logItem.StatusCode,
		IPAddress:  logItem.IPAddress,
		UserAgent:  logItem.UserAgent,
		Message:    logItem.Message,
		Metadata:   logItem.Metadata,
		DurationMs: logItem.DurationMs,
		CreatedAt:  logItem.CreatedAt,
	}
	if logItem.UserID != nil && users[*logItem.UserID].ID != 0 {
		user := users[*logItem.UserID]
		item.User = &auditLogUserResponse{
			ID:       user.ID,
			Username: user.Username,
			Email:    user.Email,
		}
	}
	return item
}

func tokenLogFilterFromRequest(c *gin.Context, userID *uint) (model.TokenLogFilter, error) {
	filter := model.TokenLogFilter{UserID: userID}
	if userID == nil {
		if value := positiveIntQuery(c, "user_id", 0); value > 0 {
			parsed := uint(value)
			filter.UserID = &parsed
		}
	}
	if value := positiveIntQuery(c, "api_key_id", 0); value > 0 {
		parsed := uint(value)
		filter.APIKeyID = &parsed
	}
	if value := positiveIntQuery(c, "user_channel_id", 0); value > 0 {
		parsed := uint(value)
		filter.UserChannelID = &parsed
	}
	if value := positiveIntQuery(c, "channel_id", 0); value > 0 {
		parsed := uint(value)
		filter.ChannelID = &parsed
	}
	filter.ModelName = strings.TrimSpace(c.Query("model_name"))
	return filter, applyLogTimeRange(c, &filter.Since, &filter.Until)
}

func auditLogFilterFromRequest(c *gin.Context) (model.AuditLogFilter, error) {
	filter := model.AuditLogFilter{
		LogType:    strings.TrimSpace(c.Query("log_type")),
		Action:     strings.TrimSpace(c.Query("action")),
		Path:       strings.TrimSpace(c.Query("path")),
		StatusCode: positiveIntQuery(c, "status_code", 0),
	}
	if value := positiveIntQuery(c, "user_id", 0); value > 0 {
		parsed := uint(value)
		filter.UserID = &parsed
	}
	return filter, applyLogTimeRange(c, &filter.Since, &filter.Until)
}

func applyLogTimeRange(c *gin.Context, since, until **time.Time) error {
	if raw := firstNonEmptyString(c.Query("start_time"), c.Query("start_date")); strings.TrimSpace(raw) != "" {
		parsed, _, err := parseTimeBoundary(raw, false)
		if err != nil {
			return err
		}
		*since = &parsed
	}
	if raw := firstNonEmptyString(c.Query("end_time"), c.Query("end_date")); strings.TrimSpace(raw) != "" {
		parsed, exclusive, err := parseTimeBoundary(raw, true)
		if err != nil {
			return err
		}
		if exclusive {
			parsed = parsed.Add(-time.Nanosecond)
		}
		*until = &parsed
	}
	return nil
}

func auditLogUsers(logs []model.AuditLog) map[uint]model.User {
	ids := make([]uint, 0, len(logs))
	seen := map[uint]struct{}{}
	for _, entry := range logs {
		if entry.UserID != nil && *entry.UserID != 0 {
			if _, exists := seen[*entry.UserID]; !exists {
				seen[*entry.UserID] = struct{}{}
				ids = append(ids, *entry.UserID)
			}
		}
	}
	users := map[uint]model.User{}
	if len(ids) == 0 {
		return users
	}
	var records []model.User
	if err := model.DB.Where("id IN ?", ids).Find(&records).Error; err != nil {
		return users
	}
	for _, user := range records {
		users[user.ID] = user
	}
	return users
}

func (api *StatsAPI) GetChannelUsage(c *gin.Context) {
	var sourceUserChannels []model.UserChannel
	if err := model.DB.Order("name ASC").Find(&sourceUserChannels).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load user channel usage"})
		return
	}
	userChannels := make([]userChannelUsageItem, 0, len(sourceUserChannels))
	for _, channel := range sourceUserChannels {
		summary, err := model.SummarizeTokenLogs(model.TokenLogFilter{UserChannelID: &channel.ID})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load user channel usage"})
			return
		}
		userChannels = append(userChannels, userChannelUsageItem{ID: channel.ID, Name: channel.Name, Description: channel.Description, RoutingAlgorithm: channel.RoutingAlgorithm, Multiplier: channel.Multiplier, RequestCount: summary.RequestCount, InputTokens: summary.InputTokens, OutputTokens: summary.OutputTokens, CachedInputTokens: summary.CachedInputTokens, TotalTokens: summary.TotalTokens, TotalCost: summary.TotalCost})
	}

	var sourceChannels []model.Channel
	if err := model.DB.Preload("UserChannel").Order("priority DESC, weight DESC, name ASC").Find(&sourceChannels).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load upstream channel usage"})
		return
	}
	upstreamChannels := make([]upstreamChannelUsageItem, 0, len(sourceChannels))
	for _, channel := range sourceChannels {
		summary, err := model.SummarizeTokenLogs(model.TokenLogFilter{ChannelID: &channel.ID})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load upstream channel usage"})
			return
		}
		name := ""
		if channel.UserChannelID != nil {
			name = channel.UserChannel.Name
		}
		upstreamChannels = append(upstreamChannels, upstreamChannelUsageItem{ID: channel.ID, Name: channel.Name, Type: channel.Type, UserChannelID: channel.UserChannelID, UserChannelName: name, Priority: channel.Priority, Weight: channel.Weight, RequestCount: summary.RequestCount, InputTokens: summary.InputTokens, OutputTokens: summary.OutputTokens, CachedInputTokens: summary.CachedInputTokens, TotalTokens: summary.TotalTokens, TotalCost: summary.TotalCost})
	}

	c.JSON(http.StatusOK, gin.H{
		"user_channels":     userChannels,
		"upstream_channels": upstreamChannels,
	})
}

func (api *StatsAPI) GetUserLogs(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	filter, err := tokenLogFilterFromRequest(c, &user.ID)
	if writePaginationError(c, err) {
		return
	}
	if !wantsPaginatedResponse(c) {
		logs, _, err := model.ListTokenLogs(filter, 0, 100)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load usage logs"})
			return
		}
		c.JSON(http.StatusOK, logs)
		return
	}

	page, pageSize := parsePagination(c)
	logs, total, err := model.ListTokenLogs(filter, (page-1)*pageSize, pageSize)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load usage logs"})
		return
	}
	c.JSON(http.StatusOK, paginatedResponse{Items: logs, Total: total, Page: page, PageSize: pageSize})
}

func (api *StatsAPI) GetDashboardStats(c *gin.Context) {
	var userCount int64
	var channelCount int64
	var todayRequests int64
	var totalCost decimal.Decimal

	model.DB.Model(&model.User{}).Count(&userCount)
	model.DB.Model(&model.Channel{}).Count(&channelCount)
	today := time.Now().Truncate(24 * time.Hour)
	if summary, err := model.SummarizeTokenLogs(model.TokenLogFilter{Since: &today}); err == nil {
		todayRequests = summary.RequestCount
	}
	if summary, err := model.SummarizeTokenLogs(model.TokenLogFilter{}); err == nil {
		totalCost = summary.TotalCost
	}

	c.JSON(http.StatusOK, gin.H{
		"users":          userCount,
		"channels":       channelCount,
		"total_cost":     totalCost,
		"today_requests": todayRequests,
	})
}

func (api *StatsAPI) GetUserDashboardStats(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}

	var totalRequests int64
	var todayRequests int64
	var totalCost decimal.Decimal
	var rpm int64
	var tpm int64
	rateWindowStart := time.Now().Add(-1 * time.Minute)

	if summary, err := model.SummarizeTokenLogs(model.TokenLogFilter{UserID: &user.ID}); err == nil {
		totalRequests = summary.RequestCount
		totalCost = summary.TotalCost
	}
	today := time.Now().Truncate(24 * time.Hour)
	if summary, err := model.SummarizeTokenLogs(model.TokenLogFilter{UserID: &user.ID, Since: &today}); err == nil {
		todayRequests = summary.RequestCount
	}
	if summary, err := model.SummarizeTokenLogs(model.TokenLogFilter{UserID: &user.ID, Since: &rateWindowStart}); err == nil {
		rpm = summary.RequestCount
		tpm = summary.TotalTokens
	}

	c.JSON(http.StatusOK, gin.H{
		"balance":        user.Balance,
		"group":          user.Group,
		"total_requests": totalRequests,
		"today_requests": todayRequests,
		"total_cost":     totalCost,
		"rpm":            rpm,
		"tpm":            tpm,
	})
}

func currentUser(c *gin.Context) (*model.User, bool) {
	val, exists := c.Get("user")
	if !exists {
		return nil, false
	}
	user, ok := val.(*model.User)
	return user, ok && user != nil
}

func toAPIKeyResponse(apiKey model.APIKey) apiKeyResponse {
	usage := apiKeyUsageStats(apiKey.ID, apiKey.UserID, apiKey.UsageResetAt)
	response := apiKeyResponse{
		ID:                  apiKey.ID,
		Name:                apiKey.Name,
		APIKey:              apiKey.APIKey,
		KeyPrefix:           apiKey.KeyPrefix,
		AllowedModels:       service.ParseList(apiKey.AllowedModels),
		AllowedUserChannels: service.ParseUintList(apiKey.AllowedUserChannels),
		AllowedIPs:          service.ParseList(apiKey.AllowedIPs),
		QuotaLimit:          apiKey.QuotaLimit,
		Enabled:             apiKey.Enabled,
		Usage:               usage,
		LastUsedAt:          apiKey.LastUsedAt,
		UsageResetAt:        apiKey.UsageResetAt,
		CreatedAt:           apiKey.CreatedAt,
		UpdatedAt:           apiKey.UpdatedAt,
	}
	if apiKey.QuotaLimit.GreaterThan(decimal.Zero) {
		remaining := apiKey.QuotaLimit.Sub(usage.TotalCost)
		if remaining.LessThan(decimal.Zero) {
			remaining = decimal.Zero
		}
		response.QuotaRemaining = &remaining
	}
	return response
}

func apiKeyUsageStats(apiKeyID uint, userID uint, usageResetAt *time.Time) usageStats {
	if apiKeyID == 0 || userID == 0 {
		return usageStats{}
	}
	filter := model.TokenLogFilter{APIKeyID: &apiKeyID, UserID: &userID, Since: usageResetAt}
	summary, err := model.SummarizeTokenLogs(filter)
	if err != nil {
		return usageStats{}
	}
	return usageStats{RequestCount: summary.RequestCount, InputTokens: summary.InputTokens, OutputTokens: summary.OutputTokens, CachedInputTokens: summary.CachedInputTokens, TotalTokens: summary.TotalTokens, TotalCost: summary.TotalCost}
}

func validateAPIKeyUserChannel(userChannelIDs []uint) (uint, error) {
	if service.PersonalModeEnabled() {
		if len(userChannelIDs) == 0 {
			return 0, nil
		}
		return userChannelIDs[0], nil
	}
	if len(userChannelIDs) != 1 || userChannelIDs[0] == 0 {
		return 0, errors.New("API key must be bound to exactly one user channel")
	}
	var userChannel model.UserChannel
	if err := model.DB.Where("id = ? AND enabled = ?", userChannelIDs[0], true).First(&userChannel).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, errors.New("User channel not found or disabled")
		}
		return 0, err
	}
	return userChannel.ID, nil
}

func validateAPIKeyQuotaLimit(value *decimal.Decimal) (decimal.Decimal, error) {
	if value == nil {
		return decimal.Zero, nil
	}
	if value.LessThan(decimal.Zero) {
		return decimal.Zero, errors.New("API key quota limit cannot be negative")
	}
	return *value, nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func ensureReferralCode(user *model.User) (string, error) {
	if user == nil {
		return "", nil
	}
	if user.ReferralCode != nil && strings.TrimSpace(*user.ReferralCode) != "" {
		return strings.TrimSpace(*user.ReferralCode), nil
	}
	code, err := model.NewUniqueReferralCode()
	if err != nil {
		return "", err
	}
	if err := model.DB.Model(user).Update("referral_code", code).Error; err != nil {
		return "", err
	}
	user.ReferralCode = &code
	return code, nil
}

func referralLink(c *gin.Context, code string) string {
	baseURL := strings.TrimRight(settingString("base_url", ""), "/")
	if baseURL == "" {
		scheme := "http"
		if c.Request.TLS != nil {
			scheme = "https"
		}
		baseURL = scheme + "://" + c.Request.Host
	}
	return baseURL + "/?ref=" + code
}
