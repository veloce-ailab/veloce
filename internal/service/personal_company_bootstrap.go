package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type personalCompanyRequestContext struct {
	userID       uint
	agentGroupID string
}

type personalCompanyBootstrapInput struct {
	Name              string          `json:"name"`
	Mission           string          `json:"mission"`
	Timezone          string          `json:"timezone"`
	DailyBudget       decimal.Decimal `json:"daily_budget"`
	MonthlyBudget     decimal.Decimal `json:"monthly_budget"`
	BalanceFloor      decimal.Decimal `json:"balance_floor"`
	AutonomyLevel     string          `json:"autonomy_level"`
	Goals             json.RawMessage `json:"goals"`
	DataBoundaries    json.RawMessage `json:"data_boundaries"`
	ProhibitedActions json.RawMessage `json:"prohibited_actions"`
}

type personalCompanyCharterInput struct {
	Mission           string          `json:"mission"`
	Goals             json.RawMessage `json:"goals"`
	DataBoundaries    json.RawMessage `json:"data_boundaries"`
	ProhibitedActions json.RawMessage `json:"prohibited_actions"`
	ApprovalPolicy    json.RawMessage `json:"approval_policy"`
}

func (api *personalCompanyAPI) getCompany(c *gin.Context) {
	ctx, ok := api.personalCompanyContext(c)
	if !ok {
		return
	}
	company, err := ensurePersonalCompanyOperating(ctx.userID, ctx.agentGroupID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load Personal Company"})
		return
	}
	dashboard, err := personalCompanyDashboard(company)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load Personal Company dashboard"})
		return
	}
	c.JSON(http.StatusOK, dashboard)
}

// ensurePersonalCompanyOperating makes Studio operations the default for every
// persisted Agent Studio. A user may later pause the operation, but a fresh
// Studio starts with a minimal governing charter and an active Chief loop.
func ensurePersonalCompanyOperating(userID uint, agentGroupID string) (model.PersonalCompany, error) {
	company, err := loadPersonalCompany(userID, agentGroupID)
	if err == nil || !errors.Is(err, gorm.ErrRecordNotFound) {
		return company, err
	}
	group, err := readAdvancedChatAgentGroup(context.Background(), userID, nil, agentGroupID)
	if err != nil {
		return model.PersonalCompany{}, err
	}
	company = model.PersonalCompany{OwnerUserID: userID, AgentGroupID: group.ID, Name: truncatePersonalCompanyText(group.Name, 160), State: model.PersonalCompanyStateOperating, Timezone: "UTC", AutonomyLevel: model.PersonalCompanyAutonomyR0}
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		var existing model.PersonalCompany
		if err := tx.Where("owner_user_id = ? AND agent_group_id = ?", userID, group.ID).First(&existing).Error; err == nil {
			company = existing
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := tx.Create(&company).Error; err != nil {
			return err
		}
		charter := model.CompanyCharterRevision{PersonalCompanyID: company.ID, Revision: 1, Mission: "Chief-scheduled delivery through immutable internal Studio sessions.", Goals: "[]", DataBoundaries: "[]", ProhibitedActions: "[]", ApprovalPolicy: `{"r3":"owner_required","r4":"forbidden"}`, CreatedByUserID: userID}
		if err := tx.Create(&charter).Error; err != nil {
			return err
		}
		if err := tx.Model(&company).Updates(map[string]interface{}{"charter_revision_id": charter.ID}).Error; err != nil {
			return err
		}
		company.CharterRevisionID = &charter.ID
		return createPersonalCompanyAuditEvent(tx, company.ID, nil, "system", 0, "company.default_enabled", `{}`)
	})
	return company, err
}

