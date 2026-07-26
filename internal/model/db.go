package model

import (
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/veloce-ailab/veloce/internal/config"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

var DB *gorm.DB

// gormConfig 统一的 GORM 配置：忽略 ErrRecordNotFound 日志。
// First/Take 查不到记录属于正常业务分支（例如价格同步时判断模型是否已存在），
// GORM 默认 logger 却会把每次未命中都作为 ERROR 打到终端，产生大量日志噪音。
func gormConfig() *gorm.Config {
	return &gorm.Config{
		Logger: gormlogger.New(log.New(os.Stdout, "\r\n", log.LstdFlags), gormlogger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  gormlogger.Warn,
			IgnoreRecordNotFoundError: true,
			Colorful:                  true,
		}),
	}
}

func InitDB() {
	dialector, isSQLite, err := databaseDialector()
	if err != nil {
		log.Fatal(err)
	}
	DB, err = gorm.Open(dialector, gormConfig())
	if err != nil {
		log.Fatalf("failed to connect database: %v", err)
	}
	sqlDB, err := DB.DB()
	if err != nil {
		log.Fatalf("failed to access database connection pool: %v", err)
	}
	if err := configureDatabaseConnection(sqlDB, isSQLite); err != nil {
		log.Fatalf("failed to configure database connection: %v", err)
	}
	if err := sqlDB.Ping(); err != nil {
		log.Fatalf("failed to ping database: %v", err)
	}
	if isSQLite {
		logSQLiteConfiguration(sqlDB)
	}
	hadCachedInputPrice := DB.Migrator().HasColumn(&Model{}, "cached_input_price")
	hadCacheWriteInputPrice := DB.Migrator().HasColumn(&Model{}, "cache_write_input_price")
	hadCacheWrite1hInputPrice := DB.Migrator().HasColumn(&Model{}, "cache_write_1h_input_price")

	// Auto Migrate
	err = DB.AutoMigrate(
		&User{},
		&UserAvatar{},
		&APIKey{},
		&EmailVerificationCode{},
		&PhoneVerificationCode{},
		&OIDCBindRequest{},
		&WebAuthnChallenge{},
		&PasskeyCredential{},
		&CheckInRecord{},
		&PaymentOrder{},
		&WalletTransaction{},
		&WalletLimitUsage{},
		&Group{},
		&UserGroupMembership{},
		&ChannelGroupMultiplier{},
		&ModelGroupMultiplier{},
		&ReferralCommissionLog{},
		&UserChannel{},
		&UserChannelGroupAccess{},
		&UserChannelUserAccess{},
		&Channel{},
		&Model{},
		&ModelConfig{},
		&StatusMonitor{},
		&Announcement{},
		&SystemSetting{},
		&VideoTask{},
		&Plugin{},
		&UserPluginState{},
		&UserPluginConfig{},
		&PluginKV{},
		&Ticket{},
		&TicketMessage{},
		&PersonalCompany{},
		&CompanyCharterRevision{},
		&PersonalCompanyEmployee{},
		&CompanyRoleTemplate{},
		&CompanyEmployeeVersion{},
		&CompanyCapabilityEvidence{},
		&CompanyRecruitmentPlan{},
		&CompanyObjective{},
		&CompanyWorkItem{},
		&CompanyWorkAttempt{},
		&CompanyArtifact{},
		&CompanyHandoffPackage{},
		&CompanyApprovalRequest{},
		&CompanyBudgetLedger{},
		&CompanyAuditEvent{},
		&CompanySignal{},
		&CompanyOutboxEvent{},
	)
	if err != nil {
		log.Fatalf("failed to migrate database: %v", err)
	}
	if err := DB.Model(&Channel{}).
		Where("price_sync_cron IS NULL OR price_sync_cron = ?", "").
		Updates(map[string]interface{}{
			"price_sync_enabled": true,
			"price_sync_cron":    "0 * * * *",
		}).Error; err != nil {
		log.Fatalf("failed to initialize channel price sync settings: %v", err)
	}
	// Studio operations are independently scoped by owner and Studio. Older
	// PersonalCompany schemas used a single-column owner uniqueness constraint.
	if DB.Migrator().HasIndex(&PersonalCompany{}, "idx_personal_companies_owner_user_id") {
		if err := DB.Migrator().DropIndex(&PersonalCompany{}, "idx_personal_companies_owner_user_id"); err != nil {
			log.Printf("failed to replace legacy personal company owner index: %v", err)
		}
	}
	enterpriseModels := []interface{}{
		&Organization{},
		&Department{},
		&Workspace{},
		&OrganizationMember{},
		&WorkspaceMember{},
		&Permission{},
		&Role{},
		&RolePermission{},
		&RoleBinding{},
		&DepartmentRoleBinding{},
		&EnterpriseTask{},
		&EnterpriseTaskAssignment{},
		&EnterpriseTaskDepartment{},
		&DepartmentMember{},
		&EnterpriseSharedPool{},
		&EnterpriseSharedSession{},
		&EnterpriseSharedFile{},
		&EnterpriseDevice{},
		&EnterpriseDeviceAssignment{},
		&QuotaAccount{},
		&QuotaLedger{},
	}
	// A number of enterprise references use composite keys to keep every
	// relation inside its organization. PostgreSQL requires the referenced
	// unique index to exist before it accepts a self-referential foreign key,
	// while AutoMigrate otherwise attempts both in one pass. SQLite rebuilds
	// tables when adding foreign keys, so it must retain the normal one-pass
	// migration behavior that creates those constraints inline.
	if DB.Dialector.Name() != "sqlite" {
		if err := autoMigrateWithoutForeignKeys(DB, enterpriseModels...); err != nil {
			log.Fatalf("failed to prepare enterprise database models: %v", err)
		}
	}
	if err := DB.AutoMigrate(enterpriseModels...); err != nil {
		log.Fatalf("failed to migrate enterprise database models: %v", err)
	}
	if !hadCachedInputPrice {
		if err := DB.Model(&Model{}).
			Where("cached_input_price = ? AND input_price > ?", decimal.Zero, decimal.Zero).
			Update("cached_input_price", gorm.Expr("input_price")).Error; err != nil {
			log.Fatalf("failed to initialize cached input prices: %v", err)
		}
	}
	if !hadCacheWriteInputPrice {
		if err := DB.Model(&Model{}).
			Where("cache_write_input_price = ? AND input_price > ?", decimal.Zero, decimal.Zero).
			Update("cache_write_input_price", gorm.Expr("input_price")).Error; err != nil {
			log.Fatalf("failed to initialize cache write input prices: %v", err)
		}
	}
	if !hadCacheWrite1hInputPrice {
		if err := DB.Model(&Model{}).
			Where("cache_write_1h_input_price = ? AND input_price > ?", decimal.Zero, decimal.Zero).
			Update("cache_write_1h_input_price", gorm.Expr("input_price")).Error; err != nil {
			log.Fatalf("failed to initialize 1h cache write input prices: %v", err)
		}
	}

	// Initial data
	initData()
	if err := InitLogDB(); err != nil {
		log.Fatalf("failed to initialize log database: %v", err)
	}
	if EnterpriseModeEnabledWithDB(DB) {
		if err := EnsureEnterpriseTenantForExistingUsers(DB); err != nil {
			log.Fatalf("failed to initialize enterprise tenant: %v", err)
		}
	}
}

