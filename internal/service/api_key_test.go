package service

import (
	"strings"
	"testing"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/shopspring/decimal"
)

func TestGenerateAPIKey(t *testing.T) {
	raw, hash, err := GenerateAPIKey()
	if err != nil {
		t.Fatalf("GenerateAPIKey returned error: %v", err)
	}
	if !strings.HasPrefix(raw, "sk-") {
		t.Fatalf("raw key should have sk- prefix, got %q", raw)
	}
	if len(hash) != 64 {
		t.Fatalf("hash should be 64 hex chars, got %d", len(hash))
	}
	if hash != HashAPIKey(raw) {
		t.Fatal("generated hash does not match raw key")
	}
}

func TestHashAPIKeyIsStable(t *testing.T) {
	raw := "sk-test-key"
	if HashAPIKey(raw) != HashAPIKey(raw) {
		t.Fatal("hash should be stable")
	}
}

func TestAPIKeyQuotaExceededUsesInclusiveLimit(t *testing.T) {
	apiKey := &model.APIKey{
		ID:         1,
		UserID:     2,
		QuotaLimit: decimal.NewFromInt(10),
	}

	if exceeded := apiKeyQuotaExceeded(apiKey, decimal.NewFromInt(9), decimal.NewFromInt(1)); exceeded {
		t.Fatal("expected exact quota usage to be allowed")
	}
	if exceeded := apiKeyQuotaExceeded(apiKey, decimal.NewFromInt(10), decimal.Zero); !exceeded {
		t.Fatal("expected exhausted quota to be exceeded")
	}
	if exceeded := apiKeyQuotaExceeded(apiKey, decimal.NewFromInt(9), decimal.RequireFromString("1.000001")); !exceeded {
		t.Fatal("expected usage over quota to be exceeded")
	}
	apiKey.QuotaLimit = decimal.Zero
	if exceeded := apiKeyQuotaExceeded(apiKey, decimal.NewFromInt(100), decimal.NewFromInt(100)); exceeded {
		t.Fatal("expected zero quota limit to be unlimited")
	}
}

func TestParseUsageTokensFromStream(t *testing.T) {
	body := []byte("data: {\"choices\":[]}\n\n" +
		"data: {\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":34}}\n\n" +
		"data: [DONE]\n\n")

	usage, ok := parseUsageTokensFromStream(body)
	if !ok {
		t.Fatal("expected usage tokens from stream")
	}
	if usage.InputTokens != 12 || usage.OutputTokens != 34 {
		t.Fatalf("unexpected tokens: input=%d output=%d", usage.InputTokens, usage.OutputTokens)
	}
}

func TestParseUsageTokensIncludesCachedInput(t *testing.T) {
	usage, ok := parseUsageTokens(map[string]interface{}{
		"usage": map[string]interface{}{
			"prompt_tokens":     float64(100),
			"completion_tokens": float64(20),
			"prompt_tokens_details": map[string]interface{}{
				"cached_tokens": float64(40),
			},
		},
	})
	if !ok {
		t.Fatal("expected usage tokens")
	}
	if usage.InputTokens != 100 || usage.OutputTokens != 20 || usage.CachedInputTokens != 40 {
		t.Fatalf("unexpected usage: input=%d output=%d cached=%d", usage.InputTokens, usage.OutputTokens, usage.CachedInputTokens)
	}
}

func TestParseUsageTokensClaudeCacheRead(t *testing.T) {
	usage, ok := parseUsageTokens(map[string]interface{}{
		"usage": map[string]interface{}{
			"input_tokens":                float64(60),
			"cache_creation_input_tokens": float64(10),
			"cache_read_input_tokens":     float64(30),
			"output_tokens":               float64(20),
		},
	})
	if !ok {
		t.Fatal("expected usage tokens")
	}
	if usage.InputTokens != 100 || usage.OutputTokens != 20 || usage.CachedInputTokens != 30 {
		t.Fatalf("unexpected usage: input=%d output=%d cached=%d", usage.InputTokens, usage.OutputTokens, usage.CachedInputTokens)
	}
}

func TestParseUsageTokensDoesNotDoubleCountIncludedCache(t *testing.T) {
	usage, ok := parseUsageTokens(map[string]interface{}{
		"usage": map[string]interface{}{
			"input_tokens":            float64(10244),
			"output_tokens":           float64(169),
			"total_tokens":            float64(10413),
			"cache_read_input_tokens": float64(10240),
		},
	})
	if !ok {
		t.Fatal("expected usage tokens")
	}
	if usage.InputTokens != 10244 || usage.OutputTokens != 169 || usage.CachedInputTokens != 10240 {
		t.Fatalf("unexpected usage: input=%d output=%d cached=%d", usage.InputTokens, usage.OutputTokens, usage.CachedInputTokens)
	}
}

func TestParseUsageTokensDoesNotDoubleCountOpenAICacheWithoutTotal(t *testing.T) {
	usage, ok := parseUsageTokens(map[string]interface{}{
		"usage": map[string]interface{}{
			"prompt_tokens":           float64(10244),
			"completion_tokens":       float64(169),
			"cache_read_input_tokens": float64(10240),
		},
	})
	if !ok {
		t.Fatal("expected usage tokens")
	}
	if usage.InputTokens != 10244 || usage.OutputTokens != 169 || usage.CachedInputTokens != 10240 {
		t.Fatalf("unexpected usage: input=%d output=%d cached=%d", usage.InputTokens, usage.OutputTokens, usage.CachedInputTokens)
	}
}

func TestParseUsageTokensCacheWriteAndModalities(t *testing.T) {
	usage, ok := parseUsageTokens(map[string]interface{}{
		"usage": map[string]interface{}{
			"input_tokens":                   float64(60),
			"cache_creation_input_tokens":    float64(10),
			"cache_creation_1h_input_tokens": float64(5),
			"cache_read_input_tokens":        float64(30),
			"output_tokens":                  float64(20),
			"input_tokens_details": map[string]interface{}{
				"image_tokens": float64(8),
				"audio_tokens": float64(7),
			},
			"output_tokens_details": map[string]interface{}{
				"image_tokens": float64(4),
				"audio_tokens": float64(3),
			},
		},
	})
	if !ok {
		t.Fatal("expected usage tokens")
	}
	if usage.InputTokens != 105 || usage.OutputTokens != 20 {
		t.Fatalf("unexpected total usage: input=%d output=%d", usage.InputTokens, usage.OutputTokens)
	}
	if usage.CacheReadInputTokens != 30 || usage.CacheWriteInputTokens != 10 || usage.CacheWrite1hInputTokens != 5 {
		t.Fatalf("unexpected cache usage: read=%d write=%d write_1h=%d", usage.CacheReadInputTokens, usage.CacheWriteInputTokens, usage.CacheWrite1hInputTokens)
	}
	if usage.ImageInputTokens != 8 || usage.AudioInputTokens != 7 || usage.ImageOutputTokens != 4 || usage.AudioOutputTokens != 3 {
		t.Fatalf("unexpected multimodal usage: image_in=%d audio_in=%d image_out=%d audio_out=%d", usage.ImageInputTokens, usage.AudioInputTokens, usage.ImageOutputTokens, usage.AudioOutputTokens)
	}
}
