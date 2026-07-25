package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/veloce-ailab/veloce/internal/config"
	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2"
	"gorm.io/gorm"
)

type AuthService struct {
	jwtSecret    []byte
	oidcMu       sync.Mutex
	oidcCacheKey string
	oidcClient   *oidcRuntimeClient
}

type oidcRuntimeConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

type OAuthProviderConfig struct {
	Key          string `json:"key"`
	Name         string `json:"name"`
	Enabled      bool   `json:"enabled"`
	Issuer       string `json:"issuer"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	AuthURL      string `json:"auth_url"`
	TokenURL     string `json:"token_url"`
	UserInfoURL  string `json:"userinfo_url"`
	Scope        string `json:"scope"`
	RedirectURL  string `json:"redirect_url"`
	SubjectKey   string `json:"subject_key"`
	EmailKey     string `json:"email_key"`
	NameKey      string `json:"name_key"`
	AvatarKey    string `json:"avatar_key"`
}

type oidcRuntimeClient struct {
	oauth2Config oauth2.Config
	verifier     *oidc.IDTokenVerifier
	provider     OAuthProviderConfig
	legacyOIDC   bool
}

type oidcClaims struct {
	Subject string `json:"sub"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Picture string `json:"picture"`
}

func NewAuthService() (*AuthService, error) {
	jwtSecret := []byte(config.JWTSecret)

	return &AuthService{
		jwtSecret: jwtSecret,
	}, nil
}

func (s *AuthService) GetAuthURL(ctx context.Context, state string) (string, error) {
	client, err := s.loadOIDCClient(ctx)
	if err != nil {
		return "", err
	}
	return client.oauth2Config.AuthCodeURL(state), nil
}

func (s *AuthService) GetOAuthAuthURL(ctx context.Context, providerKey string, state string) (string, error) {
	client, err := s.loadOAuthClient(ctx, providerKey)
	if err != nil {
		return "", err
	}
	return client.oauth2Config.AuthCodeURL(state), nil
}

func (s *AuthService) HandleCallback(ctx context.Context, code string, referralCode string) (*model.User, string, error) {
	return s.handleOAuthCallback(ctx, "oidc", code, referralCode)
}

func (s *AuthService) HandleOAuthCallback(ctx context.Context, providerKey string, code string, referralCode string) (*model.User, string, error) {
	return s.handleOAuthCallback(ctx, providerKey, code, referralCode)
}

func (s *AuthService) handleOAuthCallback(ctx context.Context, providerKey string, code string, referralCode string) (*model.User, string, error) {
	if code == "" {
		return nil, "", errors.New("missing authorization code")
	}
	claims, err := s.verifyOAuthClaims(ctx, providerKey, code)
	if err != nil {
		return nil, "", err
	}
	isBootstrapAdmin, err := shouldBootstrapAdmin(claims.Email, claims.Subject)
	if err != nil {
		return nil, "", err
	}
	email := firstNonEmpty(claims.Email, claims.Subject)

	// Find or create user
	var user model.User
	foundUser, err := findUserByOIDCSubjectOrEmail(&user, claims.Subject, email)
	if err != nil {
		return nil, "", err
	}
	if foundUser {
		if user.OIDCSub == nil || *user.OIDCSub != claims.Subject {
			oidcSub := claims.Subject
			if err := model.DB.Model(&user).Update("oidc_sub", oidcSub).Error; err != nil {
				return nil, "", err
			}
			user.OIDCSub = &oidcSub
		}
		if user.AvatarURL == "" && strings.TrimSpace(claims.Picture) != "" {
			avatarURL := strings.TrimSpace(claims.Picture)
			if err := model.DB.Model(&user).Update("avatar_url", avatarURL).Error; err != nil {
				return nil, "", err
			}
			user.AvatarURL = avatarURL
		}
	} else {
		defaultGroup, err := model.EnsureDefaultGroup()
		if err != nil {
			return nil, "", err
		}
		apiKeyRaw, _, err := GenerateAPIKey()
		if err != nil {
			return nil, "", err
		}
		username, err := uniqueUsername(firstNonEmpty(claims.Name, emailUsername(claims.Email), claims.Subject))
		if err != nil {
			return nil, "", err
		}

		userReferralCode, err := model.NewUniqueReferralCode()
		if err != nil {
			return nil, "", err
		}
		referrerID, err := referrerIDFromReferralCode(referralCode)
		if err != nil {
			return nil, "", err
		}

		// Create new user
		user = model.User{
			Username:      username,
			Email:         email,
			OIDCSub:       &claims.Subject,
			EmailVerified: strings.TrimSpace(claims.Email) != "",
			AvatarURL:     strings.TrimSpace(claims.Picture),
			APIKey:        apiKeyRaw,
			GroupID:       defaultGroup.ID,
			ReferralCode:  &userReferralCode,
			ReferrerID:    referrerID,
			IsAdmin:       isBootstrapAdmin,
		}
		if err := model.DB.Create(&user).Error; err != nil {
			return nil, "", err
		}
		if err := model.DB.Where(&model.UserGroupMembership{UserID: user.ID, GroupID: defaultGroup.ID}).
			FirstOrCreate(&model.UserGroupMembership{UserID: user.ID, GroupID: defaultGroup.ID}).Error; err != nil {
			return nil, "", err
		}
	}
	if isBootstrapAdmin && !user.IsAdmin {
		if err := model.DB.Model(&user).Update("is_admin", true).Error; err != nil {
			return nil, "", err
		}
		user.IsAdmin = true
	}
	if err := ensureUserReferralCode(&user); err != nil {
		return nil, "", err
	}

	tokenString, err := s.issueJWT(&user)
	if err != nil {
		return nil, "", err
	}

	return &user, tokenString, nil
}

