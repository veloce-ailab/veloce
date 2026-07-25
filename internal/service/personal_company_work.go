package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type personalCompanyObjectiveInput struct {
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Priority    int        `json:"priority"`
	TargetDate  *time.Time `json:"target_date"`
}

type personalCompanyWorkItemInput struct {
	ObjectiveID      *uint           `json:"objective_id"`
	Title            string          `json:"title"`
	Description      string          `json:"description"`
	DefinitionOfDone string          `json:"definition_of_done"`
	Priority         int             `json:"priority"`
	IdempotencyKey   string          `json:"idempotency_key"`
	InputSnapshot    string          `json:"input_snapshot"`
	AllowedTools     string          `json:"allowed_tools"`
	EstimatedCost    decimal.Decimal `json:"estimated_cost"`
	DueAt            *time.Time      `json:"due_at"`
}

func (api *personalCompanyAPI) listObjectives(c *gin.Context) {
	ctx, ok := api.personalCompanyContext(c)
	if !ok {
		return
	}
	company, err := loadPersonalCompany(ctx.userID, ctx.agentGroupID)
	if writePersonalCompanyLoadError(c, err) {
		return
	}
	var objectives []model.CompanyObjective
	if err := model.DB.Where("personal_company_id = ? AND owner_user_id = ?", company.ID, ctx.userID).Order("priority DESC, created_at DESC").Find(&objectives).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load objectives"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"objectives": objectives})
}

func (api *personalCompanyAPI) createObjective(c *gin.Context) {
	ctx, ok := api.personalCompanyContext(c)
	if !ok {
		return
	}
	company, err := loadPersonalCompany(ctx.userID, ctx.agentGroupID)
	if writePersonalCompanyLoadError(c, err) {
		return
	}
	var input personalCompanyObjectiveInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if strings.TrimSpace(input.Title) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Objective title is required"})
		return
	}
	objective := model.CompanyObjective{PersonalCompanyID: company.ID, OwnerUserID: ctx.userID, Title: truncatePersonalCompanyText(input.Title, 200), Description: strings.TrimSpace(input.Description), Status: model.CompanyObjectiveStatusActive, Priority: input.Priority, TargetDate: input.TargetDate}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&objective).Error; err != nil {
			return err
		}
		return createPersonalCompanyAuditEvent(tx, company.ID, nil, "owner", ctx.userID, "objective.created", fmt.Sprintf(`{"objective_id":%d}`, objective.ID))
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create objective"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"objective": objective})
}

func recordPersonalCompanyWorkStartFailure(companyID, workItemID uint, startErr error) error {
	payload, err := json.Marshal(map[string]string{"error": truncatePersonalCompanyText(startErr.Error(), 1000)})
	if err != nil {
		return err
	}
	return model.DB.Transaction(func(tx *gorm.DB) error {
		return createPersonalCompanyAuditEvent(tx, companyID, &workItemID, "system", 0, "work_attempt.start_failed", string(payload))
	})
}

func (api *personalCompanyAPI) listWorkItems(c *gin.Context) {
	ctx, ok := api.personalCompanyContext(c)
	if !ok {
		return
	}
	company, err := loadPersonalCompany(ctx.userID, ctx.agentGroupID)
	if writePersonalCompanyLoadError(c, err) {
		return
	}
	var workItems []model.CompanyWorkItem
	if err := model.DB.Where("personal_company_id = ? AND owner_user_id = ?", company.ID, ctx.userID).Order("priority DESC, created_at DESC").Find(&workItems).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load work items"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"work_items": workItems})
}

