package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/veloce-ailab/veloce/internal/model"
	"gorm.io/gorm"
)

func newSubscriptionPurchaseDB(t *testing.T, dsn string) *gorm.DB {
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

func purchasablePlanFixture(t *testing.T, database *gorm.DB, durationDays int) SubscriptionPlan {
	t.Helper()
	plan := SubscriptionPlan{
		Name:              "Monthly",
		ResetAmount:       decimal.RequireFromString("20"),
		ResetIntervalDays: 30,
		Price:             decimal.RequireFromString("10"),
		DurationDays:      durationDays,
		Purchasable:       true,
		Enabled:           true,
	}
	if err := database.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	return plan
}

func subscriptionRows(t *testing.T, database *gorm.DB, userID uint) []UserSubscription {
	t.Helper()
	var rows []UserSubscription
	if err := database.Where("user_id = ?", userID).Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

// Each paid renewal must advance the expiry by one full period. Extending from a
// value read before payment would let two purchases collapse into one period.
func TestGrantPurchasedSubscriptionExtendsFromCurrentExpiry(t *testing.T) {
	database := newSubscriptionPurchaseDB(t, "file:subscription-purchase-extend?mode=memory&cache=shared")
	plan := purchasablePlanFixture(t, database, 30)
	start := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	subscription := UserSubscription{
		UserID:      7,
		PlanID:      plan.ID,
		Balance:     plan.ResetAmount,
		ActiveUntil: &start,
		NextResetAt: time.Now().Add(30 * 24 * time.Hour),
	}
	if err := database.Create(&subscription).Error; err != nil {
		t.Fatal(err)
	}

	// A concurrent purchase commits its renewal first: the expiry on disk is no
	// longer the value a caller would have read before paying.
	concurrent := start.AddDate(0, 0, plan.DurationDays)
	if err := database.Model(&UserSubscription{}).Where("id = ?", subscription.ID).Update("active_until", concurrent).Error; err != nil {
		t.Fatal(err)
	}

	if err := grantPurchasedSubscription(database, subscription.UserID, plan); err != nil {
		t.Fatalf("grantPurchasedSubscription error = %v", err)
	}

	rows := subscriptionRows(t, database, subscription.UserID)
	if len(rows) != 1 {
		t.Fatalf("subscription rows = %d, want the existing row extended rather than a parallel quota", len(rows))
	}
	want := start.AddDate(0, 0, 2*plan.DurationDays)
	if rows[0].ActiveUntil == nil || !rows[0].ActiveUntil.Truncate(time.Second).Equal(want.Truncate(time.Second)) {
		t.Fatalf("active_until = %v, want %v (two paid periods)", rows[0].ActiveUntil, want)
	}
}

func TestGrantPurchasedSubscriptionCreatesWhenNoneActive(t *testing.T) {
	database := newSubscriptionPurchaseDB(t, "file:subscription-purchase-create?mode=memory&cache=shared")
	plan := purchasablePlanFixture(t, database, 30)

	if err := grantPurchasedSubscription(database, 11, plan); err != nil {
		t.Fatalf("grantPurchasedSubscription error = %v", err)
	}
	rows := subscriptionRows(t, database, 11)
	if len(rows) != 1 {
		t.Fatalf("subscription rows = %d, want 1", len(rows))
	}
	if rows[0].ActiveUntil == nil {
		t.Fatal("a timed plan must set an expiry")
	}
	if !rows[0].Balance.Equal(plan.ResetAmount) {
		t.Fatalf("balance = %s, want the plan reset amount %s", rows[0].Balance, plan.ResetAmount)
	}
}

// An expired subscription is not extended: the user starts a fresh window.
func TestGrantPurchasedSubscriptionIgnoresExpiredRow(t *testing.T) {
	database := newSubscriptionPurchaseDB(t, "file:subscription-purchase-expired?mode=memory&cache=shared")
	plan := purchasablePlanFixture(t, database, 30)
	expired := time.Now().Add(-48 * time.Hour)
	if err := database.Create(&UserSubscription{
		UserID:      13,
		PlanID:      plan.ID,
		Balance:     decimal.Zero,
		ActiveUntil: &expired,
		NextResetAt: time.Now().Add(30 * 24 * time.Hour),
	}).Error; err != nil {
		t.Fatal(err)
	}

	if err := grantPurchasedSubscription(database, 13, plan); err != nil {
		t.Fatalf("grantPurchasedSubscription error = %v", err)
	}
	rows := subscriptionRows(t, database, 13)
	if len(rows) != 2 {
		t.Fatalf("subscription rows = %d, want a new row alongside the expired one", len(rows))
	}
	if rows[1].ActiveUntil == nil || !rows[1].ActiveUntil.After(time.Now()) {
		t.Fatalf("new active_until = %v, want a future expiry", rows[1].ActiveUntil)
	}
}
