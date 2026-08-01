package service

import (
	"strings"
)

type ModelProvider struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	IconURL string `json:"icon_url"`
}

var modelProviderPresets = []ModelProvider{
	{ID: "openai", Name: "OpenAI", IconURL: "https://cdn.jsdelivr.net/npm/simple-icons@latest/icons/openai.svg"},
	{ID: "deepseek", Name: "DeepSeek", IconURL: "https://cdn.jsdelivr.net/npm/simple-icons@latest/icons/deepseek.svg"},
	{ID: "anthropic", Name: "Anthropic", IconURL: "https://cdn.jsdelivr.net/npm/simple-icons@latest/icons/anthropic.svg"},
	{ID: "google", Name: "Google", IconURL: "https://cdn.jsdelivr.net/npm/simple-icons@latest/icons/google.svg"},
	{ID: "meta", Name: "Meta", IconURL: "https://cdn.jsdelivr.net/npm/simple-icons@latest/icons/meta.svg"},
	{ID: "mistral", Name: "Mistral AI", IconURL: "https://cdn.jsdelivr.net/npm/simple-icons@latest/icons/mistralai.svg"},
	{ID: "qwen", Name: "Qwen", IconURL: "https://cdn.jsdelivr.net/npm/simple-icons@latest/icons/alibabacloud.svg"},
	{ID: "moonshot", Name: "Moonshot AI", IconURL: "https://www.moonshot.cn/favicon.ico"},
	{ID: "zhipu", Name: "Zhipu AI", IconURL: "https://open.bigmodel.cn/favicon.ico"},
	{ID: "xai", Name: "xAI", IconURL: "https://cdn.jsdelivr.net/npm/simple-icons@latest/icons/x.svg"},
	{ID: "groq", Name: "Groq", IconURL: "https://cdn.jsdelivr.net/npm/simple-icons@latest/icons/groq.svg"},
	{ID: "cohere", Name: "Cohere", IconURL: "https://cdn.jsdelivr.net/npm/simple-icons@latest/icons/cohere.svg"},
	{ID: "perplexity", Name: "Perplexity", IconURL: "https://cdn.jsdelivr.net/npm/simple-icons@latest/icons/perplexity.svg"},
	{ID: "minimax", Name: "MiniMax", IconURL: "https://www.minimaxi.com/favicon.ico"},
	{ID: "baichuan", Name: "Baichuan AI", IconURL: "https://www.baichuan-ai.com/favicon.ico"},
	{ID: "stepfun", Name: "StepFun", IconURL: providerMonogramIcon("SF", "#1D4ED8")},
	{ID: "yi", Name: "01.AI", IconURL: "https://www.01.ai/favicon.ico"},
	{ID: "baidu", Name: "Baidu", IconURL: "https://cdn.jsdelivr.net/npm/simple-icons@latest/icons/baidu.svg"},
	{ID: "tencent", Name: "Tencent", IconURL: "https://cloud.tencent.com/favicon.ico"},
	{ID: "doubao", Name: "Doubao", IconURL: "https://www.volcengine.com/favicon.ico"},
	{ID: "siliconflow", Name: "SiliconFlow", IconURL: "https://siliconflow.cn/favicon.ico"},
	{ID: "openrouter", Name: "OpenRouter", IconURL: "https://openrouter.ai/favicon.ico"},
	{ID: "huggingface", Name: "Hugging Face", IconURL: "https://cdn.jsdelivr.net/npm/simple-icons@latest/icons/huggingface.svg"},
	{ID: "together", Name: "Together AI", IconURL: providerMonogramIcon("TA", "#6D28D9")},
	{ID: "fireworks", Name: "Fireworks AI", IconURL: "https://fireworks.ai/favicon.ico"},
	{ID: "cloudflare", Name: "Cloudflare", IconURL: "https://cdn.jsdelivr.net/npm/simple-icons@latest/icons/cloudflare.svg"},
	{ID: "ollama", Name: "Ollama", IconURL: "https://cdn.jsdelivr.net/npm/simple-icons@latest/icons/ollama.svg"},
	{ID: "jina", Name: "Jina AI", IconURL: providerMonogramIcon("JI", "#DC2626")},
	{ID: "ai21", Name: "Ai21Labs", IconURL: "https://www.ai21.com/favicon.ico"},
	{ID: "alibaba", Name: "AlibabaCloud", IconURL: "https://cdn.jsdelivr.net/npm/simple-icons@latest/icons/alibabacloud.svg"},
	{ID: "antgroup", Name: "AntGroup", IconURL: providerMonogramIcon("AG", "#1677FF")},
	{ID: "flux", Name: "Flux", IconURL: "https://cdn.jsdelivr.net/npm/simple-icons@latest/icons/flux.svg"},
	{ID: "happyhorse", Name: "HappyHorse", IconURL: providerMonogramIcon("HH", "#B45309")},
	{ID: "inception", Name: "Inception Labs", IconURL: providerMonogramIcon("IL", "#0F766E")},
	{ID: "kwaikat", Name: "KwaiKAT", IconURL: "https://www.kwai.com/favicon.ico"},
	{ID: "longcat", Name: "Longcat", IconURL: "https://longcat.chat/favicon.ico"},
	{ID: "midjourney", Name: "Midjourney", IconURL: providerMonogramIcon("MJ", "#111827")},
	{ID: "suno", Name: "Suno", IconURL: "https://cdn.jsdelivr.net/npm/simple-icons@latest/icons/suno.svg"},
	{ID: "xiaomi", Name: "Xiaomi Mimo", IconURL: "https://cdn.jsdelivr.net/npm/simple-icons@latest/icons/xiaomi.svg"},
	{ID: "custom", Name: "Custom", IconURL: ""},
}

