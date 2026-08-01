package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"github.com/veloce-ailab/veloce/internal/model"
	"gorm.io/gorm"
)

type SyncService struct {
	// Add config for multiple upstreams if needed
}

// CommunityPricingBaseURL 社区（veloce-community/shequ）站点地址，
// 作为价格同步的内置来源：社区维护的是参考零售价，按原价应用、不做加价。
const CommunityPricingBaseURL = "https://veloce-community.flweb.cn"

var (
	tieredExprThresholdPattern = regexp.MustCompile(`(?i)\b(p|len)\s*(<=|<|>=|>)\s*([0-9]+)`)
	tieredExprTierPattern      = regexp.MustCompile(`(?is)tier\s*\(\s*"[^"]*"\s*,\s*(.*?)\s*\)`)
)

func NewSyncService() *SyncService {
	return &SyncService{}
}

// StartSyncLoop registers the per-channel price sync with the unified
// scheduler; channel cron expressions decide which channels are due each scan.
func (s *SyncService) StartSyncLoop() {
	RegisterScheduledJob(ScheduledJob{
		Name:        "price_sync",
		Description: "按渠道 Cron 计划同步上游模型价格",
		PrimaryOnly: true,
		Interval:    func() time.Duration { return time.Minute },
		Run: func(ctx context.Context) (string, bool, error) {
			results := s.SyncScheduledPrices()
			if len(results) == 0 {
				return "", false, nil
			}
			failed := 0
			for _, result := range results {
				if result.Error != "" {
					failed++
				}
			}
			message := fmt.Sprintf("同步 %d 个渠道", len(results))
			if failed > 0 {
				return message, true, fmt.Errorf("%d/%d 个渠道同步失败", failed, len(results))
			}
			return message, true, nil
		},
	})
}

type ChannelSyncResult struct {
	ChannelID   uint   `json:"channel_id"`
	ChannelName string `json:"channel_name"`
	Source      string `json:"source"`
	Created     int    `json:"created"`
	Updated     int    `json:"updated"`
	Error       string `json:"error,omitempty"`
}

type upstreamModelPrice struct {
	Model                     string
	EndpointTypes             []string
	QuotaType                 int
	UsesBillingExpression     bool
	InputPrice                decimal.Decimal
	OutputPrice               decimal.Decimal
	CachedInputPrice          decimal.Decimal
	CacheWriteInputPrice      decimal.Decimal
	InputPriceTiers           model.PriceTierList
	OutputPriceTiers          model.PriceTierList
	CachedInputPriceTiers     model.PriceTierList
	CacheWriteInputPriceTiers model.PriceTierList
}

type ModelSyncOptions struct {
	Format string
	Path   string
}

type ModelSyncPreview struct {
	ChannelID   uint            `json:"channel_id"`
	ChannelName string          `json:"channel_name"`
	Source      string          `json:"source"`
	Models      []ModelSyncItem `json:"models"`
}

type ModelSyncItem struct {
	ModelName                 string              `json:"model_name"`
	QuotaType                 int                 `json:"quota_type"`
	InputPrice                decimal.Decimal     `json:"input_price"`
	OutputPrice               decimal.Decimal     `json:"output_price"`
	CachedInputPrice          decimal.Decimal     `json:"cached_input_price"`
	CacheWriteInputPrice      decimal.Decimal     `json:"cache_write_input_price"`
	InputPriceTiers           model.PriceTierList `json:"input_price_tiers"`
	OutputPriceTiers          model.PriceTierList `json:"output_price_tiers"`
	CachedInputPriceTiers     model.PriceTierList `json:"cached_input_price_tiers"`
	CacheWriteInputPriceTiers model.PriceTierList `json:"cache_write_input_price_tiers"`
	Provider                  string              `json:"provider"`
	ProviderName              string              `json:"provider_name"`
	ProviderIconURL           string              `json:"provider_icon_url"`
	Exists                    bool                `json:"exists"`
}

func (s *SyncService) SyncAll() []ChannelSyncResult {
	results, err := s.SyncChannels(nil)
	if err != nil {
		log.Printf("Failed to load channels for sync: %v", err)
		return []ChannelSyncResult{{Error: err.Error()}}
	}
	return results
}

func (s *SyncService) SyncChannels(channelIDs []uint) ([]ChannelSyncResult, error) {
	var channels []model.Channel
	query := model.DB
	if len(channelIDs) > 0 {
		query = query.Where("id IN ?", channelIDs)
	} else {
		query = query.Where("enabled = ?", true)
	}
	if err := query.Find(&channels).Error; err != nil {
		return nil, err
	}

	results := make([]ChannelSyncResult, 0, len(channels))
	for _, channel := range channels {
		log.Printf("Syncing prices for channel: %s", channel.Name)
		result := s.SyncChannel(&channel)
		results = append(results, result)
	}
	return results, nil
}

func (s *SyncService) SyncChannel(channel *model.Channel) ChannelSyncResult {
	result := ChannelSyncResult{
		ChannelID:   channel.ID,
		ChannelName: channel.Name,
	}

	items, source, err := s.fetchUpstreamAvailableModels(channel, ModelSyncOptions{})
	if err != nil {
		result.Error = err.Error()
		log.Printf("Failed to sync prices for %s: %v", channel.Name, err)
		return result
	}
	result.Source = source

	for _, item := range items {
		item.Model = strings.TrimSpace(item.Model)
		if item.Model == "" {
			continue
		}

		globalModel, provider, err := ensureGlobalModel(item.Model, "", "")
		if err != nil {
			result.Error = err.Error()
			return result
		}

		var modelConfig model.ModelConfig
		err = model.DB.Where("channel_id = ? AND model_id = ?", channel.ID, globalModel.ID).First(&modelConfig).Error
		if err == nil {
			updates := map[string]interface{}{
				"upstream_model_name": item.Model,
				"enabled":             true,
			}
			if err := model.DB.Model(&modelConfig).Updates(updates).Error; err != nil {
				result.Error = err.Error()
				return result
			}
			result.Updated++
		} else if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := model.DB.Create(&model.ModelConfig{
				ChannelID:         channel.ID,
				ModelID:           globalModel.ID,
				Model:             globalModel,
				UpstreamModelName: item.Model,
				Enabled:           true,
				ModelName:         globalModel.ModelName,
				Provider:          provider.ID,
				ProviderIconURL:   provider.IconURL,
			}).Error; err != nil {
				result.Error = err.Error()
				return result
			}
			result.Created++
		} else {
			result.Error = err.Error()
			return result
		}
	}

	return result
}

// SyncChannelPrices pulls upstream pricing and applies it to the global model catalog.
// Channel model discovery remains separate because it uses a different upstream endpoint.
func (s *SyncService) SyncChannelPrices(channel *model.Channel) ChannelSyncResult {
	result := ChannelSyncResult{
		ChannelID:   channel.ID,
		ChannelName: channel.Name,
	}

	items, source, err := s.fetchUpstreamModelPrices(channel, ModelSyncOptions{})
	if err != nil {
		result.Error = err.Error()
		log.Printf("Failed to sync prices for %s: %v", channel.Name, err)
		return result
	}
	result.Source = source

	for _, item := range multiplyTokenPricedModelPrices(items) {
		modelName := strings.TrimSpace(item.Model)
		if modelName == "" {
			continue
		}
		provider := ResolveModelProvider(modelName, "", "")
		if err := upsertGlobalModelPrice(modelName, item.QuotaType, item.InputPrice, item.OutputPrice, item.CachedInputPrice, item.CacheWriteInputPrice, item.InputPriceTiers, item.OutputPriceTiers, item.CachedInputPriceTiers, item.CacheWriteInputPriceTiers, provider, &result); err != nil {
			result.Error = err.Error()
			return result
		}
	}

	return result
}

func (s *SyncService) PreviewChannelModels(channelID uint, options ModelSyncOptions) (ModelSyncPreview, error) {
	var channel model.Channel
	if err := model.DB.First(&channel, channelID).Error; err != nil {
		return ModelSyncPreview{}, err
	}

	items, source, err := s.fetchUpstreamAvailableModels(&channel, options)
	if err != nil {
		return ModelSyncPreview{}, err
	}

	return s.buildModelSyncPreview(&channel, source, items)
}

