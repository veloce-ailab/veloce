package api

// Official payment-provider adapters.  They deliberately use the providers'
// public HTTP APIs instead of an aggregation gateway so merchant accounts,
// signatures and webhooks stay under the operator's control.

import (
	"bytes"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/veloce-ailab/veloce/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const officialPaymentMaxBody = 1024 * 1024

var officialPaymentHTTPClient = &http.Client{Timeout: 20 * time.Second}

func isOfficialPaymentProvider(provider string) bool {
	switch provider {
	case paymentProviderWeChatPay, paymentProviderAlipay, paymentProviderPayPal, paymentProviderStripe:
		return true
	default:
		return false
	}
}

func normalizeOfficialCurrency(raw string) string {
	currency := strings.ToUpper(strings.TrimSpace(raw))
	if currency == "" {
		return "CNY"
	}
	return currency
}

func paymentGatewayAmount(cfg paymentConfig, order model.PaymentOrder) (string, decimal.Decimal) {
	if !isOfficialPaymentProvider(cfg.Provider) {
		return "CNY", order.RMBAmount.Round(2)
	}
	currency := normalizeOfficialCurrency(cfg.OfficialCurrency)
	if currency == "CNY" {
		return currency, order.RMBAmount.Round(2)
	}
	return currency, order.Amount.Round(2)
}

func validateOfficialPaymentConfig(cfg paymentConfig) error {
	switch cfg.Provider {
	case paymentProviderWeChatPay:
		if normalizeOfficialCurrency(cfg.OfficialCurrency) != "CNY" {
			return errors.New("WeChat Pay Native supports CNY only")
		}
		if anyEmpty(cfg.WeChatMchID, cfg.WeChatAppID, cfg.WeChatSerialNo, cfg.WeChatPrivateKey, cfg.WeChatPlatformCert, cfg.WeChatAPIV3Key) {
			return errors.New("WeChat Pay merchant configuration is incomplete")
		}
		if len([]byte(cfg.WeChatAPIV3Key)) != 32 {
			return errors.New("WeChat Pay API v3 key must be 32 bytes")
		}
		_, err := parseRSAPrivateKey(cfg.WeChatPrivateKey)
		return err
	case paymentProviderAlipay:
		if anyEmpty(cfg.AlipayAppID, cfg.AlipayPrivateKey, cfg.AlipayPublicKey, cfg.AlipayGatewayURL) {
			return errors.New("Alipay application configuration is incomplete")
		}
		if _, err := parseRSAPrivateKey(cfg.AlipayPrivateKey); err != nil {
			return err
		}
		if _, err := parseRSAPublicKey(cfg.AlipayPublicKey); err != nil {
			return err
		}
		_, err := validHTTPURL(cfg.AlipayGatewayURL)
		return err
	case paymentProviderPayPal:
		if anyEmpty(cfg.PayPalClientID, cfg.PayPalClientSecret, cfg.PayPalWebhookID) {
			return errors.New("PayPal client and webhook configuration is incomplete")
		}
		_, err := validHTTPURL(cfg.PayPalBaseURL)
		return err
	case paymentProviderStripe:
		if anyEmpty(cfg.StripeSecretKey, cfg.StripeWebhookSecret) {
			return errors.New("Stripe API key and webhook signing secret are required")
		}
		return nil
	default:
		return errors.New("unsupported official payment provider")
	}
}

func anyEmpty(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return true
		}
	}
	return false
}

func validHTTPURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return nil, errors.New("invalid payment provider URL")
	}
	return parsed, nil
}

func buildOfficialPaymentURL(c *gin.Context, cfg paymentConfig, order model.PaymentOrder) (string, error) {
	switch cfg.Provider {
	case paymentProviderWeChatPay:
		return buildWeChatPayPaymentURL(c, cfg, order)
	case paymentProviderAlipay:
		return buildAlipayPaymentURL(c, cfg, order)
	case paymentProviderPayPal:
		return buildPayPalPaymentURL(c, cfg, order)
	case paymentProviderStripe:
		return buildStripePaymentURL(c, cfg, order)
	default:
		return "", errors.New("unsupported official payment provider")
	}
}

