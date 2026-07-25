package api

import (
	"testing"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/shopspring/decimal"
)

func TestExposedPricingDecimalDividesTokenPricesOnly(t *testing.T) {
	tokenPrice := exposedPricingDecimal(decimal.RequireFromString("4"), 0)
	if !tokenPrice.Equal(decimal.RequireFromString("2")) {
		t.Fatalf("token exposed price = %s, want 2", tokenPrice.String())
	}

	perCallPrice := exposedPricingDecimal(decimal.RequireFromString("0.12"), 1)
	if !perCallPrice.Equal(decimal.RequireFromString("0.12")) {
		t.Fatalf("per-call exposed price = %s, want 0.12", perCallPrice.String())
	}
}

func TestExposedPricingTiersDivideTokenPricesOnly(t *testing.T) {
	tokenTiers := exposedPricingTiers(model.PriceTierList{{
		MinTokens: 1000,
		Price:     decimal.RequireFromString("8"),
		Condition: model.PriceTierConditionFullInputTokens,
	}}, 0)
	if len(tokenTiers) != 1 {
		t.Fatalf("token tier count = %d, want 1", len(tokenTiers))
	}
	if !tokenTiers[0].Price.Equal(decimal.RequireFromString("4")) {
		t.Fatalf("token tier price = %s, want 4", tokenTiers[0].Price.String())
	}
	if tokenTiers[0].Condition != model.PriceTierConditionFullInputTokens {
		t.Fatalf("token tier condition = %q, want %q", tokenTiers[0].Condition, model.PriceTierConditionFullInputTokens)
	}

	perCallTiers := exposedPricingTiers(model.PriceTierList{{
		MinTokens: 1,
		Price:     decimal.RequireFromString("0.2"),
	}}, 1)
	if len(perCallTiers) != 1 {
		t.Fatalf("per-call tier count = %d, want 1", len(perCallTiers))
	}
	if !perCallTiers[0].Price.Equal(decimal.RequireFromString("0.2")) {
		t.Fatalf("per-call tier price = %s, want 0.2", perCallTiers[0].Price.String())
	}
}
