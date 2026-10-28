package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// TimePriceTier applies a price during a local wall-clock interval. End equal
// to start means the interval covers the whole day; an end before start
// crosses midnight.
type TimePriceTier struct {
	Start      string            `json:"start"`
	End        string            `json:"end"`
	Price      decimal.Decimal   `json:"price"`
	Label      string            `json:"label,omitempty"`
	Conditions *PricingCondition `json:"conditions,omitempty"`
}

type TimePriceTierList []TimePriceTier

type TimePricing struct {
	Timezone          string            `json:"timezone"`
	Input             TimePriceTierList `json:"input"`
	Output            TimePriceTierList `json:"output"`
	CachedInput       TimePriceTierList `json:"cached_input"`
	CacheWriteInput   TimePriceTierList `json:"cache_write_input"`
	CacheWrite1hInput TimePriceTierList `json:"cache_write_1h_input"`
	ImageInput        TimePriceTierList `json:"image_input"`
	ImageOutput       TimePriceTierList `json:"image_output"`
	AudioInput        TimePriceTierList `json:"audio_input"`
	AudioOutput       TimePriceTierList `json:"audio_output"`
}

func NormalizeTimePriceTiers(tiers TimePriceTierList) TimePriceTierList {
	result := make(TimePriceTierList, 0, len(tiers))
	for _, tier := range tiers {
		if tier.Price.LessThan(decimal.Zero) {
			continue
		}
		if tier.Conditions != nil {
			conditions, ok := NormalizePricingCondition(*tier.Conditions)
			if !ok {
				continue
			}
			tier.Conditions = &conditions
		} else {
			start, err := time.Parse("15:04", strings.TrimSpace(tier.Start))
			if err != nil {
				continue
			}
			end, err := time.Parse("15:04", strings.TrimSpace(tier.End))
			if err != nil {
				continue
			}
			tier.Start, tier.End = start.Format("15:04"), end.Format("15:04")
		}
		result = append(result, tier)
	}
	return result
}

func NormalizeTimePricing(pricing TimePricing) TimePricing {
	pricing.Timezone = strings.TrimSpace(pricing.Timezone)
	if pricing.Timezone == "" {
		pricing.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(pricing.Timezone); err != nil {
		pricing.Timezone = "UTC"
	}
	pricing.Input = NormalizeTimePriceTiers(pricing.Input)
	pricing.Output = NormalizeTimePriceTiers(pricing.Output)
	pricing.CachedInput = NormalizeTimePriceTiers(pricing.CachedInput)
	pricing.CacheWriteInput = NormalizeTimePriceTiers(pricing.CacheWriteInput)
	pricing.CacheWrite1hInput = NormalizeTimePriceTiers(pricing.CacheWrite1hInput)
	pricing.ImageInput = NormalizeTimePriceTiers(pricing.ImageInput)
	pricing.ImageOutput = NormalizeTimePriceTiers(pricing.ImageOutput)
	pricing.AudioInput = NormalizeTimePriceTiers(pricing.AudioInput)
	pricing.AudioOutput = NormalizeTimePriceTiers(pricing.AudioOutput)
	return pricing
}

func (pricing TimePricing) Value() (driver.Value, error) {
	return json.Marshal(NormalizeTimePricing(pricing))
}
func (pricing *TimePricing) Scan(value interface{}) error {
	if value == nil {
		*pricing = TimePricing{}
		return nil
	}
	var raw []byte
	switch v := value.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("unsupported time pricing value %T", value)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		*pricing = TimePricing{}
		return nil
	}
	var parsed TimePricing
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return err
	}
	*pricing = NormalizeTimePricing(parsed)
	return nil
}

const (
	PriceTierConditionCurrentTokens        = "current_tokens"
	PriceTierConditionFullInputTokens      = "full_input_tokens"
	PriceTierConditionFullRequestTokens    = "full_request_tokens"
	PriceTierConditionBillableInputTokens  = "billable_input_tokens"
	PriceTierConditionBillableOutputTokens = "billable_output_tokens"
)

// PriceTier applies Price per 1M units from MinTokens onward.
// Condition controls which usage count is used to select the tier. Empty keeps
// the legacy behavior: progressive segments based on the current priced units.
type PriceTier struct {
	MinTokens int             `json:"min_tokens"`
	Price     decimal.Decimal `json:"price"`
	Condition string          `json:"condition,omitempty"`
	Operator  string          `json:"operator,omitempty"`
}

type PriceTierList []PriceTier

