package service

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type personalCompanyApprovalDecisionInput struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

func (api *personalCompanyAPI) listApprovals(c *gin.Context) {
	ctx, ok := api.personalCompanyContext(c)
	if !ok {
		return
	}
	company, err := loadPersonalCompany(ctx.userID, ctx.agentGroupID)
	if writePersonalCompanyLoadError(c, err) {
		return
	}
	var approvals []model.CompanyApprovalRequest
	if err := model.DB.Where("personal_company_id = ?", company.ID).Order("created_at DESC").Find(&approvals).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load approvals"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"approvals": approvals})
}

func (api *personalCompanyAPI) approveWorkItem(c *gin.Context) {
	var input personalCompanyApprovalDecisionInput
	if err := c.ShouldBindJSON(&input); err != nil && !errors.Is(err, io.EOF) {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	input.Decision = "approved"
	api.decideApprovalForWorkItem(c, input)
}

func (api *personalCompanyAPI) decideApproval(c *gin.Context) {
	var input personalCompanyApprovalDecisionInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	api.decideApprovalRequest(c, c.Param("id"), input)
}

func (api *personalCompanyAPI) decideApprovalForWorkItem(c *gin.Context, input personalCompanyApprovalDecisionInput) {
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
	var approval model.CompanyApprovalRequest
	if err := model.DB.Where("personal_company_id = ? AND work_item_id = ?", company.ID, workItem.ID).First(&approval).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusConflict, gin.H{"error": "Work item does not have a pending approval"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load approval"})
		return
	}
	api.decideApprovalModel(c, ctx.userID, company, approval, input)
}

func (api *personalCompanyAPI) decideApprovalRequest(c *gin.Context, rawID string, input personalCompanyApprovalDecisionInput) {
	ctx, ok := api.personalCompanyContext(c)
	if !ok {
		return
	}
	company, err := loadPersonalCompany(ctx.userID, ctx.agentGroupID)
	if writePersonalCompanyLoadError(c, err) {
		return
	}
	id, err := strconv.ParseUint(strings.TrimSpace(rawID), 10, 64)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Approval not found"})
		return
	}
	var approval model.CompanyApprovalRequest
	if err := model.DB.Where("id = ? AND personal_company_id = ?", uint(id), company.ID).First(&approval).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Approval not found"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load approval"})
		return
	}
	api.decideApprovalModel(c, ctx.userID, company, approval, input)
}

func (api *personalCompanyAPI) decideApprovalModel(c *gin.Context, userID uint, company model.PersonalCompany, approval model.CompanyApprovalRequest, input personalCompanyApprovalDecisionInput) {
	decision := strings.ToLower(strings.TrimSpace(input.Decision))
	if decision != model.CompanyApprovalApproved && decision != model.CompanyApprovalRejected {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Decision must be approved or rejected"})
		return
	}
	if approval.Status != model.CompanyApprovalPending {
		c.JSON(http.StatusConflict, gin.H{"error": "Approval has already been decided"})
		return
	}
	if approval.ExpiresAt != nil && time.Now().After(*approval.ExpiresAt) {
		_ = model.DB.Model(&approval).Update("status", model.CompanyApprovalExpired)
		c.JSON(http.StatusConflict, gin.H{"error": "Approval has expired"})
		return
	}
	now := time.Now()
	workStatus := model.CompanyWorkStatusAuthorized
	if decision == model.CompanyApprovalRejected {
		workStatus = model.CompanyWorkStatusCancelled
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.CompanyApprovalRequest{}).Where("id = ? AND status = ?", approval.ID, model.CompanyApprovalPending).Updates(map[string]interface{}{"status": decision, "decision_reason": strings.TrimSpace(input.Reason), "decided_by_user_id": userID, "decided_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.CompanyWorkItem{}).Where("id = ? AND personal_company_id = ?", approval.WorkItemID, company.ID).Update("status", workStatus).Error; err != nil {
			return err
		}
		if decision == model.CompanyApprovalRejected {
			var workItem model.CompanyWorkItem
			if err := tx.Where("id = ? AND personal_company_id = ?", approval.WorkItemID, company.ID).First(&workItem).Error; err != nil {
				return err
			}
			if workItem.ReservedCost.GreaterThan(decimal.Zero) {
				if err := tx.Model(&model.CompanyWorkItem{}).Where("id = ?", workItem.ID).Update("reserved_cost", decimal.Zero).Error; err != nil {
					return err
				}
				if err := tx.Create(&model.CompanyBudgetLedger{PersonalCompanyID: company.ID, WorkItemID: &workItem.ID, EntryType: "release", Amount: workItem.ReservedCost.Neg(), ReferenceType: "approval_rejection", ReferenceID: strconv.FormatUint(uint64(approval.ID), 10), CreatedByUserID: userID}).Error; err != nil {
					return err
				}
			}
		}
		return createPersonalCompanyAuditEvent(tx, company.ID, &approval.WorkItemID, "owner", userID, "approval."+decision, fmt.Sprintf(`{"approval_id":%d}`, approval.ID))
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to decide approval"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": decision, "work_item_status": workStatus})
}

func (api *personalCompanyAPI) getOrgChart(c *gin.Context) {
	ctx, ok := api.personalCompanyContext(c)
	if !ok {
		return
	}
	company, err := loadPersonalCompany(ctx.userID, ctx.agentGroupID)
	if writePersonalCompanyLoadError(c, err) {
		return
	}
	var employees []model.PersonalCompanyEmployee
	var templates []model.CompanyRoleTemplate
	var recruitmentPlans []model.CompanyRecruitmentPlan
	if err := model.DB.Where("personal_company_id = ?", company.ID).Order("created_at ASC").Find(&employees).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load organization"})
		return
	}
	if err := model.DB.Where("personal_company_id = ?", company.ID).Order("name ASC").Find(&templates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load role templates"})
		return
	}
	if err := model.DB.Where("personal_company_id = ?", company.ID).Order("created_at DESC").Find(&recruitmentPlans).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load recruitment plans"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"company_id": company.ID, "employees": employees, "role_templates": templates, "recruitment_plans": recruitmentPlans})
}

func createPersonalCompanyAuditEvent(tx *gorm.DB, companyID uint, workItemID *uint, actorType string, actorID uint, eventType, payload string) error {
	return tx.Create(&model.CompanyAuditEvent{PersonalCompanyID: companyID, WorkItemID: workItemID, ActorType: actorType, ActorID: strconv.FormatUint(uint64(actorID), 10), EventType: eventType, Payload: payload}).Error
}
