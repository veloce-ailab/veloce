package service

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"

	"github.com/veloce-ailab/veloce/internal/cache"
	"github.com/veloce-ailab/veloce/internal/model"
)

var ErrInsufficientBalance = errors.New("insufficient balance")

type StartupHook func() error
type RouteHook func(*gin.RouterGroup)
type GeneratedAssetHook func(context.Context, GeneratedAssetInput)

type MetaModelResolveInput struct {
	ModelName    string
	RequestBody  map[string]interface{}
	OriginalBody []byte
}

type MetaModelResolveResult struct {
	Matched              bool
	ModelName            string
	BillingMode          string
	BillingModel         *model.Model
	SkipAPIKeyModelCheck bool
	ErrorStatus          int
	ErrorMessage         string
}

type MetaModelCatalogItem struct {
	Name                   string          `json:"name"`
	Description            string          `json:"description"`
	Provider               string          `json:"provider"`
	ProviderName           string          `json:"provider_name"`
	ProviderIconURL        string          `json:"provider_icon_url"`
	BillingMode            string          `json:"billing_mode"`
	InputPrice             decimal.Decimal `json:"input_price"`
	OutputPrice            decimal.Decimal `json:"output_price"`
	CachedInputPrice       decimal.Decimal `json:"cached_input_price"`
	ExposeReferencedModels bool            `json:"expose_referenced_models"`
	ReferencedModels       []string        `json:"referenced_models"`
}

type GeneratedAssetInput struct {
	UserID       uint
	Kind         string
	ModelName    string
	ResponseData map[string]interface{}
	ResponseBody []byte
	Source       string
}

var startupHooks []StartupHook
var publicAPIRouteHooks []RouteHook
var adminRouteHooks []RouteHook
var userRouteHooks []RouteHook
var generatedAssetHook GeneratedAssetHook

func RegisterStartupHook(hook StartupHook) {
	startupHooks = append(startupHooks, hook)
}

func RunStartupHooks() error {
	for _, hook := range startupHooks {
		if hook == nil {
			continue
		}
		if err := hook(); err != nil {
			return err
		}
	}
	return nil
}

func RegisterAdminRouteHook(hook RouteHook) {
	adminRouteHooks = append(adminRouteHooks, hook)
}

func RegisterPublicAPIRouteHook(hook RouteHook) {
	publicAPIRouteHooks = append(publicAPIRouteHooks, hook)
}

func RegisterUserRouteHook(hook RouteHook) {
	userRouteHooks = append(userRouteHooks, hook)
}

func ApplyPublicAPIRouteHooks(group *gin.RouterGroup) {
	for _, hook := range publicAPIRouteHooks {
		if hook != nil {
			hook(group)
		}
	}
}

func ApplyAdminRouteHooks(group *gin.RouterGroup) {
	for _, hook := range adminRouteHooks {
		if hook != nil {
			hook(group)
		}
	}
}

func ApplyUserRouteHooks(group *gin.RouterGroup) {
	for _, hook := range userRouteHooks {
		if hook != nil {
			hook(group)
		}
	}
}

func RegisterGeneratedAssetHook(hook GeneratedAssetHook) {
	generatedAssetHook = hook
}

func ListMetaModelNames(c *gin.Context) ([]string, error) {
	return ListMetaModelNamesForRequest(c)
}

func ListMetaModelCatalog(c *gin.Context) ([]MetaModelCatalogItem, error) {
	return ListMetaModelCatalogForRequest(c)
}

func ApplyGeneratedAssetHook(ctx context.Context, input GeneratedAssetInput) {
	if generatedAssetHook == nil {
		return
	}
	generatedAssetHook(ctx, input)
}

func ResolveMetaModel(c *gin.Context, input MetaModelResolveInput) (MetaModelResolveResult, error) {
	result, err := ResolveMetaModelForRequest(c, input)
	if result.ErrorStatus == 0 && result.ErrorMessage != "" {
		result.ErrorStatus = http.StatusBadRequest
	}
	return result, err
}

func ApplyUsageCharge(tx *gorm.DB, userID uint, cost decimal.Decimal) error {
	if cost.LessThanOrEqual(decimal.Zero) {
		return nil
	}
	if PersonalModeEnabledInTx(tx) {
		return nil
	}
	// Subscription quota is spent before the wallet; applySubscriptionUsageCharge
	// falls back to the wallet balance itself when the quota cannot cover it.
	return applySubscriptionUsageCharge(tx, userID, cost)
}

func ApplyUsageChargeForAPIKey(tx *gorm.DB, userID uint, apiKey *model.APIKey, cost decimal.Decimal) error {
	if cost.LessThanOrEqual(decimal.Zero) || PersonalModeEnabledInTx(tx) {
		return nil
	}
	return applySubscriptionUsageChargeForAPIKey(tx, userID, apiKey, cost)
}

// ApplyDeliveredUsageCharge charges usage for a response that has already been
// sent to the client. Refusing the charge is not an option at that point, so a
// shortfall drains whatever balance is left instead of leaving it untouched:
// otherwise a user with a fraction of a cent passes the "balance > 0" precheck,
// receives the full response, fails the charge, and can repeat that forever.
func ApplyDeliveredUsageCharge(tx *gorm.DB, userID uint, cost decimal.Decimal) error {
	err := ApplyUsageCharge(tx, userID, cost)
	if !errors.Is(err, ErrInsufficientBalance) {
		return err
	}
	drain := tx.Exec("UPDATE users SET balance = 0 WHERE id = ? AND balance > 0", userID)
	if drain.Error != nil {
		return drain.Error
	}
	return nil
}

func ApplyDeliveredUsageChargeForAPIKey(tx *gorm.DB, userID uint, apiKey *model.APIKey, cost decimal.Decimal) error {
	err := ApplyUsageChargeForAPIKey(tx, userID, apiKey, cost)
	if !errors.Is(err, ErrInsufficientBalance) {
		return err
	}
	if apiKey != nil && strings.TrimSpace(apiKey.BillingSources) != "" {
		allowsBalance := false
		for _, source := range ParseBillingSources(apiKey.BillingSources) {
			if source == billingSourceBalance {
				allowsBalance = true
				break
			}
		}
		if !allowsBalance {
			return nil
		}
	}
	drain := tx.Exec("UPDATE users SET balance = 0 WHERE id = ? AND balance > 0", userID)
	if drain.Error != nil {
		return drain.Error
	}
	return nil
}

func committedBillingBalance(tx *gorm.DB, userID uint) (decimal.Decimal, error) {
	if cache.Client() == nil {
		return decimal.Zero, nil
	}
	var user model.User
	if err := tx.Select("balance").First(&user, userID).Error; err != nil {
		return decimal.Zero, err
	}
	return user.Balance, nil
}

func billingContext(c *gin.Context) context.Context {
	if c != nil && c.Request != nil {
		return c.Request.Context()
	}
	return context.Background()
}
