package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const (
	emailCodePurposePasswordReset = "password_reset"
	phoneCodePurposePasswordReset = "password_reset"
)

const passwordResetCodeTTL = 10 * time.Minute

// PasswordResetEmailEnabled reports whether self-service password recovery by
// email is usable: password login must be enabled and SMTP must be configured.
func PasswordResetEmailEnabled() bool {
	return settingBool("password_login_enabled", true) && smtpConfigured()
}

// PasswordResetPhoneEnabled reports whether self-service password recovery by
// SMS is usable: password login must be enabled and an SMS provider configured.
func PasswordResetPhoneEnabled() bool {
	return settingBool("password_login_enabled", true) && PhoneAuthEnabled()
}

// EmailPasswordResetInput carries a verified email code and the replacement
// password.
type EmailPasswordResetInput struct {
	Email       string
	Code        string
	NewPassword string
}

// PhonePasswordResetInput carries a verified SMS code and the replacement
// password.
type PhonePasswordResetInput struct {
	Phone       string
	Code        string
	NewPassword string
}

// SendPasswordResetEmailCode emails a reset code to the account that owns the
// address. Unknown and disabled accounts intentionally return the same success
// result as a delivered code so the endpoint cannot be used to enumerate users.
func (s *AuthService) SendPasswordResetEmailCode(email string, captchaToken string) error {
	if !PasswordResetEmailEnabled() {
		return errors.New("email password recovery is not enabled")
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || !strings.Contains(email, "@") {
		return errors.New("valid email is required")
	}
	if err := verifyHCaptcha(captchaToken); err != nil {
		return err
	}

	var count int64
	if err := model.DB.Model(&model.User{}).
		Where("email = ? AND is_disabled = ?", email, false).
		Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return nil
	}

	code, err := randomNumericCode(6)
	if err != nil {
		return err
	}
	now := time.Now()
	// Only the newest reset code stays valid so several 6-digit codes are not
	// accepted at the same time.
	if err := model.DB.Model(&model.EmailVerificationCode{}).
		Where("email = ? AND purpose = ? AND used_at IS NULL", email, emailCodePurposePasswordReset).
		Update("used_at", now).Error; err != nil {
		return err
	}
	record := model.EmailVerificationCode{
		Email:     email,
		CodeHash:  hashEmailCode(email, emailCodePurposePasswordReset, code),
		Purpose:   emailCodePurposePasswordReset,
		ExpiresAt: now.Add(passwordResetCodeTTL),
	}
	if err := model.DB.Create(&record).Error; err != nil {
		return err
	}

	subject := "Reset your password"
	body := fmt.Sprintf("Your password reset code is %s. It expires in 10 minutes. If you did not request a password reset, you can safely ignore this email.", code)
	return sendMail(email, subject, body)
}

// SendPasswordResetPhoneCode texts a reset code to the account bound to the
// phone number. Unknown, unbound and disabled numbers behave like the email
// variant and report success without sending anything.
func (s *AuthService) SendPasswordResetPhoneCode(phone string, captchaToken string) error {
	if !PasswordResetPhoneEnabled() {
		return errors.New("SMS password recovery is not enabled")
	}
	normalized, err := NormalizePhone(phone)
	if err != nil {
		return err
	}
	if err := verifyHCaptcha(captchaToken); err != nil {
		return err
	}

	var count int64
	if err := model.DB.Model(&model.User{}).
		Where("phone = ? AND is_disabled = ?", normalized, false).
		Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	return createAndSendPhoneCode(normalized, phoneCodePurposePasswordReset, hCaptchaRequired())
}

// ResetPasswordWithEmailCode sets a new password after checking the emailed
// reset code.
func (s *AuthService) ResetPasswordWithEmailCode(input EmailPasswordResetInput) (*model.User, error) {
	if !PasswordResetEmailEnabled() {
		return nil, errors.New("email password recovery is not enabled")
	}
	if err := validateResetPassword(input.NewPassword); err != nil {
		return nil, err
	}
	email := strings.ToLower(strings.TrimSpace(input.Email))
	if email == "" || !strings.Contains(email, "@") {
		return nil, errors.New("valid email is required")
	}
	if strings.TrimSpace(input.Code) == "" {
		return nil, errors.New("verification code is required")
	}

	var user model.User
	err := model.DB.Where("email = ? AND is_disabled = ?", email, false).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errInvalidPasswordResetCode
	}
	if err != nil {
		return nil, err
	}
	if _, err := emailVerificationCode(email, emailCodePurposePasswordReset, input.Code); err != nil {
		return nil, err
	}

	if err := updatePassword(&user, input.NewPassword, true); err != nil {
		return nil, err
	}
	_ = markEmailCodeUsed(email, emailCodePurposePasswordReset, input.Code)
	return &user, nil
}

// ResetPasswordWithPhoneCode sets a new password after checking the SMS reset
// code sent to the bound phone number.
func (s *AuthService) ResetPasswordWithPhoneCode(input PhonePasswordResetInput) (*model.User, error) {
	if !PasswordResetPhoneEnabled() {
		return nil, errors.New("SMS password recovery is not enabled")
	}
	if err := validateResetPassword(input.NewPassword); err != nil {
		return nil, err
	}
	normalized, err := NormalizePhone(input.Phone)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(input.Code) == "" {
		return nil, errors.New("verification code is required")
	}

	var user model.User
	err = model.DB.Where("phone = ? AND is_disabled = ?", normalized, false).First(&user).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errInvalidPasswordResetCode
	}
	if err != nil {
		return nil, err
	}
	if _, err := phoneVerificationCode(normalized, phoneCodePurposePasswordReset, input.Code); err != nil {
		return nil, err
	}

	if err := updatePassword(&user, input.NewPassword, false); err != nil {
		return nil, err
	}
	_ = markPhoneCodeUsed(normalized, phoneCodePurposePasswordReset, input.Code)
	return &user, nil
}

var errInvalidPasswordResetCode = errors.New("invalid or expired verification code")

func validateResetPassword(password string) error {
	if len(password) < 8 {
		return errors.New("new password must be at least 8 characters")
	}
	return nil
}

func updatePassword(user *model.User, newPassword string, markEmailVerified bool) error {
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	updates := map[string]interface{}{"password_hash": string(passwordHash)}
	// Proving control of the address also verifies it.
	if markEmailVerified {
		updates["email_verified"] = true
	}
	return model.DB.Model(user).Updates(updates).Error
}
