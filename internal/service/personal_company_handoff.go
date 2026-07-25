package service

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var (
	ErrPersonalCompanyHandoffInvalid  = errors.New("personal company handoff is invalid")
	ErrPersonalCompanyHandoffDecided  = errors.New("personal company handoff has already been decided")
	ErrPersonalCompanyHandoffNotFound = errors.New("personal company handoff was not found")
	ErrPersonalCompanyHandoffEmployee = errors.New("personal company handoff employee is unavailable")
)

type personalCompanyHandoffInput struct {
	WorkAttemptID     *uint  `json:"work_attempt_id"`
	FromEmployeeID    *uint  `json:"from_employee_id"`
	ToEmployeeID      uint   `json:"to_employee_id"`
	CompletionSummary string `json:"completion_summary"`
	Evidence          string `json:"evidence"`
	Risks             string `json:"risks"`
	NextSteps         string `json:"next_steps"`
}

type personalCompanyHandoffDecisionInput struct {
	Decision string `json:"decision"`
	Note     string `json:"note"`
}

// CreatePersonalCompanyHandoff records a structured transfer without treating
// it as task acceptance or delivery. A future employee runtime can invoke the
// same domain operation using its own service identity.
func CreatePersonalCompanyHandoff(db *gorm.DB, company model.PersonalCompany, ownerUserID, workItemID uint, input personalCompanyHandoffInput) (model.CompanyHandoffPackage, error) {
	if db == nil || company.ID == 0 || ownerUserID == 0 || workItemID == 0 || input.ToEmployeeID == 0 || strings.TrimSpace(input.CompletionSummary) == "" || strings.TrimSpace(input.NextSteps) == "" {
		return model.CompanyHandoffPackage{}, ErrPersonalCompanyHandoffInvalid
	}
	evidence, validEvidence := normalizedPersonalCompanyStructuredText(input.Evidence, "[]")
	risks, validRisks := normalizedPersonalCompanyStructuredText(input.Risks, "[]")
	if !validEvidence || !validRisks || input.FromEmployeeID != nil && *input.FromEmployeeID == input.ToEmployeeID {
		return model.CompanyHandoffPackage{}, ErrPersonalCompanyHandoffInvalid
	}
	var handoff model.CompanyHandoffPackage
	err := db.Transaction(func(tx *gorm.DB) error {
		var workItem model.CompanyWorkItem
		if err := tx.Where("id = ? AND personal_company_id = ? AND owner_user_id = ?", workItemID, company.ID, ownerUserID).First(&workItem).Error; err != nil {
			return err
		}
		if err := validatePersonalCompanyHandoffEmployee(tx, company.ID, input.ToEmployeeID); err != nil {
			return err
		}
		if input.FromEmployeeID != nil {
			if err := validatePersonalCompanyHandoffEmployee(tx, company.ID, *input.FromEmployeeID); err != nil {
				return err
			}
		}
		if input.WorkAttemptID != nil {
			var attempt model.CompanyWorkAttempt
			if err := tx.Where("id = ? AND work_item_id = ?", *input.WorkAttemptID, workItem.ID).First(&attempt).Error; err != nil {
				return ErrPersonalCompanyHandoffInvalid
			}
		}
		handoff = model.CompanyHandoffPackage{
			WorkItemID:        workItem.ID,
			WorkAttemptID:     input.WorkAttemptID,
			FromEmployeeID:    input.FromEmployeeID,
			ToEmployeeID:      &input.ToEmployeeID,
			Status:            model.CompanyHandoffStatusPending,
			CompletionSummary: strings.TrimSpace(input.CompletionSummary),
			Evidence:          evidence,
			Risks:             risks,
			NextSteps:         strings.TrimSpace(input.NextSteps),
		}
		if err := tx.Create(&handoff).Error; err != nil {
			return err
		}
		payload := fmt.Sprintf(`{"handoff_id":%d,"to_employee_id":%d}`, handoff.ID, input.ToEmployeeID)
		outbox := model.CompanyOutboxEvent{PersonalCompanyID: company.ID, EventKey: fmt.Sprintf("handoff:%d:created", handoff.ID), EventType: "handoff.created", Payload: payload, Status: model.CompanyOutboxStatusPending}
		if err := tx.Create(&outbox).Error; err != nil {
			return err
		}
		return createPersonalCompanyAuditEvent(tx, company.ID, &workItem.ID, "owner", ownerUserID, "handoff.created", payload)
	})
	return handoff, err
}

