package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	personalCompanyChiefCreateWorkToolName = "studio_create_work"
	personalCompanyChiefListWorkToolName   = "studio_list_work"
	personalCompanyReviewWorkToolName      = "studio_review_work"
)

func init() {
	RegisterAdvancedChatRuntimeExtensionHook(personalCompanyChiefRuntimeExtension)
	RegisterAdvancedChatToolHandler(personalCompanyChiefCreateWorkToolName, handlePersonalCompanyChiefCreateWork)
	RegisterAdvancedChatToolHandler(personalCompanyChiefListWorkToolName, handlePersonalCompanyChiefListWork)
	RegisterAdvancedChatToolHandler(personalCompanyReviewWorkToolName, handlePersonalCompanyReviewWork)
}

// personalCompanyChiefRuntimeExtension gives the Chief a bounded way to turn a
// user's request into a durable Studio work item. It is never exposed outside
// an enabled Studio operation.
func personalCompanyChiefRuntimeExtension(_ context.Context, input AdvancedChatRuntimeContext) (AdvancedChatRuntimeExtension, error) {
	if input.Mode != advancedChatModeAgentGroup || strings.TrimSpace(input.AgentGroupID) == "" {
		return AdvancedChatRuntimeExtension{}, nil
	}
	if personalCompanyReviewRunIsInternal(input.RunID) {
		return AdvancedChatRuntimeExtension{
			SystemPrompt: "This is an immutable internal Studio review session. You are the Chief scheduler. Delegate the completed work to a reviewer member for inspection. After a reviewer has returned, use studio_review_work exactly once to record verified, needs_revision, or blocked. You may not create work items, inspect the work queue, or execute workspace actions in this session.",
			Tools: []ChatExecutorTool{{
				Name:        personalCompanyReviewWorkToolName,
				Description: "Record the reviewer-backed decision for the current immutable Studio work review.",
				Schema: map[string]interface{}{
					"type":     "object",
					"required": []string{"decision", "summary"},
					"properties": map[string]interface{}{
						"decision": map[string]interface{}{"type": "string", "enum": []string{"verified", "needs_revision", "blocked"}},
						"summary":  map[string]interface{}{"type": "string", "description": "Reviewer findings and evidence."},
					},
				},
			}},
		}, nil
	}
	if personalCompanyChiefRunIsInternal(input.RunID) {
		return AdvancedChatRuntimeExtension{}, nil
	}
	company, err := ensurePersonalCompanyOperating(input.UserID, input.AgentGroupID)
	if company.State != model.PersonalCompanyStateOperating {
		return AdvancedChatRuntimeExtension{}, nil
	}
	if err != nil {
		return AdvancedChatRuntimeExtension{}, err
	}
	return AdvancedChatRuntimeExtension{
		SystemPrompt: "This Studio has operations enabled. You are the external Chief and receive every user message. Your authority here is task intake and scheduling only: do not delegate work, inspect or operate a workspace, or perform implementation. A goal is a long-lived outcome or direction; a work item is one concrete, bounded, verifiable delivery. When the user asks the Studio to do concrete work, clarify the deliverable and use studio_create_work after you have enough detail. For questions about progress, active work, or pending approvals, call studio_list_work before answering. Do not create work for mere discussion, status questions, or ambiguous requests.",
		Tools: []ChatExecutorTool{
			{
				Name:        personalCompanyChiefCreateWorkToolName,
				Description: "Create and start a concrete Studio work item. Use after you have enough detail to define its outcome and acceptance criteria.",
				Schema: map[string]interface{}{
					"type":     "object",
					"required": []string{"title", "definition_of_done"},
					"properties": map[string]interface{}{
						"title":              map[string]interface{}{"type": "string", "description": "Short concrete delivery title."},
						"description":        map[string]interface{}{"type": "string", "description": "Context, constraints, and requested outcome."},
						"definition_of_done": map[string]interface{}{"type": "string", "description": "Verifiable acceptance criteria and evidence."},
						"priority":           map[string]interface{}{"type": "integer", "description": "Optional priority; higher is more urgent."},
					},
				},
			},
			{
				Name:        personalCompanyChiefListWorkToolName,
				Description: "Read the Studio's recent work status and pending owner approvals before reporting operational progress.",
				Schema:      map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
			},
		},
	}, nil
}