func (s *SyncService) PreviewChannelModelsFromBody(channelID uint, source string, body []byte) (ModelSyncPreview, error) {
	var channel model.Channel
	if err := model.DB.First(&channel, channelID).Error; err != nil {
		return ModelSyncPreview{}, err
	}

	items, err := parseUpstreamPriceItems(body)
	if err != nil {
		return ModelSyncPreview{}, err
	}
	if strings.TrimSpace(source) == "" {
		source = "browser"
	}
	return s.buildModelSyncPreview(&channel, source, items)
}

func (s *SyncService) buildModelSyncPreview(channel *model.Channel, source string, items []upstreamModelPrice) (ModelSyncPreview, error) {
	var existing []model.ModelConfig
	if err := model.DB.Preload("Model").Where("channel_id = ?", channel.ID).Find(&existing).Error; err != nil {
		return ModelSyncPreview{}, err
	}
	existingModels := map[string]struct{}{}
	for _, item := range existing {
		if strings.TrimSpace(item.Model.ModelName) != "" {
			existingModels[item.Model.ModelName] = struct{}{}
		}
	}

	seen := map[string]struct{}{}
	previewItems := make([]ModelSyncItem, 0, len(items))
	for _, item := range items {
		modelName := strings.TrimSpace(item.Model)
		if modelName == "" {
			continue
		}
		if _, ok := seen[modelName]; ok {
			continue
		}
		seen[modelName] = struct{}{}
		provider := ResolveModelProvider(modelName, "", "")
		_, exists := existingModels[modelName]
		previewItems = append(previewItems, ModelSyncItem{
			ModelName:                 modelName,
			InputPrice:                decimal.Zero,
			OutputPrice:               decimal.Zero,
			CachedInputPrice:          decimal.Zero,
			CacheWriteInputPrice:      decimal.Zero,
			InputPriceTiers:           nil,
			OutputPriceTiers:          nil,
			CachedInputPriceTiers:     nil,
			CacheWriteInputPriceTiers: nil,
			Provider:                  provider.ID,
			ProviderName:              provider.Name,
			ProviderIconURL:           provider.IconURL,
			Exists:                    exists,
		})
	}
	sort.Slice(previewItems, func(i, j int) bool {
		return previewItems[i].ModelName < previewItems[j].ModelName
	})

	return ModelSyncPreview{
		ChannelID:   channel.ID,
		ChannelName: channel.Name,
		Source:      source,
		Models:      previewItems,
	}, nil
}

func (s *SyncService) ApplyChannelModels(channelID uint, items []ModelSyncItem) (ChannelSyncResult, error) {
	var channel model.Channel
	if err := model.DB.First(&channel, channelID).Error; err != nil {
		return ChannelSyncResult{}, err
	}

	result := ChannelSyncResult{
		ChannelID:   channel.ID,
		ChannelName: channel.Name,
		Source:      "selected",
	}

	for _, item := range items {
		modelName := strings.TrimSpace(item.ModelName)
		if modelName == "" {
			continue
		}
		provider := ResolveModelProvider(modelName, item.Provider, item.ProviderIconURL)
		if err := upsertChannelModel(&channel, modelName, provider, &result); err != nil {
			result.Error = err.Error()
			return result, err
		}
	}

	return result, nil
}

func (s *SyncService) PreviewGlobalModelPrices(channelID uint, options ModelSyncOptions) (ModelSyncPreview, error) {
	var channel model.Channel
	if err := model.DB.First(&channel, channelID).Error; err != nil {
		return ModelSyncPreview{}, err
	}

	items, err := s.fetchNewAPICompatiblePrices(&channel)
	if err != nil {
		return ModelSyncPreview{}, err
	}
	items = multiplyTokenPricedModelPrices(items)

	return s.buildGlobalPriceSyncPreview(&channel, "/api/pricing", items)
}

func (s *SyncService) PreviewGlobalModelPricesFromBody(channelID uint, source string, body []byte) (ModelSyncPreview, error) {
	var channel model.Channel
	if err := model.DB.First(&channel, channelID).Error; err != nil {
		return ModelSyncPreview{}, err
	}

	items, err := parseUpstreamPriceItems(body)
	if err != nil {
		return ModelSyncPreview{}, err
	}
	if strings.TrimSpace(source) == "" {
		source = "browser"
	}
	items = multiplyTokenPricedModelPrices(items)
	return s.buildGlobalPriceSyncPreview(&channel, source, items)
}

func (s *SyncService) buildGlobalPriceSyncPreview(channel *model.Channel, source string, items []upstreamModelPrice) (ModelSyncPreview, error) {
	var existing []model.Model
	if err := model.DB.Find(&existing).Error; err != nil {
		return ModelSyncPreview{}, err
	}
	existingModels := map[string]struct{}{}
	for _, item := range existing {
		if strings.TrimSpace(item.ModelName) != "" {
			existingModels[item.ModelName] = struct{}{}
		}
	}

	seen := map[string]struct{}{}
	previewItems := make([]ModelSyncItem, 0, len(items))
	for _, item := range items {
		modelName := strings.TrimSpace(item.Model)
		if modelName == "" {
			continue
		}
		if _, ok := seen[modelName]; ok {
			continue
		}
		seen[modelName] = struct{}{}
		provider := ResolveModelProvider(modelName, "", "")
		_, exists := existingModels[modelName]
		previewItems = append(previewItems, ModelSyncItem{
			ModelName:                 modelName,
			QuotaType:                 item.QuotaType,
			InputPrice:                item.InputPrice,
			OutputPrice:               item.OutputPrice,
			CachedInputPrice:          item.CachedInputPrice,
			CacheWriteInputPrice:      item.CacheWriteInputPrice,
			InputPriceTiers:           item.InputPriceTiers,
			OutputPriceTiers:          item.OutputPriceTiers,
			CachedInputPriceTiers:     item.CachedInputPriceTiers,
			CacheWriteInputPriceTiers: item.CacheWriteInputPriceTiers,
			Provider:                  provider.ID,
			ProviderName:              provider.Name,
			ProviderIconURL:           provider.IconURL,
			Exists:                    exists,
		})
	}
	sort.Slice(previewItems, func(i, j int) bool {
		return previewItems[i].ModelName < previewItems[j].ModelName
	})

	return ModelSyncPreview{
		ChannelID:   channel.ID,
		ChannelName: channel.Name,
		Source:      source,
		Models:      previewItems,
	}, nil
}

func (s *SyncService) ApplyGlobalModelPrices(channelID uint, items []ModelSyncItem) (ChannelSyncResult, error) {
	var channel model.Channel
	if err := model.DB.First(&channel, channelID).Error; err != nil {
		return ChannelSyncResult{}, err
	}

	result := ChannelSyncResult{
		ChannelID:   channel.ID,
		ChannelName: channel.Name,
		Source:      "prices",
	}
	if err := applyGlobalPriceItems(items, &result); err != nil {
		return result, err
	}
	return result, nil
}

// PreviewCommunityModelPrices 从社区站点拉取模型价格预览。
// 社区价格为参考零售价，与渠道同步不同：按原价应用，不经过加价倍率。
func (s *SyncService) PreviewCommunityModelPrices() (ModelSyncPreview, error) {
	channel := &model.Channel{Name: "community", BaseURL: CommunityPricingBaseURL}
	items, err := s.fetchNewAPICompatiblePrices(channel)
	if err != nil {
		return ModelSyncPreview{}, err
	}
	return s.buildGlobalPriceSyncPreview(channel, "community:/api/pricing", items)
}

// ApplyCommunityModelPrices 将社区价格预览中选中的模型写入全局模型目录
func (s *SyncService) ApplyCommunityModelPrices(items []ModelSyncItem) (ChannelSyncResult, error) {
	result := ChannelSyncResult{
		ChannelName: "community",
		Source:      "community",
	}
	if err := applyGlobalPriceItems(items, &result); err != nil {
		return result, err
	}
	return result, nil
}

// applyGlobalPriceItems 把一组同步项逐个 upsert 到全局模型目录（渠道与社区来源共用）
func applyGlobalPriceItems(items []ModelSyncItem, result *ChannelSyncResult) error {
	for _, item := range items {
		modelName := strings.TrimSpace(item.ModelName)
		if modelName == "" {
			continue
		}
		provider := ResolveModelProvider(modelName, item.Provider, item.ProviderIconURL)
		if err := upsertGlobalModelPrice(modelName, item.QuotaType, item.InputPrice, item.OutputPrice, item.CachedInputPrice, item.CacheWriteInputPrice, item.InputPriceTiers, item.OutputPriceTiers, item.CachedInputPriceTiers, item.CacheWriteInputPriceTiers, provider, result); err != nil {
			result.Error = err.Error()
			return err
		}
	}
	return nil
}

