package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"golang.org/x/crypto/bcrypt"
)

const phoneCodePurposeRegistration = "registration"
const phoneCodePurposeBind = "bind"
const phoneCodePurposeLogin = "login"

// SMSLoginEnabled reports whether login via SMS verification code is switched
// on and usable.
func SMSLoginEnabled() bool {
	return PhoneAuthEnabled() && settingBool("sms_login_enabled", false)
}

// SMSBindingRequired reports whether users are required to bind a phone number.
func SMSBindingRequired() bool {
	return PhoneAuthEnabled() && settingBool("sms_binding_required", false)
}

type PhoneRegisterInput struct {
	Username     string
	Phone        string
	Password     string
	PhoneCode    string
	CaptchaToken string
	ReferralCode string
}

// SendPhoneRegistrationCode sends an SMS verification code for registering a
// new account with a phone number.
func (s *AuthService) SendPhoneRegistrationCode(phone string, captchaToken string) error {
	if required, err := s.InitialSetupRequired(); err != nil {
		return err
	} else if required {
		return ErrInitialSetupRequired
	}
	if !settingBool("password_registration_enabled", true) {
		return errors.New("password registration is disabled")
	}
	if !PhoneAuthEnabled() {
		return errors.New("phone authentication is disabled")
	}
	hCaptchaVerified := hCaptchaRequired()
	if err := verifyHCaptcha(captchaToken); err != nil {
		return err
	}

	normalized, err := NormalizePhone(phone)
	if err != nil {
		return err
	}
	var count int64
	if err := model.DB.Model(&model.User{}).Where("phone = ?", normalized).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return errors.New("phone number already exists")
	}

	return createAndSendPhoneCode(normalized, phoneCodePurposeRegistration, hCaptchaVerified)
}

// RegisterWithPhone creates a new account verified by an SMS code.
func (s *AuthService) RegisterWithPhone(input PhoneRegisterInput) (*model.User, string, error) {
	if required, err := s.InitialSetupRequired(); err != nil {
		return nil, "", err
	} else if required {
		return nil, "", ErrInitialSetupRequired
	}
	if !settingBool("password_registration_enabled", true) {
		return nil, "", errors.New("password registration is disabled")
	}
	if !PhoneAuthEnabled() {
		return nil, "", errors.New("phone authentication is disabled")
	}
	username := truncateUsername(strings.TrimSpace(input.Username))
	if username == "" {
		return nil, "", errors.New("username is required")
	}
	if len([]rune(username)) < 3 {
		return nil, "", errors.New("username is too short")
	}
	normalized, err := NormalizePhone(input.Phone)
	if err != nil {
		return nil, "", err
	}
	if len(input.Password) < 8 {
		return nil, "", errors.New("password must be at least 8 characters")
	}
	registrationCode, err := phoneVerificationCode(normalized, phoneCodePurposeRegistration, input.PhoneCode)
	if err != nil {
		return nil, "", err
	}
	if hCaptchaRequired() && !registrationCode.HCaptchaVerified {
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
	if err := model.DB.Model(&model.User{}).Where("phone = ?", normalized).Count(&count).Error; err != nil {
		return nil, "", err
	}
	if count > 0 {
		return nil, "", errors.New("phone number already exists")
	}
	// The users table requires a unique email; phone-registered accounts get a
	// deterministic placeholder until the user binds a real address.
	placeholderEmail := fmt.Sprintf("%s@phone.local", normalized)
	if err := model.DB.Model(&model.User{}).Where("email = ?", placeholderEmail).Count(&count).Error; err != nil {
		return nil, "", err
	}
	if count > 0 {
		return nil, "", errors.New("phone number already exists")
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

	user := model.User{
		Username:     username,
		Email:        placeholderEmail,
		Phone:        &normalized,
		PasswordHash: string(passwordHash),
		APIKey:       apiKeyRaw,
		GroupID:      defaultGroup.ID,
		ReferralCode: &userReferralCode,
		ReferrerID:   referrerID,
	}
	if err := model.DB.Create(&user).Error; err != nil {
		return nil, "", err
	}
	if err := model.DB.Where(&model.UserGroupMembership{UserID: user.ID, GroupID: defaultGroup.ID}).
		FirstOrCreate(&model.UserGroupMembership{UserID: user.ID, GroupID: defaultGroup.ID}).Error; err != nil {
		return nil, "", err
	}
	_ = markPhoneCodeUsed(normalized, phoneCodePurposeRegistration, input.PhoneCode)

	token, err := s.issueJWT(&user)
	if err != nil {
		return nil, "", err
	}
	return &user, token, nil
}

// SendPhoneLoginCode sends an SMS verification code for logging in with a
// registered phone number.
func (s *AuthService) SendPhoneLoginCode(phone string, captchaToken string) error {
	if required, err := s.InitialSetupRequired(); err != nil {
		return err
	} else if required {
		return ErrInitialSetupRequired
	}
	if !SMSLoginEnabled() {
		return errors.New("SMS login is disabled")
	}
	if err := verifyHCaptcha(captchaToken); err != nil {
		return err
	}
	normalized, err := NormalizePhone(phone)
	if err != nil {
		return err
	}
	var count int64
	if err := model.DB.Model(&model.User{}).Where("phone = ?", normalized).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return errors.New("phone number is not registered")
	}
	return createAndSendPhoneCode(normalized, phoneCodePurposeLogin, false)
}

// LoginWithPhoneCode signs a user in with a phone number and SMS code.
func (s *AuthService) LoginWithPhoneCode(phone string, code string) (*model.User, string, error) {
	if required, err := s.InitialSetupRequired(); err != nil {
		return nil, "", err
	} else if required {
		return nil, "", ErrInitialSetupRequired
	}
	if !SMSLoginEnabled() {
		return nil, "", errors.New("SMS login is disabled")
	}
	normalized, err := NormalizePhone(phone)
	if err != nil {
		return nil, "", err
	}
	if _, err := phoneVerificationCode(normalized, phoneCodePurposeLogin, code); err != nil {
		return nil, "", err
	}
	var user model.User
	if err := model.DB.Where("phone = ?", normalized).First(&user).Error; err != nil {
		return nil, "", errors.New("phone number is not registered")
	}
	if err := EnsureFirstAdmin(&user); err != nil {
		return nil, "", err
	}
	_ = markPhoneCodeUsed(normalized, phoneCodePurposeLogin, code)
	token, err := s.issueJWT(&user)
	if err != nil {
		return nil, "", err
	}
	return &user, token, nil
}

// SendPhoneBindCode sends an SMS verification code so an authenticated user
// can bind a phone number to their account.
func (s *AuthService) SendPhoneBindCode(userID uint, phone string) error {
	if !PhoneAuthEnabled() {
		return errors.New("phone authentication is disabled")
	}
	var user model.User
	if err := model.DB.First(&user, userID).Error; err != nil {
		return errors.New("user not found")
	}
	normalized, err := NormalizePhone(phone)
	if err != nil {
		return err
	}
	var count int64
	if err := model.DB.Model(&model.User{}).Where("phone = ? AND id <> ?", normalized, userID).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return errors.New("phone number is already bound to another account")
	}
	return createAndSendPhoneCode(normalized, phoneCodePurposeBind, false)
}