func (s *AuthService) verifyOIDCClaims(ctx context.Context, code string) (oidcClaims, error) {
	return s.verifyOAuthClaims(ctx, "oidc", code)
}

func (s *AuthService) verifyOAuthClaims(ctx context.Context, providerKey string, code string) (oidcClaims, error) {
	client, err := s.loadOAuthClient(ctx, providerKey)
	if err != nil {
		return oidcClaims{}, err
	}

	oauth2Token, err := client.oauth2Config.Exchange(ctx, code)
	if err != nil {
		return oidcClaims{}, fmt.Errorf("failed to exchange token: %v", err)
	}

	var claims oidcClaims
	if client.verifier != nil {
		rawIDToken, ok := oauth2Token.Extra("id_token").(string)
		if ok {
			idToken, err := client.verifier.Verify(ctx, rawIDToken)
			if err != nil {
				return oidcClaims{}, fmt.Errorf("failed to verify ID token: %v", err)
			}
			if err := idToken.Claims(&claims); err != nil {
				return oidcClaims{}, fmt.Errorf("failed to parse claims: %v", err)
			}
		}
	}
	if client.provider.UserInfoURL != "" {
		userInfoClaims, err := fetchOAuthUserInfo(ctx, client, oauth2Token.AccessToken)
		if err != nil {
			return oidcClaims{}, err
		}
		claims = mergeOAuthClaims(claims, userInfoClaims)
	}
	if claims.Subject == "" {
		return oidcClaims{}, errors.New("OAuth subject is missing")
	}
	if !client.legacyOIDC {
		claims.Subject = oauthStoredSubject(client.provider.Key, claims.Subject)
	}
	claims.Email = strings.TrimSpace(claims.Email)
	claims.Name = strings.TrimSpace(claims.Name)
	claims.Picture = strings.TrimSpace(claims.Picture)
	return claims, nil
}

func (s *AuthService) issueJWT(user *model.User) (string, error) {
	if user == nil || user.ID == 0 {
		return "", errors.New("user is required")
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"id":       user.ID,
		"is_admin": user.IsAdmin,
		"exp":      time.Now().Add(time.Hour * 24 * 7).Unix(),
	})
	return token.SignedString(s.jwtSecret)
}

func referrerIDFromReferralCode(code string) (*uint, error) {
	if !settingBool("referral_enabled", false) {
		return nil, nil
	}
	code = model.NormalizeReferralCode(code)
	if code == "" {
		return nil, nil
	}
	var referrer model.User
	err := model.DB.Where("referral_code = ?", code).First(&referrer).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &referrer.ID, nil
}

