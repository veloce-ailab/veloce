package service

import (
	"context"
	"net"
	"testing"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/shopspring/decimal"
)

func TestPluginHostWalletSettlementUsesPermissionsAndIdempotency(t *testing.T) {
	db := walletTestDB(t, "plugin-host-wallet")
	previous := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previous })
	user := model.User{Username: "plugin-host-user", Balance: decimal.NewFromInt(10)}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	plugin := model.Plugin{ID: "lottery", PermissionsJSON: mustJSON([]string{"wallet.balance.read", "wallet.balance.debit", "wallet.balance.credit"})}
	invocation := pluginRuntimeInvocation{UserID: user.ID, RequestID: "request-1"}

	result, status := executePluginHostCall(context.Background(), plugin, invocation, "wallet.settle", map[string]interface{}{
		"debit": "2", "credit": "5", "reference_type": "lottery_draw", "metadata": map[string]interface{}{"prize": "5"},
	})
	if status != pluginHostOK || result["balance_after"] != "13" {
		t.Fatalf("settlement status=%d result=%v", status, result)
	}
	replay, status := executePluginHostCall(context.Background(), plugin, invocation, "wallet.settle", map[string]interface{}{
		"debit": "2", "credit": "5", "reference_type": "lottery_draw", "metadata": map[string]interface{}{"prize": "5"},
	})
	if status != pluginHostOK || replay["replay"] != true {
		t.Fatalf("replay status=%d result=%v", status, replay)
	}
	if _, status := executePluginHostCall(context.Background(), plugin, invocation, "wallet.settle", map[string]interface{}{"debit": "2", "credit": "1"}); status != pluginHostConflict {
		t.Fatalf("conflicting replay status=%d", status)
	}

	noCredit := plugin
	noCredit.PermissionsJSON = mustJSON([]string{"wallet.balance.debit"})
	if _, status := executePluginHostCall(context.Background(), noCredit, pluginRuntimeInvocation{UserID: user.ID, RequestID: "request-2"}, "wallet.settle", map[string]interface{}{"debit": "1", "credit": "1"}); status != pluginHostDenied {
		t.Fatalf("credit without permission status=%d", status)
	}
}

func TestPluginChannelHTTPRequiresPermission(t *testing.T) {
	plugin := model.Plugin{ID: "channel-plugin"}
	_, status := executePluginHostCall(context.Background(), plugin, pluginRuntimeInvocation{}, "plugin.channel.http", map[string]interface{}{"url": "https://example.com"})
	if status != pluginHostDenied {
		t.Fatalf("HTTP without permission status=%d", status)
	}
}

func TestPluginChannelHTTPRejectsNonPublicAddresses(t *testing.T) {
	for _, raw := range []string{"0.0.0.0", "127.0.0.1", "10.0.0.1", "100.64.0.1", "169.254.1.1", "198.18.0.1", "::1"} {
		if pluginChannelHTTPPublicIP(net.ParseIP(raw)) {
			t.Fatalf("expected %s to be rejected", raw)
		}
	}
	if !pluginChannelHTTPPublicIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("expected public address to be accepted")
	}
}

func TestPluginHostKVIsNamespacedByUserAndPlugin(t *testing.T) {
	db := walletTestDB(t, "plugin-host-kv")
	previous := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previous })
	first := model.User{Username: "kv-first", Email: "kv-first@example.com", APIKey: "kv-first-key"}
	second := model.User{Username: "kv-second", Email: "kv-second@example.com", APIKey: "kv-second-key"}
	if err := db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	plugin := model.Plugin{ID: "stateful", PermissionsJSON: mustJSON([]string{"plugin.kv.read", "plugin.kv.write"})}
	if _, status := executePluginHostCall(context.Background(), plugin, pluginRuntimeInvocation{UserID: first.ID}, "plugin.kv.put", map[string]interface{}{"key": "count", "value": 3}); status != pluginHostOK {
		t.Fatalf("put status=%d", status)
	}
	value, status := executePluginHostCall(context.Background(), plugin, pluginRuntimeInvocation{UserID: first.ID}, "plugin.kv.get", map[string]interface{}{"key": "count"})
	if status != pluginHostOK || value["found"] != true || value["value"] != float64(3) {
		t.Fatalf("get status=%d value=%v", status, value)
	}
	other, status := executePluginHostCall(context.Background(), plugin, pluginRuntimeInvocation{UserID: second.ID}, "plugin.kv.get", map[string]interface{}{"key": "count"})
	if status != pluginHostOK || other["found"] != false {
		t.Fatalf("cross-user get status=%d value=%v", status, other)
	}
}

func TestPluginActionErrorStatus(t *testing.T) {
	if got := pluginActionErrorStatus(&PluginActionError{Code: "insufficient_balance", Message: "no"}); got != 402 {
		t.Fatalf("status = %d", got)
	}
}