func (api *personalCompanyAPI) bootstrapCompany(c *gin.Context) {
	ctx, ok := api.personalCompanyContext(c)
	if !ok {
		return
	}
	var input personalCompanyBootstrapInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.Mission) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Company name and mission are required"})
		return
	}
	if input.DailyBudget.IsNegative() || input.MonthlyBudget.IsNegative() || input.BalanceFloor.IsNegative() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Budgets cannot be negative"})
		return
	}
	company := model.PersonalCompany{
		OwnerUserID:   ctx.userID,
		Name:          truncatePersonalCompanyText(input.Name, 160),
		State:         model.PersonalCompanyStateBootstrap,
		Timezone:      firstPersonalCompanyValue(input.Timezone, "UTC"),
		AutonomyLevel: model.NormalizePersonalCompanyAutonomy(input.AutonomyLevel),
		DailyBudget:   input.DailyBudget,
		MonthlyBudget: input.MonthlyBudget,
		BalanceFloor:  input.BalanceFloor,
	}
	goals, valid := normalizedPersonalCompanyJSON(input.Goals, "[]")
	if !valid {
		c.JSON(http.StatusBadRequest, gin.H{"error": "goals must be valid JSON"})
		return
	}
	dataBoundaries, valid := normalizedPersonalCompanyJSON(input.DataBoundaries, "[]")
	if !valid {
		c.JSON(http.StatusBadRequest, gin.H{"error": "data_boundaries must be valid JSON"})
		return
	}
	prohibited, valid := normalizedPersonalCompanyJSON(input.ProhibitedActions, "[]")
	if !valid {
		c.JSON(http.StatusBadRequest, gin.H{"error": "prohibited_actions must be valid JSON"})
		return
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		var existing model.PersonalCompany
		if err := tx.Where("owner_user_id = ? AND agent_group_id = ?", ctx.userID, ctx.agentGroupID).First(&existing).Error; err == nil {
			return errPersonalCompanyAlreadyBootstrapped
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		company.AgentGroupID = ctx.agentGroupID
		if err := tx.Create(&company).Error; err != nil {
			return err
		}
		charter := model.CompanyCharterRevision{
			PersonalCompanyID: company.ID, Revision: 1, Mission: strings.TrimSpace(input.Mission), Goals: goals,
			DataBoundaries: dataBoundaries, ProhibitedActions: prohibited, ApprovalPolicy: `{"r3":"owner_required","r4":"forbidden"}`,
			CreatedByUserID: ctx.userID,
		}
		if err := tx.Create(&charter).Error; err != nil {
			return err
		}
		if err := tx.Model(&company).Updates(map[string]interface{}{
			"charter_revision_id": charter.ID,
			"state":               model.PersonalCompanyStateOperating,
		}).Error; err != nil {
			return err
		}
		company.CharterRevisionID = &charter.ID
		company.State = model.PersonalCompanyStateOperating
		return createPersonalCompanyAuditEvent(tx, company.ID, nil, "owner", ctx.userID, "company.bootstrapped", `{"charter_revision":1}`)
	}); err != nil {
		if errors.Is(err, errPersonalCompanyAlreadyBootstrapped) {
			c.JSON(http.StatusConflict, gin.H{"error": "Personal Company is already bootstrapped"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to bootstrap Personal Company"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"company": company})
}

func (api *personalCompanyAPI) updateCharter(c *gin.Context) {
	ctx, ok := api.personalCompanyContext(c)
	if !ok {
		return
	}
	company, err := loadPersonalCompany(ctx.userID, ctx.agentGroupID)
	if writePersonalCompanyLoadError(c, err) {
		return
	}
	var input personalCompanyCharterInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if strings.TrimSpace(input.Mission) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Mission is required"})
		return
	}
	goals, validGoals := normalizedPersonalCompanyJSON(input.Goals, "[]")
	boundaries, validBoundaries := normalizedPersonalCompanyJSON(input.DataBoundaries, "[]")
	prohibited, validProhibited := normalizedPersonalCompanyJSON(input.ProhibitedActions, "[]")
	policy, validPolicy := normalizedPersonalCompanyJSON(input.ApprovalPolicy, `{"r3":"owner_required","r4":"forbidden"}`)
	if !validGoals || !validBoundaries || !validProhibited || !validPolicy {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Charter structured fields must be valid JSON"})
		return
	}
	var charter model.CompanyCharterRevision
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		var latest model.CompanyCharterRevision
		if err := tx.Where("personal_company_id = ?", company.ID).Order("revision DESC").First(&latest).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		charter = model.CompanyCharterRevision{PersonalCompanyID: company.ID, Revision: latest.Revision + 1, Mission: strings.TrimSpace(input.Mission), Goals: goals, DataBoundaries: boundaries, ProhibitedActions: prohibited, ApprovalPolicy: policy, CreatedByUserID: ctx.userID}
		if err := tx.Create(&charter).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.PersonalCompany{}).Where("id = ? AND owner_user_id = ?", company.ID, ctx.userID).Update("charter_revision_id", charter.ID).Error; err != nil {
			return err
		}
		return createPersonalCompanyAuditEvent(tx, company.ID, nil, "owner", ctx.userID, "charter.revised", `{"revision":`+decimal.NewFromInt(int64(charter.Revision)).String()+`}`)
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to revise charter"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"charter": charter})
}

func (api *personalCompanyAPI) pauseCompany(c *gin.Context) {
	api.setCompanyState(c, model.PersonalCompanyStatePaused)
}
func (api *personalCompanyAPI) resumeCompany(c *gin.Context) {
	api.setCompanyState(c, model.PersonalCompanyStateOperating)
}

func (api *personalCompanyAPI) setCompanyState(c *gin.Context, state string) {
	ctx, ok := api.personalCompanyContext(c)
	if !ok {
		return
	}
	company, err := loadPersonalCompany(ctx.userID, ctx.agentGroupID)
	if writePersonalCompanyLoadError(c, err) {
		return
	}
	now := time.Now()
	updates := map[string]interface{}{"state": state}
	if state == model.PersonalCompanyStatePaused {
		updates["paused_at"] = now
	} else {
		updates["paused_at"] = nil
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.PersonalCompany{}).Where("id = ? AND owner_user_id = ?", company.ID, ctx.userID).Updates(updates).Error; err != nil {
			return err
		}
		updatedCompany := company
		updatedCompany.State = state
		if err := enqueuePersonalCompanySignal(tx, updatedCompany, nil, "owner", "company."+state, `{}`); err != nil {
			return err
		}
		return createPersonalCompanyAuditEvent(tx, company.ID, nil, "owner", ctx.userID, "company."+state, `{}`)
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update company state"})
		return
	}
	if state == model.PersonalCompanyStatePaused {
		company.OwnerUserID = ctx.userID
		if err := interruptPersonalCompanyWork(company); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Studio paused but active work could not be interrupted"})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"state": state})
}

var errPersonalCompanyAlreadyBootstrapped = errors.New("personal company already bootstrapped")

func loadPersonalCompany(userID uint, agentGroupID string) (model.PersonalCompany, error) {
	var company model.PersonalCompany
	err := model.DB.Where("owner_user_id = ? AND agent_group_id = ?", userID, strings.TrimSpace(agentGroupID)).First(&company).Error
	return company, err
}

func writePersonalCompanyLoadError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Personal Company has not been bootstrapped"})
	} else {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load Personal Company"})
	}
	return true
}

