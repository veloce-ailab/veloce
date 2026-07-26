package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const emailCodePurposeRegistration = "registration"
const emailCodePurposePasswordChange = "password_change"

type PasswordLoginInput struct {
	Identifier   string
	Password     string
	CaptchaToken string
}

type PasswordRegisterInput struct {
	Username     string
	Email        string
	Password     string
	EmailCode    string
	CaptchaToken string
	ReferralCode string
}

type InitialSetupInput struct {
	SiteName string
	Username string
	Email    string
	Password string
}

type ChangePasswordInput struct {
	UserID          uint
	CurrentPassword string
	NewPassword     string
	EmailCode       string
}

var (
	ErrInitialSetupComplete = errors.New("initial setup is already complete")
	ErrInitialSetupRequired = errors.New("initial setup is required")
)

type InitialSetupValidationError struct {
	Message string
}

func (e *InitialSetupValidationError) Error() string {
	return e.Message
}

func IsInitialSetupValidationError(err error) bool {
	var target *InitialSetupValidationError
	return errors.As(err, &target)
}

func (s *AuthService) LoginWithPassword(input PasswordLoginInput) (*model.User, string, error) {
	if required, err := s.InitialSetupRequired(); err != nil {
		return nil, "", err
	} else if required {
		return nil, "", ErrInitialSetupRequired
	}
	if !settingBool("password_login_enabled", true) {
		return nil, "", errors.New("password login is disabled")
	}
	if err := verifyHCaptcha(input.CaptchaToken); err != nil {
		return nil, "", err
	}

	identifier := strings.TrimSpace(input.Identifier)
	if identifier == "" || input.Password == "" {
		return nil, "", errors.New("username/email and password are required")
	}

	var user model.User
	query := model.DB.Where("username = ? OR email = ?", identifier, strings.ToLower(identifier))
	if phone, phoneErr := NormalizePhone(identifier); phoneErr == nil {
		query = model.DB.Where("username = ? OR email = ? OR phone = ?", identifier, strings.ToLower(identifier), phone)
	}
	err := query.First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, "", errors.New("invalid username/email or password")
	}
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(user.PasswordHash) == "" {
		return nil, "", errors.New("password login is not enabled for this account")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(input.Password)); err != nil {
		return nil, "", errors.New("invalid username/email or password")
	}
	if err := EnsureFirstAdmin(&user); err != nil {
		return nil, "", err
	}

	token, err := s.issueJWT(&user)
	if err != nil {
		return nil, "", err
	}
	return &user, token, nil
}

func (s *AuthService) InitialSetupRequired() (bool, error) {
	hasAdmin, err := hasAdminUser()
	if err != nil {
		return false, err
	}
	return !hasAdmin, nil
}