func autoMigrateWithoutForeignKeys(db *gorm.DB, models ...interface{}) error {
	original := db.Config.DisableForeignKeyConstraintWhenMigrating
	db.Config.DisableForeignKeyConstraintWhenMigrating = true
	defer func() {
		db.Config.DisableForeignKeyConstraintWhenMigrating = original
	}()
	return db.AutoMigrate(models...)
}

func databaseDialector() (gorm.Dialector, bool, error) {
	switch config.DBDriver {
	case "", "sqlite":
		if strings.TrimSpace(config.DBPath) == "" {
			return nil, false, fmt.Errorf("DB_PATH must be set when DB_DRIVER is sqlite")
		}
		return sqlite.Open(sqliteDSN(config.DBPath)), true, nil
	case "postgres", "postgresql":
		if config.DBDSN == "" {
			return nil, false, fmt.Errorf("DB_DSN or DATABASE_URL must be set when DB_DRIVER is postgres")
		}
		return postgres.Open(config.DBDSN), false, nil
	case "mysql", "mariadb":
		if config.DBDSN == "" {
			return nil, false, fmt.Errorf("DB_DSN or DATABASE_URL must be set when DB_DRIVER is mysql")
		}
		return mysql.Open(config.DBDSN), false, nil
	default:
		return nil, false, fmt.Errorf("unsupported DB_DRIVER %q; expected sqlite, postgres, or mysql", config.DBDriver)
	}
}

