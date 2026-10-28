package service

import (
	"time"

	"github.com/pkoukk/tiktoken-go"
	"github.com/shopspring/decimal"
	"github.com/veloce-ailab/veloce/internal/model"
)

func timeTierPrice(tiers model.TimePriceTierList, timezone string, now time.Time) (decimal.Decimal, bool) {
	return timeTierPriceWithMetrics(tiers, timezone, now, PriceTierMetrics{})
}

func timeTierPriceWithMetrics(tiers model.TimePriceTierList, timezone string, now time.Time, metrics PriceTierMetrics) (decimal.Decimal, bool) {
	if len(tiers) == 0 {
		return decimal.Zero, false
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		location = time.UTC
	}
	local := now.In(location)
	for _, tier := range model.NormalizeTimePriceTiers(tiers) {
		if tier.Conditions != nil && pricingConditionMatches(*tier.Conditions, local, metrics) {
			return tier.Price, true
		}
		if tier.Conditions == nil && timeRangeMatches(tier.Start, tier.End, local) {
			return tier.Price, true
		}
	}
	return decimal.Zero, false
}

func pricingConditionMatches(condition model.PricingCondition, local time.Time, metrics PriceTierMetrics) bool {
	if condition.Type == model.PricingConditionGroup {
		if len(condition.Conditions) == 0 {
			return false
		}
		if condition.Logic == "or" {
			for _, child := range condition.Conditions {
				if pricingConditionMatches(child, local, metrics) {
					return true
				}
			}
			return false
		}
		for _, child := range condition.Conditions {
			if !pricingConditionMatches(child, local, metrics) {
				return false
			}
		}
		return true
	}

	if condition.Type != model.PricingConditionLeaf {
		return false
	}
	if condition.Field == model.PricingConditionTimeRange {
		return timeConditionMatches(condition, local)
	}

	value, ok := pricingConditionMetric(condition.Field, metrics)
	if !ok {
		return false
	}
	switch condition.Operator {
	case ">":
		return value > condition.Value
	case ">=":
		return value >= condition.Value
	case "<":
		return value < condition.Value
	case "<=":
		return value <= condition.Value
	case "==":
		return value == condition.Value
	case "!=":
		return value != condition.Value
	default:
		return false
	}
}

func timeConditionMatches(condition model.PricingCondition, local time.Time) bool {
	if !timeRangeMatches(condition.Start, condition.End, local) {
		return false
	}
	if len(condition.Weekdays) == 0 {
		return true
	}
	start, _ := time.Parse("15:04", condition.Start)
	end, _ := time.Parse("15:04", condition.End)
	startMinute := start.Hour()*60 + start.Minute()
	endMinute := end.Hour()*60 + end.Minute()
	minute := local.Hour()*60 + local.Minute()
	weekday := int(local.Weekday())
	if startMinute > endMinute && minute < endMinute {
		weekday = (weekday + 6) % 7
	}
	for _, selectedWeekday := range condition.Weekdays {
		if selectedWeekday == weekday {
			return true
		}
	}
	return false
}

func pricingConditionMetric(field string, metrics PriceTierMetrics) (int, bool) {
	switch field {
	case model.PriceTierConditionCurrentTokens:
		return metrics.CurrentTokens, true
	case model.PriceTierConditionFullInputTokens:
		return metrics.FullInputTokens, true
	case model.PriceTierConditionFullRequestTokens:
		return metrics.FullRequestTokens, true
	case model.PriceTierConditionBillableInputTokens:
		return metrics.BillableInputTokens, true
	case model.PriceTierConditionBillableOutputTokens:
		return metrics.BillableOutputTokens, true
	default:
		return 0, false
	}
}

func timeRangeMatches(startValue, endValue string, local time.Time) bool {
	start, startErr := time.Parse("15:04", startValue)
	end, endErr := time.Parse("15:04", endValue)
	if startErr != nil || endErr != nil {
		return false
	}
	minute := local.Hour()*60 + local.Minute()
	startMinute := start.Hour()*60 + start.Minute()
	endMinute := end.Hour()*60 + end.Minute()
	return startMinute == endMinute ||
		(startMinute < endMinute && minute >= startMinute && minute < endMinute) ||
		(startMinute > endMinute && (minute >= startMinute || minute < endMinute))
}

func calculateTimeAwareTieredCost(tokens int, base decimal.Decimal, tokenTiers model.PriceTierList, timeTiers model.TimePriceTierList, timezone string, now time.Time, metrics PriceTierMetrics) decimal.Decimal {
	metrics.CurrentTokens = tokens
	if price, ok := timeTierPriceWithMetrics(timeTiers, timezone, now, metrics); ok {
		base = price
	}
	return CalculateTieredTokenCostWithMetrics(tokens, base, tokenTiers, metrics)
}

// CountTokens estimates the number of tokens in a string for a given model
func CountTokens(model string, text string) int {
	// For OpenAI models
	tkm, err := tiktoken.EncodingForModel(model)
	if err != nil {
		// Fallback to cl100k_base which is used by most recent models
		tkm, err = tiktoken.GetEncoding("cl100k_base")
		if err != nil {
			return estimateTokens(text)
		}
	}
	token := tkm.Encode(text, nil, nil)
	return len(token)
}

func estimateTokens(text string) int {
	if text == "" {
		return 0
	}
	count := len([]rune(text)) / 4
	if count == 0 {
		return 1
	}
	return count
}