func (s *AuthService) SetupInitialAdmin(input InitialSetupInput) (*model.User, string, error) {
	siteName := strings.TrimSpace(input.SiteName)
	username := truncateUsername(strings.TrimSpace(input.Username))
	email := strings.ToLower(strings.TrimSpace(input.Email))

	if siteName == "" {
		return nil, "", initialSetupValidationError("site name is required")
	}
	if len([]rune(siteName)) > 80 {
		return nil, "", initialSetupValidationError("site name is too long")
	}
	if username == "" {
		return nil, "", initialSetupValidationError("username is required")
	}
	if len([]rune(username)) < 3 {
		return nil, "", initialSetupValidationError("username is too short")
	}
	if email == "" || !strings.Contains(email, "@") {
		return nil, "", initialSetupValidationError("valid email is required")
	}
	if !registrationEmailAllowed(email) {
		return nil, "", errors.New("email domain is not allowed for registration")
	}
	if len([]rune(email)) > 100 {
		return nil, "", initialSetupValidationError("email is too long")
	}
	if len(input.Password) < 8 {
		return nil, "", initialSetupValidationError("password must be at least 8 characters")
	}

	defaultGroup, err := registrationGroupForEmail(email)
	if err != nil {
		return nil, "", err
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, "", err
	}
	apiKeyRaw, _, err := GenerateAPIKey()
	if err != nil {
		return nil, "", err
	}
	userReferralCode, err := model.NewUniqueReferralCode()
	if err != nil {
		return nil, "", err
	}

	var user model.User
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		var adminCount int64
		if err := tx.Model(&model.User{}).Where("is_admin = ?", true).Count(&adminCount).Error; err != nil {
			return fmt.Errorf("check admin users: %w", err)
		}
		if adminCount > 0 {
			return ErrInitialSetupComplete
		}

		var count int64
		if err := tx.Model(&model.User{}).Where("username = ?", username).Count(&count).Error; err != nil {
			return fmt.Errorf("check username: %w", err)
		}
		if count > 0 {
			return initialSetupValidationError("username already exists")
		}
		if err := tx.Model(&model.User{}).Where("email = ?", email).Count(&count).Error; err != nil {
			return fmt.Errorf("check email: %w", err)
		}
		if count > 0 {
			return initialSetupValidationError("email already exists")
		}

		user = model.User{
			Username:      username,
			Email:         email,
			PasswordHash:  string(passwordHash),
			EmailVerified: true,
			APIKey:        apiKeyRaw,
			GroupID:       defaultGroup.ID,
			ReferralCode:  &userReferralCode,
			IsAdmin:       true,
		}
		if err := tx.Create(&user).Error; err != nil {
			return fmt.Errorf("create initial admin: %w", err)
		}
		if err := tx.Where(&model.UserGroupMembership{UserID: user.ID, GroupID: defaultGroup.ID}).
			FirstOrCreate(&model.UserGroupMembership{UserID: user.ID, GroupID: defaultGroup.ID}).Error; err != nil {
			return fmt.Errorf("create default membership: %w", err)
		}

		settings := map[string]string{
			"site_name":                     siteName,
			"oidc_enabled":                  "false",
			"password_login_enabled":        "true",
			"password_registration_enabled": "true",
		}
		for key, value := range settings {
			setting := model.SystemSetting{Key: key}
			if err := tx.Where(&model.SystemSetting{Key: key}).
				Assign(model.SystemSetting{Value: value}).
				FirstOrCreate(&setting).Error; err != nil {
				return fmt.Errorf("set %s: %w", key, err)
			}
		}

		return nil
	})
	if err != nil {
		return nil, "", err
	}

	token, err := s.issueJWT(&user)
	if err != nil {
		return nil, "", err
	}
	return &user, token, nil
}

func registrationEmailAllowed(email string) bool {
	raw := model.GetSystemSetting("registration_email_suffixes", "")
	allowed := strings.FieldsFunc(strings.ToLower(raw), func(r rune) bool { return r == ',' || r == '\n' || r == ' ' || r == ';' })
	if len(allowed) == 0 {
		return true
	}
	for _, suffix := range allowed {
		suffix = strings.TrimPrefix(strings.TrimSpace(suffix), "@")
		if suffix != "" && strings.HasSuffix(email, "@"+suffix) {
			return true
		}
	}
	return false
}

func registrationGroupForEmail(email string) (model.Group, error) {
	fallback, err := model.EnsureDefaultGroup()
	if err != nil {
		return model.Group{}, err
	}
	var rules []struct {
		Suffix  string `json:"suffix"`
		GroupID uint   `json:"group_id"`
	}
	_ = json.Unmarshal([]byte(model.GetSystemSetting("registration_email_routing", "[]")), &rules)
	for _, rule := range rules {
		suffix := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(rule.Suffix)), "@")
		if suffix == "" || rule.GroupID == 0 || !strings.HasSuffix(email, "@"+suffix) {
			continue
		}
		var group model.Group
		if model.DB.First(&group, rule.GroupID).Error == nil {
			return group, nil
		}
	}
	return fallback, nil
}

