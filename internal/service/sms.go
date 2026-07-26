package service

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
)

const (
	smsProviderAliyun  = "aliyun"
	smsProviderTencent = "tencent"
)

var phonePattern = regexp.MustCompile(`^1[3-9]\d{9}$`)

// NormalizePhone accepts a mainland China mobile number with an optional
// +86/86 prefix and returns the bare 11-digit form.
func NormalizePhone(raw string) (string, error) {
	phone := strings.TrimSpace(raw)
	phone = strings.ReplaceAll(phone, " ", "")
	phone = strings.ReplaceAll(phone, "-", "")
	phone = strings.TrimPrefix(phone, "+86")
	phone = strings.TrimPrefix(phone, "86")
	if !phonePattern.MatchString(phone) {
		return "", errors.New("valid phone number is required")
	}
	return phone, nil
}

func smsProvider() string {
	provider := strings.TrimSpace(strings.ToLower(model.GetSystemSetting("sms_provider", smsProviderAliyun)))
	if provider == "" {
		provider = smsProviderAliyun
	}
	return provider
}

func smsConfigured() bool {
	switch smsProvider() {
	case smsProviderAliyun:
		return strings.TrimSpace(model.GetSystemSetting("sms_aliyun_access_key_id", "")) != "" &&
			strings.TrimSpace(model.GetSystemSetting("sms_aliyun_access_key_secret", "")) != "" &&
			strings.TrimSpace(model.GetSystemSetting("sms_aliyun_sign_name", "")) != "" &&
			strings.TrimSpace(model.GetSystemSetting("sms_aliyun_template_code", "")) != ""
	case smsProviderTencent:
		return strings.TrimSpace(model.GetSystemSetting("sms_tencent_secret_id", "")) != "" &&
			strings.TrimSpace(model.GetSystemSetting("sms_tencent_secret_key", "")) != "" &&
			strings.TrimSpace(model.GetSystemSetting("sms_tencent_sdk_app_id", "")) != "" &&
			strings.TrimSpace(model.GetSystemSetting("sms_tencent_sign_name", "")) != "" &&
			strings.TrimSpace(model.GetSystemSetting("sms_tencent_template_id", "")) != ""
	}
	return false
}

// PhoneAuthEnabled reports whether phone registration/binding is switched on
// and the selected SMS provider has usable credentials.
func PhoneAuthEnabled() bool {
	return settingBool("sms_enabled", false) && smsConfigured()
}

func sendSMSCode(phone string, code string) error {
	switch smsProvider() {
	case smsProviderAliyun:
		return sendAliyunSMSCode(phone, code)
	case smsProviderTencent:
		return sendTencentSMSCode(phone, code)
	}
	return errors.New("SMS provider is not configured")
}

var smsHTTPClient = &http.Client{Timeout: 10 * time.Second}

// --- Aliyun (dysmsapi, RPC signature with HMAC-SHA1) ---

func aliyunSpecialURLEncode(value string) string {
	encoded := url.QueryEscape(value)
	encoded = strings.ReplaceAll(encoded, "+", "%20")
	encoded = strings.ReplaceAll(encoded, "*", "%2A")
	encoded = strings.ReplaceAll(encoded, "%7E", "~")
	return encoded
}

