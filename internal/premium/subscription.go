package premium

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	communityservice "github.com/veloce-ailab/veloce/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SubscriptionPlan struct {
	ID                uint            `gorm:"primaryKey" json:"id"`
	Name              string          `gorm:"uniqueIndex;size:100;not null" json:"name"`
	ResetAmount       decimal.Decimal `gorm:"type:decimal(20,6);not null" json:"reset_amount"`
	ResetIntervalDays int             `gorm:"not null" json:"reset_interval_days"`
	Price             decimal.Decimal `gorm:"type:decimal(20,6);not null;default:0" json:"price"`
	DurationDays      int             `gorm:"default:0" json:"duration_days"`
	Purchasable       bool            `gorm:"default:false" json:"purchasable"`
	Enabled           bool            `gorm:"default:true" json:"enabled"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

type UserSubscription struct {
	ID          uint             `gorm:"primaryKey" json:"id"`
	UserID      uint             `gorm:"index;not null" json:"user_id"`
	PlanID      uint             `gorm:"index;not null" json:"plan_id"`
	Plan        SubscriptionPlan `gorm:"foreignKey:PlanID" json:"plan"`
	Balance     decimal.Decimal  `gorm:"type:decimal(20,6);not null" json:"balance"`
	ActiveUntil *time.Time       `gorm:"index" json:"active_until"`
	NextResetAt time.Time        `gorm:"index;not null" json:"next_reset_at"`
	LastResetAt *time.Time       `json:"last_reset_at"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

type PremiumRedeemCode struct {
	ID                       uint                   `gorm:"primaryKey" json:"id"`
	Code                     string                 `gorm:"uniqueIndex;size:64;not null" json:"code"`
	Amount                   decimal.Decimal        `gorm:"type:decimal(20,6);not null" json:"amount"`
	GroupID                  *uint                  `gorm:"index" json:"group_id"`
	Group                    model.Group            `gorm:"foreignKey:GroupID" json:"group,omitempty"`
	GroupDurationDays        int                    `gorm:"default:0" json:"group_duration_days"`
	SubscriptionPlanID       *uint                  `gorm:"index" json:"subscription_plan_id"`
	SubscriptionPlan         *SubscriptionPlan      `gorm:"foreignKey:SubscriptionPlanID" json:"subscription_plan,omitempty"`
	SubscriptionDurationDays int                    `gorm:"default:0" json:"subscription_duration_days"`
	AllowStacking            bool                   `gorm:"default:false" json:"allow_stacking"`
	MaxUses                  int                    `gorm:"default:1" json:"max_uses"`
	UsedCount                int                    `gorm:"default:0" json:"used_count"`
	Enabled                  bool                   `gorm:"default:true" json:"enabled"`
	ExpiresAt                *time.Time             `json:"expires_at"`
	CreatedAt                time.Time              `json:"created_at"`
	UpdatedAt                time.Time              `json:"updated_at"`
	Redemptions              []PremiumRedemptionLog `gorm:"foreignKey:RedeemCodeID" json:"-"`
}

func (PremiumRedeemCode) TableName() string {
	return "redeem_codes"
}

type PremiumRedemptionLog struct {
	ID                 uint            `gorm:"primaryKey" json:"id"`
	RedeemCodeID       uint            `gorm:"uniqueIndex:idx_redeem_code_user;not null" json:"redeem_code_id"`
	UserID             uint            `gorm:"uniqueIndex:idx_redeem_code_user;not null" json:"user_id"`
	Amount             decimal.Decimal `gorm:"type:decimal(20,6);not null" json:"amount"`
	SubscriptionPlanID *uint           `gorm:"index" json:"subscription_plan_id"`
	CreatedAt          time.Time       `json:"created_at"`
}

func (PremiumRedemptionLog) TableName() string {
	return "redeem_code_redemptions"
}

type subscriptionAPI struct{}

type subscriptionPlanInput struct {
	Name              string          `json:"name"`
	ResetAmount       decimal.Decimal `json:"reset_amount"`
	ResetIntervalDays int             `json:"reset_interval_days"`
	Price             decimal.Decimal `json:"price"`
	DurationDays      int             `json:"duration_days"`
	Purchasable       *bool           `json:"purchasable"`
	Enabled           *bool           `json:"enabled"`
}