func upsertChannelModel(channel *model.Channel, modelName string, provider ModelProvider, result *ChannelSyncResult) error {
	globalModel, provider, err := ensureGlobalModel(modelName, provider.ID, provider.IconURL)
	if err != nil {
		return err
	}

	var modelConfig model.ModelConfig
	err = model.DB.Where("channel_id = ? AND model_id = ?", channel.ID, globalModel.ID).First(&modelConfig).Error
	if err == nil {
		updates := map[string]interface{}{
			"upstream_model_name": modelName,
			"enabled":             true,
		}
		if err := model.DB.Model(&modelConfig).Updates(updates).Error; err != nil {
			return err
		}
		result.Updated++
		return nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := model.DB.Create(&model.ModelConfig{
			ChannelID:         channel.ID,
			ModelID:           globalModel.ID,
			Model:             globalModel,
			UpstreamModelName: modelName,
			Enabled:           true,
			ModelName:         globalModel.ModelName,
			Provider:          provider.ID,
			ProviderIconURL:   provider.IconURL,
		}).Error; err != nil {
			return err
		}
		result.Created++
		return nil
	}
	return err
}

func upsertGlobalModelPrice(modelName string, quotaType int, inputPrice decimal.Decimal, outputPrice decimal.Decimal, cachedInputPrice decimal.Decimal, cacheWriteInputPrice decimal.Decimal, inputPriceTiers model.PriceTierList, outputPriceTiers model.PriceTierList, cachedInputPriceTiers model.PriceTierList, cacheWriteInputPriceTiers model.PriceTierList, provider ModelProvider, result *ChannelSyncResult) error {
	quotaType = normalizeQuotaType(quotaType)
	var globalModel model.Model
	queryResult := model.DB.Where(&model.Model{ModelName: modelName}).Limit(1).Find(&globalModel)
	if queryResult.Error != nil {
		return queryResult.Error
	}
	if queryResult.RowsAffected == 0 {
		if err := model.DB.Create(&model.Model{
			ModelName:                 modelName,
			Provider:                  provider.ID,
			ProviderIconURL:           provider.IconURL,
			QuotaType:                 quotaType,
			InputPrice:                inputPrice,
			OutputPrice:               outputPrice,
			CachedInputPrice:          cachedInputPrice,
			CacheWriteInputPrice:      cacheWriteInputPrice,
			InputPriceTiers:           model.NormalizePriceTiers(inputPriceTiers),
			OutputPriceTiers:          model.NormalizePriceTiers(outputPriceTiers),
			CachedInputPriceTiers:     model.NormalizePriceTiers(cachedInputPriceTiers),
			CacheWriteInputPriceTiers: model.NormalizePriceTiers(cacheWriteInputPriceTiers),
			Enabled:                   true,
		}).Error; err != nil {
			return err
		}
		result.Created++
		return nil
	}

	updates := map[string]interface{}{
		"quota_type":                    quotaType,
		"input_price":                   inputPrice,
		"output_price":                  outputPrice,
		"cached_input_price":            cachedInputPrice,
		"cache_write_input_price":       cacheWriteInputPrice,
		"input_price_tiers":             model.NormalizePriceTiers(inputPriceTiers),
		"output_price_tiers":            model.NormalizePriceTiers(outputPriceTiers),
		"cached_input_price_tiers":      model.NormalizePriceTiers(cachedInputPriceTiers),
		"cache_write_input_price_tiers": model.NormalizePriceTiers(cacheWriteInputPriceTiers),
	}
	// 同步来源的非 Custom 提供商由模型名自动推断，必须同时刷新分类和图标，
	// 否则旧的错误分类或失效图标会一直保留在已有模型上。
	if strings.TrimSpace(provider.ID) != "" && strings.TrimSpace(provider.ID) != "custom" {
		updates["provider"] = provider.ID
		if strings.TrimSpace(provider.IconURL) != "" {
			updates["provider_icon_url"] = provider.IconURL
		}
	} else if strings.TrimSpace(globalModel.Provider) == "" && strings.TrimSpace(provider.ID) != "" {
		updates["provider"] = provider.ID
		if strings.TrimSpace(provider.IconURL) != "" {
			updates["provider_icon_url"] = provider.IconURL
		}
	}
	if err := model.DB.Model(&globalModel).Updates(updates).Error; err != nil {
		return err
	}
	result.Updated++
	return nil
}

func ensureGlobalModel(modelName, providerID, iconURL string) (model.Model, ModelProvider, error) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return model.Model{}, ModelProvider{}, errors.New("model name is required")
	}

	provider := ResolveModelProvider(modelName, providerID, iconURL)
	globalModel := model.Model{ModelName: modelName}
	if err := model.DB.Where(&model.Model{ModelName: modelName}).
		Attrs(model.Model{
			Provider:        provider.ID,
			ProviderIconURL: provider.IconURL,
			Enabled:         true,
		}).
		FirstOrCreate(&globalModel).Error; err != nil {
		return model.Model{}, provider, err
	}

	updates := map[string]interface{}{}
	if strings.TrimSpace(globalModel.Provider) == "" && strings.TrimSpace(provider.ID) != "" {
		updates["provider"] = provider.ID
	}
	if strings.TrimSpace(globalModel.ProviderIconURL) == "" && strings.TrimSpace(provider.IconURL) != "" {
		updates["provider_icon_url"] = provider.IconURL
	}
	if len(updates) > 0 {
		if err := model.DB.Model(&globalModel).Updates(updates).Error; err != nil {
			return model.Model{}, provider, err
		}
		if value, ok := updates["provider"].(string); ok {
			globalModel.Provider = value
		}
		if value, ok := updates["provider_icon_url"].(string); ok {
			globalModel.ProviderIconURL = value
		}
	}

	return globalModel, provider, nil
}

func (s *SyncService) fetchUpstreamAvailableModels(channel *model.Channel, options ModelSyncOptions) ([]upstreamModelPrice, string, error) {
	format := strings.ToLower(strings.TrimSpace(options.Format))
	if format == "" {
		format = "auto"
	}

	switch format {
	case "auto":
		return s.fetchAutoAvailableModels(channel)
	case "openai_models", "openai":
		items, err := s.fetchOpenAIModelList(channel)
		return items, "/v1/models", err
	case "generic_models", "models":
		items, err := s.fetchGenericModelPath(channel, "/models")
		return items, "/models", err
	case "api_models", "oneapi_models":
		items, err := s.fetchGenericModelPath(channel, "/api/models")
		return items, "/api/models", err
	case "custom":
		path := normalizeSyncPath(options.Path)
		if path == "" {
			return nil, "", errors.New("custom sync path is required")
		}
		items, err := s.fetchGenericModelPath(channel, path)
		return items, path, err
	default:
		return nil, "", fmt.Errorf("unsupported model list sync format: %s", options.Format)
	}
}

func (s *SyncService) fetchAutoAvailableModels(channel *model.Channel) ([]upstreamModelPrice, string, error) {
	sources := []struct {
		path  string
		fetch func(*model.Channel) ([]upstreamModelPrice, error)
	}{
		{path: "/v1/models", fetch: s.fetchOpenAIModelList},
		{path: "/models", fetch: func(channel *model.Channel) ([]upstreamModelPrice, error) {
			return s.fetchGenericModelPath(channel, "/models")
		}},
		{path: "/api/models", fetch: func(channel *model.Channel) ([]upstreamModelPrice, error) {
			return s.fetchGenericModelPath(channel, "/api/models")
		}},
	}

	failures := make([]string, 0, len(sources))
	for _, source := range sources {
		items, err := source.fetch(channel)
		if err == nil && len(items) > 0 {
			return items, source.path, nil
		}
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", source.path, err))
			continue
		}
		failures = append(failures, fmt.Sprintf("%s: upstream returned no models", source.path))
	}
	if len(failures) > 0 {
		return nil, "", fmt.Errorf("all model list sources failed: %s", strings.Join(failures, "; "))
	}
	return nil, "", errors.New("upstream returned no models")
}

