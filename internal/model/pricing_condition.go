package model

import (
	"sort"
	"strings"
	"time"
)

const (
	PricingConditionGroup     = "group"
	PricingConditionLeaf      = "condition"
	PricingConditionTimeRange = "time_range"
)

// PricingCondition is either a logical group (type=group) or a leaf condition
// (type=condition). Time conditions use the parent TimePricing timezone.
type PricingCondition struct {
	Type       string             `json:"type"`
	Logic      string             `json:"logic,omitempty"`
	Conditions []PricingCondition `json:"conditions,omitempty"`
	Field      string             `json:"field,omitempty"`
	Operator   string             `json:"operator,omitempty"`
	Value      int                `json:"value,omitempty"`
	Start      string             `json:"start,omitempty"`
	End        string             `json:"end,omitempty"`
	Weekdays   []int              `json:"weekdays,omitempty"`
}

const maxPricingConditionDepth = 12

// NormalizePricingCondition validates a condition tree and canonicalizes its
// operators, time values, and weekday list. Invalid trees are rejected whole
// so a malformed child cannot silently broaden a pricing rule.
func NormalizePricingCondition(condition PricingCondition) (PricingCondition, bool) {
	return normalizePricingCondition(condition, 0)
}

func normalizePricingCondition(condition PricingCondition, depth int) (PricingCondition, bool) {
	if depth > maxPricingConditionDepth {
		return PricingCondition{}, false
	}
	condition.Type = strings.ToLower(strings.TrimSpace(condition.Type))
	switch condition.Type {
	case PricingConditionGroup:
		condition.Logic = strings.ToLower(strings.TrimSpace(condition.Logic))
		if condition.Logic != "and" && condition.Logic != "or" {
			return PricingCondition{}, false
		}
		if len(condition.Conditions) == 0 {
			return PricingCondition{}, false
		}
		children := make([]PricingCondition, 0, len(condition.Conditions))
		for _, child := range condition.Conditions {
			normalized, ok := normalizePricingCondition(child, depth+1)
			if !ok {
				return PricingCondition{}, false
			}
			children = append(children, normalized)
		}
		condition.Conditions = children
		condition.Field = ""
		condition.Operator = ""
		condition.Value = 0
		condition.Start = ""
		condition.End = ""
		condition.Weekdays = nil
		return condition, true
	case PricingConditionLeaf:
		condition.Field = strings.ToLower(strings.TrimSpace(condition.Field))
		condition.Logic = ""
		condition.Conditions = nil
		switch condition.Field {
		case PricingConditionTimeRange:
			start, startErr := time.Parse("15:04", strings.TrimSpace(condition.Start))
			end, endErr := time.Parse("15:04", strings.TrimSpace(condition.End))
			if startErr != nil || endErr != nil {
				return PricingCondition{}, false
			}
			condition.Start = start.Format("15:04")
			condition.End = end.Format("15:04")
			condition.Operator = ""
			condition.Value = 0
			seen := make(map[int]struct{}, len(condition.Weekdays))
			weekdays := make([]int, 0, len(condition.Weekdays))
			for _, weekday := range condition.Weekdays {
				if weekday < 0 || weekday > 6 {
					return PricingCondition{}, false
				}
				if _, exists := seen[weekday]; exists {
					continue
				}
				seen[weekday] = struct{}{}
				weekdays = append(weekdays, weekday)
			}
			sort.Ints(weekdays)
			condition.Weekdays = weekdays
			return condition, true
		case PriceTierConditionCurrentTokens,
			PriceTierConditionFullInputTokens,
			PriceTierConditionFullRequestTokens,
			PriceTierConditionBillableInputTokens,
			PriceTierConditionBillableOutputTokens:
			condition.Operator = strings.TrimSpace(condition.Operator)
			switch condition.Operator {
			case ">", ">=", "<", "<=", "==", "!=":
			default:
				return PricingCondition{}, false
			}
			if condition.Value < 0 {
				return PricingCondition{}, false
			}
			condition.Start = ""
			condition.End = ""
			condition.Weekdays = nil
			return condition, true
		default:
			return PricingCondition{}, false
		}
	default:
		return PricingCondition{}, false
	}
}
