package service

import (
	"context"
	"errors"
	"testing"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func TestSettleWalletIsAtomicAndIdempotent(t *testing.T) {
	db := walletTestDB(t, "atomic")
	previous := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previous })
	user := model.User{Username: "wallet-user", Balance: decimal.NewFromInt(10)}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}

	input := WalletSettlementInput{
		UserID: user.ID, Source: "plugin:lottery", PluginID: "lottery", IdempotencyKey: "draw-1",
		DebitAmount: decimal.NewFromInt(2), CreditAmount: decimal.NewFromInt(5),
		ReferenceType: "lottery_draw", ReferenceID: "draw-1", Metadata: map[string]interface{}{"prize": "5"},
	}
	first, err := SettleWallet(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Replay || !first.Transaction.BalanceAfter.Equal(decimal.NewFromInt(13)) {
		t.Fatalf("unexpected first settlement: %+v", first)
	}
	second, err := SettleWallet(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Replay || second.Transaction.ID != first.Transaction.ID {
		t.Fatalf("expected replay of transaction %d, got %+v", first.Transaction.ID, second)
	}
	var refreshed model.User
	if err := db.First(&refreshed, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !refreshed.Balance.Equal(decimal.NewFromInt(13)) {
		t.Fatalf("balance = %s, want 13", refreshed.Balance)
	}
	var count int64
	if err := db.Model(&model.WalletTransaction{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("ledger count = %d, error = %v", count, err)
	}
}

func TestSettleWalletRejectsInsufficientBalanceAndConflictingReplay(t *testing.T) {
	db := walletTestDB(t, "rejections")
	previous := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previous })
	user := model.User{Username: "wallet-rejections", Balance: decimal.NewFromInt(1)}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}

	base := WalletSettlementInput{UserID: user.ID, Source: "plugin:lottery", IdempotencyKey: "draw-2", DebitAmount: decimal.NewFromInt(1)}
	if _, err := SettleWallet(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	changed := base
	changed.CreditAmount = decimal.NewFromInt(2)
	if _, err := SettleWallet(context.Background(), changed); !errors.Is(err, ErrWalletIdempotencyConflict) {
		t.Fatalf("conflicting replay error = %v", err)
	}
	insufficient := WalletSettlementInput{UserID: user.ID, Source: "plugin:lottery", IdempotencyKey: "draw-3", DebitAmount: decimal.NewFromInt(1)}
	if _, err := SettleWallet(context.Background(), insufficient); !errors.Is(err, ErrInsufficientBalance) {
		t.Fatalf("insufficient error = %v", err)
	}
	var count int64
	if err := db.Model(&model.WalletTransaction{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("ledger count = %d, error = %v", count, err)
	}
}

func TestSettleWalletEnforcesLimitsInsideTransaction(t *testing.T) {
	db := walletTestDB(t, "limits")
	previous := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previous })
	user := model.User{Username: "wallet-limits", Email: "wallet-limits@example.com", APIKey: "wallet-limits-key", Balance: decimal.NewFromInt(10)}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	limit := []WalletSettlementLimit{{Key: "daily:2026-07-23", Max: 1}}
	first := WalletSettlementInput{UserID: user.ID, Source: "plugin:lottery", IdempotencyKey: "limit-1", DebitAmount: decimal.NewFromInt(1), Limits: limit}
	if _, err := SettleWallet(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.IdempotencyKey = "limit-2"
	if _, err := SettleWallet(context.Background(), second); !errors.Is(err, ErrWalletLimitExceeded) {
		t.Fatalf("limit error = %v", err)
	}
	var balance model.User
	if err := db.First(&balance, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !balance.Balance.Equal(decimal.NewFromInt(9)) {
		t.Fatalf("balance = %s, want 9", balance.Balance)
	}
}

func walletTestDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:wallet-"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.SystemSetting{}, &model.Plugin{}, &model.UserPluginConfig{}, &model.WalletTransaction{}, &model.WalletLimitUsage{}, &model.PluginKV{}); err != nil {
		t.Fatal(err)
	}
	return db
}