type premiumRedeemCodeInput struct {
	Code                     string          `json:"code"`
	Amount                   decimal.Decimal `json:"amount"`
	GroupID                  *uint           `json:"group_id"`
	GroupDurationDays        int             `json:"group_duration_days"`
	SubscriptionPlanID       *uint           `json:"subscription_plan_id"`
	SubscriptionDurationDays int             `json:"subscription_duration_days"`
	AllowStacking            bool            `json:"allow_stacking"`
	MaxUses                  int             `json:"max_uses"`
	Enabled                  *bool           `json:"enabled"`
	ExpiresAt                string          `json:"expires_at"`
}

func initSubscriptionFeatures() error {
	return model.DB.AutoMigrate(&SubscriptionPlan{}, &UserSubscription{}, &PremiumRedeemCode{}, &PremiumRedemptionLog{})
}

func registerSubscriptionAdminRoutes(group *gin.RouterGroup) {
	api := &subscriptionAPI{}
	group.GET("/subscription-plans", api.listPlans)
	group.POST("/subscription-plans", api.createPlan)
	group.PUT("/subscription-plans/:id", api.updatePlan)
	group.DELETE("/subscription-plans/:id", api.deletePlan)
	group.GET("/redeem-codes", api.listRedeemCodes)
	group.POST("/redeem-codes", api.createRedeemCode)
	group.PUT("/redeem-codes/:id", api.updateRedeemCode)
	group.DELETE("/redeem-codes/:id", api.deleteRedeemCode)
}

func registerSubscriptionUserRoutes(group *gin.RouterGroup) {
	api := &subscriptionAPI{}
	group.GET("/subscription", api.mySubscription)
	group.GET("/subscription-plans", api.purchasablePlans)
	group.POST("/subscription/purchase", api.purchaseSubscription)
	group.POST("/redeem-code", api.redeemCode)
}

func requireOperationModeFeature(c *gin.Context) bool {
	if !communityservice.PersonalModeEnabled() {
		return true
	}
	c.JSON(http.StatusForbidden, gin.H{"error": "This feature is disabled in personal mode"})
	return false
}

func (api *subscriptionAPI) listPlans(c *gin.Context) {
	if !requireOperationModeFeature(c) {
		return
	}
	var plans []SubscriptionPlan
	if err := model.DB.Order("created_at DESC").Find(&plans).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, plans)
}

func (api *subscriptionAPI) createPlan(c *gin.Context) {
	if !requireOperationModeFeature(c) {
		return
	}
	var input subscriptionPlanInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	plan, ok := subscriptionPlanFromInput(c, input, true)
	if !ok {
		return
	}
	if err := model.DB.Create(&plan).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, plan)
}

func (api *subscriptionAPI) updatePlan(c *gin.Context) {
	if !requireOperationModeFeature(c) {
		return
	}
	var plan SubscriptionPlan
	if err := model.DB.First(&plan, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Subscription plan not found"})
		return
	}
	var input subscriptionPlanInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	next, ok := subscriptionPlanFromInput(c, input, plan.Enabled)
	if !ok {
		return
	}
	updates := map[string]interface{}{
		"name":                next.Name,
		"reset_amount":        next.ResetAmount,
		"reset_interval_days": next.ResetIntervalDays,
		"price":               next.Price,
		"duration_days":       next.DurationDays,
		"purchasable":         next.Purchasable,
		"enabled":             next.Enabled,
	}
	if err := model.DB.Model(&plan).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	model.DB.First(&plan, plan.ID)
	c.JSON(http.StatusOK, plan)
}

func (api *subscriptionAPI) deletePlan(c *gin.Context) {
	if !requireOperationModeFeature(c) {
		return
	}
	var count int64
	model.DB.Model(&UserSubscription{}).Where("plan_id = ?", c.Param("id")).Count(&count)
	if count > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Subscription plan is in use"})
		return
	}
	model.DB.Model(&PremiumRedeemCode{}).Where("subscription_plan_id = ?", c.Param("id")).Count(&count)
	if count > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Subscription plan is used by redeem codes"})
		return
	}
	if err := model.DB.Delete(&SubscriptionPlan{}, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Subscription plan deleted"})
}