func bootstrapPersonalCompanyEmployees(companyID uint) []model.PersonalCompanyEmployee {
	return []model.PersonalCompanyEmployee{
		{PersonalCompanyID: companyID, EmployeeKey: "chief-of-staff", Name: "Chief of Staff", Role: "coordination", Status: model.PersonalCompanyEmployeeProbation, MaxRiskLevel: "r1"},
		{PersonalCompanyID: companyID, EmployeeKey: "research", Name: "Research", Role: "research", Status: model.PersonalCompanyEmployeeProbation, MaxRiskLevel: "r1"},
		{PersonalCompanyID: companyID, EmployeeKey: "delivery", Name: "Delivery", Role: "delivery", Status: model.PersonalCompanyEmployeeProbation, MaxRiskLevel: "r1"},
		{PersonalCompanyID: companyID, EmployeeKey: "review", Name: "Review", Role: "review", Status: model.PersonalCompanyEmployeeProbation, MaxRiskLevel: "r1"},
	}
}

func bootstrapPersonalCompanyRoleTemplates(companyID, userID uint) []model.CompanyRoleTemplate {
	return []model.CompanyRoleTemplate{
		{PersonalCompanyID: companyID, TemplateKey: "chief-of-staff", Name: "Chief of Staff", Responsibilities: "Decompose objectives, coordinate work, and prepare owner decisions.", DefinitionOfDone: "A traceable plan or decision package with evidence and risk notes.", MaxRiskLevel: "r1", CreatedByUserID: userID},
		{PersonalCompanyID: companyID, TemplateKey: "research", Name: "Research", Responsibilities: "Find and synthesize permitted evidence.", DefinitionOfDone: "Cited findings with confidence and unresolved questions.", MaxRiskLevel: "r1", CreatedByUserID: userID},
		{PersonalCompanyID: companyID, TemplateKey: "delivery", Name: "Delivery", Responsibilities: "Produce bounded drafts and reversible internal outputs.", DefinitionOfDone: "A versioned artifact that satisfies the assigned acceptance criteria.", MaxRiskLevel: "r1", CreatedByUserID: userID},
		{PersonalCompanyID: companyID, TemplateKey: "review", Name: "Review", Responsibilities: "Independently check deliverables, evidence, and acceptance criteria.", DefinitionOfDone: "A review decision with concrete evidence and remaining risks.", MaxRiskLevel: "r1", CreatedByUserID: userID},
	}
}

func normalizedPersonalCompanyJSON(value json.RawMessage, fallback string) (string, bool) {
	if len(value) == 0 || strings.TrimSpace(string(value)) == "" {
		return fallback, true
	}
	var target interface{}
	if err := json.Unmarshal(value, &target); err != nil {
		return "", false
	}
	return string(value), true
}

func firstPersonalCompanyValue(value, fallback string) string {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return truncatePersonalCompanyText(trimmed, 80)
	}
	return fallback
}

func truncatePersonalCompanyText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
