package service

import (
	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/pkoukk/tiktoken-go"
	"github.com/shopspring/decimal"
)

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
		if model.NormalizePriceTierCondition(tier.Condition) != "" {
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
		if conditionValue < tier.MinTokens {
			continue
		}
		if tier.MinTokens >= bestMin {
			bestMin = tier.MinTokens
			price = tier.Price
		}
	}
	return price
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
