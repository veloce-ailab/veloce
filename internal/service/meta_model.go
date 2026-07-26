package service

import (
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

const (
	MetaBillingModeActual   = "actual"
	MetaBillingModeMeta     = "meta"
	defaultMetaProvider     = "meta"
	defaultMetaProviderName = "Meta Module"
)

type MetaModel struct {
	ID                     uint            `gorm:"primaryKey" json:"id"`
	Name                   string          `gorm:"uniqueIndex;size:100;not null" json:"name"`
	Description            string          `gorm:"size:255" json:"description"`
	DSL                    string          `gorm:"type:text;not null" json:"dsl"`
	Provider               string          `gorm:"size:80;default:meta" json:"provider"`
	ProviderName           string          `gorm:"size:100;default:Meta Module" json:"provider_name"`
	ProviderIconURL        string          `gorm:"size:255" json:"provider_icon_url"`
	ExposeReferencedModels bool            `gorm:"default:true" json:"expose_referenced_models"`
	BillingMode            string          `gorm:"size:20;default:actual" json:"billing_mode"`
	InputPrice             decimal.Decimal `gorm:"type:decimal(20,10);default:0" json:"input_price"`
	OutputPrice            decimal.Decimal `gorm:"type:decimal(20,10);default:0" json:"output_price"`
	CachedInputPrice       decimal.Decimal `gorm:"type:decimal(20,10);default:0" json:"cached_input_price"`
	Enabled                bool            `gorm:"default:true" json:"enabled"`
	CreatedAt              time.Time       `json:"created_at"`
	UpdatedAt              time.Time       `json:"updated_at"`
}

type metaModelAPI struct{}

type metaModelInput struct {
	Name             string          `json:"name"`
	Description      string          `json:"description"`
	DSL              string          `json:"dsl"`
	Provider         string          `json:"provider"`
	ProviderName     string          `json:"provider_name"`
	ProviderIconURL  string          `json:"provider_icon_url"`
	ExposeReferenced *bool           `json:"expose_referenced_models"`
	BillingMode      string          `json:"billing_mode"`
	InputPrice       decimal.Decimal `json:"input_price"`
	OutputPrice      decimal.Decimal `json:"output_price"`
	CachedInputPrice decimal.Decimal `json:"cached_input_price"`
	Enabled          *bool           `json:"enabled"`
}

func init() {
	model.RegisterSQLiteMigrationModels(&MetaModel{})
}

func InitMetaModelFeatures() error {
	return model.DB.AutoMigrate(&MetaModel{})
}

func RegisterMetaModelAdminRoutes(group *gin.RouterGroup) {
	api := &metaModelAPI{}
	group.GET("/meta-models", api.list)
	group.POST("/meta-models", api.create)
	group.POST("/meta-models/validate", api.validate)
	group.PUT("/meta-models/:id", api.update)
	group.DELETE("/meta-models/:id", api.delete)
}

func (api *metaModelAPI) list(c *gin.Context) {
	var items []MetaModel
	if err := model.DB.Order("created_at DESC").Find(&items).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, items)
}

func (api *metaModelAPI) create(c *gin.Context) {
	var input metaModelInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	item, plan, ok := metaModelFromInput(c, input, true, true, 0)
	if !ok {
		return
	}
	if err := model.DB.Create(&item).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"meta_model": item, "plan": plan})
}

func (api *metaModelAPI) update(c *gin.Context) {
	var existing MetaModel
	if err := model.DB.First(&existing, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Meta model not found"})
		return
	}
	var input metaModelInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	next, plan, ok := metaModelFromInput(c, input, existing.Enabled, existing.ExposeReferencedModels, existing.ID)
	if !ok {
		return
	}
	updates := map[string]interface{}{
		"name":                     next.Name,
		"description":              next.Description,
		"dsl":                      next.DSL,
		"provider":                 next.Provider,
		"provider_name":            next.ProviderName,
		"provider_icon_url":        next.ProviderIconURL,
		"expose_referenced_models": next.ExposeReferencedModels,
		"billing_mode":             next.BillingMode,
		"input_price":              next.InputPrice,
		"output_price":             next.OutputPrice,
		"cached_input_price":       next.CachedInputPrice,
		"enabled":                  next.Enabled,
	}
	if err := model.DB.Model(&existing).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	model.DB.First(&existing, existing.ID)
	c.JSON(http.StatusOK, gin.H{"meta_model": existing, "plan": plan})
}

