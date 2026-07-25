package api

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/veloce-ailab/veloce/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const openPaymentDiscoveryPath = "/.well-known/openpayment-configuation"

var (
	openPaymentHTTPClient = &http.Client{Timeout: 5 * time.Second}
	openPaymentCache      sync.Map
)

type openPaymentCacheEntry struct {
	discovery openPaymentDiscovery
	expiresAt time.Time
}

type openPaymentDiscovery struct {
	Spec           string                     `json:"spec"`
	SpecVersion    string                     `json:"spec_version"`
	Profile        []string                   `json:"profile"`
	Platform       openPaymentPlatform        `json:"platform"`
	Endpoints      openPaymentEndpoints       `json:"endpoints"`
	Transports     map[string][]string        `json:"transports"`
	Signing        openPaymentSigning         `json:"signing"`
	PaymentMethods []openPaymentMethod        `json:"payment_methods"`
	Fields         map[string][]string        `json:"fields"`
	Callbacks      openPaymentCallbacks       `json:"callbacks"`
	Compatibility  openPaymentCompatibility   `json:"compatibility"`
	Amount         map[string]interface{}     `json:"amount"`
	Extra          map[string]json.RawMessage `json:"-"`
}

type openPaymentPlatform struct {
	Name     string `json:"name"`
	Vendor   string `json:"vendor"`
	Homepage string `json:"homepage"`
	Charset  string `json:"charset"`
	Timezone string `json:"timezone"`
	Currency string `json:"currency"`
}

type openPaymentEndpoints struct {
	Submit string `json:"submit"`
	MAPI   string `json:"mapi"`
	API    string `json:"api"`
	Query  string `json:"query"`
	Refund string `json:"refund"`
	Close  string `json:"close"`
}

type openPaymentSigning struct {
	Supported       []string                   `json:"supported"`
	Default         string                     `json:"default"`
	IncludeSignType bool                       `json:"include_sign_type"`
	Output          string                     `json:"output"`
	HMAC            openPaymentSignatureOutput `json:"hmac"`
	RSA             map[string]interface{}     `json:"rsa"`
	Extra           map[string]json.RawMessage `json:"-"`
}

type openPaymentSignatureOutput struct {
	Output string `json:"output"`
}

type openPaymentMethod struct {
	Code    string   `json:"code"`
	Name    string   `json:"name"`
	Aliases []string `json:"aliases"`
	Scenes  []string `json:"scenes"`
	Enabled *bool    `json:"enabled"`
}

type openPaymentCallbacks struct {
	NotifySuccessBody string `json:"notify_success_body"`
	NotifyRetry       bool   `json:"notify_retry"`
	ReturnURLTrusted  bool   `json:"return_url_trusted"`
}

type openPaymentCompatibility struct {
	EpaySignTypeRequired bool     `json:"epay_sign_type_required"`
	QueryActValues       []string `json:"query_act_values"`
	SuccessStatusValues  []string `json:"success_status_values"`
}

var defaultOpenPaymentFieldAliases = map[string][]string{
	"merchant_id":       {"pid", "mch_id", "merchant_id"},
	"payment_method":    {"type", "payment_method"},
	"merchant_order_no": {"out_trade_no", "merchant_order_no"},
	"platform_order_no": {"trade_no", "platform_order_no"},
	"subject":           {"name", "subject"},
	"amount":            {"money", "amount"},
	"notify_url":        {"notify_url"},
	"return_url":        {"return_url"},
	"client_ip":         {"clientip", "client_ip"},
	"metadata":          {"param", "metadata"},
	"status":            {"trade_status", "status"},
}

func buildOpenPaymentPaymentURL(c *gin.Context, cfg paymentConfig, order model.PaymentOrder) (string, error) {
	params, endpoint, discovery, err := buildOpenPaymentPaymentParams(c, cfg, order)
	if err != nil {
		return "", err
	}
	if openPaymentSupportsTransport(discovery, "payment_create", "form_post") && !openPaymentSupportsTransport(discovery, "payment_create", "form_get") {
		return publicCallbackURL(c, "/api/payment/openpayment/submit/"+url.PathEscape(order.OrderNo)), nil
	}
	return openPaymentURLWithParams(endpoint, params)
}

