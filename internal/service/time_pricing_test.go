package service

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/veloce-ailab/veloce/internal/model"
)

func TestTimeTierPriceWithNestedConditions(t *testing.T) {
	now := time.Date(2026, time.October, 28, 10, 30, 0, 0, time.UTC) // Wednesday
	conditions := model.PricingCondition{
		Type:  model.PricingConditionGroup,
		Logic: "and",
		Conditions: []model.PricingCondition{
			{
				Type: model.PricingConditionLeaf, Field: model.PricingConditionTimeRange,
				Start: "09:00", End: "18:00", Weekdays: []int{1, 2, 3, 4, 5},
			},
			{
				Type:  model.PricingConditionGroup,
				Logic: "or",
				Conditions: []model.PricingCondition{
					{Type: model.PricingConditionLeaf, Field: model.PriceTierConditionFullInputTokens, Operator: ">=", Value: 1_000_000},
					{Type: model.PricingConditionLeaf, Field: model.PriceTierConditionFullRequestTokens, Operator: ">=", Value: 10_000_000},
				},
			},
		},
	}
	tiers := model.TimePriceTierList{{Price: decimal.NewFromInt(8), Conditions: &conditions}}
	metrics := PriceTierMetrics{FullInputTokens: 1_500_000, FullRequestTokens: 1_700_000}

	price, ok := timeTierPriceWithMetrics(tiers, "UTC", now, metrics)
	if !ok || !price.Equal(decimal.NewFromInt(8)) {
		t.Fatalf("timeTierPriceWithMetrics() = %s, %v; want 8, true", price, ok)
	}
}

func TestTimeTierPriceConditionsDoNotMatchOutsideWindowOrWeekday(t *testing.T) {
	conditions := model.PricingCondition{
		Type:  model.PricingConditionGroup,
		Logic: "and",
		Conditions: []model.PricingCondition{
			{Type: model.PricingConditionLeaf, Field: model.PricingConditionTimeRange, Start: "09:00", End: "18:00", Weekdays: []int{1, 2, 3, 4, 5}},
			{Type: model.PricingConditionLeaf, Field: model.PriceTierConditionFullInputTokens, Operator: ">=", Value: 1_000_000},
		},
	}
	tiers := model.TimePriceTierList{{Price: decimal.NewFromInt(8), Conditions: &conditions}}
	metrics := PriceTierMetrics{FullInputTokens: 1_500_000}

	for _, test := range []struct {
		name string
		now  time.Time
	}{
		{name: "outside time window", now: time.Date(2026, time.October, 28, 20, 0, 0, 0, time.UTC)},
		{name: "outside weekdays", now: time.Date(2026, time.October, 31, 10, 0, 0, 0, time.UTC)}, // Saturday
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, ok := timeTierPriceWithMetrics(tiers, "UTC", test.now, metrics); ok {
				t.Fatal("unexpectedly matched the pricing condition")
			}
		})
	}
}

func TestTimeTierPriceUsesFirstMatchingRuleAsPriority(t *testing.T) {
	allDay := model.PricingCondition{
		Type: model.PricingConditionLeaf, Field: model.PricingConditionTimeRange,
		Start: "00:00", End: "00:00",
	}
	tiers := model.TimePriceTierList{
		{Price: decimal.NewFromInt(3), Conditions: &allDay},
		{Price: decimal.NewFromInt(9), Conditions: &allDay},
	}
	price, ok := timeTierPriceWithMetrics(tiers, "UTC", time.Date(2026, time.October, 28, 10, 0, 0, 0, time.UTC), PriceTierMetrics{})
	if !ok || !price.Equal(decimal.NewFromInt(3)) {
		t.Fatalf("timeTierPriceWithMetrics() = %s, %v; want first rule price 3", price, ok)
	}
}

func TestCalculateTimeAwareTieredCostCombinesRulesWithTokenTiers(t *testing.T) {
	conditions := model.PricingCondition{
		Type:  model.PricingConditionGroup,
		Logic: "and",
		Conditions: []model.PricingCondition{
			{Type: model.PricingConditionLeaf, Field: model.PricingConditionTimeRange, Start: "09:00", End: "18:00"},
			{Type: model.PricingConditionLeaf, Field: model.PriceTierConditionFullInputTokens, Operator: ">=", Value: 1_000_000},
		},
	}
	timeTiers := model.TimePriceTierList{{Price: decimal.NewFromInt(6), Conditions: &conditions}}
	tokenTiers := model.PriceTierList{{MinTokens: 1_000_000, Price: decimal.NewFromInt(2)}}
	metrics := PriceTierMetrics{FullInputTokens: 2_000_000}
	now := time.Date(2026, time.October, 28, 10, 0, 0, 0, time.UTC)

	cost := calculateTimeAwareTieredCost(2_000_000, decimal.NewFromInt(1), tokenTiers, timeTiers, "UTC", now, metrics)
	if !cost.Equal(decimal.NewFromInt(8)) {
		t.Fatalf("calculateTimeAwareTieredCost() = %s, want 8 after the time price and token tier combine", cost)
	}
}

func TestOvernightTimeConditionUsesStartWeekday(t *testing.T) {
	conditions := model.PricingCondition{
		Type: model.PricingConditionLeaf, Field: model.PricingConditionTimeRange,
		Start: "22:00", End: "06:00", Weekdays: []int{5}, // Friday
	}
	fridayOvernight := time.Date(2026, time.October, 31, 1, 0, 0, 0, time.UTC) // Saturday
	if !timeConditionMatches(conditions, fridayOvernight) {
		t.Fatal("Saturday 01:00 should match a Friday 22:00–06:00 rule")
	}
}
