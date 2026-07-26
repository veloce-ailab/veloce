package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/veloce-ailab/veloce/internal/model"
	"gorm.io/gorm"
)

func newSpendableCreditDB(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&model.User{}, &model.SystemSetting{}, &SubscriptionPlan{}, &UserSubscription{}); err != nil {
		t.Fatal(err)
	}
	previousDB := model.DB
	model.DB = database
	t.Cleanup(func() { model.DB = previousDB })
	return database
}

func TestHasSpendableCreditWithWalletBalance(t *testing.T) {
	newSpendableCreditDB(t, "file:spendable-credit-wallet?mode=memory&cache=shared")
	user := &model.User{ID: 1, Balance: decimal.RequireFromString("5")}
	if !HasSpendableCredit(user) {
		t.Fatal("a positive wallet balance must be spendable")
	}
}

// A paid subscription with quota left must keep working after the wallet hits
// zero, since usage is charged against the subscription first.
func TestHasSpendableCreditWithSubscriptionQuotaOnly(t *testing.T) {
	database := newSpendableCreditDB(t, "file:spendable-credit-subscription?mode=memory&cache=shared")
	user := model.User{Username: "subscriber", Email: "subscriber@example.com", APIKey: "sk-spendable-sub", Balance: decimal.Zero}
	if err := database.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if HasSpendableCredit(&user) {
		t.Fatal("an empty wallet with no subscription must not be spendable")
	}

	plan := SubscriptionPlan{Name: "Monthly", ResetAmount: decimal.RequireFromString("20"), ResetIntervalDays: 30, Enabled: true}
	if err := database.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	subscription := UserSubscription{
		UserID:      user.ID,
		PlanID:      plan.ID,
		Balance:     decimal.RequireFromString("12"),
		NextResetAt: time.Now().Add(15 * 24 * time.Hour),
	}
	if err := database.Create(&subscription).Error; err != nil {
		t.Fatal(err)
	}
	if !HasSpendableCredit(&user) {
		t.Fatal("subscription quota must count as spendable credit")
	}
}

// A subscription whose window has closed carries no credit even if its stored
// balance was never spent.
func TestHasSpendableCreditIgnoresExpiredSubscription(t *testing.T) {
	database := newSpendableCreditDB(t, "file:spendable-credit-expired?mode=memory&cache=shared")
	user := model.User{Username: "lapsed", Email: "lapsed@example.com", APIKey: "sk-spendable-lapsed", Balance: decimal.Zero}
	if err := database.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	plan := SubscriptionPlan{Name: "Lapsed", ResetAmount: decimal.RequireFromString("20"), ResetIntervalDays: 30, Enabled: true}
	if err := database.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	expired := time.Now().Add(-time.Hour)
	subscription := UserSubscription{
		UserID:      user.ID,
		PlanID:      plan.ID,
		Balance:     decimal.RequireFromString("12"),
		ActiveUntil: &expired,
		NextResetAt: time.Now().Add(15 * 24 * time.Hour),
	}
	if err := database.Create(&subscription).Error; err != nil {
		t.Fatal(err)
	}
	if HasSpendableCredit(&user) {
		t.Fatal("an expired subscription must not grant credit")
	}
}

// A subscription due for reset still has credit: the charge path resets it
// before deducting, so a zero stored balance must not block the request.
func TestHasSpendableCreditCountsSubscriptionDueForReset(t *testing.T) {
	database := newSpendableCreditDB(t, "file:spendable-credit-due-reset?mode=memory&cache=shared")
	user := model.User{Username: "due", Email: "due@example.com", APIKey: "sk-spendable-due", Balance: decimal.Zero}
	if err := database.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	plan := SubscriptionPlan{Name: "Due", ResetAmount: decimal.RequireFromString("20"), ResetIntervalDays: 30, Enabled: true}
	if err := database.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	subscription := UserSubscription{
		UserID:      user.ID,
		PlanID:      plan.ID,
		Balance:     decimal.Zero,
		NextResetAt: time.Now().Add(-time.Minute),
	}
	if err := database.Create(&subscription).Error; err != nil {
		t.Fatal(err)
	}
	if !HasSpendableCredit(&user) {
		t.Fatal("a subscription due for reset must count as spendable credit")
	}
}
