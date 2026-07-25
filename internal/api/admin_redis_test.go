package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/veloce-ailab/veloce/internal/model"
	"gorm.io/gorm"
)

func TestRedisSettingsPersistWithoutExposingPassword(t *testing.T) {
	previousDB := model.DB
	t.Cleanup(func() { model.DB = previousDB })
	database, err := gorm.Open(sqlite.Open("file:redis-settings-api-test?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	model.DB = database

	body, err := json.Marshal(map[string]any{
		"redis_enabled":     true,
		"redis_address":     "cache.internal:6380",
		"redis_username":    "service",
		"redis_password":    "secret",
		"redis_database":    "2",
		"redis_tls_enabled": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodPut, "/settings", bytes.NewReader(body))
	context.Request.Header.Set("Content-Type", "application/json")
	(&SystemAPI{}).UpdateSettings(context)
	if response.Code != http.StatusOK {
		t.Fatalf("update Redis settings: status=%d body=%s", response.Code, response.Body.String())
	}
	if got := model.GetSystemSetting("redis_password", ""); got != "secret" {
		t.Fatalf("stored Redis password = %q, want secret", got)
	}

	settings := currentAdminSystemSettings()
	if !settings.RedisEnabled || settings.RedisAddress != "cache.internal:6380" || settings.RedisUsername != "service" || settings.RedisDatabase != "2" || !settings.RedisTLSEnabled || !settings.RedisPasswordSet {
		t.Fatalf("unexpected Redis settings: %+v", settings)
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("secret")) {
		t.Fatalf("Redis password was exposed in settings response: %s", encoded)
	}
}

func TestSensitiveSystemSettingsAreRedactedAndPreserved(t *testing.T) {
	previousDB := model.DB
	t.Cleanup(func() { model.DB = previousDB })
	database, err := gorm.Open(sqlite.Open("file:sensitive-settings-api-test?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	model.DB = database

	sensitiveValues := map[string]string{}
	for key := range sensitiveSystemSettingKeys {
		value := "saved-" + key
		sensitiveValues[key] = value
		if err := model.SetSystemSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}
	oauthProviders := `[{"key":"github","name":"GitHub","client_id":"client-id","client_secret":"saved-oauth-secret"}]`
	paymentChannels := `[{"id":"stripe","name":"Stripe","provider":"stripe","enabled":true,"methods":["stripe"],"config":{"gateway_url":"https://api.stripe.com","key":"saved-channel-key","openpayment_key":"saved-openpayment-key","wechat_private_key":"saved-wechat-private-key","wechat_platform_certificate":"saved-wechat-certificate","wechat_api_v3_key":"saved-wechat-api-key","alipay_private_key":"saved-alipay-private-key","alipay_public_key":"saved-alipay-public-key","paypal_client_secret":"saved-paypal-secret","stripe_secret_key":"saved-stripe-secret","stripe_webhook_secret":"saved-stripe-webhook-secret"}}]`
	if err := model.SetSystemSetting("oauth_providers", oauthProviders); err != nil {
		t.Fatal(err)
	}
	if err := model.SetSystemSetting("payment_channels", paymentChannels); err != nil {
		t.Fatal(err)
	}

	settings := currentAdminSystemSettings()
	encoded, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range sensitiveValues {
		if bytes.Contains(encoded, []byte(value)) {
			t.Fatalf("sensitive value %q was exposed in settings response: %s", value, encoded)
		}
	}
	for _, value := range []string{"saved-oauth-secret", "saved-channel-key", "saved-openpayment-key", "saved-wechat-private-key", "saved-wechat-certificate", "saved-wechat-api-key", "saved-alipay-private-key", "saved-alipay-public-key", "saved-paypal-secret", "saved-stripe-secret", "saved-stripe-webhook-secret"} {
		if bytes.Contains(encoded, []byte(value)) {
			t.Fatalf("nested sensitive value %q was exposed in settings response: %s", value, encoded)
		}
	}
	if bytes.Contains(encoded, []byte("client_secret")) {
		t.Fatalf("OAuth client_secret field was exposed in settings response: %s", encoded)
	}

	emptyUpdate := map[string]any{}
	for key := range sensitiveSystemSettingKeys {
		emptyUpdate[key] = ""
	}
	emptyUpdate["oauth_providers"] = `[{"key":"github","name":"GitHub updated","client_id":"client-id","client_secret":""}]`
	emptyUpdate["payment_channels"] = `[{"id":"stripe","name":"Stripe updated","provider":"stripe","enabled":true,"methods":["stripe"],"config":{"gateway_url":"https://api.stripe.com/v2","key":"","openpayment_key":"","wechat_private_key":"","wechat_platform_certificate":"","wechat_api_v3_key":"","alipay_private_key":"","alipay_public_key":"","paypal_client_secret":"","stripe_secret_key":"","stripe_webhook_secret":""}}]`
	updateSettingsForTest(t, emptyUpdate)
	for key, value := range sensitiveValues {
		if got := model.GetSystemSetting(key, ""); got != value {
			t.Fatalf("empty update changed %s: got %q, want %q", key, got, value)
		}
	}
	if got := model.GetSystemSetting("oauth_providers", ""); !bytes.Contains([]byte(got), []byte("saved-oauth-secret")) {
		t.Fatalf("empty OAuth client secret was not preserved: %s", got)
	}
	if got := model.GetSystemSetting("payment_channels", ""); !bytes.Contains([]byte(got), []byte("saved-stripe-secret")) || !bytes.Contains([]byte(got), []byte("saved-channel-key")) {
		t.Fatalf("empty payment channel secrets were not preserved: %s", got)
	}

	replacementUpdate := map[string]any{}
	for key := range sensitiveSystemSettingKeys {
		replacementUpdate[key] = "replacement-" + key
	}
	replacementUpdate["oauth_providers"] = `[{"key":"github","name":"GitHub updated","client_id":"client-id","client_secret":"replacement-oauth-secret"}]`
	replacementUpdate["payment_channels"] = `[{"id":"stripe","name":"Stripe updated","provider":"stripe","enabled":true,"methods":["stripe"],"config":{"key":"replacement-channel-key","stripe_secret_key":"replacement-stripe-secret"}}]`
	updateSettingsForTest(t, replacementUpdate)
	for key := range sensitiveSystemSettingKeys {
		want := "replacement-" + key
		if got := model.GetSystemSetting(key, ""); got != want {
			t.Fatalf("non-empty update did not replace %s: got %q, want %q", key, got, want)
		}
	}
	if got := model.GetSystemSetting("oauth_providers", ""); !bytes.Contains([]byte(got), []byte("replacement-oauth-secret")) {
		t.Fatalf("OAuth client secret was not replaced: %s", got)
	}
	if got := model.GetSystemSetting("payment_channels", ""); !bytes.Contains([]byte(got), []byte("replacement-stripe-secret")) || !bytes.Contains([]byte(got), []byte("replacement-channel-key")) {
		t.Fatalf("payment channel secrets were not replaced: %s", got)
	}

	exported, err := buildConfigurationExport(map[string]bool{configurationSectionSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	exportedValues := map[string]string{}
	for _, setting := range exported.SystemSettings {
		exportedValues[setting.Key] = setting.Value
	}
	if exportedValues["hcaptcha_secret"] != "replacement-hcaptcha_secret" || !bytes.Contains([]byte(exportedValues["payment_channels"]), []byte("replacement-stripe-secret")) {
		t.Fatalf("configuration export did not retain sensitive settings: %+v", exportedValues)
	}
}

func TestClearableSystemSettingsAcceptEmptyValues(t *testing.T) {
	previousDB := model.DB
	t.Cleanup(func() { model.DB = previousDB })
	database, err := gorm.Open(sqlite.Open("file:clearable-settings-api-test?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	model.DB = database

	for key, value := range map[string]string{
		"home_iframe_url": "https://status.example.com/embed",
		"sensitive_words": "one,two",
	} {
		if err := model.SetSystemSetting(key, value); err != nil {
			t.Fatal(err)
		}
	}

	updateSettingsForTest(t, map[string]any{
		"home_iframe_url": "",
		"sensitive_words": "",
	})
	for _, key := range []string{"home_iframe_url", "sensitive_words"} {
		if got := model.GetSystemSetting(key, "not-empty"); got != "" {
			t.Fatalf("empty update did not clear %s: got %q", key, got)
		}
	}
}

func updateSettingsForTest(t *testing.T, values map[string]any) {
	t.Helper()
	body, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodPut, "/settings", bytes.NewReader(body))
	context.Request.Header.Set("Content-Type", "application/json")
	(&SystemAPI{}).UpdateSettings(context)
	if response.Code != http.StatusOK {
		t.Fatalf("update settings: status=%d body=%s", response.Code, response.Body.String())
	}
}