func buildOpenPaymentPaymentParams(c *gin.Context, cfg paymentConfig, order model.PaymentOrder) (map[string]string, string, openPaymentDiscovery, error) {
	discovery, err := fetchOpenPaymentDiscovery(c.Request.Context(), cfg)
	if err != nil {
		return nil, "", discovery, err
	}
	endpoint := strings.TrimSpace(discovery.Endpoints.Submit)
	if endpoint == "" {
		return nil, "", discovery, errors.New("Open Payment submit endpoint is not configured")
	}
	parsedEndpoint, err := url.Parse(endpoint)
	if err != nil || parsedEndpoint.Scheme == "" || parsedEndpoint.Host == "" {
		return nil, "", discovery, errors.New("invalid Open Payment submit endpoint")
	}
	method, err := openPaymentMethodForSubmit(order.Method, discovery)
	if err != nil {
		return nil, "", discovery, err
	}
	signType, err := openPaymentSigningMethod(discovery)
	if err != nil {
		return nil, "", discovery, err
	}

	params := map[string]string{
		openPaymentFieldName(discovery, "merchant_id"):       openPaymentMerchantID(cfg),
		openPaymentFieldName(discovery, "payment_method"):    method,
		openPaymentFieldName(discovery, "merchant_order_no"): order.OrderNo,
		openPaymentFieldName(discovery, "subject"):           "Balance recharge " + order.OrderNo,
		openPaymentFieldName(discovery, "amount"):            order.RMBAmount.StringFixed(2),
		openPaymentFieldName(discovery, "notify_url"):        publicCallbackURL(c, "/api/payment/openpayment/notify"),
		openPaymentFieldName(discovery, "return_url"):        publicCallbackURL(c, "/api/payment/openpayment/return"),
	}
	if clientIP := strings.TrimSpace(c.ClientIP()); clientIP != "" {
		params[openPaymentFieldName(discovery, "client_ip")] = clientIP
	}
	if signType != "" {
		params["sign_type"] = signType
	}
	sign, err := buildOpenPaymentSign(params, openPaymentMerchantKey(cfg), signType, discovery)
	if err != nil {
		return nil, "", discovery, err
	}
	params["sign"] = sign
	return params, endpoint, discovery, nil
}

func (api *PaymentAPI) OpenPaymentSubmit(c *gin.Context) {
	if !paymentFeatureEnabled || service.PersonalModeEnabled() {
		c.String(http.StatusNotFound, "payment requires premium edition")
		return
	}
	cfg := currentPaymentConfig()
	if cfg.Provider != paymentProviderOpenPayment {
		c.String(http.StatusNotFound, "not found")
		return
	}
	var order model.PaymentOrder
	if err := model.DB.Where("order_no = ?", c.Param("order_no")).First(&order).Error; err != nil {
		c.String(http.StatusNotFound, "payment order not found")
		return
	}
	if order.Status != paymentStatusPending {
		c.String(http.StatusConflict, "payment order is not pending")
		return
	}
	if strings.TrimSpace(order.GatewayProvider) != "" && order.GatewayProvider != paymentProviderOpenPayment {
		c.String(http.StatusBadRequest, "payment order provider mismatch")
		return
	}
	params, endpoint, _, err := buildOpenPaymentPaymentParams(c, cfg, order)
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, openPaymentAutoSubmitHTML(endpoint, params))
}

