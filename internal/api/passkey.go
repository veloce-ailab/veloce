package api

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/veloce-ailab/veloce/internal/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type PasskeyAPI struct {
	AuthService *service.AuthService
}

type passkeyCredentialResponse struct {
	ID         uint   `json:"id"`
	Name       string `json:"name"`
	SignCount  uint32 `json:"sign_count"`
	LastUsedAt any    `json:"last_used_at,omitempty"`
	CreatedAt  any    `json:"created_at"`
}

func (api *PasskeyAPI) BeginLogin(c *gin.Context) {
	if api.AuthService == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Authentication service is unavailable"})
		return
	}
	var input struct {
		Identifier        string `json:"identifier"`
		AgreementAccepted bool   `json:"agreement_accepted"`
	}
	_ = c.ShouldBindJSON(&input)
	if err := RequireAuthAgreementAccepted(input.AgreementAccepted); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	_, rpID, origin, ok := passkeyRequestContext(c)
	if !ok {
		return
	}
	options, err := api.AuthService.BeginPasskeyLogin(input.Identifier, rpID, origin)
	if err != nil {
		writePasskeyError(c, err)
		return
	}
	c.JSON(http.StatusOK, options)
}

func (api *PasskeyAPI) CompleteLogin(c *gin.Context) {
	if api.AuthService == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Authentication service is unavailable"})
		return
	}
	var input service.PasskeyAuthenticationCredential
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	user, token, err := api.AuthService.FinishPasskeyLogin(input)
	if err != nil {
		service.RecordAuditLog(service.AuditLogInput{
			LogType:    service.AuditLogTypeLogin,
			Action:     "passkey_login_failed",
			Resource:   "passkey_login",
			Method:     c.Request.Method,
			Path:       c.Request.URL.Path,
			StatusCode: http.StatusUnauthorized,
			IPAddress:  c.ClientIP(),
			UserAgent:  c.Request.UserAgent(),
			Message:    err.Error(),
		})
		writePasskeyError(c, err)
		return
	}
	service.RecordAuditLog(service.AuditLogInput{
		LogType:    service.AuditLogTypeLogin,
		Action:     "passkey_login_success",
		Resource:   "passkey_login",
		UserID:     &user.ID,
		Method:     c.Request.Method,
		Path:       c.Request.URL.Path,
		StatusCode: http.StatusOK,
		IPAddress:  c.ClientIP(),
		UserAgent:  c.Request.UserAgent(),
	})
	c.JSON(http.StatusOK, gin.H{"token": token, "user": user})
}

func (api *PasskeyAPI) List(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var credentials []model.PasskeyCredential
	if err := model.DB.Where("user_id = ?", user.ID).Order("created_at DESC").Find(&credentials).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list passkeys"})
		return
	}
	response := make([]passkeyCredentialResponse, 0, len(credentials))
	for _, credential := range credentials {
		response = append(response, passkeyResponse(credential))
	}
	c.JSON(http.StatusOK, response)
}

func (api *PasskeyAPI) BeginRegistration(c *gin.Context) {
	if api.AuthService == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Authentication service is unavailable"})
		return
	}
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	rpName, rpID, origin, ok := passkeyRequestContext(c)
	if !ok {
		return
	}
	options, err := api.AuthService.BeginPasskeyRegistration(user, rpName, rpID, origin)
	if err != nil {
		writePasskeyError(c, err)
		return
	}
	c.JSON(http.StatusOK, options)
}

func (api *PasskeyAPI) CompleteRegistration(c *gin.Context) {
	if api.AuthService == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Authentication service is unavailable"})
		return
	}
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var input service.PasskeyRegistrationInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	credential, err := api.AuthService.FinishPasskeyRegistration(user, input)
	if err != nil {
		writePasskeyError(c, err)
		return
	}
	c.JSON(http.StatusOK, passkeyResponse(credential))
}

func (api *PasskeyAPI) Delete(c *gin.Context) {
	user, ok := currentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	result := model.DB.Where("id = ? AND user_id = ?", c.Param("id"), user.ID).Delete(&model.PasskeyCredential{})
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete passkey"})
		return
	}
	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Passkey not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Passkey deleted"})
}

func passkeyResponse(credential model.PasskeyCredential) passkeyCredentialResponse {
	return passkeyCredentialResponse{
		ID:         credential.ID,
		Name:       credential.Name,
		SignCount:  credential.SignCount,
		LastUsedAt: credential.LastUsedAt,
		CreatedAt:  credential.CreatedAt,
	}
}

func passkeyRequestContext(c *gin.Context) (string, string, string, bool) {
	siteName := settingString("site_name", "flai")
	baseURL := strings.TrimSpace(settingString("base_url", ""))
	if baseURL != "" {
		parsed, err := url.Parse(baseURL)
		if err == nil && parsed.Scheme != "" && parsed.Host != "" {
			host := stripHostPort(parsed.Host)
			if host != "" {
				return siteName, host, parsed.Scheme + "://" + parsed.Host, true
			}
		}
	}

	for _, header := range []string{c.GetHeader("Origin"), c.GetHeader("Referer")} {
		origin, host := requestOriginAndHost(header)
		if origin != "" && host != "" {
			return siteName, host, origin, true
		}
	}

	host := c.Request.Host
	if forwardedHost := strings.TrimSpace(c.GetHeader("X-Forwarded-Host")); forwardedHost != "" {
		host = forwardedHost
	}
	rpID := stripHostPort(host)
	if rpID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Passkey relying party is not configured"})
		return "", "", "", false
	}

	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	if forwardedProto := strings.TrimSpace(c.GetHeader("X-Forwarded-Proto")); forwardedProto == "http" || forwardedProto == "https" {
		scheme = forwardedProto
	}
	return siteName, rpID, scheme + "://" + host, true
}

func requestOriginAndHost(raw string) (string, string) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", ""
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", ""
	}
	host := stripHostPort(parsed.Host)
	if host == "" {
		return "", ""
	}
	return parsed.Scheme + "://" + parsed.Host, host
}

func stripHostPort(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		return strings.Trim(parsedHost, "[]")
	}
	if strings.Count(host, ":") == 1 {
		return strings.Split(host, ":")[0]
	}
	return strings.Trim(host, "[]")
}

func writePasskeyError(c *gin.Context, err error) {
	if errors.Is(err, service.ErrInitialSetupRequired) {
		c.JSON(http.StatusConflict, gin.H{"error": "Initial setup is required"})
		return
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Passkey not found"})
		return
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
}