func handlePersonalCompanyChiefCreateWork(_ context.Context, input AdvancedChatToolCallInput) (string, error) {
	if personalCompanyChiefRunIsInternal(input.RunID) {
		return "", errors.New("internal Studio work sessions cannot create additional work items")
	}
	if input.Mode != advancedChatModeAgentGroup || strings.TrimSpace(input.AgentGroupID) == "" {
		return "", errors.New("Studio Chief context is required")
	}
	company, err := loadPersonalCompany(input.UserID, input.AgentGroupID)
	if err != nil {
		return "", err
	}
	if company.State != model.PersonalCompanyStateOperating {
		return "", errors.New("Studio operations are paused")
	}
	title := truncatePersonalCompanyText(stringFromMap(input.Arguments, "title"), 200)
	description := strings.TrimSpace(stringFromMap(input.Arguments, "description"))
	definition := strings.TrimSpace(stringFromMap(input.Arguments, "definition_of_done"))
	if title == "" || definition == "" {
		return "", errors.New("title and definition_of_done are required")
	}
	priority := intFromStatusPayload(gin.H(input.Arguments), "priority")
	workItem := model.CompanyWorkItem{
		PersonalCompanyID: company.ID,
		OwnerUserID:       input.UserID,
		Title:             title,
		Description:       description,
		DefinitionOfDone:  truncatePersonalCompanyText(definition, 4000),
		Status:            model.CompanyWorkStatusQueued,
		Priority:          priority,
		RiskLevel:         "r0",
		IdempotencyKey:    newPersonalCompanyID("chief-work"),
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&workItem).Error; err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]uint{"work_item_id": workItem.ID})
		if err := enqueuePersonalCompanySignal(tx, company, &workItem.ID, "chief_chat", "work_item.queued", string(payload)); err != nil {
			return err
		}
		return createPersonalCompanyAuditEvent(tx, company.ID, &workItem.ID, "chief", 0, "work_item.created_from_chat", string(payload))
	}); err != nil {
		return "", err
	}
	return fmt.Sprintf("Created Studio work item #%d. The Chief scheduler will start an immutable internal session.", workItem.ID), nil
}

// handlePersonalCompanyChiefListWork returns a bounded operational snapshot for
// the external Chief chat. Internal work conversations stay focused on delivery.
func handlePersonalCompanyChiefListWork(_ context.Context, input AdvancedChatToolCallInput) (string, error) {
	if personalCompanyChiefRunIsInternal(input.RunID) {
		return "", errors.New("internal Studio work sessions cannot inspect the Studio work queue")
	}
	if input.Mode != advancedChatModeAgentGroup || strings.TrimSpace(input.AgentGroupID) == "" {
		return "", errors.New("Studio Chief context is required")
	}
	company, err := loadPersonalCompany(input.UserID, input.AgentGroupID)
	if err != nil {
		return "", err
	}
	type workSummary struct {
		ID       uint   `json:"id"`
		Title    string `json:"title"`
		Status   string `json:"status"`
		Priority int    `json:"priority"`
	}
	workItems := []workSummary{}
	if err := model.DB.Model(&model.CompanyWorkItem{}).
		Select("id", "title", "status", "priority").
		Where("personal_company_id = ?", company.ID).
		Order("updated_at DESC").
		Limit(12).
		Find(&workItems).Error; err != nil {
		return "", err
	}
	var workApprovals, connectorApprovals int64
	if err := model.DB.Model(&model.CompanyApprovalRequest{}).
		Where("personal_company_id = ? AND status = ?", company.ID, model.CompanyApprovalPending).
		Count(&workApprovals).Error; err != nil {
		return "", err
	}
	if err := model.DB.Table("advanced_chat_connector_tasks AS tasks").
		Joins("JOIN company_work_attempts AS attempts ON attempts.advanced_chat_run_id = tasks.run_id").
		Joins("JOIN company_work_items AS work ON work.id = attempts.work_item_id").
		Where("work.personal_company_id = ? AND tasks.status = ?", company.ID, advancedChatConnectorTaskStatusPendingApproval).
		Count(&connectorApprovals).Error; err != nil {
		return "", err
	}
	result, err := json.Marshal(gin.H{
		"studio":     gin.H{"name": company.Name, "state": company.State},
		"work_items": workItems,
		"pending_approvals": gin.H{
			"work":      workApprovals,
			"connector": connectorApprovals,
		},
	})
	if err != nil {
		return "", err
	}
	return string(result), nil
}

func personalCompanyChiefRunIsInternal(runID string) bool {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return false
	}
	var count int64
	return model.DB.Model(&model.CompanyWorkAttempt{}).Where("advanced_chat_run_id = ?", runID).Count(&count).Error == nil && count > 0
}