func buildWeChatPayPaymentURL(c *gin.Context, cfg paymentConfig, order model.PaymentOrder) (string, error) {
	privateKey, err := parseRSAPrivateKey(cfg.WeChatPrivateKey)
	if err != nil {
		return "", err
	}
	cents, err := amountInMinorUnits(order.GatewayAmount)
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(gin.H{
		"appid": cfg.WeChatAppID, "mchid": cfg.WeChatMchID, "description": "Balance recharge " + order.OrderNo,
		"out_trade_no": order.OrderNo, "notify_url": publicCallbackURL(c, "/api/payment/wechatpay/notify"),
		"amount": gin.H{"total": cents, "currency": "CNY"},
	})
	if err != nil {
		return "", err
	}
	nonce, err := randomNonce(32)
	if err != nil {
		return "", err
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	message := "POST\n/v3/pay/transactions/native\n" + timestamp + "\n" + nonce + "\n" + string(body) + "\n"
	signature, err := rsaSign(privateKey, message)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, "https://api.mch.weixin.qq.com/v3/pay/transactions/native", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf(`WECHATPAY2-SHA256-RSA2048 mchid="%s",nonce_str="%s",timestamp="%s",serial_no="%s",signature="%s"`, cfg.WeChatMchID, nonce, timestamp, cfg.WeChatSerialNo, signature))
	var response struct {
		CodeURL string `json:"code_url"`
		Message string `json:"message"`
	}
	if err := doJSON(req, &response); err != nil {
		return "", err
	}
	if strings.TrimSpace(response.CodeURL) == "" {
		return "", errors.New("WeChat Pay did not return a code URL: " + response.Message)
	}
	return response.CodeURL, nil
}

func buildAlipayPaymentURL(c *gin.Context, cfg paymentConfig, order model.PaymentOrder) (string, error) {
	privateKey, err := parseRSAPrivateKey(cfg.AlipayPrivateKey)
	if err != nil {
		return "", err
	}
	params := map[string]string{
		"app_id": cfg.AlipayAppID, "method": "alipay.trade.page.pay", "format": "JSON", "charset": "utf-8",
		"sign_type": "RSA2", "timestamp": time.Now().Format("2006-01-02 15:04:05"), "version": "1.0",
		"notify_url": publicCallbackURL(c, "/api/payment/alipay/notify"),
		"return_url": publicCallbackURL(c, "/api/payment/alipay/return?order_no="+url.QueryEscape(order.OrderNo)),
	}
	bizContent, _ := json.Marshal(map[string]string{"out_trade_no": order.OrderNo, "product_code": "FAST_INSTANT_TRADE_PAY", "total_amount": order.GatewayAmount.StringFixed(2), "subject": "Balance recharge " + order.OrderNo})
	params["biz_content"] = string(bizContent)
	sign, err := rsaSign(privateKey, canonicalPaymentParams(params))
	if err != nil {
		return "", err
	}
	params["sign"] = sign
	endpoint, err := validHTTPURL(cfg.AlipayGatewayURL)
	if err != nil {
		return "", err
	}
	query := endpoint.Query()
	for key, value := range params {
		query.Set(key, value)
	}
	endpoint.RawQuery = query.Encode()
	return endpoint.String(), nil
}

func buildPayPalPaymentURL(c *gin.Context, cfg paymentConfig, order model.PaymentOrder) (string, error) {
	accessToken, err := payPalAccessToken(c, cfg)
	if err != nil {
		return "", err
	}
	baseURL, err := validHTTPURL(cfg.PayPalBaseURL)
	if err != nil {
		return "", err
	}
	payload := map[string]interface{}{
		"intent":              "CAPTURE",
		"purchase_units":      []map[string]interface{}{{"reference_id": order.OrderNo, "custom_id": order.OrderNo, "description": "Balance recharge", "amount": map[string]string{"currency_code": order.PaymentCurrency, "value": order.GatewayAmount.StringFixed(2)}}},
		"application_context": map[string]string{"return_url": publicCallbackURL(c, "/api/payment/paypal/return?order_no="+url.QueryEscape(order.OrderNo)), "cancel_url": publicCallbackURL(c, "/api/payment/paypal/return?order_no="+url.QueryEscape(order.OrderNo)+"&payment=cancel")},
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, strings.TrimRight(baseURL.String(), "/")+"/v2/checkout/orders", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "return=representation")
	var response struct {
		ID    string `json:"id"`
		Links []struct {
			Rel  string `json:"rel"`
			Href string `json:"href"`
		} `json:"links"`
	}
	if err := doJSON(req, &response); err != nil {
		return "", err
	}
	for _, link := range response.Links {
		if link.Rel == "approve" && link.Href != "" {
			_ = model.DB.Model(&model.PaymentOrder{}).Where("id = ?", order.ID).Update("gateway_trade_no", response.ID).Error
			return link.Href, nil
		}
	}
	return "", errors.New("PayPal did not return an approval URL")
}