func (s *SyncService) fetchUpstreamModelPrices(channel *model.Channel, options ModelSyncOptions) ([]upstreamModelPrice, string, error) {
	format := strings.ToLower(strings.TrimSpace(options.Format))
	if format == "" {
		format = "auto"
	}

	switch format {
	case "auto":
		return s.fetchAutoModelPrices(channel)
	case "newapi_prices", "newapi_pricing", "api_pricing":
		items, err := s.fetchNewAPICompatiblePrices(channel)
		return items, "/api/pricing", err
	case "api_prices", "legacy_api_prices":
		items, err := s.fetchGenericModelPath(channel, "/api/prices")
		return items, "/api/prices", err
	case "generic_models", "models":
		items, err := s.fetchGenericModelPath(channel, "/models")
		return items, "/models", err
	case "api_models", "oneapi_models":
		items, err := s.fetchGenericModelPath(channel, "/api/models")
		return items, "/api/models", err
	case "custom":
		path := normalizeSyncPath(options.Path)
		if path == "" {
			return nil, "", errors.New("custom sync path is required")
		}
		items, err := s.fetchGenericModelPath(channel, path)
		return items, path, err
	default:
		return nil, "", fmt.Errorf("unsupported sync format: %s", options.Format)
	}
}

func (s *SyncService) fetchAutoModelPrices(channel *model.Channel) ([]upstreamModelPrice, string, error) {
	sources := []struct {
		path  string
		fetch func(*model.Channel) ([]upstreamModelPrice, error)
	}{
		{path: "/api/pricing", fetch: func(channel *model.Channel) ([]upstreamModelPrice, error) {
			return s.fetchNewAPICompatiblePrices(channel)
		}},
		{path: "/api/prices", fetch: func(channel *model.Channel) ([]upstreamModelPrice, error) {
			return s.fetchGenericModelPath(channel, "/api/prices")
		}},
		{path: "/models", fetch: func(channel *model.Channel) ([]upstreamModelPrice, error) {
			return s.fetchGenericModelPath(channel, "/models")
		}},
		{path: "/api/models", fetch: func(channel *model.Channel) ([]upstreamModelPrice, error) {
			return s.fetchGenericModelPath(channel, "/api/models")
		}},
	}

	failures := make([]string, 0, len(sources))
	for _, source := range sources {
		items, err := source.fetch(channel)
		if err == nil && len(items) > 0 {
			return items, source.path, nil
		}
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", source.path, err))
			continue
		}
		failures = append(failures, fmt.Sprintf("%s: upstream returned no models", source.path))
	}
	if len(failures) > 0 {
		return nil, "", fmt.Errorf("all sync sources failed: %s", strings.Join(failures, "; "))
	}
	return nil, "", errors.New("upstream returned no models")
}

func (s *SyncService) fetchNewAPICompatiblePrices(channel *model.Channel) ([]upstreamModelPrice, error) {
	body, err := getUpstreamJSONWithoutAuth(channel, "/api/pricing")
	if err != nil {
		return nil, err
	}
	items, err := parseUpstreamPriceItems(body)
	if err != nil {
		return nil, fmt.Errorf("parse /api/pricing response: %w", err)
	}
	return items, nil
}

func (s *SyncService) fetchGenericModelPath(channel *model.Channel, path string) ([]upstreamModelPrice, error) {
	body, err := getUpstreamJSON(channel, path)
	if err != nil {
		return nil, err
	}
	items, err := parseUpstreamPriceItems(body)
	if err != nil {
		return nil, fmt.Errorf("parse %s response: %w", path, err)
	}
	return items, nil
}

func (s *SyncService) fetchOpenAIModelList(channel *model.Channel) ([]upstreamModelPrice, error) {
	body, err := getUpstreamJSON(channel, "/v1/models")
	if err != nil {
		return nil, err
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}

	items := make([]upstreamModelPrice, 0, len(payload.Data))
	for _, item := range payload.Data {
		if strings.TrimSpace(item.ID) == "" {
			continue
		}
		items = append(items, upstreamModelPrice{
			Model:                 item.ID,
			InputPrice:            decimal.Zero,
			OutputPrice:           decimal.Zero,
			CachedInputPrice:      decimal.Zero,
			InputPriceTiers:       nil,
			OutputPriceTiers:      nil,
			CachedInputPriceTiers: nil,
		})
	}
	return items, nil
}

func getUpstreamJSON(channel *model.Channel, path string) ([]byte, error) {
	return getUpstreamJSONWithAuth(channel, path, true)
}

func getUpstreamJSONWithoutAuth(channel *model.Channel, path string) ([]byte, error) {
	return getUpstreamJSONWithAuth(channel, path, false)
}

func getUpstreamJSONWithAuth(channel *model.Channel, path string, includeAuth bool) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	if err := ValidateConfiguredHTTPURL(channel.BaseURL); err != nil {
		return nil, fmt.Errorf("upstream URL blocked by SSRF protection: %w", err)
	}
	requestURL := upstreamURLForPath(channel.BaseURL, path)
	req, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create GET %s request: %w", requestURL, err)
	}
	if includeAuth && strings.TrimSpace(channel.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+channel.APIKey)
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Sync request failed: GET %s: %v", requestURL, err)
		return nil, fmt.Errorf("GET %s failed: %w", requestURL, err)
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, fmt.Errorf("read GET %s response: %w", requestURL, readErr)
	}
	contentType := resp.Header.Get("Content-Type")
	if resp.StatusCode != http.StatusOK {
		message := bodyPreview(body)
		log.Printf("Sync request failed: GET %s returned status %d content-type=%q body=%q", requestURL, resp.StatusCode, contentType, message)
		return nil, fmt.Errorf("GET %s returned status %d content-type=%q body=%q", requestURL, resp.StatusCode, contentType, message)
	}
	if strings.Contains(strings.ToLower(contentType), "text/html") || bodyLooksLikeHTML(body) {
		message := bodyPreview(body)
		log.Printf("Sync request failed: GET %s returned HTML instead of JSON status=%d content-type=%q body=%q", requestURL, resp.StatusCode, contentType, message)
		return nil, fmt.Errorf("GET %s returned HTML instead of JSON content-type=%q body=%q", requestURL, contentType, message)
	}
	return body, nil
}

func upstreamURLForPath(baseURL string, path string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if base == "" {
		return path
	}

	lowerBase := strings.ToLower(base)
	if strings.HasSuffix(lowerBase, "/v1") {
		if path == "/v1" {
			return base
		}
		if strings.HasPrefix(path, "/v1/") {
			return base + strings.TrimPrefix(path, "/v1")
		}
		return base[:len(base)-len("/v1")] + path
	}
	return base + path
}

func bodyLooksLikeHTML(body []byte) bool {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return false
	}
	trimmed = strings.ToLower(trimmed)
	return strings.HasPrefix(trimmed, "<!doctype html") || strings.HasPrefix(trimmed, "<html") || strings.HasPrefix(trimmed, "<script")
}

func bodyPreview(body []byte) string {
	preview := strings.TrimSpace(string(body))
	preview = strings.Join(strings.Fields(preview), " ")
	if len(preview) > 500 {
		return preview[:500] + "..."
	}
	return preview
}

func parseUpstreamPriceItems(body []byte) ([]upstreamModelPrice, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()

	var raw interface{}
	if err := decoder.Decode(&raw); err != nil {
		return nil, err
	}
	if items := collectSplitPriceItems(raw); len(items) > 0 {
		return items, nil
	}
	return collectPriceItems("", raw), nil
}

func collectSplitPriceItems(raw interface{}) []upstreamModelPrice {
	switch value := raw.(type) {
	case []interface{}:
		items := make([]upstreamModelPrice, 0, len(value))
		for _, item := range value {
			items = append(items, collectSplitPriceItems(item)...)
		}
		return items
	case map[string]interface{}:
		if items := splitPriceItemsFromMap(value); len(items) > 0 {
			return items
		}
		for _, key := range []string{"data", "prices", "models", "model_prices", "modelPrices", "list", "items"} {
			if nested, ok := value[key]; ok {
				items := collectSplitPriceItems(nested)
				if len(items) > 0 {
					return items
				}
			}
		}

		items := make([]upstreamModelPrice, 0, len(value))
		for _, item := range value {
			items = append(items, collectSplitPriceItems(item)...)
		}
		return items
	default:
		return nil
	}
}

