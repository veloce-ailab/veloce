package api

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/veloce-ailab/veloce/internal/model"
	"gorm.io/gorm"
)

func TestModelFromInputAllowsClearingProviderIcon(t *testing.T) {
	existing := &model.Model{
		ModelName:       "gpt-4o",
		Provider:        "openai",
		ProviderIconURL: "https://example.com/openai.svg",
	}

	updated, err := modelFromInput(modelConfigInput{
		ModelName:       existing.ModelName,
		Provider:        "openai",
		ProviderIconURL: "",
	}, existing)
	if err != nil {
		t.Fatalf("modelFromInput returned error: %v", err)
	}
	if updated.ProviderIconURL != "" {
		t.Fatalf("provider icon URL = %q, want empty", updated.ProviderIconURL)
	}
}

func TestModelFromInputKeepsInferredProviderIcon(t *testing.T) {
	updated, err := modelFromInput(modelConfigInput{ModelName: "gpt-4o"}, nil)
	if err != nil {
		t.Fatalf("modelFromInput returned error: %v", err)
	}
	if updated.Provider != "openai" || updated.ProviderIconURL == "" {
		t.Fatalf("unexpected inferred provider: provider=%q icon=%q", updated.Provider, updated.ProviderIconURL)
	}
}

func TestGlobalModelFromInputAllowsClearingProviderIcon(t *testing.T) {
	previousDB := model.DB
	t.Cleanup(func() { model.DB = previousDB })
	database, err := gorm.Open(sqlite.Open("file:model-provider-icon-clear-test?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := database.AutoMigrate(&model.Model{}); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	model.DB = database

	stored := model.Model{
		ModelName:       "gpt-4o",
		Provider:        "openai",
		ProviderIconURL: "https://example.com/openai.svg",
		Enabled:         true,
	}
	if err := database.Create(&stored).Error; err != nil {
		t.Fatalf("create model: %v", err)
	}

	updated, err := globalModelFromInput(modelConfigInput{
		ModelID:         stored.ID,
		Provider:        "openai",
		ProviderIconURL: "",
	}, nil)
	if err != nil {
		t.Fatalf("globalModelFromInput returned error: %v", err)
	}
	if updated.ProviderIconURL != "" {
		t.Fatalf("returned provider icon URL = %q, want empty", updated.ProviderIconURL)
	}

	var persisted model.Model
	if err := database.First(&persisted, stored.ID).Error; err != nil {
		t.Fatalf("load model: %v", err)
	}
	if persisted.ProviderIconURL != "" {
		t.Fatalf("persisted provider icon URL = %q, want empty", persisted.ProviderIconURL)
	}
}

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