func ensureUserReferralCode(user *model.User) error {
	if user == nil {
		return nil
	}
	if user.ReferralCode != nil && strings.TrimSpace(*user.ReferralCode) != "" {
		return nil
	}
	code, err := model.NewUniqueReferralCode()
	if err != nil {
		return err
	}
	if err := model.DB.Model(user).Update("referral_code", code).Error; err != nil {
		return err
	}
	user.ReferralCode = &code
	return nil
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

func (s *AuthService) loadOIDCClient(ctx context.Context) (*oidcRuntimeClient, error) {
	return s.loadOAuthClient(ctx, "oidc")
}

func (s *AuthService) loadOAuthClient(ctx context.Context, providerKey string) (*oidcRuntimeClient, error) {
	if !settingBool("oidc_enabled", false) {
		return nil, errors.New("OAuth login is disabled")
	}
	provider, legacyOIDC := oauthProviderConfig(providerKey)
	if provider.Key == "" {
		return nil, errors.New("OAuth provider is not configured")
	}
	if !provider.Enabled {
		return nil, errors.New("OAuth provider is disabled")
	}
	if provider.ClientID == "" || provider.ClientSecret == "" {
		return nil, errors.New("OAuth provider client is not configured")
	}
	if provider.RedirectURL == "" {
		return nil, errors.New("OAuth provider redirect URL is not configured")
	}

	cacheKey := oauthCacheKey(provider, legacyOIDC)
	s.oidcMu.Lock()
	if s.oidcClient != nil && s.oidcCacheKey == cacheKey {
		client := s.oidcClient
		s.oidcMu.Unlock()
		return client, nil
	}
	s.oidcMu.Unlock()

	var verifier *oidc.IDTokenVerifier
	endpoint := oauth2.Endpoint{
		AuthURL:  provider.AuthURL,
		TokenURL: provider.TokenURL,
	}
	if provider.Issuer != "" && (provider.AuthURL == "" || provider.TokenURL == "") {
		oidcProvider, err := oidc.NewProvider(ctx, provider.Issuer)
		if err != nil {
			if config.IsDevelopmentLike(config.Environment) {
				log.Printf("OIDC provider initialization failed; dashboard login is disabled until configuration or provider availability is fixed: %v", err)
			}
			return nil, fmt.Errorf("initialize OIDC provider %q: %w", provider.Issuer, err)
		}
		verifier = oidcProvider.Verifier(&oidc.Config{ClientID: provider.ClientID})
		endpoint = oidcProvider.Endpoint()
	}
	if endpoint.AuthURL == "" || endpoint.TokenURL == "" {
		return nil, errors.New("OAuth provider endpoints are not configured")
	}

	oauthConfig := oauth2.Config{
		ClientID:     provider.ClientID,
		ClientSecret: provider.ClientSecret,
		Endpoint:     endpoint,
		RedirectURL:  provider.RedirectURL,
		Scopes:       oauthScopes(provider.Scope, provider.Issuer != ""),
	}
	client := &oidcRuntimeClient{
		oauth2Config: oauthConfig,
		verifier:     verifier,
		provider:     provider,
		legacyOIDC:   legacyOIDC,
	}

	s.oidcMu.Lock()
	s.oidcCacheKey = cacheKey
	s.oidcClient = client
	s.oidcMu.Unlock()

	return client, nil
}

func fetchOAuthUserInfo(ctx context.Context, client *oidcRuntimeClient, accessToken string) (oidcClaims, error) {
	if strings.TrimSpace(accessToken) == "" {
		return oidcClaims{}, errors.New("OAuth access token is missing")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, client.provider.UserInfoURL, nil)
	if err != nil {
		return oidcClaims{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return oidcClaims{}, fmt.Errorf("failed to fetch OAuth user info: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return oidcClaims{}, fmt.Errorf("OAuth user info returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return oidcClaims{}, err
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return oidcClaims{}, fmt.Errorf("failed to parse OAuth user info: %w", err)
	}
	return oidcClaims{
		Subject: stringFromJSONPath(payload, firstNonEmpty(client.provider.SubjectKey, "sub", "id")),
		Email:   stringFromJSONPath(payload, firstNonEmpty(client.provider.EmailKey, "email")),
		Name:    stringFromJSONPath(payload, firstNonEmpty(client.provider.NameKey, "name", "login", "username")),
		Picture: stringFromJSONPath(payload, firstNonEmpty(client.provider.AvatarKey, "picture", "avatar_url")),
	}, nil
}

func OAuthProviderConfigs() []OAuthProviderConfig {
	configs := parseOAuthProviders(model.GetSystemSetting("oauth_providers", "[]"))
	legacy := legacyOIDCProviderConfig()
	if legacy.Key != "" {
		found := false
		for _, provider := range configs {
			if provider.Key == legacy.Key {
				found = true
				break
			}
		}
		if !found {
			configs = append([]OAuthProviderConfig{legacy}, configs...)
		}
	}
	return configs
}

func EnabledOAuthProviderConfigs() []OAuthProviderConfig {
	configs := OAuthProviderConfigs()
	enabled := make([]OAuthProviderConfig, 0, len(configs))
	for _, provider := range configs {
		if provider.Enabled && provider.Key != "" {
			enabled = append(enabled, provider)
		}
	}
	return enabled
}

func NormalizeOAuthProvidersJSON(raw string) (string, error) {
	var configs []OAuthProviderConfig
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &configs); err != nil {
			return "", errors.New("OAuth providers must be valid JSON")
		}
	}
	seen := map[string]bool{}
	normalized := make([]OAuthProviderConfig, 0, len(configs))
	for _, provider := range configs {
		provider = normalizeOAuthProvider(provider)
		if provider.Key == "" && provider.Name == "" && provider.ClientID == "" && provider.AuthURL == "" && provider.TokenURL == "" {
			continue
		}
		if provider.Key == "" {
			return "", errors.New("OAuth provider key is required")
		}
		if seen[provider.Key] {
			return "", errors.New("OAuth provider key must be unique")
		}
		seen[provider.Key] = true
		normalized = append(normalized, provider)
	}
	body, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func OAuthCallbackPath(providerKey string) string {
	key := normalizeOAuthProviderKey(providerKey)
	if key == "" {
		return ""
	}
	return "/auth/oauth/" + key + "/callback"
}

func OAuthCallbackURL(provider OAuthProviderConfig) string {
	if strings.TrimSpace(provider.RedirectURL) != "" {
		return strings.TrimSpace(provider.RedirectURL)
	}
	baseURL := strings.TrimRight(strings.TrimSpace(model.GetSystemSetting("base_url", "")), "/")
	if baseURL == "" {
		return ""
	}
	return baseURL + OAuthCallbackPath(provider.Key)
}

func oauthProviderConfig(providerKey string) (OAuthProviderConfig, bool) {
	key := normalizeOAuthProviderKey(providerKey)
	if key == "" {
		key = "oidc"
	}
	for _, provider := range parseOAuthProviders(model.GetSystemSetting("oauth_providers", "[]")) {
		provider = normalizeOAuthProvider(provider)
		if provider.Key == key {
			provider.RedirectURL = OAuthCallbackURL(provider)
			return provider, false
		}
	}
	if key == "oidc" {
		provider := legacyOIDCProviderConfig()
		provider.RedirectURL = OAuthCallbackURL(provider)
		return provider, true
	}
	return OAuthProviderConfig{}, false
}

func legacyOIDCProviderConfig() OAuthProviderConfig {
	cfg := currentOIDCConfig()
	if cfg.Issuer == "" && cfg.ClientID == "" && cfg.ClientSecret == "" && cfg.RedirectURL == "" {
		return OAuthProviderConfig{}
	}
	return OAuthProviderConfig{
		Key:          "oidc",
		Name:         "OIDC",
		Enabled:      true,
		Issuer:       cfg.Issuer,
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  cfg.RedirectURL,
		SubjectKey:   "sub",
		EmailKey:     "email",
		NameKey:      "name",
		AvatarKey:    "picture",
	}
}

func parseOAuthProviders(raw string) []OAuthProviderConfig {
	var providers []OAuthProviderConfig
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &providers); err != nil {
		return nil
	}
	for index := range providers {
		providers[index] = normalizeOAuthProvider(providers[index])
	}
	return providers
}

func normalizeOAuthProvider(provider OAuthProviderConfig) OAuthProviderConfig {
	provider.Key = normalizeOAuthProviderKey(provider.Key)
	provider.Name = strings.TrimSpace(provider.Name)
	provider.Issuer = strings.TrimSpace(provider.Issuer)
	provider.ClientID = strings.TrimSpace(provider.ClientID)
	provider.ClientSecret = strings.TrimSpace(provider.ClientSecret)
	provider.AuthURL = strings.TrimSpace(provider.AuthURL)
	provider.TokenURL = strings.TrimSpace(provider.TokenURL)
	provider.UserInfoURL = strings.TrimSpace(provider.UserInfoURL)
	provider.Scope = strings.TrimSpace(provider.Scope)
	provider.RedirectURL = strings.TrimSpace(provider.RedirectURL)
	provider.SubjectKey = firstNonEmpty(strings.TrimSpace(provider.SubjectKey), "sub")
	provider.EmailKey = firstNonEmpty(strings.TrimSpace(provider.EmailKey), "email")
	provider.NameKey = firstNonEmpty(strings.TrimSpace(provider.NameKey), "name")
	provider.AvatarKey = firstNonEmpty(strings.TrimSpace(provider.AvatarKey), "picture")
	if provider.Name == "" && provider.Key != "" {
		provider.Name = provider.Key
	}
	return provider
}

func normalizeOAuthProviderKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastDash := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if r == '-' || r == '_' || r == ' ' {
			if !lastDash && builder.Len() > 0 {
				builder.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(builder.String(), "-")
}

func oauthScopes(raw string, oidcProvider bool) []string {
	fields := strings.Fields(strings.ReplaceAll(raw, ",", " "))
	if len(fields) == 0 && oidcProvider {
		return []string{oidc.ScopeOpenID, "profile", "email"}
	}
	return fields
}

func oauthCacheKey(provider OAuthProviderConfig, legacyOIDC bool) string {
	values := []string{
		provider.Key,
		provider.Issuer,
		provider.ClientID,
		provider.ClientSecret,
		provider.AuthURL,
		provider.TokenURL,
		provider.UserInfoURL,
		provider.Scope,
		provider.RedirectURL,
		strconv.FormatBool(legacyOIDC),
	}
	return strings.Join(values, "\x00")
}

func oauthStoredSubject(providerKey, subject string) string {
	return "oauth:" + normalizeOAuthProviderKey(providerKey) + ":" + strings.TrimSpace(subject)
}

func mergeOAuthClaims(base, next oidcClaims) oidcClaims {
	if next.Subject != "" {
		base.Subject = next.Subject
	}
	if next.Email != "" {
		base.Email = next.Email
	}
	if next.Name != "" {
		base.Name = next.Name
	}
	if next.Picture != "" {
		base.Picture = next.Picture
	}
	return base
}

func stringFromJSONPath(payload map[string]interface{}, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	var current interface{} = payload
	for _, part := range strings.Split(path, ".") {
		part = strings.TrimSpace(part)
		if part == "" {
			return ""
		}
		object, ok := current.(map[string]interface{})
		if !ok {
			return ""
		}
		current = object[part]
	}
	switch value := current.(type) {
	case string:
		return strings.TrimSpace(value)
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(value)
	default:
		return ""
	}
}

func currentOIDCConfig() oidcRuntimeConfig {
	return oidcRuntimeConfig{
		Issuer:       model.GetSystemSetting("oidc_issuer", config.OIDCIssuer),
		ClientID:     model.GetSystemSetting("oidc_client_id", config.OIDCClientID),
		ClientSecret: model.GetSystemSetting("oidc_client_secret", config.OIDCSecret),
		RedirectURL:  model.GetSystemSetting("oidc_redirect_url", config.OIDCRedirect),
	}
}

func (cfg oidcRuntimeConfig) cacheKey() string {
	return strings.Join([]string{cfg.Issuer, cfg.ClientID, cfg.ClientSecret, cfg.RedirectURL}, "\x00")
}

func findUserByOIDCSubjectOrEmail(user *model.User, oidcSub, email string) (bool, error) {
	result := model.DB.Where("oidc_sub = ?", oidcSub).First(user)
	if result.Error == nil {
		return true, nil
	}
	if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return false, result.Error
	}

	email = strings.TrimSpace(email)
	if email == "" {
		return false, nil
	}
	result = model.DB.Where("email = ?", email).First(user)
	if result.Error == nil {
		return true, nil
	}
	if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return false, result.Error
	}
	return false, nil
}