func personalCompanyReviewRunIsInternal(runID string) bool {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return false
	}
	var count int64
	return model.DB.Model(&model.CompanyWorkAttempt{}).Where("advanced_chat_run_id = ? AND kind = ?", runID, model.CompanyWorkAttemptKindReview).Count(&count).Error == nil && count > 0
}

func personalCompanyExternalChiefSchedulingRun(userID uint, groupID, runID string) bool {
	if userID == 0 || strings.TrimSpace(groupID) == "" || personalCompanyChiefRunIsInternal(runID) {
		return false
	}
	company, err := loadPersonalCompany(userID, groupID)
	return err == nil && company.State == model.PersonalCompanyStateOperating
}

func personalCompanyInternalSession(userID uint, sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	if userID == 0 || sessionID == "" {
		return false
	}
	var count int64
	return model.DB.Table("company_work_attempts AS attempts").
		Joins("JOIN advanced_chat_runs AS runs ON runs.id = attempts.advanced_chat_run_id").
		Where("runs.session_id = ? AND runs.user_id = ?", sessionID, userID).
		Count(&count).Error == nil && count > 0
}

func handlePersonalCompanyReviewWork(_ context.Context, input AdvancedChatToolCallInput) (string, error) {
	if !personalCompanyReviewRunIsInternal(input.RunID) {
		return "", errors.New("internal Studio review context is required")
	}
	decision := strings.TrimSpace(stringFromMap(input.Arguments, "decision"))
	summary := truncatePersonalCompanyText(stringFromMap(input.Arguments, "summary"), 4000)
	if summary == "" {
		return "", errors.New("review summary is required")
	}
	if decision != "verified" && decision != "needs_revision" && decision != "blocked" {
		return "", errors.New("review decision is invalid")
	}
	var attempt model.CompanyWorkAttempt
	if err := model.DB.Where("advanced_chat_run_id = ? AND kind = ?", input.RunID, model.CompanyWorkAttemptKindReview).First(&attempt).Error; err != nil {
		return "", err
	}
	var reviewerDelegationCount int64
	if err := model.DB.Model(&AdvancedChatRunEvent{}).Where("run_id = ? AND event = ? AND payload LIKE ?", input.RunID, "agent_task", "%\"agent_type\":\"reviewer\"%").Count(&reviewerDelegationCount).Error; err != nil {
		return "", err
	}
	if reviewerDelegationCount == 0 {
		return "", errors.New("Chief must receive a reviewer delegation result before recording review")
	}
	return summary, model.DB.Transaction(func(tx *gorm.DB) error {
		var work model.CompanyWorkItem
		if err := tx.Where("id = ?", attempt.WorkItemID).First(&work).Error; err != nil {
			return err
		}
		company := model.PersonalCompany{}
		if err := tx.Where("id = ?", work.PersonalCompanyID).First(&company).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		attemptStatus, workStatus := model.CompanyWorkStatusVerified, model.CompanyWorkStatusVerified
		if decision == "needs_revision" {
			attemptStatus, workStatus = model.CompanyWorkStatusRetryableFailure, model.CompanyWorkStatusQueued
		} else if decision == "blocked" {
			attemptStatus, workStatus = model.CompanyWorkStatusBlocked, model.CompanyWorkStatusBlocked
		}
		if err := tx.Model(&model.CompanyWorkAttempt{}).Where("id = ? AND status = ?", attempt.ID, model.CompanyWorkStatusExecuting).Updates(map[string]interface{}{"status": attemptStatus, "finished_at": now, "result_summary": summary}).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.CompanyWorkItem{}).Where("id = ? AND status = ?", work.ID, model.CompanyWorkStatusExecuting).Update("status", workStatus).Error; err != nil {
			return err
		}
		if decision == "verified" {
			if err := tx.Model(&model.CompanyArtifact{}).Where("work_item_id = ?", work.ID).Update("acceptance_state", "verified").Error; err != nil {
				return err
			}
		}
		payload := fmt.Sprintf(`{"attempt_id":%d,"decision":%q}`, attempt.ID, decision)
		if err := enqueuePersonalCompanySignal(tx, company, &work.ID, "review", "work_item."+workStatus, payload); err != nil {
			return err
		}
		return createPersonalCompanyAuditEvent(tx, company.ID, &work.ID, "reviewer", 0, "review_attempt."+decision, payload)
	})
}