func sqliteDSN(path string) string {
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	return path + separator + "_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(ON)"
}

func configureDatabaseConnection(sqlDB *sql.DB, isSQLite bool) error {
	if !isSQLite {
		sqlDB.SetMaxOpenConns(config.DBMaxOpenConns)
		sqlDB.SetMaxIdleConns(config.DBMaxIdleConns)
		sqlDB.SetConnMaxLifetime(time.Duration(config.DBConnMaxLifetimeSeconds) * time.Second)
		return nil
	}

	// SQLite has a single writer. One connection serializes concurrent writes
	// and prevents SQLITE_BUSY errors between background jobs and requests.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	for _, statement := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA busy_timeout = 10000",
		"PRAGMA foreign_keys = ON",
	} {
		if _, err := sqlDB.Exec(statement); err != nil {
			return fmt.Errorf("%s: %w", statement, err)
		}
	}
	return nil
}

func logSQLiteConfiguration(sqlDB *sql.DB) {
	var journalMode string
	var busyTimeout int
	if err := sqlDB.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		journalMode = "unknown"
	}
	if err := sqlDB.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		busyTimeout = 0
	}
	stats := sqlDB.Stats()
	log.Printf("sqlite configured: path=%q journal_mode=%s busy_timeout_ms=%d max_open_connections=%d max_idle_connections=%d", config.DBPath, journalMode, busyTimeout, stats.MaxOpenConnections, 1)
}

func initData() {
	if _, err := EnsureDefaultGroup(); err != nil {
		log.Fatalf("failed to initialize default group: %v", err)
	}
	if err := EnsureDefaultUserGroupMemberships(); err != nil {
		log.Fatalf("failed to initialize user group memberships: %v", err)
	}
	if err := EnsureUserReferralCodes(); err != nil {
		log.Fatalf("failed to initialize user referral codes: %v", err)
	}
	if err := NormalizeEmptyOIDCSubjects(); err != nil {
		log.Fatalf("failed to normalize empty oidc subjects: %v", err)
	}
	if _, err := EnsureDefaultUserChannel(); err != nil {
		log.Fatalf("failed to initialize default user channel: %v", err)
	}
	if err := EnsureGlobalModels(); err != nil {
		log.Fatalf("failed to initialize global models: %v", err)
	}
	if err := EnsureDefaultSystemSettings(); err != nil {
		log.Fatalf("failed to initialize system settings: %v", err)
	}
}

func NormalizeEmptyOIDCSubjects() error {
	return DB.Model(&User{}).Where("oidc_sub = ?", "").Update("oidc_sub", nil).Error
}

func EnsureDefaultGroup() (Group, error) {
	group := Group{Name: "user"}
	err := DB.Where(&Group{Name: "user"}).
		Attrs(Group{Multiplier: decimal.NewFromInt(1)}).
		FirstOrCreate(&group).Error
	return group, err
}

