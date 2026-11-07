package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/veloce-ailab/veloce/internal/model"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func newPasswordResetDB(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(
		&model.User{},
		&model.SystemSetting{},
		&model.EmailVerificationCode{},
		&model.PhoneVerificationCode{},
	); err != nil {
		t.Fatal(err)
	}
	previousDB := model.DB
	model.DB = database
	t.Cleanup(func() { model.DB = previousDB })
	return database
}

// enablePasswordRecovery configures both recovery channels: SMTP settings make
// email recovery available and a complete SMSBao config makes SMS recovery
// available.
func enablePasswordRecovery(t *testing.T) {
	t.Helper()
	settings := map[string]string{
		"password_login_enabled":      "true",
		"smtp_host":                   "smtp.example.com",
		"smtp_port":                   "587",
		"smtp_from":                   "noreply@example.com",
		"sms_enabled":                 "true",
		"sms_provider":                "smsbao",
		"sms_smsbao_username":         "sms-user",
		"sms_smsbao_password":         "sms-pass",
		"sms_smsbao_sign_name":        "Example",
		"registration_email_suffixes": "",
	}
	for key, value := range settings {
		if err := model.SetSystemSetting(key, value); err != nil {
			t.Fatalf("set %s: %v", key, err)
		}
	}
}

func passwordResetUserFixture(t *testing.T, database *gorm.DB, username string, email string, phone string) model.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("old-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	user := model.User{Username: username, Email: email, PasswordHash: string(hash)}
	if phone != "" {
		user.Phone = &phone
	}
	if err := database.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	return user
}

func insertEmailResetCode(t *testing.T, database *gorm.DB, email string, code string, expiresAt time.Time) {
	t.Helper()
	record := model.EmailVerificationCode{
		Email:     email,
		CodeHash:  hashEmailCode(email, emailCodePurposePasswordReset, code),
		Purpose:   emailCodePurposePasswordReset,
		ExpiresAt: expiresAt,
	}
	if err := database.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
}

func insertPhoneResetCode(t *testing.T, database *gorm.DB, phone string, code string, expiresAt time.Time) {
	t.Helper()
	record := model.PhoneVerificationCode{
		Phone:     phone,
		CodeHash:  hashPhoneCode(phone, phoneCodePurposePasswordReset, code),
		Purpose:   phoneCodePurposePasswordReset,
		ExpiresAt: expiresAt,
	}
	if err := database.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
}

