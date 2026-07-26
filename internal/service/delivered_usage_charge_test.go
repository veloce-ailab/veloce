package service

import (
	"errors"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/veloce-ailab/veloce/internal/model"
	"gorm.io/gorm"
)

func newDeliveredChargeDB(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&model.User{}, &model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	previousDB := model.DB
	model.DB = database
	t.Cleanup(func() { model.DB = previousDB })
	return database
}

func deliveredChargeUserBalance(t *testing.T, database *gorm.DB, userID uint) decimal.Decimal {
	t.Helper()
	var user model.User
	if err := database.Select("balance").First(&user, userID).Error; err != nil {
		t.Fatal(err)
	}
	return user.Balance
}

// A response that was already streamed cannot be refused, so a shortfall must
// consume the remaining balance; otherwise the next request passes the
// "balance > 0" precheck again and the usage is never paid for.
func TestApplyDeliveredUsageChargeDrainsShortfall(t *testing.T) {
	database := newDeliveredChargeDB(t, "file:delivered-usage-charge-drain?mode=memory&cache=shared")
	dust := decimal.RequireFromString("0.000001")
	user := model.User{Username: "dust", Email: "dust@example.com", APIKey: "sk-delivered-drain", Balance: dust}
	if err := database.Create(&user).Error; err != nil {
		t.Fatal(err)
	}

	cost := decimal.RequireFromString("1")
	if err := ApplyUsageCharge(database, user.ID, cost); !errors.Is(err, ErrInsufficientBalance) {
		t.Fatalf("ApplyUsageCharge error = %v, want ErrInsufficientBalance", err)
	}
	if balance := deliveredChargeUserBalance(t, database, user.ID); !balance.Equal(dust) {
		t.Fatalf("balance after refused charge = %s, want it untouched at %s", balance, dust)
	}

	if err := ApplyDeliveredUsageCharge(database, user.ID, cost); err != nil {
		t.Fatalf("ApplyDeliveredUsageCharge error = %v, want nil", err)
	}
	balance := deliveredChargeUserBalance(t, database, user.ID)
	if !balance.IsZero() {
		t.Fatalf("balance after delivered charge = %s, want 0", balance)
	}
}

func TestApplyDeliveredUsageChargeDeductsWhenAffordable(t *testing.T) {
	database := newDeliveredChargeDB(t, "file:delivered-usage-charge-affordable?mode=memory&cache=shared")
	user := model.User{Username: "funded", Email: "funded@example.com", APIKey: "sk-delivered-funded", Balance: decimal.RequireFromString("10")}
	if err := database.Create(&user).Error; err != nil {
		t.Fatal(err)
	}

	if err := ApplyDeliveredUsageCharge(database, user.ID, decimal.RequireFromString("2.5")); err != nil {
		t.Fatalf("ApplyDeliveredUsageCharge error = %v, want nil", err)
	}
	balance := deliveredChargeUserBalance(t, database, user.ID)
	if !balance.Equal(decimal.RequireFromString("7.5")) {
		t.Fatalf("balance = %s, want 7.5", balance)
	}
}
