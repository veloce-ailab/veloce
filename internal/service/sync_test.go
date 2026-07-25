package service

import (
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func TestParseUpstreamPriceItemsSplitRatios(t *testing.T) {
	body := []byte(`{
		"success": true,
		"data": {
			"model_ratio": {
				"gpt-4o": 2.5,
				"deepseek-chat": "0.7"
			},
			"completion_ratio": {
				"gpt-4o": 3,
				"deepseek-chat": "1"
			}
		}
	}`)

	items, err := parseUpstreamPriceItems(body)
	if err != nil {
		t.Fatalf("parseUpstreamPriceItems returned error: %v", err)
	}

	prices := pricesByModel(items)
	assertPrice(t, prices, "gpt-4o", "2.5", "7.5")
	assertPrice(t, prices, "deepseek-chat", "0.7", "0.7")
}

func TestParseUpstreamPriceItemsModelPriceObjects(t *testing.T) {
	body := []byte(`{
		"data": {
			"model_prices": {
				"gpt-4o": {
					"model_ratio": 2,
					"completion_ratio": 4,
					"cache_ratio": 1
				},
				"claude-3-5-sonnet": {
					"input_price": "0.003",
					"output_price": "0.015",
					"cached_input_price": "0.0015"
				}
			}
		}
	}`)

	items, err := parseUpstreamPriceItems(body)
	if err != nil {
		t.Fatalf("parseUpstreamPriceItems returned error: %v", err)
	}

	prices := pricesByModel(items)
	assertPrice(t, prices, "gpt-4o", "2", "8")
	assertCachedPrice(t, prices, "gpt-4o", "1")
	assertPrice(t, prices, "claude-3-5-sonnet", "0.003", "0.015")
	assertCachedPrice(t, prices, "claude-3-5-sonnet", "0.0015")
}

func TestParseUpstreamPriceItemsOpenAIVideoPrice(t *testing.T) {
	body := []byte(`[
		{
			"model_name": "doubao-seedance-1-5-pro-251215",
			"icon": "Doubao.Color",
			"vendor_id": 7,
			"quota_type": 1,
			"model_ratio": 0,
			"model_price": 0.12,
			"owner_by": "",
			"completion_ratio": 0,
			"enable_groups": ["default"],
			"supported_endpoint_types": ["openai-video"]
		}
	]`)

	items, err := parseUpstreamPriceItems(body)
	if err != nil {
		t.Fatalf("parseUpstreamPriceItems returned error: %v", err)
	}

	prices := pricesByModel(items)
	if prices["doubao-seedance-1-5-pro-251215"].QuotaType != 1 {
		t.Fatalf("quota type = %d, want 1", prices["doubao-seedance-1-5-pro-251215"].QuotaType)
	}
	assertPrice(t, prices, "doubao-seedance-1-5-pro-251215", "0", "0.12")
	assertCachedPrice(t, prices, "doubao-seedance-1-5-pro-251215", "0")
	if !hasEndpointType(prices["doubao-seedance-1-5-pro-251215"].EndpointTypes, "openai-video") {
		t.Fatalf("expected openai-video endpoint type")
	}
}

func TestParseUpstreamPriceItemsSplitCachedPrices(t *testing.T) {
	body := []byte(`{
		"success": true,
		"data": {
			"input_price": { "gpt-4o": 2.5 },
			"output_price": { "gpt-4o": 10 },
			"cached_input_price": { "gpt-4o": 1.25 }
		}
	}`)

	items, err := parseUpstreamPriceItems(body)
	if err != nil {
		t.Fatalf("parseUpstreamPriceItems returned error: %v", err)
	}

	prices := pricesByModel(items)
	assertPrice(t, prices, "gpt-4o", "2.5", "10")
	assertCachedPrice(t, prices, "gpt-4o", "1.25")
}

func TestParseUpstreamPriceItemsSplitQuotaTypes(t *testing.T) {
	body := []byte(`{
		"success": true,
		"data": {
			"quota_type": { "video-model": 1 },
			"input_price": { "video-model": 0 },
			"output_price": { "video-model": 0.12 },
			"cached_input_price": { "video-model": 0 }
		}
	}`)

	items, err := parseUpstreamPriceItems(body)
	if err != nil {
		t.Fatalf("parseUpstreamPriceItems returned error: %v", err)
	}

	prices := pricesByModel(items)
	if prices["video-model"].QuotaType != 1 {
		t.Fatalf("quota type = %d, want 1", prices["video-model"].QuotaType)
	}
	assertPrice(t, prices, "video-model", "0", "0.12")
	assertCachedPrice(t, prices, "video-model", "0")
}

func TestMultiplyTokenPricedModelPricesSkipsPerCallPrices(t *testing.T) {
	items := multiplyTokenPricedModelPrices([]upstreamModelPrice{
		{
			Model:                 "chat-model",
			QuotaType:             0,
			InputPrice:            decimal.RequireFromString("1"),
			OutputPrice:           decimal.RequireFromString("2"),
			CachedInputPrice:      decimal.RequireFromString("0.5"),
			OutputPriceTiers:      model.PriceTierList{{MinTokens: 1, Price: decimal.RequireFromString("0.2")}},
			CachedInputPriceTiers: model.PriceTierList{{MinTokens: 1, Price: decimal.RequireFromString("0.1")}},
		},
		{
			Model:            "video-model",
			QuotaType:        1,
			OutputPrice:      decimal.RequireFromString("0.12"),
			OutputPriceTiers: model.PriceTierList{{MinTokens: 1, Price: decimal.RequireFromString("0.2")}},
		},
	})

	if len(items) != 2 {
		t.Fatalf("item count = %d, want 2", len(items))
	}
	assertDecimalString(t, items[0].InputPrice, "2")
	assertDecimalString(t, items[0].OutputPrice, "4")
	assertDecimalString(t, items[0].CachedInputPrice, "1")
	assertTier(t, items[0].OutputPriceTiers, 1, "0.4")
	assertTier(t, items[0].CachedInputPriceTiers, 1, "0.2")

	if items[1].QuotaType != 1 {
		t.Fatalf("quota type = %d, want 1", items[1].QuotaType)
	}
	assertDecimalString(t, items[1].InputPrice, "0")
	assertDecimalString(t, items[1].OutputPrice, "0.12")
	assertTier(t, items[1].OutputPriceTiers, 1, "0.2")
}

func TestMultiplyTokenPricedModelPricesSkipsBillingExpressions(t *testing.T) {
	items := multiplyTokenPricedModelPrices([]upstreamModelPrice{{
		Model:                     "tiered-model",
		UsesBillingExpression:     true,
		InputPrice:                decimal.NewFromInt(5),
		OutputPrice:               decimal.NewFromInt(30),
		CachedInputPrice:          decimal.RequireFromString("0.5"),
		CacheWriteInputPrice:      decimal.RequireFromString("6.25"),
		InputPriceTiers:           model.PriceTierList{{MinTokens: 272001, Price: decimal.NewFromInt(10)}},
		CacheWriteInputPriceTiers: model.PriceTierList{{MinTokens: 272001, Price: decimal.RequireFromString("12.5")}},
	}})

	assertDecimalString(t, items[0].InputPrice, "5")
	assertDecimalString(t, items[0].OutputPrice, "30")
	assertDecimalString(t, items[0].CachedInputPrice, "0.5")
	assertDecimalString(t, items[0].CacheWriteInputPrice, "6.25")
	assertTier(t, items[0].InputPriceTiers, 272001, "10")
	assertTier(t, items[0].CacheWriteInputPriceTiers, 272001, "12.5")
}

func TestParseUpstreamPriceItemsNewAPITieredExpression(t *testing.T) {
	body := []byte(`{
		"success": true,
		"data": [
			{
				"model_name": "gpt-5.5",
				"model_ratio": 37.5,
				"completion_ratio": 6,
				"billing_mode": "tiered_expr",
				"billing_expr": "p <= 272000 ? tier(\"base\", p * 5 + c * 30 + cr * 0.5) : tier(\"long_context\", p * 10 + c * 45 + cr * 1)"
			}
		]
	}`)

	items, err := parseUpstreamPriceItems(body)
	if err != nil {
		t.Fatalf("parseUpstreamPriceItems returned error: %v", err)
	}

	prices := pricesByModel(items)
	assertPrice(t, prices, "gpt-5.5", "5", "30")
	assertCachedPrice(t, prices, "gpt-5.5", "0.5")
	assertTier(t, prices["gpt-5.5"].InputPriceTiers, 272001, "10")
	assertTier(t, prices["gpt-5.5"].OutputPriceTiers, 272001, "45")
	assertTier(t, prices["gpt-5.5"].CachedInputPriceTiers, 272001, "1")
}

func TestParseUpstreamPriceItemsTieredExpressionWithTotalTokensAndCacheWrite(t *testing.T) {
	body := []byte(`[
		{
			"model_name": "gpt-5.6-sol",
			"quota_type": 0,
			"billing_mode": "tiered_expr",
			"billing_expr": "len <= 272000 ? tier(\"standard\", p * 5 + c * 30 + cr * 0.5 + cc * 6.25) : tier(\"long_context\", p * 10 + c * 45 + cr * 1 + cc * 12.5)"
		}
	]`)

	items, err := parseUpstreamPriceItems(body)
	if err != nil {
		t.Fatalf("parseUpstreamPriceItems returned error: %v", err)
	}

	prices := pricesByModel(items)
	item, ok := prices["gpt-5.6-sol"]
	if !ok {
		t.Fatal("missing gpt-5.6-sol")
	}
	assertDecimalString(t, item.InputPrice, "5")
	assertDecimalString(t, item.OutputPrice, "30")
	assertDecimalString(t, item.CachedInputPrice, "0.5")
	assertDecimalString(t, item.CacheWriteInputPrice, "6.25")
	assertTier(t, item.InputPriceTiers, 272001, "10")
	assertTier(t, item.OutputPriceTiers, 272001, "45")
	assertTier(t, item.CachedInputPriceTiers, 272001, "1")
	assertTier(t, item.CacheWriteInputPriceTiers, 272001, "12.5")
	if item.InputPriceTiers[0].Condition != model.PriceTierConditionFullRequestTokens {
		t.Fatalf("tier condition = %q, want %q", item.InputPriceTiers[0].Condition, model.PriceTierConditionFullRequestTokens)
	}
}

func TestSyncChannelPricesPersistsTieredExpressionAndCacheWritePrice(t *testing.T) {
	previousDB := model.DB
	database, err := gorm.Open(sqlite.Open("file:sync-channel-prices-test?mode=memory&cache=shared"), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := database.AutoMigrate(&model.Model{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	model.DB = database
	t.Cleanup(func() {
		model.DB = previousDB
		sqlDB, _ := database.DB()
		_ = sqlDB.Close()
	})

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/pricing" {
			http.NotFound(writer, request)
			return
		}
		_, _ = writer.Write([]byte(`[
			{
				"model_name": "gpt-5.6-sol",
				"billing_mode": "tiered_expr",
				"billing_expr": "len <= 272000 ? tier(\"standard\", p * 5 + c * 30 + cr * 0.5 + cc * 6.25) : tier(\"long_context\", p * 10 + c * 45 + cr * 1 + cc * 12.5)"
			}
		]`))
	}))
	defer server.Close()

	result := NewSyncService().SyncChannelPrices(&model.Channel{ID: 1, Name: "upstream", BaseURL: server.URL})
	if result.Error != "" {
		t.Fatalf("SyncChannelPrices returned error: %s", result.Error)
	}
	if result.Source != "/api/pricing" || result.Created != 1 {
		t.Fatalf("unexpected sync result: %+v", result)
	}

	var saved model.Model
	if err := database.Where("model_name = ?", "gpt-5.6-sol").First(&saved).Error; err != nil {
		t.Fatalf("load synced model: %v", err)
	}
	assertDecimalString(t, saved.InputPrice, "5")
	assertDecimalString(t, saved.OutputPrice, "30")
	assertDecimalString(t, saved.CachedInputPrice, "0.5")
	assertDecimalString(t, saved.CacheWriteInputPrice, "6.25")
	assertTier(t, saved.InputPriceTiers, 272001, "10")
	assertTier(t, saved.CacheWriteInputPriceTiers, 272001, "12.5")
	if saved.InputPriceTiers[0].Condition != model.PriceTierConditionFullRequestTokens {
		t.Fatalf("stored tier condition = %q, want %q", saved.InputPriceTiers[0].Condition, model.PriceTierConditionFullRequestTokens)
	}
}