// BindPhone verifies the SMS code and binds the phone number to the user.
func (s *AuthService) BindPhone(userID uint, phone string, code string) error {
	if !PhoneAuthEnabled() {
		return errors.New("phone authentication is disabled")
	}
	var user model.User
	if err := model.DB.First(&user, userID).Error; err != nil {
		return errors.New("user not found")
	}
	normalized, err := NormalizePhone(phone)
	if err != nil {
		return err
	}
	if _, err := phoneVerificationCode(normalized, phoneCodePurposeBind, code); err != nil {
		return err
	}
	var count int64
	if err := model.DB.Model(&model.User{}).Where("phone = ? AND id <> ?", normalized, userID).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return errors.New("phone number is already bound to another account")
	}
	if err := model.DB.Model(&user).Update("phone", normalized).Error; err != nil {
		return err
	}
	_ = markPhoneCodeUsed(normalized, phoneCodePurposeBind, code)
	return nil
}

func createAndSendPhoneCode(phone string, purpose string, hCaptchaVerified bool) error {
	code, err := randomNumericCode(6)
	if err != nil {
		return err
	}
	// Only the newest code stays valid. Leaving earlier codes live means several
	// 6-digit codes are simultaneously accepted, multiplying the odds of a
	// successful guess.
	now := time.Now()
	if err := model.DB.Model(&model.PhoneVerificationCode{}).
		Where("phone = ? AND purpose = ? AND used_at IS NULL", phone, purpose).
		Update("used_at", now).Error; err != nil {
		return err
	}
	record := model.PhoneVerificationCode{
		Phone:            phone,
		CodeHash:         hashPhoneCode(phone, purpose, code),
		Purpose:          purpose,
		HCaptchaVerified: hCaptchaVerified,
		ExpiresAt:        time.Now().Add(10 * time.Minute),
	}
	if err := model.DB.Create(&record).Error; err != nil {
		return err
	}
	return sendSMSCode(phone, code)
}

func phoneVerificationCode(phone string, purpose string, code string) (*model.PhoneVerificationCode, error) {
	if strings.TrimSpace(code) == "" {
		return nil, errors.New("SMS verification code is required")
	}
	codeHash := hashPhoneCode(phone, purpose, code)
	now := time.Now()
	var records []model.PhoneVerificationCode
	if err := model.DB.
		Where("phone = ? AND purpose = ? AND used_at IS NULL AND expires_at > ?", phone, purpose, now).
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
	return nil, errors.New("invalid or expired SMS verification code")
}

func markPhoneCodeUsed(phone string, purpose string, code string) error {
	codeHash := hashPhoneCode(phone, purpose, code)
	now := time.Now()
	return model.DB.Model(&model.PhoneVerificationCode{}).
		Where("phone = ? AND purpose = ? AND code_hash = ? AND used_at IS NULL", phone, purpose, codeHash).
		Update("used_at", now).Error
}

func hashPhoneCode(phone string, purpose string, code string) string {
	sum := sha256.Sum256([]byte(phone + "|" + purpose + "|" + strings.TrimSpace(code)))
	return hex.EncodeToString(sum[:])
}