func splitPriceItemsFromMap(value map[string]interface{}) []upstreamModelPrice {
	inputPrices, hasInputPrices := firstMapValue(value, "input_price", "inputPrice", "input_prices", "inputPrices", "prompt_price", "promptPrice", "prompt_prices", "promptPrices", "prompt_token_price", "promptTokenPrice")
	outputPrices, hasOutputPrices := firstMapValue(value, "output_price", "outputPrice", "output_prices", "outputPrices", "completion_price", "completionPrice", "completion_prices", "completionPrices", "completion_token_price", "completionTokenPrice")
	cachedInputPrices, hasCachedInputPrices := firstMapValue(value, cachedInputPriceKeys()...)
	cacheWriteInputPrices, hasCacheWriteInputPrices := firstMapValue(value, cacheWriteInputPriceKeys()...)
	inputPriceTiers, hasInputPriceTiers := firstMapValue(value, inputPriceTierKeys()...)
	outputPriceTiers, hasOutputPriceTiers := firstMapValue(value, outputPriceTierKeys()...)
	cachedInputPriceTiers, hasCachedInputPriceTiers := firstMapValue(value, cachedInputPriceTierKeys()...)
	cacheWriteInputPriceTiers, hasCacheWriteInputPriceTiers := firstMapValue(value, cacheWriteInputPriceTierKeys()...)
	modelRatios, hasModelRatios := firstMapValue(value, "model_ratio", "modelRatio", "model_ratios", "modelRatios")
	completionRatios, hasCompletionRatios := firstMapValue(value, "completion_ratio", "completionRatio", "completion_ratios", "completionRatios")
	genericPrices, hasGenericPrices := firstMapValue(value, "price", "prices", "ratio", "ratios", "model_price", "modelPrice", "model_prices", "modelPrices")
	quotaTypes, hasQuotaTypes := firstMapValue(value, "quota_type", "quotaType", "quota_types", "quotaTypes")
	if !hasInputPrices && !hasOutputPrices && !hasCachedInputPrices && !hasCacheWriteInputPrices && !hasInputPriceTiers && !hasOutputPriceTiers && !hasCachedInputPriceTiers && !hasCacheWriteInputPriceTiers && !hasModelRatios && !hasCompletionRatios && !hasGenericPrices && !hasQuotaTypes {
		return nil
	}

	modelNames := map[string]struct{}{}
	addMapKeys := func(values map[string]interface{}) {
		for key := range values {
			if strings.TrimSpace(key) != "" {
				modelNames[key] = struct{}{}
			}
		}
	}
	addMapKeys(inputPrices)
	addMapKeys(outputPrices)
	addMapKeys(cachedInputPrices)
	addMapKeys(cacheWriteInputPrices)
	addMapKeys(inputPriceTiers)
	addMapKeys(outputPriceTiers)
	addMapKeys(cachedInputPriceTiers)
	addMapKeys(cacheWriteInputPriceTiers)
	addMapKeys(modelRatios)
	addMapKeys(completionRatios)
	addMapKeys(genericPrices)
	addMapKeys(quotaTypes)
	if len(modelNames) == 0 {
		return nil
	}

	names := make([]string, 0, len(modelNames))
	for name := range modelNames {
		names = append(names, name)
	}
	sort.Strings(names)

	items := make([]upstreamModelPrice, 0, len(names))
	for _, name := range names {
		inputPrice, inputOK := decimalFromMapValue(inputPrices, name)
		outputPrice, outputOK := decimalFromMapValue(outputPrices, name)
		cachedInputPrice, cachedInputOK := cachedDecimalFromMapValue(cachedInputPrices, name)
		cacheWriteInputPrice, cacheWriteInputOK := decimalFromMapValue(cacheWriteInputPrices, name)
		inputTiers, inputTiersOK := priceTiersFromMapValue(inputPriceTiers, name)
		outputTiers, outputTiersOK := priceTiersFromMapValue(outputPriceTiers, name)
		cachedInputTiers, cachedInputTiersOK := priceTiersFromMapValue(cachedInputPriceTiers, name)
		cacheWriteInputTiers, _ := priceTiersFromMapValue(cacheWriteInputPriceTiers, name)
		modelRatio, modelRatioOK := decimalFromMapValue(modelRatios, name)
		completionRatio, completionRatioOK := decimalFromMapValue(completionRatios, name)
		genericPrice, genericPriceOK := priceItemFromMapValue(genericPrices, name)
		usesBillingExpression := genericPriceOK && genericPrice.UsesBillingExpression
		quotaType, _ := intFromMapValue(quotaTypes, name)
		quotaType = normalizeQuotaType(quotaType)

		if !inputOK && modelRatioOK {
			inputPrice = modelRatio
			inputOK = true
		}
		if !inputOK && genericPriceOK {
			inputPrice = genericPrice.InputPrice
			inputOK = true
		}
		if !outputOK {
			switch {
			case modelRatioOK && completionRatioOK:
				outputPrice = modelRatio.Mul(completionRatio)
				outputOK = true
			case inputOK && completionRatioOK:
				outputPrice = inputPrice.Mul(completionRatio)
				outputOK = true
			case genericPriceOK:
				outputPrice = genericPrice.OutputPrice
				outputOK = true
			}
		}
		if !inputOK && outputOK {
			inputPrice = outputPrice
			inputOK = true
		}
		if !outputOK && inputOK {
			outputPrice = inputPrice
			outputOK = true
		}
		if !cachedInputOK {
			if genericPriceOK {
				cachedInputPrice = genericPrice.CachedInputPrice
				cachedInputOK = true
			} else if inputOK {
				cachedInputPrice = inputPrice
				cachedInputOK = true
			}
		}
		if !inputTiersOK && genericPriceOK {
			inputTiers = genericPrice.InputPriceTiers
		}
		if !outputTiersOK && genericPriceOK {
			outputTiers = genericPrice.OutputPriceTiers
		}
		if !cachedInputTiersOK && genericPriceOK {
			cachedInputTiers = genericPrice.CachedInputPriceTiers
		}
		if !cacheWriteInputOK && genericPriceOK {
			cacheWriteInputPrice = genericPrice.CacheWriteInputPrice
		}
		if !inputOK && !outputOK && len(inputTiers) == 0 && len(outputTiers) == 0 && len(cachedInputTiers) == 0 && len(cacheWriteInputTiers) == 0 {
			continue
		}

		items = append(items, upstreamModelPrice{
			Model:                     name,
			QuotaType:                 quotaType,
			UsesBillingExpression:     usesBillingExpression,
			InputPrice:                inputPrice,
			OutputPrice:               outputPrice,
			CachedInputPrice:          cachedInputPrice,
			CacheWriteInputPrice:      cacheWriteInputPrice,
			InputPriceTiers:           model.NormalizePriceTiers(inputTiers),
			OutputPriceTiers:          model.NormalizePriceTiers(outputTiers),
			CachedInputPriceTiers:     model.NormalizePriceTiers(cachedInputTiers),
			CacheWriteInputPriceTiers: model.NormalizePriceTiers(cacheWriteInputTiers),
		})
	}
	return items
}

func collectPriceItems(modelName string, raw interface{}) []upstreamModelPrice {
	switch value := raw.(type) {
	case []interface{}:
		items := make([]upstreamModelPrice, 0, len(value))
		for _, item := range value {
			items = append(items, collectPriceItems("", item)...)
		}
		return items
	case map[string]interface{}:
		for _, key := range []string{"data", "prices", "models", "model_prices", "modelPrices", "list", "items"} {
			if nested, ok := value[key]; ok {
				items := collectPriceItems("", nested)
				if len(items) > 0 {
					return items
				}
			}
		}
		if item, ok := priceItemFromMap(modelName, value); ok {
			return []upstreamModelPrice{item}
		}

		items := make([]upstreamModelPrice, 0, len(value))
		for key, item := range value {
			items = append(items, collectPriceItems(key, item)...)
		}
		return items
	case string:
		value = strings.TrimSpace(value)
		if value == "" {
			return nil
		}
		if modelName == "" {
			return []upstreamModelPrice{{
				Model:            value,
				InputPrice:       decimal.Zero,
				OutputPrice:      decimal.Zero,
				CachedInputPrice: decimal.Zero,
			}}
		}
		if price, ok := decimalFromValue(value); ok {
			return []upstreamModelPrice{{
				Model:            modelName,
				InputPrice:       price,
				OutputPrice:      price,
				CachedInputPrice: price,
			}}
		}
		return nil
	default:
		if modelName == "" {
			return nil
		}
		if price, ok := decimalFromValue(value); ok {
			return []upstreamModelPrice{{
				Model:            modelName,
				InputPrice:       price,
				OutputPrice:      price,
				CachedInputPrice: price,
			}}
		}
		return nil
	}
}