func assertPassword(t *testing.T, database *gorm.DB, userID uint, password string) {
	t.Helper()
	var user model.User
	if err := database.First(&user, userID).Error; err != nil {
		t.Fatal(err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		t.Fatalf("password does not match %q: %v", password, err)
	}
}

func TestResetPasswordWithEmailCodeUpdatesPasswordAndConsumesCode(t *testing.T) {
	database := newPasswordResetDB(t, "file:password-reset-email-success?mode=memory&cache=shared")
	enablePasswordRecovery(t)
	user := passwordResetUserFixture(t, database, "reset-email", "reset-email@example.com", "")
	insertEmailResetCode(t, database, user.Email, "123456", time.Now().Add(5*time.Minute))

	auth := &AuthService{}
	resetUser, err := auth.ResetPasswordWithEmailCode(EmailPasswordResetInput{
		Email:       "Reset-Email@Example.com",
		Code:        "123456",
		NewPassword: "brand-new-password",
	})
	if err != nil {
		t.Fatalf("ResetPasswordWithEmailCode error = %v", err)
	}
	if resetUser == nil || resetUser.ID != user.ID {
		t.Fatalf("ResetPasswordWithEmailCode returned %+v, want user %d", resetUser, user.ID)
	}
	assertPassword(t, database, user.ID, "brand-new-password")

	var record model.EmailVerificationCode
	if err := database.Where("email = ? AND purpose = ?", user.Email, emailCodePurposePasswordReset).First(&record).Error; err != nil {
		t.Fatal(err)
	}
	if record.UsedAt == nil {
		t.Fatal("the reset code must be consumed after a successful reset")
	}
}

func TestResetPasswordWithEmailCodeRejectsBadCodes(t *testing.T) {
	database := newPasswordResetDB(t, "file:password-reset-email-bad-code?mode=memory&cache=shared")
	enablePasswordRecovery(t)
	user := passwordResetUserFixture(t, database, "reset-bad-code", "reset-bad-code@example.com", "")
	insertEmailResetCode(t, database, user.Email, "123456", time.Now().Add(5*time.Minute))
	insertEmailResetCode(t, database, user.Email, "654321", time.Now().Add(-time.Minute))

	auth := &AuthService{}
	if _, err := auth.ResetPasswordWithEmailCode(EmailPasswordResetInput{
		Email: user.Email, Code: "000000", NewPassword: "brand-new-password",
	}); err == nil {
		t.Fatal("wrong code must be rejected")
	}
	if _, err := auth.ResetPasswordWithEmailCode(EmailPasswordResetInput{
		Email: user.Email, Code: "654321", NewPassword: "brand-new-password",
	}); err == nil {
		t.Fatal("expired code must be rejected")
	}
	if _, err := auth.ResetPasswordWithEmailCode(EmailPasswordResetInput{
		Email: user.Email, Code: "123456", NewPassword: "short",
	}); err == nil {
		t.Fatal("short passwords must be rejected")
	}
	if _, err := auth.ResetPasswordWithEmailCode(EmailPasswordResetInput{
		Email: user.Email, Code: "", NewPassword: "brand-new-password",
	}); err == nil {
		t.Fatal("a missing code must be rejected")
	}
	assertPassword(t, database, user.ID, "old-password")
}

func TestResetPasswordWithEmailCodeIgnoresDisabledAccounts(t *testing.T) {
	database := newPasswordResetDB(t, "file:password-reset-email-disabled?mode=memory&cache=shared")
	enablePasswordRecovery(t)
	user := passwordResetUserFixture(t, database, "reset-disabled", "reset-disabled@example.com", "")
	if err := database.Model(&user).Update("is_disabled", true).Error; err != nil {
		t.Fatal(err)
	}
	insertEmailResetCode(t, database, user.Email, "123456", time.Now().Add(5*time.Minute))

	if err := (&AuthService{}).SendPasswordResetEmailCode(user.Email, ""); err != nil {
		t.Fatalf("SendPasswordResetEmailCode for a disabled account error = %v, want nil", err)
	}
	if _, err := (&AuthService{}).ResetPasswordWithEmailCode(EmailPasswordResetInput{
		Email: user.Email, Code: "123456", NewPassword: "brand-new-password",
	}); err == nil {
		t.Fatal("disabled accounts must not be able to reset their password")
	}
	assertPassword(t, database, user.ID, "old-password")
}

func TestSendPasswordResetEmailCodeDoesNotRevealUnknownAccounts(t *testing.T) {
	database := newPasswordResetDB(t, "file:password-reset-email-unknown?mode=memory&cache=shared")
	enablePasswordRecovery(t)

	if err := (&AuthService{}).SendPasswordResetEmailCode("nobody@example.com", ""); err != nil {
		t.Fatalf("unknown email error = %v, want nil", err)
	}
	var count int64
	if err := database.Model(&model.EmailVerificationCode{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("verification codes = %d, want 0 for an unknown address", count)
	}
	if err := (&AuthService{}).SendPasswordResetEmailCode("not-an-email", ""); err == nil {
		t.Fatal("invalid email addresses must be rejected")
	}
}

func TestResetPasswordWithPhoneCodeUpdatesPassword(t *testing.T) {
	database := newPasswordResetDB(t, "file:password-reset-phone-success?mode=memory&cache=shared")
	enablePasswordRecovery(t)
	user := passwordResetUserFixture(t, database, "reset-phone", "reset-phone@example.com", "13800138000")
	insertPhoneResetCode(t, database, "13800138000", "123456", time.Now().Add(5*time.Minute))

	resetUser, err := (&AuthService{}).ResetPasswordWithPhoneCode(PhonePasswordResetInput{
		Phone:       "+86 138-0013-8000",
		Code:        "123456",
		NewPassword: "brand-new-password",
	})
	if err != nil {
		t.Fatalf("ResetPasswordWithPhoneCode error = %v", err)
	}
	if resetUser == nil || resetUser.ID != user.ID {
		t.Fatalf("ResetPasswordWithPhoneCode returned %+v, want user %d", resetUser, user.ID)
	}
	assertPassword(t, database, user.ID, "brand-new-password")

	var record model.PhoneVerificationCode
	if err := database.Where("phone = ? AND purpose = ?", "13800138000", phoneCodePurposePasswordReset).First(&record).Error; err != nil {
		t.Fatal(err)
	}
	if record.UsedAt == nil {
		t.Fatal("the SMS reset code must be consumed after a successful reset")
	}
}

func TestSendPasswordResetPhoneCodeDoesNotRevealUnknownAccounts(t *testing.T) {
	database := newPasswordResetDB(t, "file:password-reset-phone-unknown?mode=memory&cache=shared")
	enablePasswordRecovery(t)

	if err := (&AuthService{}).SendPasswordResetPhoneCode("13900139000", ""); err != nil {
		t.Fatalf("unknown phone error = %v, want nil", err)
	}
	var count int64
	if err := database.Model(&model.PhoneVerificationCode{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("verification codes = %d, want 0 for an unbound phone", count)
	}
	if err := (&AuthService{}).SendPasswordResetPhoneCode("12345", ""); err == nil {
		t.Fatal("invalid phone numbers must be rejected")
	}
}

func TestPasswordResetRequiresConfiguredChannel(t *testing.T) {
	database := newPasswordResetDB(t, "file:password-reset-disabled?mode=memory&cache=shared")
	user := passwordResetUserFixture(t, database, "reset-off", "reset-off@example.com", "13700137000")
	insertEmailResetCode(t, database, user.Email, "123456", time.Now().Add(5*time.Minute))
	insertPhoneResetCode(t, database, "13700137000", "123456", time.Now().Add(5*time.Minute))

	if PasswordResetEmailEnabled() {
		t.Fatal("email recovery must be off without SMTP settings")
	}
	if PasswordResetPhoneEnabled() {
		t.Fatal("SMS recovery must be off without SMS settings")
	}
	auth := &AuthService{}
	if err := auth.SendPasswordResetEmailCode(user.Email, ""); err == nil {
		t.Fatal("sending an email reset code must fail when SMTP is not configured")
	}
	if err := auth.SendPasswordResetPhoneCode("13700137000", ""); err == nil {
		t.Fatal("sending an SMS reset code must fail when SMS is not configured")
	}
	if _, err := auth.ResetPasswordWithEmailCode(EmailPasswordResetInput{
		Email: user.Email, Code: "123456", NewPassword: "brand-new-password",
	}); err == nil {
		t.Fatal("email reset must fail when SMTP is not configured")
	}
	if _, err := auth.ResetPasswordWithPhoneCode(PhonePasswordResetInput{
		Phone: "13700137000", Code: "123456", NewPassword: "brand-new-password",
	}); err == nil {
		t.Fatal("SMS reset must fail when SMS is not configured")
	}
	assertPassword(t, database, user.ID, "old-password")
}
