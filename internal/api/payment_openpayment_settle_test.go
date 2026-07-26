package api

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/veloce-ailab/veloce/internal/model"
	"gorm.io/gorm"
)

func newOpenPaymentSettleDB(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&model.User{}, &model.PaymentOrder{}, &model.SystemSetting{}); err != nil {
		t.Fatal(err)
	}
	previousDB := model.DB
	model.DB = database
	t.Cleanup(func() { model.DB = previousDB })
	return database
}

func settleTestOrder(t *testing.T, database *gorm.DB, orderNo, provider, channel string) (model.User, model.PaymentOrder) {
	t.Helper()
	user := model.User{
		Username: orderNo + "-user",
		Email:    orderNo + "@example.com",
		APIKey:   "sk-" + orderNo,
		Balance:  decimal.Zero,
	}
	if err := database.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	order := model.PaymentOrder{
		OrderNo: orderNo, UserID: user.ID,
		Amount: decimal.RequireFromString("10"), RMBAmount: decimal.RequireFromString("72"),
		ExchangeRate: decimal.RequireFromString("7.2"), PaymentCurrency: "CNY",
		GatewayAmount: decimal.RequireFromString("72"), Method: "alipay",
		Status: paymentStatusPending, GatewayProvider: provider, GatewayChannel: channel,
	}
	if err := database.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	return user, order
}

func assertOrderUnpaid(t *testing.T, database *gorm.DB, orderID, userID uint) {
	t.Helper()
	var order model.PaymentOrder
	if err := database.First(&order, orderID).Error; err != nil {
		t.Fatal(err)
	}
	if order.Status != paymentStatusPending {
		t.Fatalf("order status = %q, want it left %q", order.Status, paymentStatusPending)
	}
	var user model.User
	if err := database.First(&user, userID).Error; err != nil {
		t.Fatal(err)
	}
	if !user.Balance.IsZero() {
		t.Fatalf("balance = %s, want 0", user.Balance)
	}
}

func TestMarkOpenPaymentOrderPaidSettlesOwnOrder(t *testing.T) {
	database := newOpenPaymentSettleDB(t, "file:openpayment-settle-own?mode=memory&cache=shared")
	user, order := settleTestOrder(t, database, "OP-1", paymentProviderOpenPayment, "")

	if err := markOpenPaymentOrderPaid(order.OrderNo, "", "PLATFORM-1", decimal.RequireFromString("72"), "{}"); err != nil {
		t.Fatalf("markOpenPaymentOrderPaid error = %v", err)
	}
	var settled model.PaymentOrder
	if err := database.First(&settled, order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if settled.Status != paymentStatusPaid {
		t.Fatalf("order status = %q, want %q", settled.Status, paymentStatusPaid)
	}
	if settled.GatewayProvider != paymentProviderOpenPayment {
		t.Fatalf("gateway provider = %q, want it unchanged", settled.GatewayProvider)
	}
	var credited model.User
	if err := database.First(&credited, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !credited.Balance.Equal(decimal.RequireFromString("10")) {
		t.Fatalf("balance = %s, want 10", credited.Balance)
	}
}

// An Open Payment callback must not settle another provider's pending order,
// even when the RMB amount lines up.
func TestMarkOpenPaymentOrderPaidRejectsForeignProvider(t *testing.T) {
	database := newOpenPaymentSettleDB(t, "file:openpayment-settle-foreign?mode=memory&cache=shared")
	for _, provider := range []string{paymentProviderStripe, paymentProviderPayPal, paymentProviderYipay} {
		user, order := settleTestOrder(t, database, "OP-"+provider, provider, "")
		err := markOpenPaymentOrderPaid(order.OrderNo, "", "PLATFORM-X", decimal.RequireFromString("72"), "{}")
		if err == nil {
			t.Fatalf("settling a %s order via Open Payment must fail", provider)
		}
		assertOrderUnpaid(t, database, order.ID, user.ID)
	}
}

func TestMarkOpenPaymentOrderPaidRejectsOtherChannel(t *testing.T) {
	database := newOpenPaymentSettleDB(t, "file:openpayment-settle-channel?mode=memory&cache=shared")
	user, order := settleTestOrder(t, database, "OP-2", paymentProviderOpenPayment, "channel-a")

	if err := markOpenPaymentOrderPaid(order.OrderNo, "channel-b", "PLATFORM-2", decimal.RequireFromString("72"), "{}"); err == nil {
		t.Fatal("a callback from another channel must not settle the order")
	}
	assertOrderUnpaid(t, database, order.ID, user.ID)

	// The legacy single-channel config carries no channel id and must still work.
	if err := markOpenPaymentOrderPaid(order.OrderNo, "", "PLATFORM-2", decimal.RequireFromString("72"), "{}"); err != nil {
		t.Fatalf("legacy channel-less callback error = %v", err)
	}
}

func TestMarkOpenPaymentOrderPaidRejectsAmountMismatch(t *testing.T) {
	database := newOpenPaymentSettleDB(t, "file:openpayment-settle-amount?mode=memory&cache=shared")
	user, order := settleTestOrder(t, database, "OP-3", paymentProviderOpenPayment, "")

	if err := markOpenPaymentOrderPaid(order.OrderNo, "", "PLATFORM-3", decimal.RequireFromString("1"), "{}"); err == nil {
		t.Fatal("an amount mismatch must not settle the order")
	}
	assertOrderUnpaid(t, database, order.ID, user.ID)
}

func TestMarkOpenPaymentOrderPaidIsIdempotent(t *testing.T) {
	database := newOpenPaymentSettleDB(t, "file:openpayment-settle-replay?mode=memory&cache=shared")
	user, order := settleTestOrder(t, database, "OP-4", paymentProviderOpenPayment, "")

	for i := 0; i < 3; i++ {
		if err := markOpenPaymentOrderPaid(order.OrderNo, "", "PLATFORM-4", decimal.RequireFromString("72"), "{}"); err != nil {
			t.Fatalf("attempt %d error = %v", i+1, err)
		}
	}
	var credited model.User
	if err := database.First(&credited, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !credited.Balance.Equal(decimal.RequireFromString("10")) {
		t.Fatalf("balance = %s, want a single credit of 10", credited.Balance)
	}
}