func (s *AuthService) VerifyJWT(tokenString string) (uint, bool, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return s.jwtSecret, nil
	})

	if err != nil || !token.Valid {
		return 0, false, err
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return 0, false, errors.New("invalid claims")
	}

	exp, err := claims.GetExpirationTime()
	if err != nil || exp == nil {
		return 0, false, errors.New("missing or invalid expiration")
	}

	userID, err := claimUint(claims, "id")
	if err != nil {
		return 0, false, err
	}
	isAdmin, err := claimBool(claims, "is_admin")
	if err != nil {
		return 0, false, err
	}

	return userID, isAdmin, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func uniqueUsername(raw string) (string, error) {
	base := truncateUsername(strings.TrimSpace(raw))
	if base == "" {
		base = "user"
	}

	for i := 0; i < 1000; i++ {
		candidate := base
		if i > 0 {
			suffix := fmt.Sprintf("-%d", i+1)
			candidate = truncateUsernameForSuffix(base, suffix) + suffix
		}

		var count int64
		if err := model.DB.Model(&model.User{}).Where("username = ?", candidate).Count(&count).Error; err != nil {
			return "", err
		}
		if count == 0 {
			return candidate, nil
		}
	}

	return "", errors.New("failed to generate unique username")
}

func emailUsername(email string) string {
	email = strings.TrimSpace(email)
	if at := strings.Index(email, "@"); at > 0 {
		return email[:at]
	}
	return email
}