func TestPriceSyncPreviewAndApplyDoesNotMultiplyTieredExpression(t *testing.T) {
	previousDB := model.DB
	database, err := gorm.Open(sqlite.Open("file:price-sync-preview-apply-test?mode=memory&cache=shared"), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := database.AutoMigrate(&model.Channel{}, &model.Model{}); err != nil {
		t.Fatalf("migrate database: %v", err)
	}
	model.DB = database
	t.Cleanup(func() {
		model.DB = previousDB
		sqlDB, _ := database.DB()
		_ = sqlDB.Close()
	})

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/pricing" {
			http.NotFound(writer, request)
			return
		}
		_, _ = writer.Write([]byte(`[
			{
				"model_name": "gpt-5.6-sol",
				"vendor_id": 1,
				"quota_type": 0,
				"model_ratio": 37.5,
				"model_price": 0,
				"owner_by": "",
				"completion_ratio": 6,
				"supported_endpoint_types": ["openai"],
				"billing_mode": "tiered_expr",
				"billing_expr": "len \u003C= 272000 ? tier(\"standard\", p * 5 + c * 30 + cr * 0.5 + cc * 6.25) : tier(\"long_context\", p * 10 + c * 45 + cr * 1 + cc * 12.5)"
			}
		]`))
	}))
	defer server.Close()

	channel := model.Channel{Name: "upstream", BaseURL: server.URL, Enabled: true}
	if err := database.Create(&channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}

	service := NewSyncService()
	preview, err := service.PreviewGlobalModelPrices(channel.ID, ModelSyncOptions{})
	if err != nil {
		t.Fatalf("PreviewGlobalModelPrices returned error: %v", err)
	}
	if len(preview.Models) != 1 {
		t.Fatalf("preview model count = %d, want 1", len(preview.Models))
	}
	item := preview.Models[0]
	assertDecimalString(t, item.InputPrice, "5")
	assertDecimalString(t, item.OutputPrice, "30")
	assertDecimalString(t, item.CachedInputPrice, "0.5")
	assertDecimalString(t, item.CacheWriteInputPrice, "6.25")
	assertTier(t, item.InputPriceTiers, 272001, "10")
	assertTier(t, item.OutputPriceTiers, 272001, "45")
	assertTier(t, item.CachedInputPriceTiers, 272001, "1")
	assertTier(t, item.CacheWriteInputPriceTiers, 272001, "12.5")

	if _, err := service.ApplyGlobalModelPrices(channel.ID, preview.Models); err != nil {
		t.Fatalf("ApplyGlobalModelPrices returned error: %v", err)
	}
	var saved model.Model
	if err := database.Where("model_name = ?", "gpt-5.6-sol").First(&saved).Error; err != nil {
		t.Fatalf("load synced model: %v", err)
	}
	assertDecimalString(t, saved.InputPrice, "5")
	assertDecimalString(t, saved.OutputPrice, "30")
	assertDecimalString(t, saved.CachedInputPrice, "0.5")
	assertDecimalString(t, saved.CacheWriteInputPrice, "6.25")
	assertTier(t, saved.InputPriceTiers, 272001, "10")
	assertTier(t, saved.OutputPriceTiers, 272001, "45")
	assertTier(t, saved.CachedInputPriceTiers, 272001, "1")
	assertTier(t, saved.CacheWriteInputPriceTiers, 272001, "12.5")
}