func buildStripePaymentURL(c *gin.Context, cfg paymentConfig, order model.PaymentOrder) (string, error) {
	minor, err := amountInMinorUnits(order.GatewayAmount)
	if err != nil {
		return "", err
	}
	form := url.Values{
		"mode": {"payment"}, "success_url": {publicCallbackURL(c, "/api/payment/stripe/return?order_no="+url.QueryEscape(order.OrderNo)+"&payment=success")}, "cancel_url": {publicCallbackURL(c, "/api/payment/stripe/return?order_no="+url.QueryEscape(order.OrderNo)+"&payment=cancel")},
		"metadata[order_no]": {order.OrderNo}, "line_items[0][quantity]": {"1"}, "line_items[0][price_data][currency]": {strings.ToLower(order.PaymentCurrency)}, "line_items[0][price_data][unit_amount]": {strconv.FormatInt(minor, 10)}, "line_items[0][price_data][product_data][name]": {"Balance recharge"},
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, "https://api.stripe.com/v1/checkout/sessions", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(cfg.StripeSecretKey, "")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var response struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if err := doJSON(req, &response); err != nil {
		return "", err
	}
	if response.URL == "" {
		return "", errors.New("Stripe did not return a Checkout URL")
	}
	_ = model.DB.Model(&model.PaymentOrder{}).Where("id = ?", order.ID).Update("gateway_trade_no", response.ID).Error
	return response.URL, nil
}

func (api *PaymentAPI) WeChatPayNotify(c *gin.Context) {
	api.officialNotify(c, paymentProviderWeChatPay)
}
func (api *PaymentAPI) AlipayNotify(c *gin.Context) { api.officialNotify(c, paymentProviderAlipay) }
func (api *PaymentAPI) PayPalNotify(c *gin.Context) { api.officialNotify(c, paymentProviderPayPal) }
func (api *PaymentAPI) StripeNotify(c *gin.Context) { api.officialNotify(c, paymentProviderStripe) }

func (api *PaymentAPI) OfficialReturn(c *gin.Context) {
	orderNo := strings.TrimSpace(c.Query("order_no"))
	status := strings.TrimSpace(c.Query("payment"))
	if status == "" {
		status = "pending"
	}
	c.Redirect(http.StatusFound, "/dashboard/wallet?payment="+url.QueryEscape(status)+"&order_no="+url.QueryEscape(orderNo))
}

func (api *PaymentAPI) officialNotify(c *gin.Context, provider string) {
	if !paymentFeatureEnabled || service.PersonalModeEnabled() {
		c.String(http.StatusNotFound, "payment requires premium edition")
		return
	}
	channels := make([]paymentConfig, 0)
	for _, channel := range paymentChannels() {
		if channel.Provider == provider {
			channels = append(channels, channel)
		}
	}
	if len(channels) == 0 {
		c.String(http.StatusNotFound, "payment provider is not enabled")
		return
	}
	var err error
	for _, cfg := range channels {
		switch provider {
		case paymentProviderWeChatPay:
			err = handleWeChatPayNotify(c, cfg)
		case paymentProviderAlipay:
			err = handleAlipayNotify(c, cfg)
		case paymentProviderPayPal:
			err = handlePayPalNotify(c, cfg)
		case paymentProviderStripe:
			err = handleStripeNotify(c, cfg)
		}
		if err == nil {
			break
		}
	}
	if err != nil {
		if provider == paymentProviderWeChatPay {
			c.JSON(http.StatusBadRequest, gin.H{"code": "FAIL", "message": "invalid notification"})
		} else {
			c.String(http.StatusBadRequest, "fail")
		}
		return
	}
	if provider == paymentProviderWeChatPay {
		c.JSON(http.StatusOK, gin.H{"code": "SUCCESS", "message": "success"})
	} else {
		c.String(http.StatusOK, "success")
	}
}

func handleWeChatPayNotify(c *gin.Context, cfg paymentConfig) error {
	body, err := readPaymentBody(c)
	if err != nil {
		return err
	}
	publicKey, err := parseRSAPublicKey(cfg.WeChatPlatformCert)
	if err != nil {
		return err
	}
	timestamp, nonce, signature := c.GetHeader("Wechatpay-Timestamp"), c.GetHeader("Wechatpay-Nonce"), c.GetHeader("Wechatpay-Signature")
	if anyEmpty(timestamp, nonce, signature) {
		return errors.New("missing WeChat Pay signature")
	}
	if err := rsaVerify(publicKey, timestamp+"\n"+nonce+"\n"+string(body)+"\n", signature); err != nil {
		return err
	}
	var envelope struct {
		Resource struct{ Algorithm, Nonce, Ciphertext, AssociatedData string } `json:"resource"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return err
	}
	if envelope.Resource.Algorithm != "AEAD_AES_256_GCM" {
		return errors.New("unsupported WeChat Pay encryption")
	}
	block, err := aes.NewCipher([]byte(cfg.WeChatAPIV3Key))
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	ciphertext, err := base64.StdEncoding.DecodeString(envelope.Resource.Ciphertext)
	if err != nil {
		return err
	}
	plain, err := gcm.Open(nil, []byte(envelope.Resource.Nonce), ciphertext, []byte(envelope.Resource.AssociatedData))
	if err != nil {
		return err
	}
	var transaction struct {
		OutTradeNo    string `json:"out_trade_no"`
		TransactionID string `json:"transaction_id"`
		TradeState    string `json:"trade_state"`
		Amount        struct {
			Total    int64  `json:"total"`
			Currency string `json:"currency"`
		} `json:"amount"`
	}
	if err := json.Unmarshal(plain, &transaction); err != nil {
		return err
	}
	if transaction.TradeState != "SUCCESS" || transaction.Amount.Currency != "CNY" {
		return errors.New("WeChat Pay transaction is not successful")
	}
	return completeOfficialPayment(paymentProviderWeChatPay, cfg.ChannelID, transaction.OutTradeNo, transaction.TransactionID, decimal.NewFromInt(transaction.Amount.Total).Div(decimal.NewFromInt(100)), "CNY", string(plain))
}

func handleAlipayNotify(c *gin.Context, cfg paymentConfig) error {
	params := paymentParams(c)
	publicKey, err := parseRSAPublicKey(cfg.AlipayPublicKey)
	if err != nil {
		return err
	}
	if err := rsaVerify(publicKey, canonicalPaymentParams(params), params["sign"]); err != nil {
		return err
	}
	if params["app_id"] != cfg.AlipayAppID || (params["trade_status"] != "TRADE_SUCCESS" && params["trade_status"] != "TRADE_FINISHED") {
		return errors.New("Alipay trade is not successful")
	}
	amount, err := decimal.NewFromString(params["total_amount"])
	if err != nil {
		return err
	}
	return completeOfficialPayment(paymentProviderAlipay, cfg.ChannelID, params["out_trade_no"], params["trade_no"], amount, normalizeOfficialCurrency(cfg.OfficialCurrency), paramsJSON(params))
}

func handlePayPalNotify(c *gin.Context, cfg paymentConfig) error {
	body, err := readPaymentBody(c)
	if err != nil {
		return err
	}
	if err := verifyPayPalWebhook(c, cfg, body); err != nil {
		return err
	}
	var event struct {
		EventType string          `json:"event_type"`
		Resource  json.RawMessage `json:"resource"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		return err
	}
	// Orders are created with intent=CAPTURE, so approval alone moves no money:
	// the buyer agreed but the funds are only taken once we call capture. Only a
	// completed capture may credit the balance.
	if event.EventType == "CHECKOUT.ORDER.APPROVED" {
		return capturePayPalApprovedOrder(c, cfg, event.Resource)
	}
	if event.EventType != "PAYMENT.CAPTURE.COMPLETED" {
		return nil
	}
	orderNo, tradeNo, amount, currency, err := paypalEventPayment(event.Resource)
	if err != nil {
		return err
	}
	if orderNo == "" && tradeNo != "" {
		var order model.PaymentOrder
		if err := model.DB.Where("gateway_provider = ? AND gateway_trade_no = ?", paymentProviderPayPal, tradeNo).First(&order).Error; err == nil {
			orderNo = order.OrderNo
		}
	}
	return completeOfficialPayment(paymentProviderPayPal, cfg.ChannelID, orderNo, tradeNo, amount, currency, string(body))
}

// capturePayPalApprovedOrder captures an approved order. The capture response is
// credited directly when PayPal reports it COMPLETED; otherwise the matching
// PAYMENT.CAPTURE.COMPLETED webhook settles the order later.
func capturePayPalApprovedOrder(c *gin.Context, cfg paymentConfig, resource json.RawMessage) error {
	var approved struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(resource, &approved); err != nil {
		return err
	}
	orderID := strings.TrimSpace(approved.ID)
	if orderID == "" {
		return errors.New("PayPal approval event has no order id")
	}
	accessToken, err := payPalAccessToken(c, cfg)
	if err != nil {
		return err
	}
	baseURL, err := validHTTPURL(cfg.PayPalBaseURL)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, strings.TrimRight(baseURL.String(), "/")+"/v2/checkout/orders/"+url.PathEscape(orderID)+"/capture", strings.NewReader("{}"))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	// Replaying an approval webhook must not capture twice.
	req.Header.Set("PayPal-Request-Id", "capture-"+orderID)
	var captured struct {
		ID            string `json:"id"`
		Status        string `json:"status"`
		PurchaseUnits []struct {
			CustomID string `json:"custom_id"`
			Payments struct {
				Captures []struct {
					ID     string `json:"id"`
					Status string `json:"status"`
					Amount struct {
						Value        string `json:"value"`
						CurrencyCode string `json:"currency_code"`
					} `json:"amount"`
				} `json:"captures"`
			} `json:"payments"`
		} `json:"purchase_units"`
	}
	if err := doJSON(req, &captured); err != nil {
		return fmt.Errorf("capture PayPal order %s: %w", orderID, err)
	}
	payload, _ := json.Marshal(captured)
	for _, unit := range captured.PurchaseUnits {
		for _, capture := range unit.Payments.Captures {
			if !strings.EqualFold(capture.Status, "COMPLETED") {
				continue
			}
			amount, err := decimal.NewFromString(capture.Amount.Value)
			if err != nil {
				return err
			}
			orderNo := strings.TrimSpace(unit.CustomID)
			if orderNo == "" {
				var order model.PaymentOrder
				if err := model.DB.Where("gateway_provider = ? AND gateway_trade_no = ?", paymentProviderPayPal, orderID).First(&order).Error; err != nil {
					return err
				}
				orderNo = order.OrderNo
			}
			return completeOfficialPayment(paymentProviderPayPal, cfg.ChannelID, orderNo, orderID, amount, capture.Amount.CurrencyCode, string(payload))
		}
	}
	// Not captured yet (for example PENDING for review); wait for the webhook.
	return nil
}