func providerMonogramIcon(label, color string) string {
	// Keep the URL below the provider_icon_url VARCHAR(255) storage limit.
	color = strings.TrimPrefix(color, "#")
	return `data:image/svg+xml,<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><path fill="%23` + color + `" d="M0 0h1v1H0z"/><text x=".5" y=".65" fill="white" font-size=".4" text-anchor="middle">` + label + `</text></svg>`
}

func ModelProviderPresets() []ModelProvider {
	presets := make([]ModelProvider, len(modelProviderPresets))
	copy(presets, modelProviderPresets)
	return presets
}

func ResolveModelProvider(modelName, provider, customIconURL string) ModelProvider {
	provider = strings.TrimSpace(provider)
	customIconURL = strings.TrimSpace(customIconURL)
	if provider == "" {
		provider = InferModelProvider(modelName)
	}

	if preset, ok := ModelProviderPreset(provider); ok {
		if customIconURL != "" {
			preset.IconURL = customIconURL
		}
		return preset
	}

	return ModelProvider{
		ID:      provider,
		Name:    provider,
		IconURL: customIconURL,
	}
}

func ModelProviderPreset(provider string) (ModelProvider, bool) {
	normalized := normalizeProvider(provider)
	for _, preset := range modelProviderPresets {
		if preset.ID == normalized || normalizeProvider(preset.Name) == normalized {
			return preset, true
		}
	}
	return ModelProvider{}, false
}

