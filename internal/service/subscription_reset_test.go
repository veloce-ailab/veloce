package service

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func dueSubscriptionFixture(t *testing.T, database *gorm.DB, userID uint) (SubscriptionPlan, UserSubscription) {
	t.Helper()
	plan := SubscriptionPlan{
		Name:              "Resetting",
		ResetAmount:       decimal.RequireFromString("20"),
		ResetIntervalDays: 30,
		Enabled:           true,
	}
	if err := database.Create(&plan).Error; err != nil {
		t.Fatal(err)
	}
	subscription := UserSubscription{
		UserID:      userID,
		PlanID:      plan.ID,
		Balance:     decimal.Zero,
		NextResetAt: time.Now().Add(-time.Hour),
	}
	if err := database.Create(&subscription).Error; err != nil {
		t.Fatal(err)
	}
	subscription.Plan = plan
	return plan, subscription
}

func subscriptionBalance(t *testing.T, database *gorm.DB, id uint) decimal.Decimal {
	t.Helper()
	var row UserSubscription
	if err := database.First(&row, id).Error; err != nil {
		t.Fatal(err)
	}
	return row.Balance
}

func TestResetSubscriptionIfDueRefillsAndAdvancesWindow(t *testing.T) {
	database := newSubscriptionPurchaseDB(t, "file:subscription-reset-refill?mode=memory&cache=shared")
	plan, subscription := dueSubscriptionFixture(t, database, 21)

	if err := resetSubscriptionIfDue(database, subscription, time.Now()); err != nil {
		t.Fatalf("resetSubscriptionIfDue error = %v", err)
	}
	var refreshed UserSubscription
	if err := database.First(&refreshed, subscription.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !refreshed.Balance.Equal(plan.ResetAmount) {
		t.Fatalf("balance = %s, want the plan reset amount %s", refreshed.Balance, plan.ResetAmount)
	}
	if !refreshed.NextResetAt.After(time.Now()) {
		t.Fatalf("next_reset_at = %v, want a future time", refreshed.NextResetAt)
	}
	if refreshed.LastResetAt == nil {
		t.Fatal("last_reset_at must be recorded")
	}
}

// A request holding a pre-reset snapshot must not refill an already reset
// subscription: doing so erases usage that was charged against the new balance.
func TestResetSubscriptionIfDueIgnoresStaleSnapshot(t *testing.T) {
	database := newSubscriptionPurchaseDB(t, "file:subscription-reset-stale?mode=memory&cache=shared")
	_, subscription := dueSubscriptionFixture(t, database, 22)
	stale := subscription

	// Another request resets first and then usage is charged against the fresh
	// balance, leaving 15 of the 20 available.
	if err := resetSubscriptionIfDue(database, subscription, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&UserSubscription{}).Where("id = ?", subscription.ID).
		Update("balance", decimal.RequireFromString("15")).Error; err != nil {
		t.Fatal(err)
	}

	if err := resetSubscriptionIfDue(database, stale, time.Now()); err != nil {
		t.Fatalf("resetSubscriptionIfDue with a stale snapshot error = %v", err)
	}
	if balance := subscriptionBalance(t, database, subscription.ID); !balance.Equal(decimal.RequireFromString("15")) {
		t.Fatalf("balance = %s, want the charged 15 preserved", balance)
	}
}

func TestResetSubscriptionIfDueSkipsDisabledPlan(t *testing.T) {
	database := newSubscriptionPurchaseDB(t, "file:subscription-reset-disabled?mode=memory&cache=shared")
	_, subscription := dueSubscriptionFixture(t, database, 23)
	subscription.Plan.Enabled = false

	if err := resetSubscriptionIfDue(database, subscription, time.Now()); err != nil {
		t.Fatalf("resetSubscriptionIfDue error = %v", err)
	}
	if balance := subscriptionBalance(t, database, subscription.ID); !balance.IsZero() {
		t.Fatalf("balance = %s, want a disabled plan not to refill", balance)
	}
}