func handleOpenPaymentCallback(c *gin.Context, cfg paymentConfig) (bool, error) {
	if strings.TrimSpace(openPaymentMerchantID(cfg)) == "" || strings.TrimSpace(openPaymentMerchantKey(cfg)) == "" {
		return false, errors.New("Open Payment merchant is not configured")
	}
	discovery, err := fetchOpenPaymentDiscovery(c.Request.Context(), cfg)
	if err != nil {
		return false, err
	}
	params := paymentParams(c)
	if !verifyOpenPaymentSign(params, openPaymentMerchantKey(cfg), discovery) {
		return false, errors.New("invalid sign")
	}
	if merchantID := openPaymentParam(params, discovery, "merchant_id"); merchantID != openPaymentMerchantID(cfg) {
		return false, errors.New("invalid merchant id")
	}
	if !openPaymentTradeSuccessful(params, discovery) {
		return false, errors.New("trade not successful")
	}
	orderNo := strings.TrimSpace(openPaymentParam(params, discovery, "merchant_order_no"))
	if orderNo == "" {
		return false, errors.New("missing order no")
	}
	money, err := decimal.NewFromString(strings.TrimSpace(openPaymentParam(params, discovery, "amount")))
	if err != nil {
		return false, err
	}
	notifyPayload := paramsJSON(params)
	gatewayTradeNo := openPaymentParam(params, discovery, "platform_order_no")
	var order model.PaymentOrder
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_no = ?", orderNo).First(&order).Error; err != nil {
			return err
		}
		if order.Status == paymentStatusPaid {
			return nil
		}
		if !money.Round(2).Equal(order.RMBAmount.Round(2)) {
			return errors.New("payment amount mismatch")
		}
		now := time.Now()
		updates := map[string]interface{}{
			"status":           paymentStatusPaid,
			"gateway_provider": paymentProviderOpenPayment,
			"gateway_trade_no": gatewayTradeNo,
			"notify_payload":   notifyPayload,
			"paid_at":          &now,
		}
		if err := tx.Model(&order).Updates(updates).Error; err != nil {
			return err
		}
		return tx.Model(&model.User{}).Where("id = ?", order.UserID).UpdateColumn("balance", gorm.Expr("balance + ?", order.Amount)).Error
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

func paymentNotifySuccessBody(c *gin.Context) string {
	cfg := currentPaymentConfig()
	if cfg.Provider != paymentProviderOpenPayment {
		return "success"
	}
	discovery, err := fetchOpenPaymentDiscovery(c.Request.Context(), cfg)
	if err != nil {
		return "success"
	}
	return firstNonEmptyString(discovery.Callbacks.NotifySuccessBody, "success")
}

func openPaymentMerchantID(cfg paymentConfig) string {
	return strings.TrimSpace(firstNonEmptyString(cfg.OpenPaymentMerchantID, cfg.PID))
}

func openPaymentMerchantKey(cfg paymentConfig) string {
	return strings.TrimSpace(firstNonEmptyString(cfg.OpenPaymentKey, cfg.Key))
}