func InferModelProvider(modelName string) string {
	name := strings.ToLower(strings.TrimSpace(modelName))
	switch {
	case strings.Contains(name, "jamba"), strings.Contains(name, "ai21"):
		return "ai21"
	case strings.Contains(name, "wan"):
		return "alibaba"
	case strings.Contains(name, "inclusionai"), strings.HasPrefix(name, "ling-"), strings.HasPrefix(name, "ring-"):
		return "antgroup"
	case strings.Contains(name, "deepseek"):
		return "deepseek"
	case strings.Contains(name, "claude"):
		return "anthropic"
	case strings.Contains(name, "gemini"), strings.Contains(name, "gemma"), strings.Contains(name, "veo"), strings.Contains(name, "lyria"), strings.Contains(name, "palm"), strings.Contains(name, "bison"):
		return "google"
	case strings.Contains(name, "llama"), strings.Contains(name, "muse-spark"):
		return "meta"
	case strings.Contains(name, "mistral"), strings.Contains(name, "mixtral"), strings.Contains(name, "codestral"):
		return "mistral"
	case strings.Contains(name, "qwen"), strings.Contains(name, "qwq"):
		return "qwen"
	case strings.Contains(name, "kimi"), strings.Contains(name, "moonshot"):
		return "moonshot"
	case strings.Contains(name, "glm"), strings.Contains(name, "zhipu"):
		return "zhipu"
	case strings.Contains(name, "grok"), strings.Contains(name, "xai"), strings.Contains(name, "x-ai"):
		return "xai"
	case strings.Contains(name, "groq"):
		return "groq"
	case strings.Contains(name, "command"), strings.Contains(name, "cohere"):
		return "cohere"
	case strings.Contains(name, "sonar"), strings.Contains(name, "perplexity"):
		return "perplexity"
	case strings.Contains(name, "flux"), strings.Contains(name, "black-forest"), strings.HasPrefix(name, "bfl-"):
		return "flux"
	case strings.Contains(name, "happyhorse"):
		return "happyhorse"
	case strings.Contains(name, "mercury"):
		return "inception"
	case strings.Contains(name, "kat-coder"), strings.Contains(name, "kwaikat"):
		return "kwaikat"
	case strings.Contains(name, "longcat"):
		return "longcat"
	case strings.HasPrefix(name, "mj_"), strings.Contains(name, "midjourney"):
		return "midjourney"
	case strings.Contains(name, "abab"), strings.Contains(name, "minimax"):
		return "minimax"
	case strings.Contains(name, "baichuan"):
		return "baichuan"
	case strings.Contains(name, "stepfun"), strings.HasPrefix(name, "step-"), strings.HasPrefix(name, "step_"):
		return "stepfun"
	case strings.HasPrefix(name, "yi-"), strings.HasPrefix(name, "yi_"), strings.Contains(name, "01-ai"), strings.Contains(name, "lingyi"):
		return "yi"
	case strings.Contains(name, "ernie"), strings.Contains(name, "wenxin"), strings.Contains(name, "baidu"):
		return "baidu"
	case name == "hy3", strings.Contains(name, "hunyuan"), strings.Contains(name, "tencent"):
		return "tencent"
	case strings.Contains(name, "doubao"), strings.Contains(name, "volcengine"), strings.Contains(name, "volc-"):
		return "doubao"
	case strings.Contains(name, "siliconflow"):
		return "siliconflow"
	case strings.Contains(name, "openrouter"):
		return "openrouter"
	case strings.Contains(name, "huggingface"), strings.Contains(name, "hf-"):
		return "huggingface"
	case strings.Contains(name, "together"):
		return "together"
	case strings.Contains(name, "fireworks"):
		return "fireworks"
	case strings.Contains(name, "cloudflare"):
		return "cloudflare"
	case strings.Contains(name, "ollama"):
		return "ollama"
	case strings.Contains(name, "jina"):
		return "jina"
	case strings.HasPrefix(name, "suno_"), strings.HasPrefix(name, "suno-"):
		return "suno"
	case strings.Contains(name, "mimo"):
		return "xiaomi"
	case name == "embedding-v1", strings.Contains(name, "gpt"), strings.Contains(name, "dall-e"), strings.Contains(name, "dalle"), strings.Contains(name, "o1"), strings.Contains(name, "o3"), strings.Contains(name, "o4"), strings.Contains(name, "text-embedding"), strings.Contains(name, "whisper"), strings.HasPrefix(name, "tts-"):
		return "openai"
	default:
		return "custom"
	}
}

func normalizeProvider(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	provider = strings.ReplaceAll(provider, " ", "")
	provider = strings.ReplaceAll(provider, "-", "")
	provider = strings.ReplaceAll(provider, "_", "")
	return provider
}