func (api *personalCompanyAPI) createWorkItem(c *gin.Context) {
	ctx, ok := api.personalCompanyContext(c)
	if !ok {
		return
	}
	company, err := loadPersonalCompany(ctx.userID, ctx.agentGroupID)
	if writePersonalCompanyLoadError(c, err) {
		return
	}
	if company.State != model.PersonalCompanyStateOperating {
		c.JSON(http.StatusConflict, gin.H{"error": "Personal Company is not operating"})
		return
	}
	var input personalCompanyWorkItemInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if strings.TrimSpace(input.Title) == "" || strings.TrimSpace(input.DefinitionOfDone) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Work item title and definition_of_done are required"})
		return
	}
	if input.EstimatedCost.IsNegative() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Estimated cost cannot be negative"})
		return
	}
	if input.ObjectiveID != nil {
		var objective model.CompanyObjective
		if err := model.DB.Where("id = ? AND personal_company_id = ? AND owner_user_id = ?", *input.ObjectiveID, company.ID, ctx.userID).First(&objective).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Objective is not available to this Personal Company"})
			return
		}
	}
	idempotencyKey := truncatePersonalCompanyText(input.IdempotencyKey, 100)
	if idempotencyKey == "" {
		idempotencyKey = newPersonalCompanyID("work")
	}
	// User-created work always enters the governed Studio queue. Connector
	// operations remain subject to Checker approval during execution.
	status := model.CompanyWorkStatusQueued
	inputSnapshot, validSnapshot := normalizedPersonalCompanyStructuredText(input.InputSnapshot, "{}")
	allowedTools, validTools := normalizedPersonalCompanyStructuredText(input.AllowedTools, "[]")
	if !validSnapshot || !validTools {
		c.JSON(http.StatusBadRequest, gin.H{"error": "input_snapshot and allowed_tools must be valid JSON"})
		return
	}
	workItem := model.CompanyWorkItem{PersonalCompanyID: company.ID, OwnerUserID: ctx.userID, ObjectiveID: input.ObjectiveID, Title: truncatePersonalCompanyText(input.Title, 200), Description: strings.TrimSpace(input.Description), DefinitionOfDone: strings.TrimSpace(input.DefinitionOfDone), Status: status, Priority: input.Priority, RiskLevel: "r0", IdempotencyKey: idempotencyKey, InputSnapshot: inputSnapshot, AllowedTools: allowedTools, EstimatedCost: input.EstimatedCost, ReservedCost: input.EstimatedCost, DueAt: input.DueAt}
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		if err := ensurePersonalCompanyBudgetAvailable(tx, company, input.EstimatedCost); err != nil {
			return err
		}
		if err := tx.Create(&workItem).Error; err != nil {
			return err
		}
		if input.EstimatedCost.GreaterThan(decimal.Zero) {
			if err := tx.Create(&model.CompanyBudgetLedger{PersonalCompanyID: company.ID, WorkItemID: &workItem.ID, EntryType: "reservation", Amount: input.EstimatedCost, ReferenceType: "work_item", ReferenceID: strconv.FormatUint(uint64(workItem.ID), 10), CreatedByUserID: ctx.userID}).Error; err != nil {
				return err
			}
		}
		if err := enqueuePersonalCompanySignal(tx, company, &workItem.ID, "owner", "work_item."+status, fmt.Sprintf(`{"work_item_id":%d}`, workItem.ID)); err != nil {
			return err
		}
		return createPersonalCompanyAuditEvent(tx, company.ID, &workItem.ID, "owner", ctx.userID, "work_item.created", fmt.Sprintf(`{"status":%q}`, status))
	})
	if err != nil {
		switch {
		case errors.Is(err, errPersonalCompanyBudgetExceeded):
			c.JSON(http.StatusPaymentRequired, gin.H{"error": "Work item exceeds the available Personal Company budget"})
		case isPersonalCompanyUniqueError(err):
			c.JSON(http.StatusConflict, gin.H{"error": "A work item with this idempotency key already exists"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create work item"})
		}
		return
	}
	c.JSON(http.StatusCreated, gin.H{"work_item": workItem})
}

func (api *personalCompanyAPI) getWorkItemTimeline(c *gin.Context) {
	ctx, ok := api.personalCompanyContext(c)
	if !ok {
		return
	}
	company, err := loadPersonalCompany(ctx.userID, ctx.agentGroupID)
	if writePersonalCompanyLoadError(c, err) {
		return
	}
	workItem, err := loadPersonalCompanyWorkItem(company.ID, ctx.userID, c.Param("id"))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Work item not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load work item"})
		return
	}
	var attempts []model.CompanyWorkAttempt
	var artifacts []model.CompanyArtifact
	var handoffs []model.CompanyHandoffPackage
	var events []model.CompanyAuditEvent
	if err := model.DB.Where("work_item_id = ?", workItem.ID).Order("attempt_number ASC").Find(&attempts).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load work timeline"})
		return
	}
	if err := model.DB.Where("work_item_id = ?", workItem.ID).Order("created_at ASC").Find(&artifacts).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load work timeline"})
		return
	}
	if err := model.DB.Where("work_item_id = ?", workItem.ID).Order("created_at ASC").Find(&handoffs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load work timeline"})
		return
	}
	if err := model.DB.Where("personal_company_id = ? AND work_item_id = ?", company.ID, workItem.ID).Order("created_at ASC").Find(&events).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load work timeline"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"work_item": workItem, "attempts": attempts, "artifacts": artifacts, "handoffs": handoffs, "events": events})
}

func (api *personalCompanyAPI) cancelWorkItem(c *gin.Context) {
	ctx, ok := api.personalCompanyContext(c)
	if !ok {
		return
	}
	company, err := loadPersonalCompany(ctx.userID, ctx.agentGroupID)
	if writePersonalCompanyLoadError(c, err) {
		return
	}
	workItem, err := loadPersonalCompanyWorkItem(company.ID, ctx.userID, c.Param("id"))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Work item not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load work item"})
		return
	}
	if workItem.Status == model.CompanyWorkStatusDelivered || workItem.Status == model.CompanyWorkStatusCancelled {
		c.JSON(http.StatusConflict, gin.H{"error": "Work item cannot be cancelled in its current state"})
		return
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.CompanyWorkItem{}).Where("id = ? AND personal_company_id = ?", workItem.ID, company.ID).Updates(map[string]interface{}{"status": model.CompanyWorkStatusCancelled, "reserved_cost": decimal.Zero}).Error; err != nil {
			return err
		}
		if workItem.ReservedCost.GreaterThan(decimal.Zero) {
			if err := tx.Create(&model.CompanyBudgetLedger{PersonalCompanyID: company.ID, WorkItemID: &workItem.ID, EntryType: "release", Amount: workItem.ReservedCost.Neg(), ReferenceType: "work_item", ReferenceID: strconv.FormatUint(uint64(workItem.ID), 10), CreatedByUserID: ctx.userID}).Error; err != nil {
				return err
			}
		}
		return createPersonalCompanyAuditEvent(tx, company.ID, &workItem.ID, "owner", ctx.userID, "work_item.cancelled", `{}`)
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to cancel work item"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": model.CompanyWorkStatusCancelled})
}