func (s *AuthService) RegisterWithPassword(input PasswordRegisterInput) (*model.User, string, error) {
	if required, err := s.InitialSetupRequired(); err != nil {
		return nil, "", err
	} else if required {
		return nil, "", ErrInitialSetupRequired
	}
	if !settingBool("password_registration_enabled", true) {
		return nil, "", errors.New("password registration is disabled")
	}
	username := truncateUsername(strings.TrimSpace(input.Username))
	email := strings.ToLower(strings.TrimSpace(input.Email))
	if username == "" {
		return nil, "", errors.New("username is required")
	}
	if len([]rune(username)) < 3 {
		return nil, "", errors.New("username is too short")
	}
	if email == "" || !strings.Contains(email, "@") {
		return nil, "", errors.New("valid email is required")
	}
	if len(input.Password) < 8 {
		return nil, "", errors.New("password must be at least 8 characters")
	}
	var registrationCode *model.EmailVerificationCode
	if settingBool("email_verification_required", false) {
		var err error
		registrationCode, err = emailVerificationCode(email, emailCodePurposeRegistration, input.EmailCode)
		if err != nil {
			return nil, "", err
		}
	}
	if hCaptchaRequired() && (registrationCode == nil || !registrationCode.HCaptchaVerified) {
		if err := verifyHCaptcha(input.CaptchaToken); err != nil {
			return nil, "", err
		}
	}

	var count int64
	if err := model.DB.Model(&model.User{}).Where("username = ?", username).Count(&count).Error; err != nil {
		return nil, "", err
	}
	if count > 0 {
		return nil, "", errors.New("username already exists")
	}
	if err := model.DB.Model(&model.User{}).Where("email = ?", email).Count(&count).Error; err != nil {
		return nil, "", err
	}
	if count > 0 {
		return nil, "", errors.New("email already exists")
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, "", err
	}
	defaultGroup, err := model.EnsureDefaultGroup()
	if err != nil {
		return nil, "", err
	}
	apiKeyRaw, _, err := GenerateAPIKey()
	if err != nil {
		return nil, "", err
	}
	userReferralCode, err := model.NewUniqueReferralCode()
	if err != nil {
		return nil, "", err
	}
	referrerID, err := referrerIDFromReferralCode(input.ReferralCode)
	if err != nil {
		return nil, "", err
	}
	isAdmin, err := shouldBootstrapAdmin(email, "")
	if err != nil {
		return nil, "", err
	}

	user := model.User{
		Username:      username,
		Email:         email,
		PasswordHash:  string(passwordHash),
		EmailVerified: settingBool("email_verification_required", false),
		APIKey:        apiKeyRaw,
		GroupID:       defaultGroup.ID,
		ReferralCode:  &userReferralCode,
		ReferrerID:    referrerID,
		IsAdmin:       isAdmin,
	}
	if err := model.DB.Create(&user).Error; err != nil {
		return nil, "", err
	}
	if err := model.DB.Where(&model.UserGroupMembership{UserID: user.ID, GroupID: defaultGroup.ID}).
		FirstOrCreate(&model.UserGroupMembership{UserID: user.ID, GroupID: defaultGroup.ID}).Error; err != nil {
		return nil, "", err
	}
	if settingBool("email_verification_required", false) {
		_ = markEmailCodeUsed(email, emailCodePurposeRegistration, input.EmailCode)
	}

	token, err := s.issueJWT(&user)
	if err != nil {
		return nil, "", err
	}
	return &user, token, nil
}

func (s *AuthService) SendRegistrationEmailCode(email string, captchaToken string) error {
	if required, err := s.InitialSetupRequired(); err != nil {
		return err
	} else if required {
		return ErrInitialSetupRequired
	}
	if !settingBool("password_registration_enabled", true) {
		return errors.New("password registration is disabled")
	}
	hCaptchaVerified := hCaptchaRequired()
	if err := verifyHCaptcha(captchaToken); err != nil {
		return err
	}

	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || !strings.Contains(email, "@") {
		return errors.New("valid email is required")
	}
	var count int64
	if err := model.DB.Model(&model.User{}).Where("email = ?", email).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return errors.New("email already exists")
	}

	code, err := randomNumericCode(6)
	if err != nil {
		return err
	}
	record := model.EmailVerificationCode{
		Email:            email,
		CodeHash:         hashEmailCode(email, emailCodePurposeRegistration, code),
		Purpose:          emailCodePurposeRegistration,
		HCaptchaVerified: hCaptchaVerified,
		ExpiresAt:        time.Now().Add(10 * time.Minute),
	}
	if err := model.DB.Create(&record).Error; err != nil {
		return err
	}

	subject := "Your verification code"
	body := fmt.Sprintf("Your verification code is %s. It expires in 10 minutes.", code)
	return sendMail(email, subject, body)
}

func PasswordChangeMethod() string {
	if smtpConfigured() {
		return "email_code"
	}
	return "current_password"
}