func subscriptionPlanFromInput(c *gin.Context, input subscriptionPlanInput, fallbackEnabled bool) (SubscriptionPlan, bool) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Plan name is required"})
		return SubscriptionPlan{}, false
	}
	if input.ResetAmount.LessThanOrEqual(decimal.Zero) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Reset amount must be greater than zero"})
		return SubscriptionPlan{}, false
	}
	if input.ResetIntervalDays <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Reset interval must be greater than zero"})
		return SubscriptionPlan{}, false
	}
	if input.Price.LessThan(decimal.Zero) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Price must not be negative"})
		return SubscriptionPlan{}, false
	}
	if input.DurationDays < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Duration must not be negative"})
		return SubscriptionPlan{}, false
	}
	purchasable := input.Purchasable != nil && *input.Purchasable
	if purchasable && input.Price.LessThanOrEqual(decimal.Zero) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Purchasable plans require a price greater than zero"})
		return SubscriptionPlan{}, false
	}
	enabled := fallbackEnabled
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	return SubscriptionPlan{
		Name:              name,
		ResetAmount:       input.ResetAmount,
		ResetIntervalDays: input.ResetIntervalDays,
		Price:             input.Price,
		DurationDays:      input.DurationDays,
		Purchasable:       purchasable,
		Enabled:           enabled,
	}, true
}

func (api *subscriptionAPI) listRedeemCodes(c *gin.Context) {
	if !requireOperationModeFeature(c) {
		return
	}
	var codes []PremiumRedeemCode
	if err := model.DB.Preload("Group").Preload("SubscriptionPlan").Order("created_at DESC").Find(&codes).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, codes)
}

func (api *subscriptionAPI) createRedeemCode(c *gin.Context) {
	if !requireOperationModeFeature(c) {
		return
	}
	var input premiumRedeemCodeInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	code, ok := redeemCodeFromInput(c, input, true)
	if !ok {
		return
	}
	if err := model.DB.Create(&code).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	model.DB.Preload("Group").Preload("SubscriptionPlan").First(&code, code.ID)
	c.JSON(http.StatusOK, code)
}

func (api *subscriptionAPI) updateRedeemCode(c *gin.Context) {
	if !requireOperationModeFeature(c) {
		return
	}
	var existing PremiumRedeemCode
	if err := model.DB.First(&existing, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Redeem code not found"})
		return
	}
	var input premiumRedeemCodeInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	next, ok := redeemCodeFromInput(c, input, existing.Enabled)
	if !ok {
		return
	}
	updates := map[string]interface{}{
		"amount":                     next.Amount,
		"group_id":                   next.GroupID,
		"group_duration_days":        next.GroupDurationDays,
		"subscription_plan_id":       next.SubscriptionPlanID,
		"subscription_duration_days": next.SubscriptionDurationDays,
		"allow_stacking":             next.AllowStacking,
		"max_uses":                   next.MaxUses,
		"enabled":                    next.Enabled,
		"expires_at":                 next.ExpiresAt,
	}
	if next.Code != "" {
		updates["code"] = next.Code
	}
	if err := model.DB.Model(&existing).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	model.DB.Preload("Group").Preload("SubscriptionPlan").First(&existing, existing.ID)
	c.JSON(http.StatusOK, existing)
}

func (api *subscriptionAPI) deleteRedeemCode(c *gin.Context) {
	if !requireOperationModeFeature(c) {
		return
	}
	if err := model.DB.Delete(&PremiumRedeemCode{}, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Redeem code deleted"})
}

func redeemCodeFromInput(c *gin.Context, input premiumRedeemCodeInput, fallbackEnabled bool) (PremiumRedeemCode, bool) {
	if input.Amount.LessThan(decimal.Zero) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Amount must not be negative"})
		return PremiumRedeemCode{}, false
	}
	planID := uintFromPtr(input.SubscriptionPlanID)
	if input.Amount.IsZero() && uintFromPtr(input.GroupID) == 0 && planID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Amount, group, or subscription plan is required"})
		return PremiumRedeemCode{}, false
	}
	if planID != 0 {
		var plan SubscriptionPlan
		if err := model.DB.Where("id = ? AND enabled = ?", planID, true).First(&plan).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Subscription plan not found or disabled"})
			return PremiumRedeemCode{}, false
		}
		if input.SubscriptionDurationDays < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Subscription duration must not be negative"})
			return PremiumRedeemCode{}, false
		}
	}
	if groupID := uintFromPtr(input.GroupID); groupID != 0 {
		var group model.Group
		if err := model.DB.First(&group, groupID).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Group not found"})
			return PremiumRedeemCode{}, false
		}
	}
	code := normalizePremiumRedeemCode(input.Code)
	if code == "" {
		generated, err := generatePremiumRedeemCode()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate redeem code"})
			return PremiumRedeemCode{}, false
		}
		code = generated
	}
	expiresAt, err := parsePremiumTime(input.ExpiresAt)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid expiration time"})
		return PremiumRedeemCode{}, false
	}
	maxUses := input.MaxUses
	if maxUses <= 0 {
		maxUses = 1
	}
	enabled := fallbackEnabled
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	return PremiumRedeemCode{
		Code:                     code,
		Amount:                   input.Amount,
		GroupID:                  nullableUint(input.GroupID),
		GroupDurationDays:        input.GroupDurationDays,
		SubscriptionPlanID:       nullableUint(input.SubscriptionPlanID),
		SubscriptionDurationDays: input.SubscriptionDurationDays,
		AllowStacking:            input.AllowStacking,
		MaxUses:                  maxUses,
		Enabled:                  enabled,
		ExpiresAt:                expiresAt,
	}, true
}