func priceItemFromMap(modelName string, value map[string]interface{}) (upstreamModelPrice, bool) {
	modelName = firstStringValue(value, modelName, "model", "model_name", "modelName", "name", "id")
	if strings.TrimSpace(modelName) == "" {
		return upstreamModelPrice{}, false
	}
	endpointTypes := endpointTypesFromMap(value)
	quotaType, _ := firstIntValue(value, "quota_type", "quotaType")
	quotaType = normalizeQuotaType(quotaType)
	perCallPricing := quotaType == 1

	inputPrice, inputOK := firstDecimalValue(value, "input_price", "inputPrice", "prompt_price", "promptPrice", "prompt_token_price", "promptTokenPrice", "input_token_price", "inputTokenPrice", "input", "prompt", "input_ratio", "inputRatio", "prompt_ratio", "promptRatio")
	outputPrice, outputOK := firstDecimalValue(value, "output_price", "outputPrice", "completion_price", "completionPrice", "completion_token_price", "completionTokenPrice", "output_token_price", "outputTokenPrice", "completion", "output", "output_ratio", "outputRatio")
	cachedInputPrice, cachedInputOK := firstDecimalValue(value, cachedInputPriceKeys()...)
	cacheWriteInputPrice, cacheWriteInputOK := firstDecimalValue(value, cacheWriteInputPriceKeys()...)
	inputPriceTiers, inputTiersOK := firstPriceTierListValue(value, inputPriceTierKeys()...)
	outputPriceTiers, outputTiersOK := firstPriceTierListValue(value, outputPriceTierKeys()...)
	cachedInputPriceTiers, cachedInputTiersOK := firstPriceTierListValue(value, cachedInputPriceTierKeys()...)
	cacheWriteInputPriceTiers, _ := firstPriceTierListValue(value, cacheWriteInputPriceTierKeys()...)
	usesBillingExpression := false
	if exprPrice, ok := priceItemFromBillingExpression(modelName, value); ok {
		usesBillingExpression = true
		inputPrice = exprPrice.InputPrice
		outputPrice = exprPrice.OutputPrice
		cachedInputPrice = exprPrice.CachedInputPrice
		cacheWriteInputPrice = exprPrice.CacheWriteInputPrice
		inputPriceTiers = exprPrice.InputPriceTiers
		outputPriceTiers = exprPrice.OutputPriceTiers
		cachedInputPriceTiers = exprPrice.CachedInputPriceTiers
		cacheWriteInputPriceTiers = exprPrice.CacheWriteInputPriceTiers
		inputOK = true
		outputOK = true
		cachedInputOK = true
		inputTiersOK = len(inputPriceTiers) > 0
		outputTiersOK = len(outputPriceTiers) > 0
		cachedInputTiersOK = len(cachedInputPriceTiers) > 0
		cacheWriteInputOK = true
	}
	genericPriceTiers, genericTiersOK := firstPriceTierListValue(value, "price_tiers", "priceTiers", "prices_tiers", "pricesTiers", "model_price_tiers", "modelPriceTiers", "model_tiers", "modelTiers", "tiers")
	modelRatio, modelRatioOK := firstDecimalValue(value, "model_ratio", "modelRatio")
	completionRatio, completionRatioOK := firstDecimalValue(value, "completion_ratio", "completionRatio")
	genericPrice, genericPriceOK := firstDecimalValue(value, "price", "model_price", "modelPrice", "ratio")
	if !inputTiersOK && genericTiersOK && !perCallPricing {
		inputPriceTiers = genericPriceTiers
	}
	if !outputTiersOK && genericTiersOK {
		outputPriceTiers = genericPriceTiers
	}
	if !cachedInputTiersOK && genericTiersOK && !perCallPricing {
		cachedInputPriceTiers = genericPriceTiers
	}
	if perCallPricing {
		if !outputOK && genericPriceOK {
			outputPrice = genericPrice
			outputOK = true
		}
		if !inputOK {
			inputPrice = decimal.Zero
			inputOK = true
		}
		if !cachedInputOK {
			cachedInputPrice = decimal.Zero
			cachedInputOK = true
		}
		if !cacheWriteInputOK {
			cacheWriteInputPrice = decimal.Zero
			cacheWriteInputOK = true
		}
	} else {
		if !inputOK && modelRatioOK {
			inputPrice = modelRatio
			inputOK = true
		}
		if !inputOK && genericPriceOK {
			inputPrice = genericPrice
			inputOK = true
		}
		if !outputOK {
			switch {
			case modelRatioOK && completionRatioOK:
				outputPrice = modelRatio.Mul(completionRatio)
				outputOK = true
			case inputOK && completionRatioOK:
				outputPrice = inputPrice.Mul(completionRatio)
				outputOK = true
			case genericPriceOK:
				outputPrice = genericPrice
				outputOK = true
			}
		}
	}
	if !inputOK && outputOK {
		inputPrice = outputPrice
	}
	if !outputOK && inputOK {
		outputPrice = inputPrice
	}
	if !inputOK && !outputOK {
		inputPrice = decimal.Zero
		outputPrice = decimal.Zero
	}
	if !cachedInputOK {
		cachedInputPrice = inputPrice
	}

	return upstreamModelPrice{
		Model:                     modelName,
		EndpointTypes:             endpointTypes,
		QuotaType:                 quotaType,
		UsesBillingExpression:     usesBillingExpression,
		InputPrice:                inputPrice,
		OutputPrice:               outputPrice,
		CachedInputPrice:          cachedInputPrice,
		CacheWriteInputPrice:      cacheWriteInputPrice,
		InputPriceTiers:           model.NormalizePriceTiers(inputPriceTiers),
		OutputPriceTiers:          model.NormalizePriceTiers(outputPriceTiers),
		CachedInputPriceTiers:     model.NormalizePriceTiers(cachedInputPriceTiers),
		CacheWriteInputPriceTiers: model.NormalizePriceTiers(cacheWriteInputPriceTiers),
	}, true
}

type tieredExprPrices struct {
	Input           decimal.Decimal
	Output          decimal.Decimal
	CachedInput     decimal.Decimal
	CacheWriteInput decimal.Decimal
}

func priceItemFromBillingExpression(modelName string, value map[string]interface{}) (upstreamModelPrice, bool) {
	expr := firstStringValue(value, "", "billing_expr", "billingExpr")
	if strings.TrimSpace(expr) == "" {
		return upstreamModelPrice{}, false
	}

	tierMatches := tieredExprTierPattern.FindAllStringSubmatch(expr, -1)
	if len(tierMatches) == 0 {
		return upstreamModelPrice{}, false
	}

	firstPrices, ok := tieredExprPricesFromSegment(tierMatches[0][1])
	if !ok {
		return upstreamModelPrice{}, false
	}
	basePrices := firstPrices
	var tierPrices *tieredExprPrices
	threshold := 0
	condition := model.PriceTierConditionFullInputTokens

	if len(tierMatches) > 1 {
		secondPrices, secondOK := tieredExprPricesFromSegment(tierMatches[1][1])
		if secondOK {
			variable, operator, parsedThreshold, thresholdOK := tieredExprThreshold(expr)
			if thresholdOK {
				threshold = tieredExprTierStart(operator, parsedThreshold)
				condition = tieredExprTierCondition(variable)
				if operator == ">" || operator == ">=" {
					basePrices = secondPrices
					tierPrices = &firstPrices
				} else {
					tierPrices = &secondPrices
				}
			}
		}
	}

	item := upstreamModelPrice{
		Model:                 modelName,
		UsesBillingExpression: true,
		InputPrice:            basePrices.Input,
		OutputPrice:           basePrices.Output,
		CachedInputPrice:      basePrices.CachedInput,
		CacheWriteInputPrice:  basePrices.CacheWriteInput,
	}
	if tierPrices != nil {
		item.InputPriceTiers = model.NormalizePriceTiers(model.PriceTierList{{
			MinTokens: threshold,
			Price:     tierPrices.Input,
			Condition: condition,
		}})
		item.OutputPriceTiers = model.NormalizePriceTiers(model.PriceTierList{{
			MinTokens: threshold,
			Price:     tierPrices.Output,
			Condition: condition,
		}})
		item.CachedInputPriceTiers = model.NormalizePriceTiers(model.PriceTierList{{
			MinTokens: threshold,
			Price:     tierPrices.CachedInput,
			Condition: condition,
		}})
		item.CacheWriteInputPriceTiers = model.NormalizePriceTiers(model.PriceTierList{{
			MinTokens: threshold,
			Price:     tierPrices.CacheWriteInput,
			Condition: condition,
		}})
	}
	return item, true
}