func (api *metaModelAPI) validate(c *gin.Context) {
	var input metaModelInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = "__preview__"
	}
	plan, err := validateMetaModelDSL(name, input.DSL, 0)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"plan": plan})
}

func (api *metaModelAPI) delete(c *gin.Context) {
	if err := model.DB.Delete(&MetaModel{}, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Meta model deleted"})
}

func metaModelFromInput(c *gin.Context, input metaModelInput, fallbackEnabled bool, fallbackExposeReferenced bool, currentID uint) (MetaModel, MetaProgram, bool) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Meta model name is required"})
		return MetaModel{}, MetaProgram{}, false
	}
	billingMode := normalizeMetaBillingMode(input.BillingMode)
	if billingMode == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid billing mode"})
		return MetaModel{}, MetaProgram{}, false
	}
	if input.InputPrice.LessThan(decimal.Zero) || input.OutputPrice.LessThan(decimal.Zero) || input.CachedInputPrice.LessThan(decimal.Zero) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Prices must not be negative"})
		return MetaModel{}, MetaProgram{}, false
	}
	plan, err := validateMetaModelDSL(name, input.DSL, currentID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return MetaModel{}, MetaProgram{}, false
	}
	enabled := fallbackEnabled
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	exposeReferenced := fallbackExposeReferenced
	if input.ExposeReferenced != nil {
		exposeReferenced = *input.ExposeReferenced
	}
	if billingMode == MetaBillingModeActual {
		input.InputPrice = decimal.Zero
		input.OutputPrice = decimal.Zero
		input.CachedInputPrice = decimal.Zero
	}
	return MetaModel{
		Name:                   name,
		Description:            strings.TrimSpace(input.Description),
		DSL:                    strings.TrimSpace(input.DSL),
		Provider:               normalizeMetaProvider(input.Provider),
		ProviderName:           normalizeMetaProviderName(input.ProviderName),
		ProviderIconURL:        strings.TrimSpace(input.ProviderIconURL),
		ExposeReferencedModels: exposeReferenced,
		BillingMode:            billingMode,
		InputPrice:             input.InputPrice,
		OutputPrice:            input.OutputPrice,
		CachedInputPrice:       input.CachedInputPrice,
		Enabled:                enabled,
	}, plan, true
}

func validateMetaModelDSL(name string, dsl string, currentID uint) (MetaProgram, error) {
	if strings.TrimSpace(dsl) == "" {
		return MetaProgram{}, errors.New("DSL is required")
	}
	plan, err := ParseMetaModuleDSL(dsl)
	if err != nil {
		return MetaProgram{}, err
	}
	if err := ensureExecutableMetaAction(plan.Root); err != nil {
		return MetaProgram{}, err
	}
	modelNames := referencedMetaActionModels(plan.Root)
	for referenced := range modelNames {
		if referenced == name {
			return MetaProgram{}, errors.New("Meta model cannot reference itself")
		}
		if err := ensureRealModelReference(referenced, currentID); err != nil {
			return MetaProgram{}, err
		}
	}
	return plan, nil
}

func ensureExecutableMetaAction(action MetaAction) error {
	switch action.Kind {
	case MetaActionCall:
		return nil
	case MetaActionRoute:
		for _, route := range action.Routes {
			if err := ensureExecutableMetaAction(route.Action); err != nil {
				return err
			}
		}
		return nil
	case MetaActionSwitch:
		weightedBranches := 0
		seenOtherwise := false
		for _, branch := range action.Switches {
			if branch.Otherwise {
				if seenOtherwise {
					return errors.New("switch cannot have duplicate otherwise branches")
				}
				seenOtherwise = true
			} else {
				if branch.Weight <= 0 {
					return errors.New("switch weight must be positive")
				}
				weightedBranches++
			}
			if err := ensureExecutableMetaAction(branch.Action); err != nil {
				return err
			}
		}
		if weightedBranches == 0 {
			return errors.New("switch requires at least one weighted branch")
		}
		return nil
	case MetaActionParallel:
		return errors.New("parallel meta model execution is not implemented yet")
	case MetaActionJudge:
		return errors.New("judge meta model execution is not implemented yet")
	default:
		return errors.New("unsupported meta model action")
	}
}