func (api *subscriptionAPI) redeemCode(c *gin.Context) {
	if !requireOperationModeFeature(c) {
		return
	}
	user, ok := currentPremiumUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var input struct {
		Code string `json:"code"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	code := normalizePremiumRedeemCode(input.Code)
	if code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Redeem code is required"})
		return
	}

	var redeemed PremiumRedeemCode
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("code = ?", code).First(&redeemed).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errors.New("Redeem code not found")
			}
			return err
		}
		if !redeemed.Enabled {
			return errors.New("Redeem code is disabled")
		}
		if redeemed.ExpiresAt != nil && time.Now().After(*redeemed.ExpiresAt) {
			return errors.New("Redeem code has expired")
		}
		if redeemed.MaxUses > 0 && redeemed.UsedCount >= redeemed.MaxUses {
			return errors.New("Redeem code has been fully used")
		}
		var existing int64
		if err := tx.Model(&PremiumRedemptionLog{}).Where("redeem_code_id = ? AND user_id = ?", redeemed.ID, user.ID).Count(&existing).Error; err != nil {
			return err
		}
		if existing > 0 {
			return errors.New("Redeem code already used")
		}
		if err := tx.Create(&PremiumRedemptionLog{
			RedeemCodeID:       redeemed.ID,
			UserID:             user.ID,
			Amount:             redeemed.Amount,
			SubscriptionPlanID: redeemed.SubscriptionPlanID,
		}).Error; err != nil {
			return err
		}
		if redeemed.Amount.GreaterThan(decimal.Zero) {
			if err := tx.Model(&model.User{}).Where("id = ?", user.ID).UpdateColumn("balance", gorm.Expr("balance + ?", redeemed.Amount)).Error; err != nil {
				return err
			}
		}
		if redeemed.GroupID != nil && *redeemed.GroupID != 0 {
			if err := applyPremiumGroupGrant(tx, user.ID, *redeemed.GroupID, redeemed.GroupDurationDays, redeemed.AllowStacking); err != nil {
				return err
			}
		}
		if redeemed.SubscriptionPlanID != nil && *redeemed.SubscriptionPlanID != 0 {
			if err := grantPremiumSubscription(tx, user.ID, *redeemed.SubscriptionPlanID, redeemed.SubscriptionDurationDays, redeemed.AllowStacking); err != nil {
				return err
			}
		}
		return tx.Model(&PremiumRedeemCode{}).Where("id = ?", redeemed.ID).UpdateColumn("used_count", gorm.Expr("used_count + ?", 1)).Error
	}); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var refreshed model.User
	model.DB.Preload("Group").First(&refreshed, user.ID)
	c.JSON(http.StatusOK, gin.H{"amount": redeemed.Amount, "balance": refreshed.Balance})
}

func (api *subscriptionAPI) mySubscription(c *gin.Context) {
	user, ok := currentPremiumUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		return resetDueSubscriptions(tx, user.ID, time.Now())
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	var subscriptions []UserSubscription
	if err := activeSubscriptions(model.DB, user.ID, time.Now()).Preload("Plan").Find(&subscriptions).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, subscriptions)
}

// purchasablePlans lists the enabled plans users can buy directly.
func (api *subscriptionAPI) purchasablePlans(c *gin.Context) {
	if !requireOperationModeFeature(c) {
		return
	}
	var plans []SubscriptionPlan
	if err := model.DB.Where("enabled = ? AND purchasable = ?", true, true).Order("price ASC, created_at DESC").Find(&plans).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, plans)
}

// purchaseSubscription buys a plan with wallet balance. Renewing an active
// timed subscription extends its expiry instead of creating a parallel quota.
func (api *subscriptionAPI) purchaseSubscription(c *gin.Context) {
	if !requireOperationModeFeature(c) {
		return
	}
	user, ok := currentPremiumUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	var input struct {
		PlanID uint `json:"plan_id"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var plan SubscriptionPlan
	if err := model.DB.Where("id = ? AND enabled = ? AND purchasable = ?", input.PlanID, true, true).First(&plan).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Subscription plan is not available for purchase"})
		return
	}
	if plan.Price.LessThanOrEqual(decimal.Zero) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Subscription plan is not available for purchase"})
		return
	}

	now := time.Now()
	var existing UserSubscription
	hasExisting := activeSubscriptions(model.DB, user.ID, now).Where("plan_id = ?", plan.ID).Order("id DESC").First(&existing).Error == nil
	if hasExisting && (plan.DurationDays <= 0 || existing.ActiveUntil == nil) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "You already have an active subscription for this plan"})
		return
	}

	purchaseRef, err := generatePremiumRedeemCode()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create purchase reference"})
		return
	}
	settlement, err := communityservice.SettleWallet(c.Request.Context(), communityservice.WalletSettlementInput{
		UserID:         user.ID,
		Source:         "subscription_purchase",
		IdempotencyKey: purchaseRef,
		DebitAmount:    plan.Price,
		ReferenceType:  "subscription_plan",
		ReferenceID:    strconv.FormatUint(uint64(plan.ID), 10),
		Description:    "Purchase subscription plan: " + plan.Name,
	})
	if err != nil {
		if errors.Is(err, communityservice.ErrInsufficientBalance) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Insufficient balance"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	grantErr := model.DB.Transaction(func(tx *gorm.DB) error {
		if hasExisting && existing.ActiveUntil != nil && plan.DurationDays > 0 {
			renewed := existing.ActiveUntil.AddDate(0, 0, plan.DurationDays)
			result := tx.Model(&UserSubscription{}).Where("id = ?", existing.ID).Update("active_until", renewed)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected > 0 {
				return nil
			}
		}
		return grantPremiumSubscription(tx, user.ID, plan.ID, plan.DurationDays, true)
	})
	if grantErr != nil {
		// Refund the debit so a failed grant never costs the user.
		_, refundErr := communityservice.SettleWallet(c.Request.Context(), communityservice.WalletSettlementInput{
			UserID:         user.ID,
			Source:         "subscription_purchase_refund",
			IdempotencyKey: purchaseRef,
			CreditAmount:   plan.Price,
			ReferenceType:  "subscription_plan",
			ReferenceID:    strconv.FormatUint(uint64(plan.ID), 10),
			Description:    "Refund failed subscription purchase: " + plan.Name,
		})
		if refundErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Subscription grant failed and refund failed; contact support"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": grantErr.Error()})
		return
	}

	var subscriptions []UserSubscription
	if err := activeSubscriptions(model.DB, user.ID, time.Now()).Preload("Plan").Find(&subscriptions).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"balance":       settlement.Transaction.BalanceAfter,
		"subscriptions": subscriptions,
	})
}