func fetchOpenPaymentDiscovery(ctx context.Context, cfg paymentConfig) (openPaymentDiscovery, error) {
	discoveryURL, err := openPaymentDiscoveryURL(cfg)
	if err != nil {
		return openPaymentDiscovery{}, err
	}
	if cached, ok := openPaymentCache.Load(discoveryURL); ok {
		entry := cached.(openPaymentCacheEntry)
		if time.Now().Before(entry.expiresAt) {
			return entry.discovery, nil
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return openPaymentDiscovery{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "flai-open-payment/1.0")
	resp, err := openPaymentHTTPClient.Do(req)
	if err != nil {
		return openPaymentDiscovery{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return openPaymentDiscovery{}, fmt.Errorf("Open Payment discovery returned HTTP %d", resp.StatusCode)
	}
	var discovery openPaymentDiscovery
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&discovery); err != nil {
		return openPaymentDiscovery{}, err
	}
	if err := normalizeOpenPaymentDiscovery(&discovery); err != nil {
		return openPaymentDiscovery{}, err
	}
	openPaymentCache.Store(discoveryURL, openPaymentCacheEntry{discovery: discovery, expiresAt: time.Now().Add(5 * time.Minute)})
	return discovery, nil
}

func openPaymentDiscoveryURL(cfg paymentConfig) (string, error) {
	if rawURL := strings.TrimSpace(cfg.OpenPaymentConfigURL); rawURL != "" {
		parsed, err := url.Parse(rawURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return "", errors.New("invalid Open Payment discovery URL")
		}
		return parsed.String(), nil
	}
	parsed, err := url.Parse(strings.TrimSpace(cfg.OpenPaymentBaseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("invalid Open Payment base URL")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + openPaymentDiscoveryPath
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func normalizeOpenPaymentDiscovery(discovery *openPaymentDiscovery) error {
	if discovery == nil {
		return errors.New("Open Payment discovery is empty")
	}
	if discovery.Spec != "" && discovery.Spec != "Open Payment Specification" {
		return errors.New("invalid Open Payment discovery spec")
	}
	if strings.TrimSpace(discovery.Endpoints.Submit) == "" {
		return errors.New("Open Payment discovery is missing submit endpoint")
	}
	if discovery.Fields == nil {
		discovery.Fields = map[string][]string{}
	}
	for field, aliases := range defaultOpenPaymentFieldAliases {
		if len(discovery.Fields[field]) == 0 {
			discovery.Fields[field] = aliases
		}
	}
	if discovery.Callbacks.NotifySuccessBody == "" {
		discovery.Callbacks.NotifySuccessBody = "success"
	}
	if len(discovery.Compatibility.SuccessStatusValues) == 0 {
		discovery.Compatibility.SuccessStatusValues = []string{"TRADE_SUCCESS", "TRADE_FINISHED", "SUCCESS", "1", "success", "paid"}
	}
	return nil
}

func openPaymentFieldName(discovery openPaymentDiscovery, field string) string {
	aliases := openPaymentFieldAliases(discovery, field)
	if len(aliases) == 0 {
		return field
	}
	return aliases[0]
}

func openPaymentParam(params map[string]string, discovery openPaymentDiscovery, field string) string {
	for _, alias := range openPaymentFieldAliases(discovery, field) {
		if value := strings.TrimSpace(params[alias]); value != "" {
			return value
		}
	}
	return ""
}

func openPaymentFieldAliases(discovery openPaymentDiscovery, field string) []string {
	seen := map[string]struct{}{}
	var aliases []string
	add := func(values ...string) {
		for _, value := range values {
			name := strings.TrimSpace(value)
			if name == "" {
				continue
			}
			if _, exists := seen[name]; exists {
				continue
			}
			seen[name] = struct{}{}
			aliases = append(aliases, name)
		}
	}
	add(discovery.Fields[field]...)
	add(defaultOpenPaymentFieldAliases[field]...)
	add(field)
	return aliases
}

func openPaymentMethodForSubmit(method string, discovery openPaymentDiscovery) (string, error) {
	method = strings.TrimSpace(method)
	if len(discovery.PaymentMethods) == 0 {
		return method, nil
	}
	for _, candidate := range discovery.PaymentMethods {
		if candidate.Enabled != nil && !*candidate.Enabled {
			continue
		}
		if strings.EqualFold(candidate.Code, method) || stringListContainsFold(candidate.Aliases, method) {
			for _, alias := range candidate.Aliases {
				if strings.TrimSpace(alias) != "" {
					return strings.TrimSpace(alias), nil
				}
			}
			return strings.TrimSpace(candidate.Code), nil
		}
	}
	return "", errors.New("payment method is not supported by Open Payment provider")
}

func openPaymentTradeSuccessful(params map[string]string, discovery openPaymentDiscovery) bool {
	status := openPaymentParam(params, discovery, "status")
	if status == "" {
		status = firstNonEmptyString(params["trade_status"], params["status"])
	}
	for _, success := range discovery.Compatibility.SuccessStatusValues {
		if strings.EqualFold(strings.TrimSpace(status), strings.TrimSpace(success)) {
			return true
		}
	}
	return false
}

func openPaymentSigningMethod(discovery openPaymentDiscovery) (string, error) {
	defaultMethod := normalizeOpenPaymentSignType(discovery.Signing.Default)
	if defaultMethod != "" && openPaymentCanSign(defaultMethod) && openPaymentSignSupported(discovery, defaultMethod) {
		return defaultMethod, nil
	}
	for _, method := range discovery.Signing.Supported {
		normalized := normalizeOpenPaymentSignType(method)
		if openPaymentCanSign(normalized) {
			return normalized, nil
		}
	}
	if len(discovery.Signing.Supported) == 0 {
		return "MD5", nil
	}
	return "", errors.New("unsupported Open Payment signing method")
}

func verifyOpenPaymentSign(params map[string]string, key string, discovery openPaymentDiscovery) bool {
	sign := strings.TrimSpace(params["sign"])
	if sign == "" {
		return false
	}
	signType := normalizeOpenPaymentSignType(params["sign_type"])
	if signType == "" {
		var err error
		signType, err = openPaymentSigningMethod(discovery)
		if err != nil {
			return false
		}
	}
	candidates, err := openPaymentSignCandidates(params, key, signType, discovery)
	if err != nil {
		return false
	}
	for _, expected := range candidates {
		if openPaymentSignEqual(signType, sign, expected) {
			return true
		}
	}
	return false
}

func buildOpenPaymentSign(params map[string]string, key, signType string, discovery openPaymentDiscovery) (string, error) {
	candidates, err := openPaymentSignCandidates(params, key, signType, discovery)
	if err != nil {
		return "", err
	}
	return candidates[0], nil
}

func openPaymentSignCandidates(params map[string]string, key, signType string, discovery openPaymentDiscovery) ([]string, error) {
	canonical := openPaymentCanonicalString(params, discovery.Signing.IncludeSignType)
	switch normalizeOpenPaymentSignType(signType) {
	case "MD5":
		sum := md5.Sum([]byte(canonical + key))
		return []string{hex.EncodeToString(sum[:])}, nil
	case "HMAC-SHA256":
		mac := hmac.New(sha256.New, []byte(key))
		_, _ = mac.Write([]byte(canonical))
		raw := mac.Sum(nil)
		hexSign := hex.EncodeToString(raw)
		base64Sign := base64.StdEncoding.EncodeToString(raw)
		if strings.EqualFold(openPaymentHMACOutput(discovery), "base64") {
			return []string{base64Sign, hexSign}, nil
		}
		return []string{hexSign, base64Sign}, nil
	default:
		return nil, errors.New("unsupported Open Payment signing method")
	}
}

func openPaymentCanonicalString(params map[string]string, includeSignType bool) string {
	keys := make([]string, 0, len(params))
	for name, value := range params {
		if strings.EqualFold(name, "sign") || (!includeSignType && strings.EqualFold(name, "sign_type")) || strings.TrimSpace(value) == "" {
			continue
		}
		keys = append(keys, name)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, name := range keys {
		parts = append(parts, name+"="+params[name])
	}
	return strings.Join(parts, "&")
}

func normalizeOpenPaymentSignType(signType string) string {
	switch strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(signType), "_", "-")) {
	case "", "MD5":
		return strings.ToUpper(strings.TrimSpace(signType))
	case "HMAC-SHA256", "HMACSHA256":
		return "HMAC-SHA256"
	case "RSA-SHA256", "RSASHA256":
		return "RSA-SHA256"
	default:
		return strings.ToUpper(strings.TrimSpace(signType))
	}
}

func openPaymentCanSign(signType string) bool {
	return signType == "MD5" || signType == "HMAC-SHA256"
}

func openPaymentSignSupported(discovery openPaymentDiscovery, signType string) bool {
	if len(discovery.Signing.Supported) == 0 {
		return true
	}
	for _, supported := range discovery.Signing.Supported {
		if normalizeOpenPaymentSignType(supported) == signType {
			return true
		}
	}
	return false
}

func openPaymentSignEqual(signType, received, expected string) bool {
	if normalizeOpenPaymentSignType(signType) == "MD5" || isLowerHexSignature(expected) {
		return hmac.Equal([]byte(strings.ToLower(received)), []byte(strings.ToLower(expected)))
	}
	return hmac.Equal([]byte(received), []byte(expected))
}

func openPaymentHMACOutput(discovery openPaymentDiscovery) string {
	return firstNonEmptyString(discovery.Signing.HMAC.Output, discovery.Signing.Output)
}

func isLowerHexSignature(value string) bool {
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return value != ""
}

func openPaymentSupportsTransport(discovery openPaymentDiscovery, scene, transport string) bool {
	transports := discovery.Transports[scene]
	if len(transports) == 0 {
		return transport == "form_get"
	}
	return stringListContainsFold(transports, transport)
}

func openPaymentURLWithParams(endpoint string, params map[string]string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("invalid Open Payment submit endpoint")
	}
	query := parsed.Query()
	for key, value := range params {
		query.Set(key, value)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func openPaymentAutoSubmitHTML(endpoint string, params map[string]string) string {
	var builder strings.Builder
	builder.WriteString("<!doctype html><html><head><meta charset=\"utf-8\"><title>Redirecting to payment</title></head><body>")
	builder.WriteString("<form id=\"payment\" method=\"post\" action=\"")
	builder.WriteString(html.EscapeString(endpoint))
	builder.WriteString("\">")
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		builder.WriteString("<input type=\"hidden\" name=\"")
		builder.WriteString(html.EscapeString(key))
		builder.WriteString("\" value=\"")
		builder.WriteString(html.EscapeString(params[key]))
		builder.WriteString("\">")
	}
	builder.WriteString("<noscript><button type=\"submit\">Continue to payment</button></noscript>")
	builder.WriteString("</form><script>document.getElementById('payment').submit()</script></body></html>")
	return builder.String()
}

func stringListContainsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}