func EnsureDefaultUserGroupMemberships() error {
	group, err := EnsureDefaultGroup()
	if err != nil {
		return err
	}

	if err := DB.Model(&User{}).Where("group_id = 0 OR group_id IS NULL").Update("group_id", group.ID).Error; err != nil {
		return err
	}

	var users []User
	if err := DB.Find(&users).Error; err != nil {
		return err
	}
	for _, user := range users {
		groupID := user.GroupID
		if groupID == 0 {
			groupID = group.ID
		}
		membership := UserGroupMembership{UserID: user.ID, GroupID: groupID}
		if err := DB.Where(&UserGroupMembership{UserID: user.ID, GroupID: groupID}).
			FirstOrCreate(&membership).Error; err != nil {
			return err
		}
	}
	return nil
}

func EnsureUserReferralCodes() error {
	var users []User
	if err := DB.Where("referral_code IS NULL OR referral_code = ?", "").Find(&users).Error; err != nil {
		return err
	}
	for _, user := range users {
		code, err := NewUniqueReferralCode()
		if err != nil {
			return err
		}
		if err := DB.Model(&user).Update("referral_code", code).Error; err != nil {
			return err
		}
	}
	return nil
}

func NewUniqueReferralCode() (string, error) {
	for i := 0; i < 50; i++ {
		code, err := NewReferralCode()
		if err != nil {
			return "", err
		}
		var count int64
		if err := DB.Model(&User{}).Where("referral_code = ?", code).Count(&count).Error; err != nil {
			return "", err
		}
		if count == 0 {
			return code, nil
		}
	}
	return "", gorm.ErrDuplicatedKey
}

func NewReferralCode() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:]), nil
}

func NormalizeReferralCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	code = strings.ReplaceAll(code, " ", "")
	code = strings.ReplaceAll(code, "-", "")
	return code
}

func EnsureDefaultUserChannel() (UserChannel, error) {
	userChannel := UserChannel{Name: "default"}
	err := DB.Where(&UserChannel{Name: "default"}).
		Attrs(UserChannel{Description: "Default user-facing channel", Multiplier: decimal.NewFromInt(1), RoutingAlgorithm: "priority", Enabled: true}).
		FirstOrCreate(&userChannel).Error
	if err != nil {
		return userChannel, err
	}

	if err := DB.Model(&UserChannel{}).
		Where("multiplier = ?", decimal.Zero).
		Update("multiplier", decimal.NewFromInt(1)).Error; err != nil {
		return userChannel, err
	}
	if err := DB.Model(&UserChannel{}).
		Where("routing_algorithm = ? OR routing_algorithm IS NULL", "").
		Update("routing_algorithm", "priority").Error; err != nil {
		return userChannel, err
	}

	err = DB.Model(&Channel{}).
		Where("user_channel_id IS NULL").
		Update("user_channel_id", userChannel.ID).Error
	return userChannel, err
}