func applySubscriptionUsageCharge(tx *gorm.DB, userID uint, cost decimal.Decimal) error {
	if cost.LessThanOrEqual(decimal.Zero) {
		return nil
	}
	now := time.Now()
	if err := resetDueSubscriptions(tx, userID, now); err != nil {
		return err
	}
	remaining := cost
	var subscriptions []UserSubscription
	if err := activeSubscriptions(tx, userID, now).Where("balance > ?", decimal.Zero).Order("active_until ASC, id ASC").Find(&subscriptions).Error; err != nil {
		return err
	}
	for _, subscription := range subscriptions {
		if remaining.LessThanOrEqual(decimal.Zero) {
			break
		}
		deduction := decimal.Min(subscription.Balance, remaining)
		if deduction.LessThanOrEqual(decimal.Zero) {
			continue
		}
		update := tx.Model(&UserSubscription{}).Where("id = ? AND balance >= ?", subscription.ID, deduction).UpdateColumn("balance", gorm.Expr("balance - ?", deduction))
		if update.Error != nil {
			return update.Error
		}
		// A concurrent charge may have drained this subscription first; only
		// count the deduction when it actually applied, otherwise the shortfall
		// must fall through to the next subscription or the wallet balance.
		if update.RowsAffected > 0 {
			remaining = remaining.Sub(deduction)
		}
	}
	if remaining.LessThanOrEqual(decimal.Zero) {
		return nil
	}
	balanceUpdate := tx.Model(&model.User{}).Where("id = ? AND balance >= ?", userID, remaining).UpdateColumn("balance", gorm.Expr("balance - ?", remaining))
	if balanceUpdate.Error != nil {
		return balanceUpdate.Error
	}
	if balanceUpdate.RowsAffected == 0 {
		return communityservice.ErrInsufficientBalance
	}
	return nil
}

