package service

import (
	"strings"
	"testing"
)

func TestInferModelProviderCoversCommunityCatalog(t *testing.T) {
	cases := map[string]string{
		"Jamba-Large-1.7":               "ai21",
		"wan2.7-t2v":                    "alibaba",
		"inclusionai/ling-2.6-1t":       "antgroup",
		"claude-opus-5":                 "anthropic",
		"ERNIE-5.1":                     "baidu",
		"DeepSeek-V3.2":                 "deepseek",
		"doubao-seed-2-1-pro":           "doubao",
		"flux-2-pro":                    "flux",
		"gemma-4-31b-it":                "google",
		"happyhorse-1.1-t2v":            "happyhorse",
		"Mercury-Coder":                 "inception",
		"jina-embeddings-v5-text-small": "jina",
		"kat-coder-pro-v2.5":            "kwaikat",
		"longcat-2.0":                   "longcat",
		"muse-spark-1.1":                "meta",
		"mj_imagine":                    "midjourney",
		"MiniMax-M3":                    "minimax",
		"kimi-k2.7-code":                "moonshot",
		"embedding-v1":                  "openai",
		"qwen-image-2.0":                "qwen",
		"stepfun-ai/step3":              "stepfun",
		"suno_music":                    "suno",
		"hy3":                           "tencent",
		"mimo-v2.5-pro":                 "xiaomi",
		"glm-5.2-fast-preview":          "zhipu",
		"grok-code-fast-1":              "xai",
	}
	for modelName, want := range cases {
		if got := InferModelProvider(modelName); got != want {
			t.Errorf("InferModelProvider(%q) = %q, want %q", modelName, got, want)
		}
	}
}

func TestModelProviderPresetHasValidIconForCatalogProviders(t *testing.T) {
	for _, provider := range []string{"ai21", "alibaba", "antgroup", "flux", "happyhorse", "inception", "jina", "kwaikat", "longcat", "midjourney", "suno", "xiaomi"} {
		preset, ok := ModelProviderPreset(provider)
		if !ok || preset.IconURL == "" {
			t.Errorf("provider %q has no icon preset", provider)
		}
	}
}

func TestProviderMonogramIconFitsDatabaseColumn(t *testing.T) {
	icon := providerMonogramIcon("HH", "#B45309")
	if len(icon) > 255 {
		t.Fatalf("monogram icon length = %d, exceeds provider_icon_url limit", len(icon))
	}
	if !strings.HasPrefix(icon, "data:image/svg+xml,") {
		t.Fatalf("monogram icon has unexpected URL scheme: %q", icon)
	}
}