func ensureRealModelReference(modelName string, currentID uint) error {
	var count int64
	if err := model.DB.Model(&MetaModel{}).
		Where("name = ? AND id <> ?", modelName, currentID).
		Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return errors.New("Meta model cannot reference another meta model yet: " + modelName)
	}
	if err := model.DB.Model(&model.Model{}).Where("model_name = ?", modelName).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return errors.New("Referenced model not found: " + modelName)
	}
	return nil
}

func referencedMetaActionModels(action MetaAction) map[string]struct{} {
	models := map[string]struct{}{}
	collectReferencedMetaActionModels(action, models)
	return models
}

func sortedReferencedMetaActionModels(action MetaAction) []string {
	modelSet := referencedMetaActionModels(action)
	models := make([]string, 0, len(modelSet))
	for modelName := range modelSet {
		models = append(models, modelName)
	}
	sort.Strings(models)
	return models
}

func collectReferencedMetaActionModels(action MetaAction, models map[string]struct{}) {
	if strings.TrimSpace(action.Model) != "" {
		models[action.Model] = struct{}{}
	}
	if strings.TrimSpace(action.SynthesizeModel) != "" {
		models[action.SynthesizeModel] = struct{}{}
	}
	for _, call := range action.Calls {
		collectReferencedMetaActionModels(call, models)
	}
	for _, route := range action.Routes {
		collectReferencedMetaActionModels(route.Action, models)
	}
	for _, branch := range action.Switches {
		collectReferencedMetaActionModels(branch.Action, models)
	}
}