func tieredExprThreshold(expr string) (string, string, int, bool) {
	match := tieredExprThresholdPattern.FindStringSubmatch(expr)
	if len(match) != 4 {
		return "", "", 0, false
	}
	threshold, ok := intFromValue(match[3])
	return strings.ToLower(strings.TrimSpace(match[1])), strings.TrimSpace(match[2]), threshold, ok
}

func tieredExprTierStart(operator string, threshold int) int {
	if operator == "<=" || operator == ">" {
		return threshold + 1
	}
	return threshold
}

func tieredExprTierCondition(variable string) string {
	if strings.EqualFold(strings.TrimSpace(variable), "len") {
		return model.PriceTierConditionFullRequestTokens
	}
	return model.PriceTierConditionFullInputTokens
}

func tieredExprPricesFromSegment(segment string) (tieredExprPrices, bool) {
	input, inputOK := tieredExprCoefficient(segment, "p")
	output, outputOK := tieredExprCoefficient(segment, "c")
	cachedInput, cachedOK := tieredExprCoefficient(segment, "cr")
	cacheWriteInput, cacheWriteOK := tieredExprCoefficient(segment, "cc")
	if !cachedOK {
		cachedInput = input
	}
	return tieredExprPrices{
		Input:           input,
		Output:          output,
		CachedInput:     cachedInput,
		CacheWriteInput: cacheWriteInput,
	}, inputOK || outputOK || cachedOK || cacheWriteOK
}

func tieredExprCoefficient(segment string, variable string) (decimal.Decimal, bool) {
	escapedVariable := regexp.QuoteMeta(variable)
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b` + escapedVariable + `\b\s*\*\s*([0-9]+(?:\.[0-9]+)?)`),
		regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s*\*\s*\b` + escapedVariable + `\b`),
	}
	for _, pattern := range patterns {
		match := pattern.FindStringSubmatch(segment)
		if len(match) == 2 {
			parsed, err := decimal.NewFromString(match[1])
			return parsed, err == nil
		}
	}
	return decimal.Zero, false
}

func normalizeSyncPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		parsedIndex := strings.Index(path, "://")
		remainder := path[parsedIndex+3:]
		if slashIndex := strings.Index(remainder, "/"); slashIndex >= 0 {
			path = remainder[slashIndex:]
		} else {
			return ""
		}
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}

func endpointTypesFromMap(value map[string]interface{}) []string {
	for _, key := range []string{"supported_endpoint_types", "supportedEndpointTypes", "endpoint_types", "endpointTypes", "endpoints"} {
		if raw, ok := value[key]; ok {
			return endpointTypesFromValue(raw)
		}
	}
	return nil
}

func endpointTypesFromValue(value interface{}) []string {
	switch typed := value.(type) {
	case []interface{}:
		items := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				text = strings.TrimSpace(text)
				if text != "" {
					items = append(items, text)
				}
			}
		}
		return items
	case []string:
		items := make([]string, 0, len(typed))
		for _, item := range typed {
			item = strings.TrimSpace(item)
			if item != "" {
				items = append(items, item)
			}
		}
		return items
	case string:
		parts := strings.FieldsFunc(typed, func(r rune) bool {
			return r == ',' || r == ';' || r == '|'
		})
		items := make([]string, 0, len(parts))
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part != "" {
				items = append(items, part)
			}
		}
		return items
	default:
		return nil
	}
}

func hasEndpointType(endpointTypes []string, want string) bool {
	want = normalizeEndpointType(want)
	for _, endpointType := range endpointTypes {
		if normalizeEndpointType(endpointType) == want {
			return true
		}
	}
	return false
}

func normalizeQuotaType(value int) int {
	switch value {
	case 1:
		return 1
	case model.QuotaTypeVideoResolutionDuration:
		return model.QuotaTypeVideoResolutionDuration
	}
	return 0
}

func multiplyTokenPricedModelPrices(items []upstreamModelPrice) []upstreamModelPrice {
	multiplier := decimal.NewFromInt(2)
	result := make([]upstreamModelPrice, len(items))
	for index, item := range items {
		if normalizeQuotaType(item.QuotaType) == 1 || item.UsesBillingExpression {
			result[index] = item
			continue
		}
		item.InputPrice = item.InputPrice.Mul(multiplier)
		item.OutputPrice = item.OutputPrice.Mul(multiplier)
		item.CachedInputPrice = item.CachedInputPrice.Mul(multiplier)
		item.CacheWriteInputPrice = item.CacheWriteInputPrice.Mul(multiplier)
		item.InputPriceTiers = model.MultiplyPriceTiers(item.InputPriceTiers, multiplier)
		item.OutputPriceTiers = model.MultiplyPriceTiers(item.OutputPriceTiers, multiplier)
		item.CachedInputPriceTiers = model.MultiplyPriceTiers(item.CachedInputPriceTiers, multiplier)
		item.CacheWriteInputPriceTiers = model.MultiplyPriceTiers(item.CacheWriteInputPriceTiers, multiplier)
		result[index] = item
	}
	return result
}

func normalizeEndpointType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "-")
	return value
}

func firstStringValue(value map[string]interface{}, fallback string, keys ...string) string {
	for _, key := range keys {
		if raw, ok := value[key].(string); ok && strings.TrimSpace(raw) != "" {
			return raw
		}
	}
	return fallback
}

func firstMapValue(value map[string]interface{}, keys ...string) (map[string]interface{}, bool) {
	for _, key := range keys {
		if raw, ok := value[key]; ok {
			if parsed, ok := raw.(map[string]interface{}); ok {
				return parsed, true
			}
		}
	}
	return nil, false
}

func decimalFromMapValue(value map[string]interface{}, key string) (decimal.Decimal, bool) {
	if value == nil {
		return decimal.Zero, false
	}
	raw, ok := value[key]
	if !ok {
		return decimal.Zero, false
	}
	if parsed, ok := decimalFromValue(raw); ok {
		return parsed, true
	}
	if nested, ok := raw.(map[string]interface{}); ok {
		return firstDecimalValue(nested, "input_price", "inputPrice", "output_price", "outputPrice", "prompt_price", "promptPrice", "completion_price", "completionPrice", "model_ratio", "modelRatio", "completion_ratio", "completionRatio", "price", "ratio")
	}
	return decimal.Zero, false
}

func intFromMapValue(value map[string]interface{}, key string) (int, bool) {
	if value == nil {
		return 0, false
	}
	raw, ok := value[key]
	if !ok {
		return 0, false
	}
	return intFromValue(raw)
}

func priceItemFromMapValue(value map[string]interface{}, key string) (upstreamModelPrice, bool) {
	if value == nil {
		return upstreamModelPrice{}, false
	}
	raw, ok := value[key]
	if !ok {
		return upstreamModelPrice{}, false
	}
	if parsed, ok := decimalFromValue(raw); ok {
		return upstreamModelPrice{
			Model:            key,
			InputPrice:       parsed,
			OutputPrice:      parsed,
			CachedInputPrice: parsed,
		}, true
	}
	if nested, ok := raw.(map[string]interface{}); ok {
		return priceItemFromMap(key, nested)
	}
	return upstreamModelPrice{}, false
}

func cachedDecimalFromMapValue(value map[string]interface{}, key string) (decimal.Decimal, bool) {
	if value == nil {
		return decimal.Zero, false
	}
	raw, ok := value[key]
	if !ok {
		return decimal.Zero, false
	}
	if parsed, ok := decimalFromValue(raw); ok {
		return parsed, true
	}
	if nested, ok := raw.(map[string]interface{}); ok {
		return firstDecimalValue(nested, cachedInputPriceKeys()...)
	}
	return decimal.Zero, false
}

func firstPriceTierListValue(value map[string]interface{}, keys ...string) (model.PriceTierList, bool) {
	for _, key := range keys {
		if raw, ok := value[key]; ok {
			if parsed, ok := priceTiersFromValue(raw); ok {
				return parsed, true
			}
		}
	}
	return nil, false
}