func truncateUsername(value string) string {
	runes := []rune(value)
	if len(runes) <= 100 {
		return string(runes)
	}
	return string(runes[:100])
}

func truncateUsernameForSuffix(value, suffix string) string {
	limit := 100 - len([]rune(suffix))
	if limit < 1 {
		limit = 1
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit])
}

func shouldBootstrapAdmin(email, oidcSub string) (bool, error) {
	if config.IsBootstrapAdmin(email, oidcSub) {
		return true, nil
	}

	hasAdmin, err := hasAdminUser()
	if err != nil {
		return false, err
	}
	return !hasAdmin, nil
}

func EnsureFirstAdmin(user *model.User) error {
	if user == nil || user.IsAdmin {
		return nil
	}

	hasAdmin, err := hasAdminUser()
	if err != nil {
		return err
	}
	if hasAdmin {
		return nil
	}

	if err := model.DB.Model(user).Update("is_admin", true).Error; err != nil {
		return err
	}
	user.IsAdmin = true
	return nil
}

func hasAdminUser() (bool, error) {
	var adminCount int64
	if err := model.DB.Model(&model.User{}).Where("is_admin = ?", true).Count(&adminCount).Error; err != nil {
		return false, err
	}
	return adminCount > 0, nil
}

func claimUint(claims jwt.MapClaims, key string) (uint, error) {
	switch value := claims[key].(type) {
	case float64:
		if value <= 0 || math.Trunc(value) != value {
			return 0, fmt.Errorf("invalid %s claim", key)
		}
		return uint(value), nil
	case string:
		parsed, err := strconv.ParseUint(value, 10, 0)
		if err != nil || parsed == 0 {
			return 0, fmt.Errorf("invalid %s claim", key)
		}
		return uint(parsed), nil
	default:
		return 0, fmt.Errorf("missing %s claim", key)
	}
}

func claimBool(claims jwt.MapClaims, key string) (bool, error) {
	switch value := claims[key].(type) {
	case bool:
		return value, nil
	case string:
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return false, fmt.Errorf("invalid %s claim", key)
		}
		return parsed, nil
	default:
		return false, fmt.Errorf("missing %s claim", key)
	}
}
