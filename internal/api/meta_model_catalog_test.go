package api

import (
	"testing"

	"github.com/veloce-ailab/veloce/internal/service"
	"github.com/shopspring/decimal"
)

func TestAddMetaModelCatalogItemsCanHideReferencedModelsAndCustomizeProvider(t *testing.T) {
	baseInputPrice := decimal.NewFromFloat(1.25)
	baseOutputPrice := decimal.NewFromFloat(2.5)
	baseCachedInputPrice := decimal.NewFromFloat(0.25)
	baseChannel := publicModelUserChannel{
		ID:                        7,
		Name:                      "default",
		Multiplier:                decimal.NewFromInt(1),
		InputPrice:                baseInputPrice,
		OutputPrice:               baseOutputPrice,
		CachedInputPrice:          baseCachedInputPrice,
		EffectiveInputPrice:       baseInputPrice,
		EffectiveOutputPrice:      baseOutputPrice,
		EffectiveCachedInputPrice: baseCachedInputPrice,
	}
	catalog := map[string]*publicModelCatalogAggregate{
		"gpt-4o": {
			publicModelCatalogItem: publicModelCatalogItem{
				ModelName:        "gpt-4o",
				Provider:         "openai",
				ProviderName:     "OpenAI",
				InputPrice:       baseInputPrice,
				OutputPrice:      baseOutputPrice,
				CachedInputPrice: baseCachedInputPrice,
			},
			userChannelMap: map[uint]*publicModelUserChannel{
				baseChannel.ID: &baseChannel,
			},
		},
	}

	addMetaModelCatalogItems(catalog, []service.MetaModelCatalogItem{
		{
			Name:                   "meta-stealth",
			Provider:               "openai",
			ProviderName:           "OpenAI",
			ProviderIconURL:        "https://example.com/openai.png",
			BillingMode:            "actual",
			ExposeReferencedModels: false,
			ReferencedModels:       []string{"gpt-4o"},
		},
	})

	item := catalog["meta-stealth"]
	if item == nil {
		t.Fatal("meta-stealth was not added to catalog")
	}
	if item.Provider != "openai" || item.ProviderName != "OpenAI" || item.ProviderIconURL == "" {
		t.Fatalf("unexpected provider fields: provider=%q provider_name=%q icon=%q", item.Provider, item.ProviderName, item.ProviderIconURL)
	}
	if len(item.ReferencedModels) != 0 {
		t.Fatalf("ReferencedModels length = %d, want 0", len(item.ReferencedModels))
	}
	if _, ok := item.userChannelMap[7]; !ok {
		t.Fatal("hidden referenced models should still be used to derive user channels")
	}
	if !item.InputPrice.Equal(baseInputPrice) || !item.OutputPrice.Equal(baseOutputPrice) || !item.CachedInputPrice.Equal(baseCachedInputPrice) {
		t.Fatalf("unexpected actual billing prices: input=%s output=%s cached=%s", item.InputPrice, item.OutputPrice, item.CachedInputPrice)
	}
}