// EstimateClaudeTokens approximates Claude tokens (typically ~10% more than OpenAI)
func EstimateClaudeTokens(text string) int {
	// Crude estimation: characters / 4 * 1.1
	// In a real scenario, one might use a specific library if available
	openaiCount := CountTokens("gpt-3.5-turbo", text)
	return int(float64(openaiCount) * 1.1)
}

// CalculateCost calculates the cost based on tokens and multipliers
func CalculateCost(inputTokens, outputTokens int, inputPrice, outputPrice, groupMultiplier, channelMultiplier float64) float64 {
	// Prices are stored per 1M tokens.
	baseCost := (float64(inputTokens)*inputPrice + float64(outputTokens)*outputPrice) / 1000000.0
	return baseCost * groupMultiplier * channelMultiplier
}

type PriceTierMetrics struct {
	CurrentTokens        int
	FullInputTokens      int
	FullRequestTokens    int
	BillableInputTokens  int
	BillableOutputTokens int
}

func CalculateTieredTokenCost(tokens int, basePrice decimal.Decimal, tiers model.PriceTierList) decimal.Decimal {
	return CalculateTieredTokenCostWithMetrics(tokens, basePrice, tiers, PriceTierMetrics{CurrentTokens: tokens})
}

func CalculateTieredTokenCostWithMetrics(tokens int, basePrice decimal.Decimal, tiers model.PriceTierList, metrics PriceTierMetrics) decimal.Decimal {
	if tokens <= 0 {
		return decimal.Zero
	}

	normalized := model.NormalizePriceTiers(tiers)
	if usesConditionalPriceTiers(normalized) {
		price := conditionalTierPrice(basePrice, normalized, withCurrentTokens(metrics, tokens))
		return price.Mul(decimal.NewFromInt(int64(tokens))).Div(decimal.NewFromInt(1000000))
	}

	currentMin := 0
	currentPrice := basePrice
	total := decimal.Zero

	for _, tier := range normalized {
		if tier.MinTokens <= currentMin {
			currentPrice = tier.Price
			continue
		}
		if tokens <= currentMin {
			break
		}

		segmentEnd := tier.MinTokens
		if segmentEnd > tokens {
			segmentEnd = tokens
		}
		if segmentEnd > currentMin {
			total = total.Add(currentPrice.Mul(decimal.NewFromInt(int64(segmentEnd - currentMin))))
			currentMin = segmentEnd
		}
		currentPrice = tier.Price
	}

	if tokens > currentMin {
		total = total.Add(currentPrice.Mul(decimal.NewFromInt(int64(tokens - currentMin))))
	}
	return total.Div(decimal.NewFromInt(1000000))
}

func usesConditionalPriceTiers(tiers model.PriceTierList) bool {
	for _, tier := range tiers {
		if model.NormalizePriceTierCondition(tier.Condition) != "" || model.NormalizePriceTierOperator(tier.Operator) != ">=" {
			return true
		}
	}
	return false
}

func conditionalTierPrice(basePrice decimal.Decimal, tiers model.PriceTierList, metrics PriceTierMetrics) decimal.Decimal {
	price := basePrice
	bestMin := -1
	for _, tier := range tiers {
		conditionValue := tierConditionValue(tier.Condition, metrics)
		if !priceTierMatches(conditionValue, tier) {
			continue
		}
		// More-specific lower bounds win; upper-bound conditions are applied in
		// ascending order so the tightest matching bound wins.
		if model.NormalizePriceTierOperator(tier.Operator) == "<" || model.NormalizePriceTierOperator(tier.Operator) == "<=" {
			if bestMin == -1 || tier.MinTokens < bestMin {
				bestMin = tier.MinTokens
				price = tier.Price
			}
			continue
		}
		if tier.MinTokens >= bestMin {
			bestMin = tier.MinTokens
			price = tier.Price
		}
	}
	return price
}

func priceTierMatches(value int, tier model.PriceTier) bool {
	switch model.NormalizePriceTierOperator(tier.Operator) {
	case ">":
		return value > tier.MinTokens
	case "<=":
		return value <= tier.MinTokens
	case "<":
		return value < tier.MinTokens
	default:
		return value >= tier.MinTokens
	}
}

func withCurrentTokens(metrics PriceTierMetrics, tokens int) PriceTierMetrics {
	metrics.CurrentTokens = tokens
	if metrics.FullInputTokens == 0 {
		metrics.FullInputTokens = tokens
	}
	if metrics.FullRequestTokens == 0 {
		metrics.FullRequestTokens = tokens
	}
	if metrics.BillableInputTokens == 0 {
		metrics.BillableInputTokens = tokens
	}
	if metrics.BillableOutputTokens == 0 {
		metrics.BillableOutputTokens = tokens
	}
	return metrics
}

func tierConditionValue(condition string, metrics PriceTierMetrics) int {
	switch model.NormalizePriceTierCondition(condition) {
	case model.PriceTierConditionFullInputTokens:
		return metrics.FullInputTokens
	case model.PriceTierConditionFullRequestTokens:
		return metrics.FullRequestTokens
	case model.PriceTierConditionBillableInputTokens:
		return metrics.BillableInputTokens
	case model.PriceTierConditionBillableOutputTokens:
		return metrics.BillableOutputTokens
	default:
		return metrics.CurrentTokens
	}
}