func TestUpstreamURLForPathAvoidsDuplicateV1(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		path    string
		want    string
	}{
		{
			name:    "newapi pricing uses root when base ends with v1",
			baseURL: "https://example.com/v1",
			path:    "/api/pricing",
			want:    "https://example.com/api/pricing",
		},
		{
			name:    "openai models keeps single v1 when base ends with v1",
			baseURL: "https://example.com/v1",
			path:    "/v1/models",
			want:    "https://example.com/v1/models",
		},
		{
			name:    "root base appends openai v1 path",
			baseURL: "https://example.com",
			path:    "/v1/models",
			want:    "https://example.com/v1/models",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := upstreamURLForPath(test.baseURL, test.path)
			if got != test.want {
				t.Fatalf("upstreamURLForPath() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCalculateCostUsesPerMillionPrices(t *testing.T) {
	got := CalculateCost(1000, 2000, 2.5, 7.5, 1, 1)
	want := 0.0175
	if math.Abs(got-want) > 0.000000001 {
		t.Fatalf("CalculateCost() = %v, want %v", got, want)
	}
}

func TestCalculateTieredTokenCost(t *testing.T) {
	got := CalculateTieredTokenCost(1500, decimal.NewFromInt(10), model.PriceTierList{{
		MinTokens: 1000,
		Price:     decimal.NewFromInt(5),
	}})
	want := decimal.RequireFromString("0.0125")
	if !got.Equal(want) {
		t.Fatalf("CalculateTieredTokenCost() = %s, want %s", got.String(), want.String())
	}
}

func TestCalculateTieredTokenCostWithFullInputCondition(t *testing.T) {
	got := CalculateTieredTokenCostWithMetrics(1000, decimal.NewFromInt(10), model.PriceTierList{{
		MinTokens: 2000,
		Price:     decimal.NewFromInt(20),
		Condition: model.PriceTierConditionFullInputTokens,
	}}, PriceTierMetrics{
		FullInputTokens:      3000,
		BillableInputTokens:  1000,
		BillableOutputTokens: 50,
	})
	want := decimal.RequireFromString("0.02")
	if !got.Equal(want) {
		t.Fatalf("CalculateTieredTokenCostWithMetrics() = %s, want %s", got.String(), want.String())
	}
}

func TestCalculateModelUsageCostUsesTotalTokensForExpressionTiers(t *testing.T) {
	tiers := model.PriceTierList{{
		MinTokens: 272001,
		Price:     decimal.NewFromInt(10),
		Condition: model.PriceTierConditionFullRequestTokens,
	}}
	got := calculateModelUsageCost(usageTokenCounts{
		InputTokens:           272000,
		OutputTokens:          1,
		CacheWriteInputTokens: 10,
	}, model.Model{
		InputPrice:                decimal.NewFromInt(5),
		OutputPrice:               decimal.NewFromInt(30),
		CacheWriteInputPrice:      decimal.RequireFromString("6.25"),
		InputPriceTiers:           tiers,
		OutputPriceTiers:          model.PriceTierList{{MinTokens: 272001, Price: decimal.NewFromInt(45), Condition: model.PriceTierConditionFullRequestTokens}},
		CacheWriteInputPriceTiers: model.PriceTierList{{MinTokens: 272001, Price: decimal.RequireFromString("12.5"), Condition: model.PriceTierConditionFullRequestTokens}},
	})
	want := decimal.RequireFromString("2.72007")
	if !got.Equal(want) {
		t.Fatalf("calculateModelUsageCost() = %s, want %s", got.String(), want.String())
	}
}

func TestCalculateModelUsageCostSplitsCacheAndAudio(t *testing.T) {
	got := calculateModelUsageCost(usageTokenCounts{
		InputTokens:             100,
		OutputTokens:            50,
		CachedInputTokens:       20,
		CacheReadInputTokens:    20,
		CacheWriteInputTokens:   10,
		CacheWrite1hInputTokens: 5,
		AudioOutputTokens:       10,
	}, model.Model{
		InputPrice:             decimal.NewFromInt(10),
		OutputPrice:            decimal.NewFromInt(20),
		CachedInputPrice:       decimal.NewFromInt(1),
		CacheWriteInputPrice:   decimal.NewFromInt(2),
		CacheWrite1hInputPrice: decimal.NewFromInt(3),
		AudioOutputPrice:       decimal.NewFromInt(100),
	})
	want := decimal.RequireFromString("0.002505")
	if !got.Equal(want) {
		t.Fatalf("calculateModelUsageCost() = %s, want %s", got.String(), want.String())
	}
}

func pricesByModel(items []upstreamModelPrice) map[string]upstreamModelPrice {
	prices := make(map[string]upstreamModelPrice, len(items))
	for _, item := range items {
		prices[item.Model] = item
	}
	return prices
}

func assertPrice(t *testing.T, prices map[string]upstreamModelPrice, modelName string, inputPrice string, outputPrice string) {
	t.Helper()

	item, ok := prices[modelName]
	if !ok {
		t.Fatalf("missing model %s in parsed prices", modelName)
	}
	assertDecimalString(t, item.InputPrice, inputPrice)
	assertDecimalString(t, item.OutputPrice, outputPrice)
}

func assertCachedPrice(t *testing.T, prices map[string]upstreamModelPrice, modelName string, cachedInputPrice string) {
	t.Helper()

	item, ok := prices[modelName]
	if !ok {
		t.Fatalf("missing model %s in parsed prices", modelName)
	}
	assertDecimalString(t, item.CachedInputPrice, cachedInputPrice)
}

func assertTier(t *testing.T, tiers model.PriceTierList, minTokens int, price string) {
	t.Helper()

	if len(tiers) != 1 {
		t.Fatalf("tier count mismatch: got %d, want 1", len(tiers))
	}
	if tiers[0].MinTokens != minTokens {
		t.Fatalf("tier min tokens mismatch: got %d, want %d", tiers[0].MinTokens, minTokens)
	}
	assertDecimalString(t, tiers[0].Price, price)
}

func assertDecimalString(t *testing.T, got decimal.Decimal, want string) {
	t.Helper()

	expected, err := decimal.NewFromString(want)
	if err != nil {
		t.Fatalf("invalid expected decimal %q: %v", want, err)
	}
	if !got.Equal(expected) {
		t.Fatalf("decimal mismatch: got %s, want %s", got.String(), expected.String())
	}
}