func EnsureGlobalModels() error {
	if !DB.Migrator().HasTable("model_configs") || !DB.Migrator().HasColumn("model_configs", "model_name") {
		return nil
	}

	type legacyModelConfig struct {
		ID                uint
		ModelID           uint
		ModelName         string
		Provider          string
		ProviderIconURL   string
		UpstreamModelName string
		InputPrice        decimal.Decimal
		OutputPrice       decimal.Decimal
	}

	var configs []legacyModelConfig
	if err := DB.Table("model_configs").
		Select("id, model_id, model_name, provider, provider_icon_url, upstream_model_name, input_price, output_price").
		Find(&configs).Error; err != nil {
		return err
	}

	for _, config := range configs {
		modelName := strings.TrimSpace(config.ModelName)
		if modelName == "" {
			continue
		}

		globalModel := Model{ModelName: modelName}
		if err := DB.Where(&Model{ModelName: modelName}).
			Attrs(Model{
				Provider:        strings.TrimSpace(config.Provider),
				ProviderIconURL: strings.TrimSpace(config.ProviderIconURL),
				Enabled:         true,
			}).
			FirstOrCreate(&globalModel).Error; err != nil {
			return err
		}

		updates := map[string]interface{}{}
		if config.ModelID == 0 {
			updates["model_id"] = globalModel.ID
		}
		if strings.TrimSpace(config.UpstreamModelName) == "" {
			updates["upstream_model_name"] = modelName
		}
		if len(updates) > 0 {
			if err := DB.Table("model_configs").Where("id = ?", config.ID).Updates(updates).Error; err != nil {
				return err
			}
		}

		modelUpdates := map[string]interface{}{}
		if strings.TrimSpace(globalModel.Provider) == "" && strings.TrimSpace(config.Provider) != "" {
			modelUpdates["provider"] = strings.TrimSpace(config.Provider)
		}
		if strings.TrimSpace(globalModel.ProviderIconURL) == "" && strings.TrimSpace(config.ProviderIconURL) != "" {
			modelUpdates["provider_icon_url"] = strings.TrimSpace(config.ProviderIconURL)
		}
		if globalModel.InputPrice.IsZero() && !config.InputPrice.IsZero() {
			modelUpdates["input_price"] = config.InputPrice
		}
		if globalModel.OutputPrice.IsZero() && !config.OutputPrice.IsZero() {
			modelUpdates["output_price"] = config.OutputPrice
		}
		if len(modelUpdates) > 0 {
			if err := DB.Model(&globalModel).Updates(modelUpdates).Error; err != nil {
				return err
			}
		}
	}

	return nil
}