func ListMetaModelNamesForRequest(c *gin.Context) ([]string, error) {
	var names []string
	if err := model.DB.Model(&MetaModel{}).Where("enabled = ?", true).Pluck("name", &names).Error; err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

func ListMetaModelCatalogForRequest(c *gin.Context) ([]MetaModelCatalogItem, error) {
	var items []MetaModel
	if err := model.DB.Where("enabled = ?", true).Order("name ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	catalog := make([]MetaModelCatalogItem, 0, len(items))
	for _, item := range items {
		plan, err := ParseMetaModuleDSL(item.DSL)
		if err != nil {
			return nil, err
		}
		catalog = append(catalog, MetaModelCatalogItem{
			Name:                   item.Name,
			Description:            item.Description,
			Provider:               normalizeMetaProvider(item.Provider),
			ProviderName:           normalizeMetaProviderName(item.ProviderName),
			ProviderIconURL:        strings.TrimSpace(item.ProviderIconURL),
			BillingMode:            normalizeMetaBillingMode(item.BillingMode),
			InputPrice:             item.InputPrice,
			OutputPrice:            item.OutputPrice,
			CachedInputPrice:       item.CachedInputPrice,
			ExposeReferencedModels: item.ExposeReferencedModels,
			ReferencedModels:       sortedReferencedMetaActionModels(plan.Root),
		})
	}
	return catalog, nil
}

func ResolveMetaModelForRequest(c *gin.Context, input MetaModelResolveInput) (MetaModelResolveResult, error) {
	var meta MetaModel
	queryResult := model.DB.Where("name = ? AND enabled = ?", strings.TrimSpace(input.ModelName), true).Limit(1).Find(&meta)
	if queryResult.Error != nil {
		return MetaModelResolveResult{}, queryResult.Error
	}
	if queryResult.RowsAffected == 0 {
		return MetaModelResolveResult{}, nil
	}
	if !metaModelAllowedByAPIKey(c, meta.Name) {
		return MetaModelResolveResult{
			Matched:      true,
			ErrorStatus:  http.StatusForbidden,
			ErrorMessage: "API key is not allowed to use this meta model",
		}, nil
	}

	plan, err := ParseMetaModuleDSL(meta.DSL)
	if err != nil {
		return MetaModelResolveResult{}, err
	}
	vars := metaRuntimeVariables(c, meta.Name, input)
	modelName, err := resolveMetaAction(plan.Root, vars)
	if err != nil {
		return MetaModelResolveResult{
			Matched:      true,
			ErrorStatus:  http.StatusBadRequest,
			ErrorMessage: err.Error(),
		}, nil
	}
	result := MetaModelResolveResult{
		Matched:              true,
		ModelName:            modelName,
		BillingMode:          normalizeMetaBillingMode(meta.BillingMode),
		SkipAPIKeyModelCheck: true,
	}
	if result.BillingMode == MetaBillingModeMeta {
		result.BillingModel = &model.Model{
			ModelName:        meta.Name,
			InputPrice:       meta.InputPrice,
			OutputPrice:      meta.OutputPrice,
			CachedInputPrice: meta.CachedInputPrice,
		}
	}
	return result, nil
}

func metaModelAllowedByAPIKey(c *gin.Context, modelName string) bool {
	value, exists := c.Get("api_key")
	if !exists {
		return true
	}
	apiKey, ok := value.(*model.APIKey)
	if !ok || apiKey == nil {
		return true
	}
	return APIKeyAllowsModel(apiKey, modelName)
}

func resolveMetaAction(action MetaAction, vars map[string]MetaValue) (string, error) {
	switch action.Kind {
	case MetaActionCall:
		if strings.TrimSpace(action.Model) == "" {
			return "", errors.New("call action has no model")
		}
		return action.Model, nil
	case MetaActionRoute:
		for _, route := range action.Routes {
			if route.Otherwise {
				return resolveMetaAction(route.Action, vars)
			}
			if route.Condition == nil {
				continue
			}
			ok, err := evalMetaExpression(*route.Condition, vars)
			if err != nil {
				return "", err
			}
			if ok {
				return resolveMetaAction(route.Action, vars)
			}
		}
		return "", errors.New("route did not match and has no otherwise branch")
	case MetaActionSwitch:
		branch, err := selectMetaSwitchBranch(action.Switches)
		if err != nil {
			return "", err
		}
		return resolveMetaAction(branch.Action, vars)
	case MetaActionParallel:
		return "", errors.New("parallel meta model execution is not implemented yet")
	case MetaActionJudge:
		return "", errors.New("judge meta model execution is not implemented yet")
	default:
		return "", errors.New("unsupported meta model action")
	}
}

func selectMetaSwitchBranch(branches []MetaSwitch) (MetaSwitch, error) {
	total := 0.0
	otherwiseIndex := -1
	lastWeightedIndex := -1
	for index, branch := range branches {
		if branch.Otherwise {
			otherwiseIndex = index
			continue
		}
		if branch.Weight <= 0 {
			return MetaSwitch{}, errors.New("switch weight must be positive")
		}
		total += branch.Weight
		lastWeightedIndex = index
	}
	if total <= 0 {
		return MetaSwitch{}, errors.New("switch requires at least one weighted branch")
	}

	randomMax := total
	if otherwiseIndex >= 0 && total < 1 {
		randomMax = 1
	}
	draw, err := metaRandomFloat(randomMax)
	if err != nil {
		return MetaSwitch{}, err
	}
	cursor := 0.0
	for _, branch := range branches {
		if branch.Otherwise {
			continue
		}
		cursor += branch.Weight
		if draw < cursor {
			return branch, nil
		}
	}
	if otherwiseIndex >= 0 {
		return branches[otherwiseIndex], nil
	}
	if lastWeightedIndex >= 0 {
		return branches[lastWeightedIndex], nil
	}
	return MetaSwitch{}, errors.New("switch did not select a branch")
}

func metaRandomFloat(max float64) (float64, error) {
	if max <= 0 {
		return 0, errors.New("random max must be positive")
	}
	const precision = int64(1 << 53)
	value, err := cryptorand.Int(cryptorand.Reader, big.NewInt(precision))
	if err != nil {
		return 0, err
	}
	return (float64(value.Int64()) / float64(precision)) * max, nil
}

func evalMetaExpression(expr MetaExpression, vars map[string]MetaValue) (bool, error) {
	left, ok := vars[expr.Left]
	if !ok {
		return false, errors.New("Unknown variable: " + expr.Left)
	}
	switch expr.Operator {
	case "==", "!=":
		equal, err := metaValuesEqual(left, expr.Right)
		if err != nil {
			return false, err
		}
		if expr.Operator == "!=" {
			return !equal, nil
		}
		return equal, nil
	case "<", "<=", ">", ">=":
		if left.Kind != MetaValueNumber || expr.Right.Kind != MetaValueNumber {
			return false, errors.New("Operator " + expr.Operator + " requires number operands")
		}
		switch expr.Operator {
		case "<":
			return left.Number < expr.Right.Number, nil
		case "<=":
			return left.Number <= expr.Right.Number, nil
		case ">":
			return left.Number > expr.Right.Number, nil
		default:
			return left.Number >= expr.Right.Number, nil
		}
	case "contains", "not_contains", "starts_with", "ends_with", "matches":
		if left.Kind != MetaValueString || expr.Right.Kind != MetaValueString {
			return false, errors.New("Operator " + expr.Operator + " requires string operands")
		}
		switch expr.Operator {
		case "contains":
			return strings.Contains(left.String, expr.Right.String), nil
		case "not_contains":
			return !strings.Contains(left.String, expr.Right.String), nil
		case "starts_with":
			return strings.HasPrefix(left.String, expr.Right.String), nil
		case "ends_with":
			return strings.HasSuffix(left.String, expr.Right.String), nil
		default:
			matched, err := regexp.MatchString(expr.Right.String, left.String)
			if err != nil {
				return false, errors.New("Invalid regular expression: " + err.Error())
			}
			return matched, nil
		}
	default:
		return false, errors.New("Unsupported operator: " + expr.Operator)
	}
}

func metaValuesEqual(left MetaValue, right MetaValue) (bool, error) {
	if left.Kind != right.Kind {
		return false, nil
	}
	switch left.Kind {
	case MetaValueString:
		return left.String == right.String, nil
	case MetaValueNumber:
		return left.Number == right.Number, nil
	case MetaValueBool:
		return left.Bool == right.Bool, nil
	default:
		return false, errors.New("Unsupported value kind")
	}
}

func metaRuntimeVariables(c *gin.Context, metaModelName string, input MetaModelResolveInput) map[string]MetaValue {
	inputTokens := CountTokens(metaModelName, string(input.OriginalBody))
	maxOutputTokens := numberFromRequest(input.RequestBody, "max_tokens", "max_completion_tokens", "maxOutputTokens")
	requestText := metaRequestText(input.RequestBody)
	lastUserMessage := metaLastMessageText(input.RequestBody, "user")
	systemPrompt := metaSystemPromptText(input.RequestBody)
	apiKeyName, apiKeyQuotaLimit := apiKeyRuntimeValues(c)
	userID, userGroup, isAdmin := userRuntimeValues(c)
	values := map[string]MetaValue{
		"request.input_tokens":           {Kind: MetaValueNumber, Number: float64(inputTokens)},
		"request.max_output_tokens":      {Kind: MetaValueNumber, Number: float64(maxOutputTokens)},
		"request.total_estimated_tokens": {Kind: MetaValueNumber, Number: float64(inputTokens + maxOutputTokens)},
		"request.message_count":          {Kind: MetaValueNumber, Number: float64(messageCount(input.RequestBody))},
		"request.text":                   {Kind: MetaValueString, String: requestText},
		"request.last_user_message":      {Kind: MetaValueString, String: lastUserMessage},
		"request.system":                 {Kind: MetaValueString, String: systemPrompt},
		"request.has_image":              {Kind: MetaValueBool, Bool: requestContainsAnyKey(input.RequestBody, "image", "image_url", "images")},
		"request.has_audio":              {Kind: MetaValueBool, Bool: requestContainsAnyKey(input.RequestBody, "audio", "input_audio", "audio_url")},
		"request.has_tools":              {Kind: MetaValueBool, Bool: requestContainsAnyKey(input.RequestBody, "tools", "tool_choice", "function_call", "functions")},
		"request.tool_count":             {Kind: MetaValueNumber, Number: float64(toolCount(input.RequestBody))},
		"request.stream":                 {Kind: MetaValueBool, Bool: boolFromRequest(input.RequestBody, "stream")},
		"request.temperature":            {Kind: MetaValueNumber, Number: numberFromRequestFloat(input.RequestBody, "temperature")},
		"user.balance":                   {Kind: MetaValueNumber, Number: userBalance(c)},
		"user.id":                        {Kind: MetaValueNumber, Number: float64(userID)},
		"user.group":                     {Kind: MetaValueString, String: userGroup},
		"user.is_admin":                  {Kind: MetaValueBool, Bool: isAdmin},
		"api_key.name":                   {Kind: MetaValueString, String: apiKeyName},
		"api_key.quota_limit":            {Kind: MetaValueNumber, Number: apiKeyQuotaLimit},
		"api_key.quota_remaining":        {Kind: MetaValueNumber, Number: apiKeyQuotaRemaining(c)},
		"channel.name":                   {Kind: MetaValueString, String: ""},
	}
	return values
}

func numberFromRequest(request map[string]interface{}, keys ...string) int {
	for _, key := range keys {
		switch value := request[key].(type) {
		case float64:
			return int(value)
		case int:
			return value
		case json.Number:
			parsed, err := value.Int64()
			if err == nil {
				return int(parsed)
			}
		case string:
			parsed, err := strconv.Atoi(value)
			if err == nil {
				return parsed
			}
		}
	}
	return 0
}

func numberFromRequestFloat(request map[string]interface{}, keys ...string) float64 {
	for _, key := range keys {
		switch value := request[key].(type) {
		case float64:
			return value
		case int:
			return float64(value)
		case json.Number:
			parsed, err := value.Float64()
			if err == nil {
				return parsed
			}
		case string:
			parsed, err := strconv.ParseFloat(value, 64)
			if err == nil {
				return parsed
			}
		}
	}
	return 0
}

func boolFromRequest(request map[string]interface{}, keys ...string) bool {
	for _, key := range keys {
		switch value := request[key].(type) {
		case bool:
			return value
		case string:
			parsed, err := strconv.ParseBool(value)
			if err == nil {
				return parsed
			}
		}
	}
	return false
}

func messageCount(request map[string]interface{}) int {
	if messages, ok := request["messages"].([]interface{}); ok {
		return len(messages)
	}
	if contents, ok := request["contents"].([]interface{}); ok {
		return len(contents)
	}
	if input, ok := request["input"].([]interface{}); ok {
		return len(input)
	}
	return 0
}

func toolCount(request map[string]interface{}) int {
	if tools, ok := request["tools"].([]interface{}); ok {
		return len(tools)
	}
	if functions, ok := request["functions"].([]interface{}); ok {
		return len(functions)
	}
	return 0
}

func metaRequestText(request map[string]interface{}) string {
	parts := []string{}
	for _, key := range []string{"input", "messages", "contents", "prompt", "system"} {
		if text := metaContentToText(request[key]); strings.TrimSpace(text) != "" {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func metaLastMessageText(request map[string]interface{}, role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	for _, key := range []string{"messages", "input", "contents"} {
		items, ok := request[key].([]interface{})
		if !ok {
			continue
		}
		for i := len(items) - 1; i >= 0; i-- {
			item, ok := items[i].(map[string]interface{})
			if !ok {
				continue
			}
			itemRole, _ := item["role"].(string)
			if role != "" && strings.ToLower(strings.TrimSpace(itemRole)) != role {
				continue
			}
			if text := metaContentToText(item["content"]); strings.TrimSpace(text) != "" {
				return text
			}
			if text := metaContentToText(item["text"]); strings.TrimSpace(text) != "" {
				return text
			}
		}
	}
	return ""
}

func metaSystemPromptText(request map[string]interface{}) string {
	if text := metaContentToText(request["system"]); strings.TrimSpace(text) != "" {
		return text
	}
	for _, key := range []string{"messages", "input"} {
		items, ok := request[key].([]interface{})
		if !ok {
			continue
		}
		for _, raw := range items {
			item, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			role, _ := item["role"].(string)
			if strings.ToLower(strings.TrimSpace(role)) != "system" && strings.ToLower(strings.TrimSpace(role)) != "developer" {
				continue
			}
			if text := metaContentToText(item["content"]); strings.TrimSpace(text) != "" {
				return text
			}
		}
	}
	return ""
}

func metaContentToText(raw interface{}) string {
	switch value := raw.(type) {
	case string:
		return value
	case []interface{}:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			if text := metaContentToText(item); strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]interface{}:
		for _, key := range []string{"text", "content", "input_text"} {
			if text, ok := value[key].(string); ok {
				return text
			}
		}
		if parts, ok := value["parts"].([]interface{}); ok {
			return metaContentToText(parts)
		}
	}
	return ""
}

func requestContainsAnyKey(value interface{}, keys ...string) bool {
	keySet := map[string]struct{}{}
	for _, key := range keys {
		keySet[strings.ToLower(key)] = struct{}{}
	}
	return requestContainsKeySet(value, keySet)
}

func requestContainsKeySet(value interface{}, keys map[string]struct{}) bool {
	switch typed := value.(type) {
	case map[string]interface{}:
		for key, item := range typed {
			if _, ok := keys[strings.ToLower(key)]; ok {
				return true
			}
			if requestContainsKeySet(item, keys) {
				return true
			}
		}
	case []interface{}:
		for _, item := range typed {
			if requestContainsKeySet(item, keys) {
				return true
			}
		}
	}
	return false
}

func userBalance(c *gin.Context) float64 {
	if PersonalModeEnabled() {
		return -1
	}
	if c == nil {
		return 0
	}
	value, exists := c.Get("user")
	if !exists {
		return 0
	}
	user, ok := value.(*model.User)
	if !ok || user == nil {
		return 0
	}
	balance, _ := user.Balance.Float64()
	return balance
}

func userRuntimeValues(c *gin.Context) (uint, string, bool) {
	if c == nil {
		return 0, "", false
	}
	value, exists := c.Get("user")
	if !exists {
		return 0, "", false
	}
	user, ok := value.(*model.User)
	if !ok || user == nil {
		return 0, "", false
	}
	group := strings.TrimSpace(user.Group.Name)
	if group == "" && user.GroupID != 0 {
		var storedGroup model.Group
		if err := model.DB.First(&storedGroup, user.GroupID).Error; err == nil {
			group = storedGroup.Name
		}
	}
	return user.ID, group, user.IsAdmin
}

func apiKeyRuntimeValues(c *gin.Context) (string, float64) {
	if PersonalModeEnabled() {
		return "", -1
	}
	if c == nil {
		return "", 0
	}
	value, exists := c.Get("api_key")
	if !exists {
		return "", 0
	}
	apiKey, ok := value.(*model.APIKey)
	if !ok || apiKey == nil {
		return "", 0
	}
	quotaLimit, _ := apiKey.QuotaLimit.Float64()
	return apiKey.Name, quotaLimit
}

func apiKeyQuotaRemaining(c *gin.Context) float64 {
	if PersonalModeEnabled() {
		return -1
	}
	if c == nil {
		return 0
	}
	value, exists := c.Get("api_key")
	if !exists {
		return 0
	}
	apiKey, ok := value.(*model.APIKey)
	if !ok || apiKey == nil || apiKey.QuotaLimit.LessThanOrEqual(decimal.Zero) {
		return 0
	}
	used, err := APIKeyUsageCost(model.DB, apiKey.ID, apiKey.UserID)
	if err != nil {
		return 0
	}
	remaining := apiKey.QuotaLimit.Sub(used)
	if remaining.LessThan(decimal.Zero) {
		remaining = decimal.Zero
	}
	valueFloat, _ := remaining.Float64()
	return valueFloat
}

func normalizeMetaBillingMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", MetaBillingModeActual:
		return MetaBillingModeActual
	case MetaBillingModeMeta:
		return MetaBillingModeMeta
	default:
		return ""
	}
}

func normalizeMetaProvider(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultMetaProvider
	}
	return value
}

func normalizeMetaProviderName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultMetaProviderName
	}
	return value
}
