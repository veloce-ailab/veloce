package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/smtp"
	"net/url"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	advancedChatDeliveryMethodWebhook = "webhook"
	advancedChatDeliveryMethodEmail   = "email"
)

type AdvancedChatDelivery struct {
	ID             string     `gorm:"primaryKey;size:80" json:"id"`
	UserID         uint       `gorm:"index;not null" json:"user_id"`
	User           model.User `gorm:"foreignKey:UserID" json:"-"`
	Name           string     `gorm:"size:120;not null" json:"name"`
	Description    string     `gorm:"type:text;not null" json:"description"`
	Method         string     `gorm:"size:20;not null" json:"method"`
	WebhookURL     string     `gorm:"type:text;not null" json:"webhook_url,omitempty"`
	WebhookHeaders string     `gorm:"type:text;not null;default:'{}'" json:"webhook_headers,omitempty"`
	EmailTo        string     `gorm:"size:320;not null" json:"email_to,omitempty"`
	SMTPHost       string     `gorm:"size:255;not null" json:"smtp_host,omitempty"`
	SMTPPort       string     `gorm:"size:20;not null" json:"smtp_port,omitempty"`
	SMTPUsername   string     `gorm:"size:255;not null" json:"smtp_username,omitempty"`
	SMTPPassword   string     `gorm:"type:text;not null" json:"smtp_password,omitempty"`
	SMTPFrom       string     `gorm:"size:320;not null" json:"smtp_from,omitempty"`
	Enabled        bool       `gorm:"default:true" json:"enabled"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type advancedChatDeliveryInput struct {
	Name           string `json:"name"`
	Description    string `json:"description"`
	Method         string `json:"method"`
	WebhookURL     string `json:"webhook_url"`
	WebhookHeaders string `json:"webhook_headers"`
	EmailTo        string `json:"email_to"`
	SMTPHost       string `json:"smtp_host"`
	SMTPPort       string `json:"smtp_port"`
	SMTPUsername   string `json:"smtp_username"`
	SMTPPassword   string `json:"smtp_password"`
	SMTPFrom       string `json:"smtp_from"`
	Enabled        *bool  `json:"enabled"`
}

var advancedChatDeliveryHTTPClient = &http.Client{Timeout: 20 * time.Second}

func (api *advancedChatAPI) listDeliveries(c *gin.Context) {
	if !advancedChatMessageDeliveryEnabled() {
		c.JSON(http.StatusForbidden, gin.H{"error": "Message delivery is disabled"})
		return
	}
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var deliveries []AdvancedChatDelivery
	if err := model.DB.Where("user_id = ?", user.ID).Order("created_at DESC").Find(&deliveries).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list deliveries"})
		return
	}
	c.JSON(http.StatusOK, deliveries)
}

func (api *advancedChatAPI) createDelivery(c *gin.Context) {
	if !advancedChatMessageDeliveryEnabled() {
		c.JSON(http.StatusForbidden, gin.H{"error": "Message delivery is disabled"})
		return
	}
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var input advancedChatDeliveryInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	delivery, ok := advancedChatDeliveryFromInput(c, user.ID, input)
	if !ok {
		return
	}
	delivery.ID = newAdvancedChatID("acd")
	if err := model.DB.Create(&delivery).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create delivery"})
		return
	}
	c.JSON(http.StatusOK, delivery)
}

func (api *advancedChatAPI) updateDelivery(c *gin.Context) {
	if !advancedChatMessageDeliveryEnabled() {
		c.JSON(http.StatusForbidden, gin.H{"error": "Message delivery is disabled"})
		return
	}
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var delivery AdvancedChatDelivery
	if err := model.DB.Where("id = ? AND user_id = ?", c.Param("id"), user.ID).First(&delivery).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Delivery not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load delivery"})
		return
	}
	var input advancedChatDeliveryInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	next, ok := advancedChatDeliveryFromInput(c, user.ID, input)
	if !ok {
		return
	}
	if err := model.DB.Model(&delivery).Updates(map[string]interface{}{
		"name":            next.Name,
		"description":     next.Description,
		"method":          next.Method,
		"webhook_url":     next.WebhookURL,
		"webhook_headers": next.WebhookHeaders,
		"email_to":        next.EmailTo,
		"smtp_host":       next.SMTPHost,
		"smtp_port":       next.SMTPPort,
		"smtp_username":   next.SMTPUsername,
		"smtp_password":   next.SMTPPassword,
		"smtp_from":       next.SMTPFrom,
		"enabled":         next.Enabled,
	}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update delivery"})
		return
	}
	_ = model.DB.Where("id = ? AND user_id = ?", delivery.ID, user.ID).First(&delivery).Error
	c.JSON(http.StatusOK, delivery)
}

func (api *advancedChatAPI) deleteDelivery(c *gin.Context) {
	if !advancedChatMessageDeliveryEnabled() {
		c.JSON(http.StatusForbidden, gin.H{"error": "Message delivery is disabled"})
		return
	}
	user, ok := currentAdvancedChatUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var taskCount int64
	if err := model.DB.Model(&AdvancedChatScheduledTask{}).Where("user_id = ? AND delivery_id = ?", user.ID, c.Param("id")).Count(&taskCount).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to check delivery usage"})
		return
	}
	if taskCount > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "Delivery is used by scheduled tasks"})
		return
	}
	if err := model.DB.Where("id = ? AND user_id = ?", c.Param("id"), user.ID).Delete(&AdvancedChatDelivery{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete delivery"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Delivery deleted"})
}

func advancedChatDeliveryFromInput(c *gin.Context, userID uint, input advancedChatDeliveryInput) (AdvancedChatDelivery, bool) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Delivery name is required"})
		return AdvancedChatDelivery{}, false
	}
	if len([]rune(name)) > 120 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Delivery name is too long"})
		return AdvancedChatDelivery{}, false
	}
	description := strings.TrimSpace(input.Description)
	if len([]rune(description)) > 2000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Delivery description is too long"})
		return AdvancedChatDelivery{}, false
	}
	method := normalizeAdvancedChatDeliveryMethod(input.Method)
	if method == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Delivery method is invalid"})
		return AdvancedChatDelivery{}, false
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	delivery := AdvancedChatDelivery{
		UserID:      userID,
		Name:        name,
		Description: description,
		Method:      method,
		Enabled:     enabled,
	}
	switch method {
	case advancedChatDeliveryMethodWebhook:
		endpoint := strings.TrimSpace(input.WebhookURL)
		if endpoint == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Webhook URL is required"})
			return AdvancedChatDelivery{}, false
		}
		if len(endpoint) > 2000 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Webhook URL is too long"})
			return AdvancedChatDelivery{}, false
		}
		if _, err := url.ParseRequestURI(endpoint); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Webhook URL is invalid"})
			return AdvancedChatDelivery{}, false
		}
		if err := ValidateConfiguredHTTPURL(endpoint); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Webhook URL is blocked by SSRF protection"})
			return AdvancedChatDelivery{}, false
		}
		headers, ok := normalizeAdvancedChatDeliveryHeaders(c, input.WebhookHeaders)
		if !ok {
			return AdvancedChatDelivery{}, false
		}
		delivery.WebhookURL = endpoint
		delivery.WebhookHeaders = headers
	case advancedChatDeliveryMethodEmail:
		emailTo := strings.TrimSpace(input.EmailTo)
		if emailTo == "" || !strings.Contains(emailTo, "@") || len(emailTo) > 320 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Email recipient is invalid"})
			return AdvancedChatDelivery{}, false
		}
		delivery.EmailTo = emailTo
		delivery.SMTPHost = strings.TrimSpace(input.SMTPHost)
		delivery.SMTPPort = strings.TrimSpace(input.SMTPPort)
		delivery.SMTPUsername = strings.TrimSpace(input.SMTPUsername)
		delivery.SMTPPassword = input.SMTPPassword
		delivery.SMTPFrom = strings.TrimSpace(input.SMTPFrom)
		if delivery.SMTPPort == "" {
			delivery.SMTPPort = "587"
		}
		if hasAdvancedChatDeliveryCustomSMTP(delivery) && !advancedChatDeliveryCustomSMTPConfigured(delivery) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Custom SMTP requires host and from address"})
			return AdvancedChatDelivery{}, false
		}
		if !advancedChatDeliverySystemSMTPEnabled() && !advancedChatDeliveryCustomSMTPConfigured(delivery) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Custom SMTP is required"})
			return AdvancedChatDelivery{}, false
		}
		delivery.WebhookHeaders = "{}"
	}
	return delivery, true
}

func normalizeAdvancedChatDeliveryMethod(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case advancedChatDeliveryMethodWebhook:
		return advancedChatDeliveryMethodWebhook
	case advancedChatDeliveryMethodEmail:
		return advancedChatDeliveryMethodEmail
	default:
		return ""
	}
}

func normalizeAdvancedChatDeliveryHeaders(c *gin.Context, raw string) (string, bool) {
	headers := parseAdvancedChatDeliveryHeaders(raw)
	if len(headers) > 20 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Too many webhook headers"})
		return "", false
	}
	for key, value := range headers {
		if len(key) > 100 || len(value) > 1000 || strings.ContainsAny(key, "\r\n:") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Webhook headers are invalid"})
			return "", false
		}
	}
	data, err := json.Marshal(headers)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to encode webhook headers"})
		return "", false
	}
	return string(data), true
}

func parseAdvancedChatDeliveryHeaders(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	headers := map[string]string{}
	if raw == "" {
		return headers
	}
	var object map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &object); err == nil {
		for key, value := range object {
			if text, ok := value.(string); ok && strings.TrimSpace(key) != "" {
				headers[strings.TrimSpace(key)] = strings.TrimSpace(text)
			}
		}
		return headers
	}
	for _, line := range strings.Split(raw, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(key) != "" {
			headers[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return headers
}

func advancedChatDeliveryTool(name string) ChatExecutorTool {
	return ChatExecutorTool{
		Name:        name,
		Description: "Deliver the final scheduled task result to the configured destination.",
		Schema: map[string]interface{}{
			"type":     "object",
			"required": []string{"title", "body"},
			"properties": map[string]interface{}{
				"title": map[string]interface{}{"type": "string", "description": "Short result title."},
				"body":  map[string]interface{}{"type": "string", "description": "Final result body to deliver."},
				"format": map[string]interface{}{
					"type":        "string",
					"description": "Optional body format.",
					"enum":        []string{"text", "markdown", "json"},
				},
			},
		},
	}
}

func deliverAdvancedChatResult(ctx context.Context, userID uint, delivery *AdvancedChatDelivery, args map[string]interface{}) (string, error) {
	if !advancedChatMessageDeliveryEnabled() {
		return "", errors.New("message delivery is disabled")
	}
	if delivery == nil {
		return "", errors.New("delivery is not configured")
	}
	if delivery.UserID != userID {
		return "", errors.New("delivery is not available")
	}
	if !delivery.Enabled {
		return "", errors.New("delivery is disabled")
	}
	title := strings.TrimSpace(stringArgument(args, "title"))
	body := strings.TrimSpace(stringArgument(args, "body"))
	format := strings.TrimSpace(stringArgument(args, "format"))
	if title == "" {
		title = "Scheduled task result"
	}
	if body == "" {
		return "", errors.New("delivery body is required")
	}
	switch delivery.Method {
	case advancedChatDeliveryMethodWebhook:
		return deliverAdvancedChatWebhook(ctx, delivery, title, body, format)
	case advancedChatDeliveryMethodEmail:
		return deliverAdvancedChatEmail(delivery, title, body)
	default:
		return "", errors.New("unsupported delivery method")
	}
}

func deliverAdvancedChatWebhook(ctx context.Context, delivery *AdvancedChatDelivery, title string, body string, format string) (string, error) {
	if err := ValidateConfiguredHTTPURL(delivery.WebhookURL); err != nil {
		return "", fmt.Errorf("webhook URL blocked: %w", err)
	}
	payload := map[string]interface{}{
		"delivery_id": delivery.ID,
		"delivery":    delivery.Name,
		"title":       title,
		"body":        body,
		"format":      format,
		"created_at":  time.Now().Format(time.RFC3339),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, delivery.WebhookURL, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range parseAdvancedChatDeliveryHeaders(delivery.WebhookHeaders) {
		req.Header.Set(key, value)
	}
	resp, err := advancedChatDeliveryHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
	}
	return "Result delivered to webhook", nil
}

func deliverAdvancedChatEmail(delivery *AdvancedChatDelivery, title string, body string) (string, error) {
	if err := sendAdvancedChatDeliveryMail(delivery, title, body); err != nil {
		return "", err
	}
	return "Result delivered by email", nil
}

func sendAdvancedChatDeliveryMail(delivery *AdvancedChatDelivery, subject string, body string) error {
	host, port, username, password, from := advancedChatSMTPSettingsForDelivery(delivery)
	if host == "" || from == "" {
		return errors.New("SMTP is not configured")
	}
	if port == "" {
		port = "587"
	}
	addr := host + ":" + port
	var auth smtp.Auth
	if username != "" {
		auth = smtp.PlainAuth("", username, password, host)
	}
	var message bytes.Buffer
	message.WriteString("From: " + from + "\r\n")
	message.WriteString("To: " + delivery.EmailTo + "\r\n")
	message.WriteString("Subject: " + sanitizeMailHeader(subject) + "\r\n")
	message.WriteString("MIME-Version: 1.0\r\n")
	message.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	message.WriteString("\r\n")
	message.WriteString(body)
	return smtp.SendMail(addr, auth, from, []string{delivery.EmailTo}, message.Bytes())
}

func advancedChatSMTPSettingsForDelivery(delivery *AdvancedChatDelivery) (host, port, username, password, from string) {
	if delivery != nil && advancedChatDeliveryCustomSMTPConfigured(*delivery) {
		host = strings.TrimSpace(delivery.SMTPHost)
		port = strings.TrimSpace(delivery.SMTPPort)
		username = strings.TrimSpace(delivery.SMTPUsername)
		password = delivery.SMTPPassword
		from = strings.TrimSpace(delivery.SMTPFrom)
		return
	}
	if !advancedChatDeliverySystemSMTPEnabled() {
		return "", "", "", "", ""
	}
	host = strings.TrimSpace(model.GetSystemSetting("smtp_host", ""))
	port = strings.TrimSpace(model.GetSystemSetting("smtp_port", "587"))
	username = strings.TrimSpace(model.GetSystemSetting("smtp_username", ""))
	password = model.GetSystemSetting("smtp_password", "")
	from = strings.TrimSpace(model.GetSystemSetting("smtp_from", ""))
	return
}

func hasAdvancedChatDeliveryCustomSMTP(delivery AdvancedChatDelivery) bool {
	return strings.TrimSpace(delivery.SMTPHost) != "" ||
		strings.TrimSpace(delivery.SMTPFrom) != "" ||
		strings.TrimSpace(delivery.SMTPUsername) != "" ||
		delivery.SMTPPassword != ""
}

func advancedChatDeliveryCustomSMTPConfigured(delivery AdvancedChatDelivery) bool {
	return strings.TrimSpace(delivery.SMTPHost) != "" && strings.TrimSpace(delivery.SMTPFrom) != ""
}

func sanitizeMailHeader(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.TrimSpace(value)
	if value == "" {
		return "Scheduled task result"
	}
	return value
}
