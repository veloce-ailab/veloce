package service

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/shopspring/decimal"
)

// This is opt-in because its fixture is built by the separate lottery module.
func TestLotteryPluginWASM(t *testing.T) {
	wasmPath := os.Getenv("VELOCE_LOTTERY_TEST_WASM")
	if wasmPath == "" {
		t.Skip("VELOCE_LOTTERY_TEST_WASM is not set")
	}
	db := walletTestDB(t, "lottery-plugin")
	previous := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previous })
	user := model.User{Username: "lottery-user", Email: "lottery-user@example.com", APIKey: "lottery-user-key", Balance: decimal.NewFromInt(10)}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	permissions := []string{"plugin.settings.global", "wallet.balance.read", "wallet.balance.debit", "wallet.balance.credit"}
	plugin := model.Plugin{
		ID: "balance-lottery", Name: "Lottery", Version: "0.1.0", ManifestJSON: "{}", Path: "plugins", WASMPath: wasmPath,
		Enabled: true, PermissionsJSON: mustJSON(permissions),
		GlobalConfigJSON: `{"enabled":true,"entry_fee":"1","daily_limit":1,"total_limit":5,"prizes":[{"id":"only","name":"固定奖","reward":"2","weight":1,"enabled":true}]}`,
	}
	if err := db.Create(&plugin).Error; err != nil {
		t.Fatal(err)
	}

	first, err := InvokePluginAction(context.Background(), plugin, user.ID, "lottery-request-1", "draw", map[string]interface{}{"values": map[string]interface{}{}})
	if err != nil {
		t.Fatal(err)
	}
	if first["prize"] != "固定奖" || first["balance"] != "11" {
		t.Fatalf("first draw = %#v", first)
	}
	replay, err := InvokePluginAction(context.Background(), plugin, user.ID, "lottery-request-1", "draw", map[string]interface{}{"values": map[string]interface{}{}})
	if err != nil {
		t.Fatal(err)
	}
	if replay["replay"] != true || replay["transaction_id"] != first["transaction_id"] {
		t.Fatalf("replay = %#v", replay)
	}
	_, err = InvokePluginAction(context.Background(), plugin, user.ID, "lottery-request-2", "draw", map[string]interface{}{"values": map[string]interface{}{}})
	var actionErr *PluginActionError
	if !errors.As(err, &actionErr) || actionErr.Code != "participation_limit" {
		t.Fatalf("second draw error = %v", err)
	}
	var transactions int64
	var usages int64
	if err := db.Model(&model.WalletTransaction{}).Count(&transactions).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.WalletLimitUsage{}).Count(&usages).Error; err != nil {
		t.Fatal(err)
	}
	if transactions != 1 || usages != 2 {
		t.Fatalf("transactions=%d usages=%d", transactions, usages)
	}
}
