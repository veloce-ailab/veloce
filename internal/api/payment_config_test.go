package api

import "testing"

func TestPaymentChannelConfigAllowsClearingOptionalURLs(t *testing.T) {
	base := paymentConfig{
		GatewayURL: "https://gateway.example.com",
		NotifyURL:  "https://legacy.example.com/notify",
		ReturnURL:  "https://legacy.example.com/return",
	}
	channel := storedPaymentChannel{
		ID:       "channel",
		Name:     "Channel",
		Provider: paymentProviderYipay,
		Methods:  []string{"alipay"},
		Config: map[string]string{
			"notify_url": "",
			"return_url": "",
		},
	}

	resolved := paymentConfigFromStoredChannel(base, channel)
	if resolved.NotifyURL != "" || resolved.ReturnURL != "" {
		t.Fatalf("optional URLs were not cleared: notify=%q return=%q", resolved.NotifyURL, resolved.ReturnURL)
	}
	if resolved.GatewayURL != base.GatewayURL {
		t.Fatalf("missing gateway URL should retain fallback: got %q want %q", resolved.GatewayURL, base.GatewayURL)
	}
}
