package service

import (
	"encoding/json"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/shopspring/decimal"
)

type personalCompanyDashboardResponse struct {
	Company            model.PersonalCompany              `json:"company"`
	Charter            *model.CompanyCharterRevision      `json:"charter,omitempty"`
	Objectives         []model.CompanyObjective           `json:"objectives"`
	WorkItems          []model.CompanyWorkItem            `json:"work_items"`
	Approvals          []model.CompanyApprovalRequest     `json:"approvals"`
	Budget             personalCompanyBudgetSummary       `json:"budget"`
	Health             personalCompanyHealthSummary       `json:"health"`
	Balance            personalCompanyBalanceSummary      `json:"balance_guard"`
	ConnectorApprovals []personalCompanyConnectorApproval `json:"connector_approvals"`
}

type personalCompanyConnectorApproval struct {
	ID            string                 `json:"id"`
	WorkItemID    uint                   `json:"work_item_id"`
	WorkItemTitle string                 `json:"work_item_title"`
	RunID         string                 `json:"run_id"`
	Action        string                 `json:"action"`
	WorkspacePath string                 `json:"workspace_path"`
	Payload       map[string]interface{} `json:"payload"`
	CreatedAt     time.Time              `json:"created_at"`
}

type personalCompanyBalanceSummary struct {
	Current decimal.Decimal `json:"current"`
	Floor   decimal.Decimal `json:"floor"`
}

type personalCompanyBudgetSummary struct {
	DailyLimit      decimal.Decimal `json:"daily_limit"`
	MonthlyLimit    decimal.Decimal `json:"monthly_limit"`
	Reserved        decimal.Decimal `json:"reserved"`
	Consumed        decimal.Decimal `json:"consumed"`
	MonthlyReserved decimal.Decimal `json:"monthly_reserved"`
}

type personalCompanyHealthSummary struct {
	ActiveObjectives int `json:"active_objectives"`
	ActiveWorkItems  int `json:"active_work_items"`
	BlockedWorkItems int `json:"blocked_work_items"`
	PendingApprovals int `json:"pending_approvals"`
}

func personalCompanyDashboard(company model.PersonalCompany) (personalCompanyDashboardResponse, error) {
	response := personalCompanyDashboardResponse{
		Company: company, Objectives: []model.CompanyObjective{}, WorkItems: []model.CompanyWorkItem{}, Approvals: []model.CompanyApprovalRequest{}, ConnectorApprovals: []personalCompanyConnectorApproval{},
		Budget: personalCompanyBudgetSummary{DailyLimit: company.DailyBudget, MonthlyLimit: company.MonthlyBudget},
	}
	var owner model.User
	if err := model.DB.Select("balance").Where("id = ?", company.OwnerUserID).First(&owner).Error; err != nil {
		return response, err
	}
	response.Balance = personalCompanyBalanceSummary{Current: owner.Balance, Floor: company.BalanceFloor}
	if company.CharterRevisionID != nil {
		var charter model.CompanyCharterRevision
		if err := model.DB.Where("id = ? AND personal_company_id = ?", *company.CharterRevisionID, company.ID).First(&charter).Error; err == nil {
			response.Charter = &charter
		} else {
			return response, err
		}
	}
	if err := model.DB.Where("personal_company_id = ?", company.ID).Order("priority DESC, created_at DESC").Limit(8).Find(&response.Objectives).Error; err != nil {
		return response, err
	}
	if err := model.DB.Where("personal_company_id = ?", company.ID).Order("priority DESC, created_at DESC").Limit(12).Find(&response.WorkItems).Error; err != nil {
		return response, err
	}
	if err := model.DB.Where("personal_company_id = ? AND status = ?", company.ID, model.CompanyApprovalPending).Order("created_at ASC").Find(&response.Approvals).Error; err != nil {
		return response, err
	}
	var connectorRows []struct {
		AdvancedChatConnectorTask
		WorkItemID uint
		Title      string
	}
	if err := model.DB.Table("advanced_chat_connector_tasks AS tasks").
		Select("tasks.*, work.id AS work_item_id, work.title").
		Joins("JOIN company_work_attempts AS attempts ON attempts.advanced_chat_run_id = tasks.run_id").
		Joins("JOIN company_work_items AS work ON work.id = attempts.work_item_id").
		Where("work.personal_company_id = ? AND tasks.status = ?", company.ID, advancedChatConnectorTaskStatusPendingApproval).
		Order("tasks.created_at ASC").
		Scan(&connectorRows).Error; err != nil {
		return response, err
	}
	for _, row := range connectorRows {
		payload := map[string]interface{}{}
		_ = json.Unmarshal([]byte(row.Payload), &payload)
		response.ConnectorApprovals = append(response.ConnectorApprovals, personalCompanyConnectorApproval{ID: row.ID, WorkItemID: row.WorkItemID, WorkItemTitle: row.Title, RunID: row.RunID, Action: row.Action, WorkspacePath: row.WorkspacePath, Payload: payload, CreatedAt: row.CreatedAt})
	}
	if err := model.DB.Model(&model.CompanyWorkItem{}).Select("COALESCE(SUM(reserved_cost), 0)").Where("personal_company_id = ? AND status NOT IN ?", company.ID, []string{model.CompanyWorkStatusCancelled, model.CompanyWorkStatusDelivered}).Scan(&response.Budget.Reserved).Error; err != nil {
		return response, err
	}
	if err := model.DB.Model(&model.CompanyWorkItem{}).Select("COALESCE(SUM(consumed_cost), 0)").Where("personal_company_id = ?", company.ID).Scan(&response.Budget.Consumed).Error; err != nil {
		return response, err
	}
	monthStart := time.Now().UTC().AddDate(0, 0, -time.Now().UTC().Day()+1).Truncate(24 * time.Hour)
	if err := model.DB.Model(&model.CompanyBudgetLedger{}).Select("COALESCE(SUM(amount), 0)").Where("personal_company_id = ? AND entry_type IN ? AND created_at >= ?", company.ID, []string{"reservation", "release"}, monthStart).Scan(&response.Budget.MonthlyReserved).Error; err != nil {
		return response, err
	}
	var count int64
	if err := model.DB.Model(&model.CompanyObjective{}).Where("personal_company_id = ? AND status = ?", company.ID, model.CompanyObjectiveStatusActive).Count(&count).Error; err != nil {
		return response, err
	}
	response.Health.ActiveObjectives = int(count)
	if err := model.DB.Model(&model.CompanyWorkItem{}).Where("personal_company_id = ? AND status IN ?", company.ID, []string{model.CompanyWorkStatusPlanned, model.CompanyWorkStatusAuthorized, model.CompanyWorkStatusQueued, model.CompanyWorkStatusExecuting, model.CompanyWorkStatusAwaitingReview}).Count(&count).Error; err != nil {
		return response, err
	}
	response.Health.ActiveWorkItems = int(count)
	if err := model.DB.Model(&model.CompanyWorkItem{}).Where("personal_company_id = ? AND status = ?", company.ID, model.CompanyWorkStatusBlocked).Count(&count).Error; err != nil {
		return response, err
	}
	response.Health.BlockedWorkItems = int(count)
	if err := model.DB.Model(&model.CompanyApprovalRequest{}).Where("personal_company_id = ? AND status = ?", company.ID, model.CompanyApprovalPending).Count(&count).Error; err != nil {
		return response, err
	}
	response.Health.PendingApprovals = int(count) + len(response.ConnectorApprovals)
	return response, nil
}