func resetDueSubscriptions(tx *gorm.DB, userID uint, now time.Time) error {
	var subscriptions []UserSubscription
	if err := activeSubscriptions(tx, userID, now).Preload("Plan").Where("next_reset_at <= ?", now).Find(&subscriptions).Error; err != nil {
		return err
	}
	for _, subscription := range subscriptions {
		if subscription.Plan.ID == 0 || !subscription.Plan.Enabled || subscription.Plan.ResetIntervalDays <= 0 {
			continue
		}
		nextReset := subscription.NextResetAt
		for !nextReset.After(now) {
			nextReset = nextReset.AddDate(0, 0, subscription.Plan.ResetIntervalDays)
		}
		lastReset := now
		if err := tx.Model(&subscription).Updates(map[string]interface{}{
			"balance":       subscription.Plan.ResetAmount,
			"next_reset_at": nextReset,
			"last_reset_at": &lastReset,
		}).Error; err != nil {
			return err
		}
	}
	return nil
}

func activeSubscriptions(tx *gorm.DB, userID uint, now time.Time) *gorm.DB {
	return tx.Where("user_id = ? AND (active_until IS NULL OR active_until > ?)", userID, now)
}

func grantPremiumSubscription(tx *gorm.DB, userID uint, planID uint, durationDays int, allowStacking bool) error {
	var plan SubscriptionPlan
	if err := tx.Where("id = ? AND enabled = ?", planID, true).First(&plan).Error; err != nil {
		return err
	}
	now := time.Now()
	var activeUntil *time.Time
	if durationDays > 0 {
		base := now
		var existing UserSubscription
		if allowStacking {
			err := tx.Where("user_id = ? AND plan_id = ? AND active_until > ?", userID, planID, now).Order("active_until DESC").First(&existing).Error
			if err == nil && existing.ActiveUntil != nil {
				base = *existing.ActiveUntil
			} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		next := base.AddDate(0, 0, durationDays)
		activeUntil = &next
	}
	nextReset := now.AddDate(0, 0, plan.ResetIntervalDays)
	subscription := UserSubscription{
		UserID:      userID,
		PlanID:      plan.ID,
		Balance:     plan.ResetAmount,
		ActiveUntil: activeUntil,
		NextResetAt: nextReset,
	}
	return tx.Create(&subscription).Error
}

func applyPremiumGroupGrant(tx *gorm.DB, userID uint, groupID uint, durationDays int, allowStacking bool) error {
	var group model.Group
	if err := tx.First(&group, groupID).Error; err != nil {
		return err
	}
	var expiresAt *time.Time
	if durationDays > 0 {
		base := time.Now()
		var existing model.UserGroupMembership
		if allowStacking {
			if err := tx.Where("user_id = ? AND group_id = ?", userID, groupID).First(&existing).Error; err == nil && existing.ExpiresAt != nil && existing.ExpiresAt.After(base) {
				base = *existing.ExpiresAt
			} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		next := base.AddDate(0, 0, durationDays)
		expiresAt = &next
	}
	var membership model.UserGroupMembership
	err := tx.Where("user_id = ? AND group_id = ?", userID, groupID).First(&membership).Error
	if err == nil {
		return tx.Model(&membership).Updates(map[string]interface{}{"expires_at": expiresAt}).Error
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return tx.Create(&model.UserGroupMembership{UserID: userID, GroupID: groupID, ExpiresAt: expiresAt}).Error
}

func currentPremiumUser(c *gin.Context) (*model.User, bool) {
	value, exists := c.Get("user")
	if !exists {
		return nil, false
	}
	user, ok := value.(*model.User)
	return user, ok && user != nil
}

func normalizePremiumRedeemCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	code = strings.ReplaceAll(code, " ", "")
	return strings.ReplaceAll(code, "-", "")
}

func generatePremiumRedeemCode() (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:]), nil
}

func parsePremiumTime(value string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return &parsed, nil
		}
	}
	return nil, errors.New("invalid time")
}

func nullableUint(value *uint) *uint {
	if value == nil || *value == 0 {
		return nil
	}
	return value
}

func uintFromPtr(value *uint) uint {
	if value == nil {
		return 0
	}
	return *value
}