func handleStripeNotify(c *gin.Context, cfg paymentConfig) error {
	body, err := readPaymentBody(c)
	if err != nil {
		return err
	}
	if err := verifyStripeSignature(c.GetHeader("Stripe-Signature"), cfg.StripeWebhookSecret, body); err != nil {
		return err
	}
	var event struct {
		Type string `json:"type"`
		Data struct {
			Object struct {
				ID            string `json:"id"`
				PaymentStatus string `json:"payment_status"`
				Currency      string `json:"currency"`
				AmountTotal   int64  `json:"amount_total"`
				Metadata      struct {
					OrderNo string `json:"order_no"`
				} `json:"metadata"`
			} `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		return err
	}
	if event.Type != "checkout.session.completed" || event.Data.Object.PaymentStatus != "paid" {
		return nil
	}
	return completeOfficialPayment(paymentProviderStripe, cfg.ChannelID, event.Data.Object.Metadata.OrderNo, event.Data.Object.ID, decimal.NewFromInt(event.Data.Object.AmountTotal).Div(decimal.NewFromInt(100)), strings.ToUpper(event.Data.Object.Currency), string(body))
}

func completeOfficialPayment(provider, channelID, orderNo, tradeNo string, amount decimal.Decimal, currency, payload string) error {
	if strings.TrimSpace(orderNo) == "" {
		return errors.New("missing payment order number")
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		var order model.PaymentOrder
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("order_no = ?", orderNo).First(&order).Error; err != nil {
			return err
		}
		if order.GatewayProvider != provider || (order.GatewayChannel != "" && order.GatewayChannel != channelID) {
			return errors.New("payment provider mismatch")
		}
		if order.Status == paymentStatusPaid {
			return nil
		}
		if normalizeOfficialCurrency(order.PaymentCurrency) != normalizeOfficialCurrency(currency) || !order.GatewayAmount.Round(2).Equal(amount.Round(2)) {
			return errors.New("payment amount mismatch")
		}
		now := time.Now()
		if err := tx.Model(&order).Updates(map[string]interface{}{"status": paymentStatusPaid, "gateway_trade_no": tradeNo, "notify_payload": payload, "paid_at": &now}).Error; err != nil {
			return err
		}
		return tx.Model(&model.User{}).Where("id = ?", order.UserID).UpdateColumn("balance", gorm.Expr("balance + ?", order.Amount)).Error
	})
}

func parseRSAPrivateKey(raw string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(raw)))
	if block == nil {
		return nil, errors.New("invalid RSA private key")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rsaKey, ok := key.(*rsa.PrivateKey); ok {
			return rsaKey, nil
		}
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, errors.New("invalid RSA private key")
}

func parseRSAPublicKey(raw string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(raw)))
	if block == nil {
		return nil, errors.New("invalid RSA public key or certificate")
	}
	if cert, err := x509.ParseCertificate(block.Bytes); err == nil {
		if key, ok := cert.PublicKey.(*rsa.PublicKey); ok {
			return key, nil
		}
	}
	if key, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		if rsaKey, ok := key.(*rsa.PublicKey); ok {
			return rsaKey, nil
		}
	}
	if key, err := x509.ParsePKCS1PublicKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, errors.New("invalid RSA public key or certificate")
}

func rsaSign(key *rsa.PrivateKey, message string) (string, error) {
	sum := sha256.Sum256([]byte(message))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	return base64.StdEncoding.EncodeToString(signature), err
}
func rsaVerify(key *rsa.PublicKey, message, encodedSignature string) error {
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encodedSignature))
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(message))
	return rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], signature)
}

func canonicalPaymentParams(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for key, value := range params {
		if key != "sign" && key != "sign_type" && strings.TrimSpace(value) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+params[key])
	}
	return strings.Join(parts, "&")
}
func randomNonce(length int) (string, error) {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
func amountInMinorUnits(amount decimal.Decimal) (int64, error) {
	value := amount.Mul(decimal.NewFromInt(100))
	if !value.Equal(value.Truncate(0)) || value.LessThanOrEqual(decimal.Zero) {
		return 0, errors.New("payment amount must have at most two decimal places")
	}
	return value.IntPart(), nil
}

func readPaymentBody(c *gin.Context) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, officialPaymentMaxBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > officialPaymentMaxBody {
		return nil, errors.New("payment notification body is too large")
	}
	c.Request.Body.Close()
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}
func doJSON(req *http.Request, destination interface{}) error {
	response, err := officialPaymentHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, officialPaymentMaxBody))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("payment provider returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, destination)
}

func payPalAccessToken(c *gin.Context, cfg paymentConfig) (string, error) {
	baseURL, err := validHTTPURL(cfg.PayPalBaseURL)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, strings.TrimRight(baseURL.String(), "/")+"/v1/oauth2/token", strings.NewReader("grant_type=client_credentials"))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(cfg.PayPalClientID, cfg.PayPalClientSecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var response struct {
		AccessToken string `json:"access_token"`
	}
	if err := doJSON(req, &response); err != nil {
		return "", err
	}
	if response.AccessToken == "" {
		return "", errors.New("PayPal did not return an access token")
	}
	return response.AccessToken, nil
}
func verifyPayPalWebhook(c *gin.Context, cfg paymentConfig, body []byte) error {
	token, err := payPalAccessToken(c, cfg)
	if err != nil {
		return err
	}
	baseURL, err := validHTTPURL(cfg.PayPalBaseURL)
	if err != nil {
		return err
	}
	payload := map[string]interface{}{"auth_algo": c.GetHeader("PAYPAL-AUTH-ALGO"), "cert_url": c.GetHeader("PAYPAL-CERT-URL"), "transmission_id": c.GetHeader("PAYPAL-TRANSMISSION-ID"), "transmission_sig": c.GetHeader("PAYPAL-TRANSMISSION-SIG"), "transmission_time": c.GetHeader("PAYPAL-TRANSMISSION-TIME"), "webhook_id": cfg.PayPalWebhookID, "webhook_event": json.RawMessage(body)}
	data, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, strings.TrimRight(baseURL.String(), "/")+"/v1/notifications/verify-webhook-signature", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	var response struct {
		VerificationStatus string `json:"verification_status"`
	}
	if err := doJSON(req, &response); err != nil {
		return err
	}
	if response.VerificationStatus != "SUCCESS" {
		return errors.New("PayPal webhook signature verification failed")
	}
	return nil
}
func paypalEventPayment(resource json.RawMessage) (string, string, decimal.Decimal, string, error) {
	var payload struct {
		ID            string                               `json:"id"`
		CustomID      string                               `json:"custom_id"`
		Amount        struct{ Value, CurrencyCode string } `json:"amount"`
		PurchaseUnits []struct {
			CustomID string                               `json:"custom_id"`
			Amount   struct{ Value, CurrencyCode string } `json:"amount"`
		} `json:"purchase_units"`
		SupplementaryData struct {
			RelatedIDs struct {
				OrderID string `json:"order_id"`
			} `json:"related_ids"`
		} `json:"supplementary_data"`
	}
	if err := json.Unmarshal(resource, &payload); err != nil {
		return "", "", decimal.Zero, "", err
	}
	orderNo, amountValue, currency := payload.CustomID, payload.Amount.Value, payload.Amount.CurrencyCode
	if len(payload.PurchaseUnits) > 0 {
		if orderNo == "" {
			orderNo = payload.PurchaseUnits[0].CustomID
		}
		if amountValue == "" {
			amountValue = payload.PurchaseUnits[0].Amount.Value
			currency = payload.PurchaseUnits[0].Amount.CurrencyCode
		}
	}
	amount, err := decimal.NewFromString(amountValue)
	if err != nil {
		return "", "", decimal.Zero, "", err
	}
	tradeNo := payload.SupplementaryData.RelatedIDs.OrderID
	if tradeNo == "" {
		tradeNo = payload.ID
	}
	return orderNo, tradeNo, amount, currency, nil
}
func verifyStripeSignature(header, secret string, body []byte) error {
	var timestamp string
	signatures := []string{}
	for _, entry := range strings.Split(header, ",") {
		pair := strings.SplitN(entry, "=", 2)
		if len(pair) != 2 {
			continue
		}
		if pair[0] == "t" {
			timestamp = pair[1]
		}
		if pair[0] == "v1" {
			signatures = append(signatures, pair[1])
		}
	}
	unix, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || len(signatures) == 0 || time.Since(time.Unix(unix, 0)).Abs() > 5*time.Minute {
		return errors.New("invalid Stripe webhook signature")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(body)
	expected := mac.Sum(nil)
	for _, signature := range signatures {
		actual, err := hex.DecodeString(signature)
		if err == nil && subtle.ConstantTimeCompare(expected, actual) == 1 {
			return nil
		}
	}
	return errors.New("invalid Stripe webhook signature")
}