func (s *AuthService) SendPasswordChangeEmailCode(userID uint) error {
	if !smtpConfigured() {
		return errors.New("SMTP is not configured")
	}

	var user model.User
	if err := model.DB.First(&user, userID).Error; err != nil {
		return errors.New("user not found")
	}
	email := strings.ToLower(strings.TrimSpace(user.Email))
	if email == "" || !strings.Contains(email, "@") {
		return errors.New("valid email is required")
	}

	code, err := randomNumericCode(6)
	if err != nil {
		return err
	}
	record := model.EmailVerificationCode{
		Email:     email,
		CodeHash:  hashEmailCode(email, emailCodePurposePasswordChange, code),
		Purpose:   emailCodePurposePasswordChange,
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}
	if err := model.DB.Create(&record).Error; err != nil {
		return err
	}

	subject := "Your password change verification code"
	body := fmt.Sprintf("Your password change verification code is %s. It expires in 10 minutes.", code)
	return sendMail(email, subject, body)
}

func (s *AuthService) ChangePassword(input ChangePasswordInput) error {
	if input.UserID == 0 {
		return errors.New("user is required")
	}
	if len(input.NewPassword) < 8 {
		return errors.New("new password must be at least 8 characters")
	}

	var user model.User
	if err := model.DB.First(&user, input.UserID).Error; err != nil {
		return errors.New("user not found")
	}

	if smtpConfigured() {
		email := strings.ToLower(strings.TrimSpace(user.Email))
		if email == "" || !strings.Contains(email, "@") {
			return errors.New("valid email is required")
		}
		if err := verifyEmailCode(email, emailCodePurposePasswordChange, input.EmailCode); err != nil {
			return err
		}
		passwordHash, err := bcrypt.GenerateFromPassword([]byte(input.NewPassword), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		if err := model.DB.Model(&user).Updates(map[string]interface{}{
			"password_hash":  string(passwordHash),
			"email_verified": true,
		}).Error; err != nil {
			return err
		}
		_ = markEmailCodeUsed(email, emailCodePurposePasswordChange, input.EmailCode)
		return nil
	}

	if strings.TrimSpace(user.PasswordHash) == "" {
		return errors.New("password login is not enabled for this account")
	}
	if input.CurrentPassword == "" {
		return errors.New("current password is required")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(input.CurrentPassword)); err != nil {
		return errors.New("current password is incorrect")
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(input.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return model.DB.Model(&user).Update("password_hash", string(passwordHash)).Error
}

func (s *AuthService) CreateOIDCBindRequest(ctx context.Context, userID uint, state string) (string, error) {
	if userID == 0 || strings.TrimSpace(state) == "" {
		return "", errors.New("invalid oidc bind request")
	}
	client, err := s.loadOIDCClient(ctx)
	if err != nil {
		return "", err
	}
	request := model.OIDCBindRequest{
		State:     state,
		UserID:    userID,
		ExpiresAt: time.Now().Add(10 * time.Minute),
	}
	if err := model.DB.Where("expires_at < ?", time.Now()).Delete(&model.OIDCBindRequest{}).Error; err != nil {
		return "", err
	}
	if err := model.DB.Create(&request).Error; err != nil {
		return "", err
	}
	return client.oauth2Config.AuthCodeURL(state), nil
}

func (s *AuthService) OIDCBindRequest(state string) (*model.OIDCBindRequest, bool, error) {
	state = strings.TrimSpace(state)
	if state == "" {
		return nil, false, nil
	}
	var request model.OIDCBindRequest
	err := model.DB.Where("state = ? AND expires_at > ?", state, time.Now()).First(&request).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &request, true, nil
}

func (s *AuthService) HandleOIDCBindCallback(ctx context.Context, code string, state string) (*model.User, string, error) {
	request, ok, err := s.OIDCBindRequest(state)
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, "", errors.New("OIDC bind request not found")
	}
	claims, err := s.verifyOIDCClaims(ctx, code)
	if err != nil {
		return nil, "", err
	}

	var existing model.User
	err = model.DB.Where("oidc_sub = ? AND id <> ?", claims.Subject, request.UserID).First(&existing).Error
	if err == nil {
		return nil, "", errors.New("OIDC account is already bound to another user")
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, "", err
	}

	var user model.User
	if err := model.DB.First(&user, request.UserID).Error; err != nil {
		return nil, "", err
	}
	updates := map[string]interface{}{
		"oidc_sub": claims.Subject,
	}
	if user.AvatarURL == "" && strings.TrimSpace(claims.Picture) != "" {
		updates["avatar_url"] = strings.TrimSpace(claims.Picture)
	}
	if strings.EqualFold(strings.TrimSpace(user.Email), strings.TrimSpace(claims.Email)) && strings.TrimSpace(claims.Email) != "" {
		updates["email_verified"] = true
	}
	if err := model.DB.Model(&user).Updates(updates).Error; err != nil {
		return nil, "", err
	}
	_ = model.DB.Delete(&model.OIDCBindRequest{}, "state = ?", state).Error
	if err := model.DB.First(&user, user.ID).Error; err != nil {
		return nil, "", err
	}
	token, err := s.issueJWT(&user)
	if err != nil {
		return nil, "", err
	}
	return &user, token, nil
}

func verifyHCaptcha(token string) error {
	if !settingBool("password_hcaptcha_enabled", false) {
		return nil
	}
	secret := strings.TrimSpace(model.GetSystemSetting("hcaptcha_secret", ""))
	if secret == "" {
		return errors.New("hCaptcha is not configured")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("hCaptcha token is required")
	}

	form := url.Values{}
	form.Set("secret", secret)
	form.Set("response", token)
	req, err := http.NewRequest(http.MethodPost, "https://hcaptcha.com/siteverify", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("verify hCaptcha: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var result struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return fmt.Errorf("parse hCaptcha response: %w", err)
	}
	if !result.Success {
		return errors.New("hCaptcha verification failed")
	}
	return nil
}

func hCaptchaRequired() bool {
	return settingBool("password_hcaptcha_enabled", false)
}

func verifyEmailCode(email string, purpose string, code string) error {
	_, err := emailVerificationCode(email, purpose, code)
	return err
}

func emailVerificationCode(email string, purpose string, code string) (*model.EmailVerificationCode, error) {
	codeHash := hashEmailCode(email, purpose, code)
	now := time.Now()
	var records []model.EmailVerificationCode
	if err := model.DB.
		Where("email = ? AND purpose = ? AND used_at IS NULL AND expires_at > ?", strings.ToLower(strings.TrimSpace(email)), purpose, now).
		Order("created_at DESC").
		Limit(5).
		Find(&records).Error; err != nil {
		return nil, err
	}
	for _, record := range records {
		if record.CodeHash == codeHash {
			return &record, nil
		}
	}
	return nil, errors.New("invalid or expired email verification code")
}

func initialSetupValidationError(message string) error {
	return &InitialSetupValidationError{Message: message}
}

func markEmailCodeUsed(email string, purpose string, code string) error {
	codeHash := hashEmailCode(email, purpose, code)
	now := time.Now()
	return model.DB.Model(&model.EmailVerificationCode{}).
		Where("email = ? AND purpose = ? AND code_hash = ? AND used_at IS NULL", strings.ToLower(strings.TrimSpace(email)), purpose, codeHash).
		Update("used_at", now).Error
}

func hashEmailCode(email string, purpose string, code string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email)) + "|" + purpose + "|" + strings.TrimSpace(code)))
	return hex.EncodeToString(sum[:])
}