func (tiers PriceTierList) Value() (driver.Value, error) {
	normalized := NormalizePriceTiers(tiers)
	if len(normalized) == 0 {
		return "[]", nil
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return nil, err
	}
	return string(data), nil
}

func (tiers *PriceTierList) Scan(value interface{}) error {
	if tiers == nil {
		return nil
	}
	if value == nil {
		*tiers = nil
		return nil
	}

	var raw string
	switch typed := value.(type) {
	case []byte:
		raw = string(typed)
	case string:
		raw = typed
	default:
		return fmt.Errorf("unsupported price tier value %T", value)
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		*tiers = nil
		return nil
	}

	var parsed []PriceTier
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return err
	}
	*tiers = NormalizePriceTiers(parsed)
	return nil
}

func NormalizePriceTiers(tiers PriceTierList) PriceTierList {
	if len(tiers) == 0 {
		return nil
	}

	byTier := map[string]PriceTier{}
	for _, tier := range tiers {
		if tier.MinTokens < 0 || tier.Price.LessThan(decimal.Zero) {
			continue
		}
		tier.Condition = NormalizePriceTierCondition(tier.Condition)
		tier.Operator = NormalizePriceTierOperator(tier.Operator)
		key := fmt.Sprintf("%s:%s:%d", tier.Condition, tier.Operator, tier.MinTokens)
		byTier[key] = tier
	}
	if len(byTier) == 0 {
		return nil
	}

	normalized := make(PriceTierList, 0, len(byTier))
	for _, tier := range byTier {
		normalized = append(normalized, tier)
	}
	sort.Slice(normalized, func(i, j int) bool {
		leftCondition := priceTierConditionSortValue(normalized[i].Condition)
		rightCondition := priceTierConditionSortValue(normalized[j].Condition)
		if leftCondition != rightCondition {
			return leftCondition < rightCondition
		}
		return normalized[i].MinTokens < normalized[j].MinTokens
	})
	return normalized
}

func MultiplyPriceTiers(tiers PriceTierList, multiplier decimal.Decimal) PriceTierList {
	normalized := NormalizePriceTiers(tiers)
	if len(normalized) == 0 {
		return nil
	}
	result := make(PriceTierList, 0, len(normalized))
	for _, tier := range normalized {
		result = append(result, PriceTier{
			MinTokens: tier.MinTokens,
			Price:     tier.Price.Mul(multiplier),
			Condition: tier.Condition,
			Operator:  tier.Operator,
		})
	}
	return result
}

// NormalizePriceTierOperator returns the comparison used by conditional tiers.
// Empty legacy values mean greater-than-or-equal-to.
func NormalizePriceTierOperator(operator string) string {
	switch strings.TrimSpace(operator) {
	case "", ">=", "gte":
		return ">="
	case ">", "gt":
		return ">"
	case "<=", "lte":
		return "<="
	case "<", "lt":
		return "<"
	default:
		return ">="
	}
}

func NormalizePriceTierCondition(condition string) string {
	switch strings.ToLower(strings.TrimSpace(condition)) {
	case "", PriceTierConditionCurrentTokens, "tokens", "current", "current_token", "metered_tokens", "metered_token", "priced_tokens", "priced_token":
		return ""
	case PriceTierConditionFullInputTokens, "full_input", "complete_input", "complete_input_tokens", "total_input", "total_input_tokens", "context", "context_tokens", "prompt_tokens":
		return PriceTierConditionFullInputTokens
	case PriceTierConditionFullRequestTokens, "full_request", "complete_request", "total_tokens", "request_tokens", "all_tokens", "len":
		return PriceTierConditionFullRequestTokens
	case PriceTierConditionBillableInputTokens, "billable_input", "pricing_input", "pricing_input_tokens", "priced_input", "priced_input_tokens", "chargeable_input", "chargeable_input_tokens":
		return PriceTierConditionBillableInputTokens
	case PriceTierConditionBillableOutputTokens, "billable_output", "pricing_output", "pricing_output_tokens", "priced_output", "priced_output_tokens", "chargeable_output", "chargeable_output_tokens", "completion_tokens":
		return PriceTierConditionBillableOutputTokens
	default:
		return ""
	}
}

func priceTierConditionSortValue(condition string) int {
	switch NormalizePriceTierCondition(condition) {
	case "":
		return 0
	case PriceTierConditionFullInputTokens:
		return 1
	case PriceTierConditionFullRequestTokens:
		return 2
	case PriceTierConditionBillableInputTokens:
		return 3
	case PriceTierConditionBillableOutputTokens:
		return 4
	default:
		return 4
	}
}