func EnsureDefaultSystemSettings() error {
	oidcDefault := "false"
	var oidcUserCount int64
	if err := DB.Model(&User{}).Where("oidc_sub IS NOT NULL AND oidc_sub <> ?", "").Count(&oidcUserCount).Error; err == nil && oidcUserCount > 0 {
		oidcDefault = "true"
	}
	defaults := map[string]string{
		"site_name":                                  "flai",
		"base_url":                                   "",
		"icon_url":                                   "",
		"footer_text":                                "",
		"about_html":                                 "",
		"home_iframe_url":                            "",
		"privacy_policy":                             "",
		"terms":                                      "",
		"privacy_policy_url":                         "",
		"terms_url":                                  "",
		"auth_agreement_mode":                        "notice",
		"announcement":                               "",
		"top_nav_enabled":                            "false",
		"top_nav_items":                              "",
		"page_layouts":                               "{}",
		"theme_light_background":                     "#ffffff",
		"theme_light_foreground":                     "#020817",
		"theme_light_card":                           "#ffffff",
		"theme_light_card_foreground":                "#020817",
		"theme_light_primary":                        "#0f172a",
		"theme_light_primary_foreground":             "#f8fafc",
		"theme_light_secondary":                      "#f1f5f9",
		"theme_light_secondary_foreground":           "#0f172a",
		"theme_light_accent":                         "#f1f5f9",
		"theme_light_accent_foreground":              "#0f172a",
		"theme_light_muted":                          "#f1f5f9",
		"theme_light_muted_foreground":               "#64748b",
		"theme_light_border":                         "#e2e8f0",
		"theme_dark_background":                      "#020817",
		"theme_dark_foreground":                      "#f8fafc",
		"theme_dark_card":                            "#020817",
		"theme_dark_card_foreground":                 "#f8fafc",
		"theme_dark_primary":                         "#f8fafc",
		"theme_dark_primary_foreground":              "#0f172a",
		"theme_dark_secondary":                       "#1e293b",
		"theme_dark_secondary_foreground":            "#f8fafc",
		"theme_dark_accent":                          "#1e293b",
		"theme_dark_accent_foreground":               "#f8fafc",
		"theme_dark_muted":                           "#1e293b",
		"theme_dark_muted_foreground":                "#94a3b8",
		"theme_dark_border":                          "#1e293b",
		"theme_background_image":                     "",
		"theme_custom_css":                           "",
		"sidebar_dashboard_enabled":                  "true",
		"sidebar_usage_enabled":                      "true",
		"sidebar_wallet_enabled":                     "true",
		"sidebar_data_board_enabled":                 "true",
		"sidebar_api_keys_enabled":                   "true",
		"sidebar_chat_enabled":                       "true",
		"sidebar_images_enabled":                     "true",
		"sidebar_settings_enabled":                   "true",
		"sidebar_system_enabled":                     "true",
		"sidebar_admin_overview_enabled":             "true",
		"sidebar_channels_enabled":                   "true",
		"sidebar_models_enabled":                     "true",
		"sidebar_users_enabled":                      "true",
		"system_mode":                                "operation",
		"chat_page_mode":                             "advanced",
		"message_channel_enabled":                    "false",
		"referral_enabled":                           "false",
		"referral_commission_rate":                   "0",
		"group_multiplier_mode":                      "min",
		"pricing_endpoint_enabled":                   "false",
		"status_monitor_enabled":                     "false",
		"reliability_auto_disable_enabled":           "false",
		"reliability_disable_after_failures":         "3",
		"reliability_auto_detect_upstream_enabled":   "false",
		"reliability_auto_detect_interval_seconds":   "300",
		"reliability_auto_detect_timeout_seconds":    "10",
		"reliability_auto_recover_enabled":           "false",
		"reliability_recovery_after_seconds":         "1800",
		"log_retention_api_days":                     "0",
		"log_retention_login_days":                   "0",
		"log_retention_admin_days":                   "0",
		"log_retention_system_days":                  "0",
		"log_retention_token_days":                   "0",
		"log_retention_cleanup_interval_hours":       "24",
		"log_storage_mode":                           "single",
		"log_retention_days":                         "30",
		"checkin_enabled":                            "false",
		"checkin_daily_reward":                       "0",
		"checkin_timezone":                           "Asia/Shanghai",
		"checkin_streak_enabled":                     "false",
		"checkin_streak_cycle_days":                  "7",
		"checkin_streak_rewards":                     "{}",
		"checkin_random_enabled":                     "false",
		"checkin_random_min":                         "0",
		"checkin_random_max":                         "0",
		"payment_enabled":                            "false",
		"payment_currency_display_name":              "$",
		"payment_usd_to_rmb_rate":                    "7.20",
		"payment_min_recharge_amount":                "1",
		"payment_recharge_presets":                   "[\"5\",\"10\",\"20\",\"50\",\"100\"]",
		"payment_methods":                            "[\"alipay\",\"wxpay\"]",
		"payment_gateway_provider":                   "yipay",
		"payment_channels":                           "[]",
		"payment_yipay_gateway_url":                  "",
		"payment_yipay_pid":                          "",
		"payment_yipay_key":                          "",
		"payment_yipay_notify_url":                   "",
		"payment_yipay_return_url":                   "",
		"payment_openpayment_base_url":               "",
		"payment_openpayment_config_url":             "",
		"payment_openpayment_merchant_id":            "",
		"payment_openpayment_key":                    "",
		"payment_openpayment_notify_url":             "",
		"payment_openpayment_return_url":             "",
		"payment_official_currency":                  "CNY",
		"payment_wechat_mch_id":                      "",
		"payment_wechat_app_id":                      "",
		"payment_wechat_serial_no":                   "",
		"payment_wechat_private_key":                 "",
		"payment_wechat_platform_certificate":        "",
		"payment_wechat_api_v3_key":                  "",
		"payment_alipay_app_id":                      "",
		"payment_alipay_private_key":                 "",
		"payment_alipay_public_key":                  "",
		"payment_alipay_gateway_url":                 "https://openapi.alipay.com/gateway.do",
		"payment_paypal_client_id":                   "",
		"payment_paypal_client_secret":               "",
		"payment_paypal_base_url":                    "https://api-m.sandbox.paypal.com",
		"payment_paypal_webhook_id":                  "",
		"payment_stripe_secret_key":                  "",
		"payment_stripe_webhook_secret":              "",
		"rate_limit_enabled":                         "true",
		"rate_limit_requests_per_minute":             "60",
		"rate_limit_burst":                           "10",
		"sensitive_filter_enabled":                   "false",
		"sensitive_words":                            "",
		"sensitive_filter_scope":                     "request",
		"ssrf_protection_enabled":                    "true",
		"ssrf_allow_private_networks":                "false",
		"ssrf_allowed_hosts":                         "",
		"oidc_enabled":                               oidcDefault,
		"passkey_enabled":                            "false",
		"password_login_enabled":                     "true",
		"password_registration_enabled":              "true",
		"token_api_enabled":                          "true",
		"password_hcaptcha_enabled":                  "false",
		"hcaptcha_site_key":                          "",
		"hcaptcha_secret":                            "",
		"email_verification_required":                "false",
		"registration_email_suffixes":                "",
		"registration_email_routing":                 "[]",
		"smtp_host":                                  "",
		"smtp_port":                                  "587",
		"smtp_username":                              "",
		"smtp_password":                              "",
		"smtp_from":                                  "",
		"sms_enabled":                                "false",
		"sms_login_enabled":                          "false",
		"sms_binding_required":                       "false",
		"sms_provider":                               "aliyun",
		"sms_aliyun_access_key_id":                   "",
		"sms_aliyun_access_key_secret":               "",
		"sms_aliyun_sign_name":                       "",
		"sms_aliyun_template_code":                   "",
		"sms_tencent_secret_id":                      "",
		"sms_tencent_secret_key":                     "",
		"sms_tencent_sdk_app_id":                     "",
		"sms_tencent_sign_name":                      "",
		"sms_tencent_template_id":                    "",
		"sms_tencent_region":                         "ap-guangzhou",
		"oidc_issuer":                                "",
		"oidc_client_id":                             "",
		"oidc_client_secret":                         "",
		"oidc_redirect_url":                          "",
		"oauth_providers":                            "[]",
		"auto_update_enabled":                        "false",
		"auto_update_interval_hours":                 "24",
		"auto_update_last_checked_at":                "",
		"auto_update_latest_version":                 "",
		"auto_update_last_error":                     "",
		"advanced_chat_attachment_max_mb":            "20",
		"advanced_chat_attachment_allowed_types":     "image/*,text/plain,application/pdf",
		"advanced_chat_file_storage_enabled":         "false",
		"advanced_chat_file_storage_total_mb":        "1024",
		"advanced_chat_scheduled_tasks_enabled":      "false",
		"advanced_chat_message_delivery_enabled":     "false",
		"advanced_chat_delivery_system_smtp_enabled": "false",
		"redis_enabled":                              "false",
		"redis_address":                              "127.0.0.1:6379",
		"redis_username":                             "",
		"redis_password":                             "",
		"redis_database":                             "0",
		"redis_tls_enabled":                          "false",
	}
	for key, value := range defaults {
		setting := SystemSetting{Key: key}
		if err := DB.Where(&SystemSetting{Key: key}).
			Attrs(SystemSetting{Value: value}).
			FirstOrCreate(&setting).Error; err != nil {
			return err
		}
	}
	return nil
}

func GetSystemSetting(key, fallback string) string {
	return GetSystemSettingWithDB(DB, key, fallback)
}

func GetSystemSettingWithDB(db *gorm.DB, key, fallback string) string {
	if db == nil {
		return fallback
	}
	var setting SystemSetting
	result := db.Where("key = ?", key).Limit(1).Find(&setting)
	if result.Error != nil || result.RowsAffected == 0 {
		return fallback
	}
	return setting.Value
}

func SetSystemSetting(key, value string) error {
	return SetSystemSettingWithDB(DB, key, value)
}

func SetSystemSettingWithDB(db *gorm.DB, key, value string) error {
	if db == nil {
		return fmt.Errorf("database is required")
	}
	setting := SystemSetting{Key: key}
	return db.Where(&SystemSetting{Key: key}).
		Assign(map[string]interface{}{"value": value}).
		FirstOrCreate(&setting).Error
}
