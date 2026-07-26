package api

import "testing"

func TestYipayTradeSuccessfulAcceptsExplicitSuccess(t *testing.T) {
	cases := []map[string]string{
		{"trade_status": "TRADE_SUCCESS", "trade_no": "T1"},
		{"trade_status": "trade_finished", "trade_no": "T2"},
		{"trade_status": "SUCCESS"},
		{"status": "1", "trade_no": "T3"},
		{"status": "success"},
		{"status": "paid"},
	}
	for _, params := range cases {
		if !yipayTradeSuccessful(params) {
			t.Errorf("yipayTradeSuccessful(%v) = false, want true", params)
		}
	}
}

// Closed, refunded and pending trades all carry a trade number. Crediting them
// hands out balance for money that was never received.
func TestYipayTradeSuccessfulRejectsUnsuccessfulTrades(t *testing.T) {
	cases := []map[string]string{
		{"trade_status": "TRADE_CLOSED", "trade_no": "T1"},
		{"trade_status": "WAIT_BUYER_PAY", "trade_no": "T2"},
		{"trade_status": "REFUND", "trade_no": "T3"},
		{"trade_status": "TRADE_PENDING", "trade_no": "T4"},
		{"status": "0", "trade_no": "T5"},
		{"status": "failed", "trade_no": "T6"},
		{"status": "refunded", "trade_no": "T7"},
	}
	for _, params := range cases {
		if yipayTradeSuccessful(params) {
			t.Errorf("yipayTradeSuccessful(%v) = true, want false", params)
		}
	}
}

// Some yipay clones report no status field and notify only on success; the
// callback is signature-verified before this check, so the trade number remains
// an acceptable signal in that case only.
func TestYipayTradeSuccessfulFallsBackToTradeNumberWithoutStatus(t *testing.T) {
	if !yipayTradeSuccessful(map[string]string{"trade_no": "T1"}) {
		t.Error("a status-less callback carrying a trade number must be accepted")
	}
	if yipayTradeSuccessful(map[string]string{}) {
		t.Error("a callback with neither status nor trade number must be rejected")
	}
	if yipayTradeSuccessful(map[string]string{"trade_no": "   "}) {
		t.Error("a blank trade number must not count as success")
	}
}