// DecidePersonalCompanyHandoff records the receiver's explicit disposition.
// It deliberately does not advance the WorkItem state; verification and
// delivery remain separate control-plane decisions.
func DecidePersonalCompanyHandoff(db *gorm.DB, companyID, ownerUserID, handoffID uint, decision, note string) (model.CompanyHandoffPackage, error) {
	if db == nil || companyID == 0 || ownerUserID == 0 || handoffID == 0 {
		return model.CompanyHandoffPackage{}, ErrPersonalCompanyHandoffInvalid
	}
	decision = model.NormalizeCompanyHandoffStatus(decision)
	if decision == model.CompanyHandoffStatusPending {
		return model.CompanyHandoffPackage{}, ErrPersonalCompanyHandoffInvalid
	}
	var handoff model.CompanyHandoffPackage
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Joins("JOIN company_work_items ON company_work_items.id = company_handoff_packages.work_item_id").Where("company_handoff_packages.id = ? AND company_work_items.personal_company_id = ? AND company_work_items.owner_user_id = ?", handoffID, companyID, ownerUserID).First(&handoff).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPersonalCompanyHandoffNotFound
			}
			return err
		}
		if handoff.Status != model.CompanyHandoffStatusPending {
			return ErrPersonalCompanyHandoffDecided
		}
		now := time.Now().UTC()
		updates := map[string]interface{}{"status": decision}
		if decision == model.CompanyHandoffStatusAccepted {
			updates["accepted_at"] = now
		}
		result := tx.Model(&model.CompanyHandoffPackage{}).Where("id = ? AND status = ?", handoff.ID, model.CompanyHandoffStatusPending).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrPersonalCompanyHandoffDecided
		}
		handoff.Status = decision
		if decision == model.CompanyHandoffStatusAccepted {
			handoff.AcceptedAt = &now
		}
		payload := fmt.Sprintf(`{"handoff_id":%d,"decision":%q,"note":%q}`, handoff.ID, decision, truncatePersonalCompanyText(note, 1000))
		outbox := model.CompanyOutboxEvent{PersonalCompanyID: companyID, EventKey: fmt.Sprintf("handoff:%d:%s", handoff.ID, decision), EventType: "handoff." + decision, Payload: payload, Status: model.CompanyOutboxStatusPending}
		if err := tx.Create(&outbox).Error; err != nil {
			return err
		}
		return createPersonalCompanyAuditEvent(tx, companyID, &handoff.WorkItemID, "owner", ownerUserID, "handoff."+decision, payload)
	})
	return handoff, err
}

func (api *personalCompanyAPI) createWorkItemHandoff(c *gin.Context) {
	ctx, ok := api.personalCompanyContext(c)
	if !ok {
		return
	}
	company, err := loadPersonalCompany(ctx.userID, ctx.agentGroupID)
	if writePersonalCompanyLoadError(c, err) {
		return
	}
	workItemID, err := personalCompanyUintID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Work item not found"})
		return
	}
	var input personalCompanyHandoffInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	handoff, err := CreatePersonalCompanyHandoff(model.DB, company, ctx.userID, workItemID, input)
	if err != nil {
		writePersonalCompanyHandoffError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"handoff": handoff})
}

func (api *personalCompanyAPI) decideHandoff(c *gin.Context) {
	ctx, ok := api.personalCompanyContext(c)
	if !ok {
		return
	}
	company, err := loadPersonalCompany(ctx.userID, ctx.agentGroupID)
	if writePersonalCompanyLoadError(c, err) {
		return
	}
	handoffID, err := personalCompanyUintID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Handoff not found"})
		return
	}
	var input personalCompanyHandoffDecisionInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	handoff, err := DecidePersonalCompanyHandoff(model.DB, company.ID, ctx.userID, handoffID, input.Decision, input.Note)
	if err != nil {
		writePersonalCompanyHandoffError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"handoff": handoff})
}

func validatePersonalCompanyHandoffEmployee(db *gorm.DB, companyID, employeeID uint) error {
	var employee model.PersonalCompanyEmployee
	if err := db.Where("id = ? AND personal_company_id = ? AND status IN ?", employeeID, companyID, []string{model.PersonalCompanyEmployeeProbation, model.PersonalCompanyEmployeeActive}).First(&employee).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrPersonalCompanyHandoffEmployee
		}
		return err
	}
	return nil
}

func writePersonalCompanyHandoffError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound), errors.Is(err, ErrPersonalCompanyHandoffNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "Work item or handoff not found"})
	case errors.Is(err, ErrPersonalCompanyHandoffInvalid), errors.Is(err, ErrPersonalCompanyHandoffEmployee):
		c.JSON(http.StatusBadRequest, gin.H{"error": "Handoff contains an unavailable employee, attempt, or invalid structured evidence"})
	case errors.Is(err, ErrPersonalCompanyHandoffDecided):
		c.JSON(http.StatusConflict, gin.H{"error": "Handoff has already been decided"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to process handoff"})
	}
}