func sendAliyunSMSCode(phone string, code string) error {
	accessKeyID := strings.TrimSpace(model.GetSystemSetting("sms_aliyun_access_key_id", ""))
	accessKeySecret := strings.TrimSpace(model.GetSystemSetting("sms_aliyun_access_key_secret", ""))
	signName := strings.TrimSpace(model.GetSystemSetting("sms_aliyun_sign_name", ""))
	templateCode := strings.TrimSpace(model.GetSystemSetting("sms_aliyun_template_code", ""))
	if accessKeyID == "" || accessKeySecret == "" || signName == "" || templateCode == "" {
		return errors.New("Aliyun SMS is not configured")
	}

	templateParam, err := json.Marshal(map[string]string{"code": code})
	if err != nil {
		return err
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	params := map[string]string{
		"AccessKeyId":      accessKeyID,
		"Action":           "SendSms",
		"Format":           "JSON",
		"PhoneNumbers":     phone,
		"RegionId":         "cn-hangzhou",
		"SignName":         signName,
		"SignatureMethod":  "HMAC-SHA1",
		"SignatureNonce":   hex.EncodeToString(nonce),
		"SignatureVersion": "1.0",
		"TemplateCode":     templateCode,
		"TemplateParam":    string(templateParam),
		"Timestamp":        time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		"Version":          "2017-05-25",
	}

	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, aliyunSpecialURLEncode(key)+"="+aliyunSpecialURLEncode(params[key]))
	}
	canonicalized := strings.Join(pairs, "&")
	stringToSign := "GET&" + aliyunSpecialURLEncode("/") + "&" + aliyunSpecialURLEncode(canonicalized)
	mac := hmac.New(sha1.New, []byte(accessKeySecret+"&"))
	mac.Write([]byte(stringToSign))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	requestURL := "https://dysmsapi.aliyuncs.com/?Signature=" + aliyunSpecialURLEncode(signature) + "&" + canonicalized
	resp, err := smsHTTPClient.Get(requestURL)
	if err != nil {
		return fmt.Errorf("send Aliyun SMS: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var result struct {
		Code    string `json:"Code"`
		Message string `json:"Message"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("parse Aliyun SMS response: %w", err)
	}
	if !strings.EqualFold(result.Code, "OK") {
		return fmt.Errorf("Aliyun SMS failed: %s (%s)", result.Message, result.Code)
	}
	return nil
}

// --- Tencent Cloud (sms.tencentcloudapi.com, TC3-HMAC-SHA256 signature) ---

func tencentHMACSHA256(key []byte, message string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(message))
	return mac.Sum(nil)
}

func sendTencentSMSCode(phone string, code string) error {
	secretID := strings.TrimSpace(model.GetSystemSetting("sms_tencent_secret_id", ""))
	secretKey := strings.TrimSpace(model.GetSystemSetting("sms_tencent_secret_key", ""))
	sdkAppID := strings.TrimSpace(model.GetSystemSetting("sms_tencent_sdk_app_id", ""))
	signName := strings.TrimSpace(model.GetSystemSetting("sms_tencent_sign_name", ""))
	templateID := strings.TrimSpace(model.GetSystemSetting("sms_tencent_template_id", ""))
	region := strings.TrimSpace(model.GetSystemSetting("sms_tencent_region", "ap-guangzhou"))
	if region == "" {
		region = "ap-guangzhou"
	}
	if secretID == "" || secretKey == "" || sdkAppID == "" || signName == "" || templateID == "" {
		return errors.New("Tencent Cloud SMS is not configured")
	}

	payload, err := json.Marshal(map[string]any{
		"PhoneNumberSet":   []string{"+86" + phone},
		"SmsSdkAppId":      sdkAppID,
		"SignName":         signName,
		"TemplateId":       templateID,
		"TemplateParamSet": []string{code},
	})
	if err != nil {
		return err
	}

	const host = "sms.tencentcloudapi.com"
	const algorithm = "TC3-HMAC-SHA256"
	const tcService = "sms"
	now := time.Now()
	timestamp := strconv.FormatInt(now.Unix(), 10)
	date := now.UTC().Format("2006-01-02")

	payloadHash := sha256.Sum256(payload)
	canonicalRequest := strings.Join([]string{
		http.MethodPost,
		"/",
		"",
		"content-type:application/json; charset=utf-8",
		"host:" + host,
		"x-tc-action:sendsms",
		"",
		"content-type;host;x-tc-action",
		hex.EncodeToString(payloadHash[:]),
	}, "\n")
	canonicalRequestHash := sha256.Sum256([]byte(canonicalRequest))
	credentialScope := date + "/" + tcService + "/tc3_request"
	stringToSign := strings.Join([]string{
		algorithm,
		timestamp,
		credentialScope,
		hex.EncodeToString(canonicalRequestHash[:]),
	}, "\n")

	secretDate := tencentHMACSHA256([]byte("TC3"+secretKey), date)
	secretService := tencentHMACSHA256(secretDate, tcService)
	secretSigning := tencentHMACSHA256(secretService, "tc3_request")
	signature := hex.EncodeToString(tencentHMACSHA256(secretSigning, stringToSign))

	req, err := http.NewRequest(http.MethodPost, "https://"+host, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Host", host)
	req.Header.Set("X-TC-Action", "SendSms")
	req.Header.Set("X-TC-Version", "2021-01-11")
	req.Header.Set("X-TC-Timestamp", timestamp)
	req.Header.Set("X-TC-Region", region)
	req.Header.Set("Authorization", fmt.Sprintf(
		"%s Credential=%s/%s, SignedHeaders=content-type;host;x-tc-action, Signature=%s",
		algorithm, secretID, credentialScope, signature,
	))

	resp, err := smsHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("send Tencent SMS: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var result struct {
		Response struct {
			Error *struct {
				Code    string `json:"Code"`
				Message string `json:"Message"`
			} `json:"Error"`
			SendStatusSet []struct {
				Code    string `json:"Code"`
				Message string `json:"Message"`
			} `json:"SendStatusSet"`
		} `json:"Response"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("parse Tencent SMS response: %w", err)
	}
	if result.Response.Error != nil {
		return fmt.Errorf("Tencent SMS failed: %s (%s)", result.Response.Error.Message, result.Response.Error.Code)
	}
	for _, status := range result.Response.SendStatusSet {
		if !strings.EqualFold(status.Code, "Ok") {
			return fmt.Errorf("Tencent SMS failed: %s (%s)", status.Message, status.Code)
		}
	}
	if len(result.Response.SendStatusSet) == 0 {
		return errors.New("Tencent SMS returned no send status")
	}
	return nil
}