func priceTiersFromMapValue(value map[string]interface{}, key string) (model.PriceTierList, bool) {
	if value == nil {
		return nil, false
	}
	raw, ok := value[key]
	if !ok {
		return nil, false
	}
	return priceTiersFromValue(raw)
}

func priceTiersFromValue(value interface{}) (model.PriceTierList, bool) {
	switch typed := value.(type) {
	case []interface{}:
		tiers := make(model.PriceTierList, 0, len(typed))
		for _, item := range typed {
			switch tierValue := item.(type) {
			case map[string]interface{}:
				if tier, ok := priceTierFromMap(tierValue); ok {
					tiers = append(tiers, tier)
				}
			case []interface{}:
				if len(tierValue) < 2 {
					continue
				}
				minTokens, minOK := intFromValue(tierValue[0])
				price, priceOK := decimalFromValue(tierValue[1])
				if minOK && priceOK {
					tier := model.PriceTier{MinTokens: minTokens, Price: price}
					if len(tierValue) > 2 {
						tier.Condition = stringFromValue(tierValue[2])
					}
					tiers = append(tiers, tier)
				}
			}
		}
		normalized := model.NormalizePriceTiers(tiers)
		return normalized, len(normalized) > 0
	case map[string]interface{}:
		if nested, ok := firstPriceTierListValue(typed, "tiers", "price_tiers", "priceTiers", "data", "items", "list"); ok {
			return nested, true
		}
		if tier, ok := priceTierFromMap(typed); ok {
			return model.NormalizePriceTiers(model.PriceTierList{tier}), true
		}

		tiers := make(model.PriceTierList, 0, len(typed))
		for key, raw := range typed {
			minTokens, minOK := intFromValue(key)
			if !minOK {
				continue
			}
			price, priceOK := decimalFromValue(raw)
			if !priceOK {
				if nested, ok := raw.(map[string]interface{}); ok {
					price, priceOK = firstDecimalValue(nested, "price", "input_price", "inputPrice", "output_price", "outputPrice", "cached_input_price", "cachedInputPrice", "ratio", "model_ratio", "modelRatio")
				}
			}
			if minOK && priceOK {
				tier := model.PriceTier{MinTokens: minTokens, Price: price}
				if nested, ok := raw.(map[string]interface{}); ok {
					tier.Condition = firstStringValue(nested, "", "condition", "tier_condition", "tierCondition", "basis", "threshold_basis", "thresholdBasis")
				}
				tiers = append(tiers, tier)
			}
		}
		normalized := model.NormalizePriceTiers(tiers)
		return normalized, len(normalized) > 0
	default:
		return nil, false
	}
}

func priceTierFromMap(value map[string]interface{}) (model.PriceTier, bool) {
	minTokens, minOK := firstIntValue(value,
		"min_tokens", "minTokens", "min_token", "minToken",
		"from_tokens", "fromTokens", "from_token", "fromToken",
		"start_tokens", "startTokens", "start_token", "startToken",
		"threshold_tokens", "thresholdTokens", "token_threshold", "tokenThreshold",
		"from", "start", "threshold", "tokens",
	)
	price, priceOK := firstDecimalValue(value,
		"price",
		"input_price", "inputPrice", "prompt_price", "promptPrice",
		"output_price", "outputPrice", "completion_price", "completionPrice",
		"cached_input_price", "cachedInputPrice", "cache_price", "cachePrice",
		"ratio", "model_ratio", "modelRatio",
	)
	if !minOK || !priceOK {
		return model.PriceTier{}, false
	}
	return model.PriceTier{
		MinTokens: minTokens,
		Price:     price,
		Condition: firstStringValue(value, "", "condition", "tier_condition", "tierCondition", "basis", "threshold_basis", "thresholdBasis"),
	}, true
}

func firstIntValue(value map[string]interface{}, keys ...string) (int, bool) {
	for _, key := range keys {
		if raw, ok := value[key]; ok {
			if parsed, ok := intFromValue(raw); ok {
				return parsed, true
			}
		}
	}
	return 0, false
}

func intFromValue(value interface{}) (int, bool) {
	parsed, ok := decimalFromValue(value)
	if !ok {
		return 0, false
	}
	if parsed.IsNegative() {
		return 0, false
	}
	return int(parsed.IntPart()), true
}

func inputPriceTierKeys() []string {
	return []string{
		"input_price_tiers", "inputPriceTiers", "input_prices_tiers", "inputPricesTiers",
		"prompt_price_tiers", "promptPriceTiers", "prompt_prices_tiers", "promptPricesTiers",
		"input_tiers", "inputTiers", "prompt_tiers", "promptTiers",
		"input_token_price_tiers", "inputTokenPriceTiers", "prompt_token_price_tiers", "promptTokenPriceTiers",
	}
}

func outputPriceTierKeys() []string {
	return []string{
		"output_price_tiers", "outputPriceTiers", "output_prices_tiers", "outputPricesTiers",
		"completion_price_tiers", "completionPriceTiers", "completion_prices_tiers", "completionPricesTiers",
		"output_tiers", "outputTiers", "completion_tiers", "completionTiers",
		"output_token_price_tiers", "outputTokenPriceTiers", "completion_token_price_tiers", "completionTokenPriceTiers",
	}
}

func cachedInputPriceTierKeys() []string {
	return []string{
		"cached_input_price_tiers", "cachedInputPriceTiers", "cached_input_tiers", "cachedInputTiers",
		"cache_input_price_tiers", "cacheInputPriceTiers", "cache_input_tiers", "cacheInputTiers",
		"cached_price_tiers", "cachedPriceTiers", "cache_price_tiers", "cachePriceTiers",
		"cache_read_price_tiers", "cacheReadPriceTiers", "cache_read_tiers", "cacheReadTiers",
		"cached_prompt_price_tiers", "cachedPromptPriceTiers", "prompt_cache_price_tiers", "promptCachePriceTiers",
		"cached_tokens_price_tiers", "cachedTokensPriceTiers",
	}
}

func cacheWriteInputPriceTierKeys() []string {
	return []string{
		"cache_write_input_price_tiers", "cacheWriteInputPriceTiers",
		"cache_write_price_tiers", "cacheWritePriceTiers",
		"cache_creation_input_price_tiers", "cacheCreationInputPriceTiers",
		"cache_creation_price_tiers", "cacheCreationPriceTiers",
	}
}

func cachedInputPriceKeys() []string {
	return []string{
		"cached_input_price", "cachedInputPrice", "cached_input_prices", "cachedInputPrices",
		"cache_input_price", "cacheInputPrice", "cache_input_prices", "cacheInputPrices",
		"cached_price", "cachedPrice", "cached_prices", "cachedPrices",
		"cache_price", "cachePrice", "cache_prices", "cachePrices",
		"cache_ratio", "cacheRatio", "cache_ratios", "cacheRatios",
		"cache_read_price", "cacheReadPrice", "cache_read_prices", "cacheReadPrices",
		"cached_prompt_price", "cachedPromptPrice", "cached_prompt_prices", "cachedPromptPrices",
		"prompt_cache_price", "promptCachePrice", "prompt_cache_prices", "promptCachePrices",
		"cached_tokens_price", "cachedTokensPrice", "cached_tokens_prices", "cachedTokensPrices",
	}
}

func cacheWriteInputPriceKeys() []string {
	return []string{
		"cache_write_input_price", "cacheWriteInputPrice",
		"cache_write_price", "cacheWritePrice",
		"cache_creation_input_price", "cacheCreationInputPrice",
		"cache_creation_price", "cacheCreationPrice",
	}
}

func firstDecimalValue(value map[string]interface{}, keys ...string) (decimal.Decimal, bool) {
	for _, key := range keys {
		if raw, ok := value[key]; ok {
			if parsed, ok := decimalFromValue(raw); ok {
				return parsed, true
			}
		}
	}
	return decimal.Zero, false
}

func decimalFromValue(value interface{}) (decimal.Decimal, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := decimal.NewFromString(typed.String())
		return parsed, err == nil
	case string:
		parsed, err := decimal.NewFromString(strings.TrimSpace(typed))
		return parsed, err == nil
	case float64:
		return decimal.NewFromFloat(typed), true
	case int:
		return decimal.NewFromInt(int64(typed)), true
	case int64:
		return decimal.NewFromInt(typed), true
	case uint:
		return decimal.NewFromInt(int64(typed)), true
	case uint64:
		return decimal.NewFromInt(int64(typed)), true
	default:
		return decimal.Zero, false
	}
}
