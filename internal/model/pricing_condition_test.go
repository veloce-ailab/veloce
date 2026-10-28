package model

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestTimePricingConditionRoundTrip(t *testing.T) {
	pricing := TimePricing{
		Timezone: "Asia/Shanghai",
		Input: TimePriceTierList{{
			Price: decimal.RequireFromString("0.75"),
			Label: "off-peak",
			Conditions: &PricingCondition{
				Type:  PricingConditionGroup,
				Logic: "and",
				Conditions: []PricingCondition{
					{Type: PricingConditionLeaf, Field: PricingConditionTimeRange, Start: "00:00", End: "08:00", Weekdays: []int{1, 2, 3, 4, 5}},
					{Type: PricingConditionLeaf, Field: PriceTierConditionFullRequestTokens, Operator: ">=", Value: 1_000_000},
				},
			},
		}},
	}

	stored, err := pricing.Value()
	if err != nil {
		t.Fatalf("TimePricing.Value() error = %v", err)
	}
	var loaded TimePricing
	if err := loaded.Scan(stored); err != nil {
		t.Fatalf("TimePricing.Scan() error = %v", err)
	}
	if len(loaded.Input) != 1 || loaded.Input[0].Conditions == nil {
		t.Fatalf("round-tripped input rules = %#v, want one rule with conditions", loaded.Input)
	}
	root := loaded.Input[0].Conditions
	if root.Type != PricingConditionGroup || root.Logic != "and" || len(root.Conditions) != 2 {
		t.Fatalf("round-tripped condition group = %#v, want AND group with two conditions", root)
	}
	if root.Conditions[0].Weekdays[0] != 1 || root.Conditions[1].Value != 1_000_000 {
		t.Fatalf("round-tripped condition details = %#v", root.Conditions)
	}
}

func TestNormalizeTimePriceTiersKeepsLegacyIntervals(t *testing.T) {
	tiers := NormalizeTimePriceTiers(TimePriceTierList{{
		Start: "22:00", End: "06:00", Price: decimal.NewFromInt(9),
	}})
	if len(tiers) != 1 || tiers[0].Start != "22:00" || tiers[0].End != "06:00" {
		t.Fatalf("normalized legacy tiers = %#v, want original overnight interval", tiers)
	}
}
