package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/veloce-ailab/veloce/internal/model"
	"gorm.io/gorm"
)

func newPayPalCaptureDB(t *testing.T, dsn string) *gorm.DB {
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

func paypalTestContext(t *testing.T) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/api/payment/paypal/notify", strings.NewReader("{}"))
	return c
}

// An approved-but-uncaptured order moves no money, so it must not credit the
// balance; the capture call is what actually takes the funds.
func TestCapturePayPalApprovedOrderCreditsOnlyCompletedCapture(t *testing.T) {
	database := newPayPalCaptureDB(t, "file:paypal-capture-completed?mode=memory&cache=shared")
	user := model.User{Username: "buyer", Email: "buyer@example.com", APIKey: "sk-paypal-capture", Balance: decimal.Zero}
	if err := database.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	order := model.PaymentOrder{
		OrderNo: "ORDER-1", UserID: user.ID,
		Amount: decimal.RequireFromString("10"), RMBAmount: decimal.RequireFromString("72"),
		ExchangeRate: decimal.RequireFromString("7.2"), PaymentCurrency: "USD",
		GatewayAmount: decimal.RequireFromString("10"), Method: "paypal",
		Status: paymentStatusPending, GatewayProvider: paymentProviderPayPal,
	}
	if err := database.Create(&order).Error; err != nil {
		t.Fatal(err)
	}

	var capturePath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/v1/oauth2/token") {
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "test-token"})
			return
		}
		capturePath = r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "PAYPAL-1", "status": "COMPLETED",
			"purchase_units": []map[string]any{{
				"custom_id": "ORDER-1",
				"payments": map[string]any{"captures": []map[string]any{{
					"id": "CAPTURE-1", "status": "COMPLETED",
					"amount": map[string]string{"value": "10.00", "currency_code": "USD"},
				}}},
			}},
		})
	}))
	defer server.Close()

	cfg := paymentConfig{Provider: paymentProviderPayPal, PayPalBaseURL: server.URL, OfficialCurrency: "USD"}
	resource := json.RawMessage(`{"id":"PAYPAL-1"}`)
	if err := capturePayPalApprovedOrder(paypalTestContext(t), cfg, resource); err != nil {
		t.Fatalf("capturePayPalApprovedOrder error = %v", err)
	}
	if !strings.HasSuffix(capturePath, "/v2/checkout/orders/PAYPAL-1/capture") {
		t.Fatalf("capture endpoint = %q, want the order capture path", capturePath)
	}

	var settled model.PaymentOrder
	if err := database.First(&settled, order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if settled.Status != paymentStatusPaid {
		t.Fatalf("order status = %q, want %q", settled.Status, paymentStatusPaid)
	}
	var credited model.User
	if err := database.First(&credited, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !credited.Balance.Equal(decimal.RequireFromString("10")) {
		t.Fatalf("balance = %s, want 10", credited.Balance)
	}
}

func TestCapturePayPalApprovedOrderLeavesPendingCaptureUnpaid(t *testing.T) {
	database := newPayPalCaptureDB(t, "file:paypal-capture-pending?mode=memory&cache=shared")
	user := model.User{Username: "waiting", Email: "waiting@example.com", APIKey: "sk-paypal-pending", Balance: decimal.Zero}
	if err := database.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	order := model.PaymentOrder{
		OrderNo: "ORDER-2", UserID: user.ID,
		Amount: decimal.RequireFromString("10"), RMBAmount: decimal.RequireFromString("72"),
		ExchangeRate: decimal.RequireFromString("7.2"), PaymentCurrency: "USD",
		GatewayAmount: decimal.RequireFromString("10"), Method: "paypal",
		Status: paymentStatusPending, GatewayProvider: paymentProviderPayPal,
	}
	if err := database.Create(&order).Error; err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/v1/oauth2/token") {
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "test-token"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "PAYPAL-2", "status": "PENDING",
			"purchase_units": []map[string]any{{
				"custom_id": "ORDER-2",
				"payments": map[string]any{"captures": []map[string]any{{
					"id": "CAPTURE-2", "status": "PENDING",
					"amount": map[string]string{"value": "10.00", "currency_code": "USD"},
				}}},
			}},
		})
	}))
	defer server.Close()

	cfg := paymentConfig{Provider: paymentProviderPayPal, PayPalBaseURL: server.URL, OfficialCurrency: "USD"}
	if err := capturePayPalApprovedOrder(paypalTestContext(t), cfg, json.RawMessage(`{"id":"PAYPAL-2"}`)); err != nil {
		t.Fatalf("capturePayPalApprovedOrder error = %v", err)
	}

	var settled model.PaymentOrder
	if err := database.First(&settled, order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if settled.Status != paymentStatusPending {
		t.Fatalf("order status = %q, want it left %q until the capture completes", settled.Status, paymentStatusPending)
	}
	var untouched model.User
	if err := database.First(&untouched, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !untouched.Balance.IsZero() {
		t.Fatalf("balance = %s, want 0 for an uncaptured order", untouched.Balance)
	}
}