func randomNumericCode(length int) (string, error) {
	if length <= 0 {
		length = 6
	}
	var builder strings.Builder
	for builder.Len() < length {
		value, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", err
		}
		builder.WriteString(strconv.FormatInt(value.Int64(), 10))
	}
	return builder.String(), nil
}

func sendMail(to string, subject string, body string) error {
	host, port, username, password, from := smtpSettings()
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
	message.WriteString("To: " + to + "\r\n")
	message.WriteString("Subject: " + subject + "\r\n")
	message.WriteString("MIME-Version: 1.0\r\n")
	message.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	message.WriteString("\r\n")
	message.WriteString(body)
	return smtp.SendMail(addr, auth, from, []string{to}, message.Bytes())
}

func smtpConfigured() bool {
	host, _, _, _, from := smtpSettings()
	return host != "" && from != ""
}

func smtpSettings() (host, port, username, password, from string) {
	host = strings.TrimSpace(model.GetSystemSetting("smtp_host", ""))
	port = strings.TrimSpace(model.GetSystemSetting("smtp_port", "587"))
	username = strings.TrimSpace(model.GetSystemSetting("smtp_username", ""))
	password = model.GetSystemSetting("smtp_password", "")
	from = strings.TrimSpace(model.GetSystemSetting("smtp_from", ""))
	return host, port, username, password, from
}