func (api *personalCompanyAPI) queueWorkItem(c *gin.Context) {
	ctx, ok := api.personalCompanyContext(c)
	if !ok {
		return
	}
	company, err := loadPersonalCompany(ctx.userID, ctx.agentGroupID)
	if writePersonalCompanyLoadError(c, err) {
		return
	}
	workItem, err := loadPersonalCompanyWorkItem(company.ID, ctx.userID, c.Param("id"))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Work item not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load work item"})
		return
	}
	if err := QueuePersonalCompanyWorkItem(model.DB, company, workItem.ID, ctx.userID); err != nil {
		if errors.Is(err, ErrPersonalCompanyWorkNotQueueable) {
			c.JSON(http.StatusConflict, gin.H{"error": "Work item is not authorized for queueing"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to queue work item"})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": model.CompanyWorkStatusQueued})
}

func loadPersonalCompanyWorkItem(companyID, userID uint, rawID string) (model.CompanyWorkItem, error) {
	id, err := strconv.ParseUint(strings.TrimSpace(rawID), 10, 64)
	if err != nil {
		return model.CompanyWorkItem{}, gorm.ErrRecordNotFound
	}
	var workItem model.CompanyWorkItem
	err = model.DB.Where("id = ? AND personal_company_id = ? AND owner_user_id = ?", uint(id), companyID, userID).First(&workItem).Error
	return workItem, err
}

func ensurePersonalCompanyBudgetAvailable(tx *gorm.DB, company model.PersonalCompany, requested decimal.Decimal) error {
	if requested.LessThanOrEqual(decimal.Zero) {
		return nil
	}
	if company.DailyBudget.GreaterThan(decimal.Zero) && requested.GreaterThan(company.DailyBudget) {
		return errPersonalCompanyBudgetExceeded
	}
	if company.MonthlyBudget.GreaterThan(decimal.Zero) && requested.GreaterThan(company.MonthlyBudget) {
		return errPersonalCompanyBudgetExceeded
	}
	startOfMonth := time.Now().UTC().AddDate(0, 0, -time.Now().UTC().Day()+1).Truncate(24 * time.Hour)
	var reserved decimal.Decimal
	if err := tx.Model(&model.CompanyBudgetLedger{}).Select("COALESCE(SUM(amount), 0)").Where("personal_company_id = ? AND entry_type IN ? AND created_at >= ?", company.ID, []string{"reservation", "release"}, startOfMonth).Scan(&reserved).Error; err != nil {
		return err
	}
	if company.MonthlyBudget.GreaterThan(decimal.Zero) && reserved.Add(requested).GreaterThan(company.MonthlyBudget) {
		return errPersonalCompanyBudgetExceeded
	}
	startOfDay := time.Now().UTC().Truncate(24 * time.Hour)
	var dailyReserved decimal.Decimal
	if err := tx.Model(&model.CompanyBudgetLedger{}).Select("COALESCE(SUM(amount), 0)").Where("personal_company_id = ? AND entry_type IN ? AND created_at >= ?", company.ID, []string{"reservation", "release"}, startOfDay).Scan(&dailyReserved).Error; err != nil {
		return err
	}
	if company.DailyBudget.GreaterThan(decimal.Zero) && dailyReserved.Add(requested).GreaterThan(company.DailyBudget) {
		return errPersonalCompanyBudgetExceeded
	}
	return nil
}

func normalizePersonalCompanyRiskLevel(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "r1", "r2", "r3", "r4":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "r0"
	}
}

func newPersonalCompanyID(prefix string) string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err == nil {
		return prefix + "_" + hex.EncodeToString(bytes[:])
	}
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

func personalCompanyParametersHash(workItem model.CompanyWorkItem) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{workItem.IdempotencyKey, workItem.RiskLevel, workItem.Title, workItem.AllowedTools, workItem.InputSnapshot}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func isPersonalCompanyUniqueError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
}

var errPersonalCompanyBudgetExceeded = errors.New("personal company budget exceeded")

func normalizedPersonalCompanyStructuredText(value, fallback string) (string, bool) {
	if strings.TrimSpace(value) == "" {
		return fallback, true
	}
	var target interface{}
	if err := json.Unmarshal([]byte(value), &target); err != nil {
		return "", false
	}
	return value, true
}
