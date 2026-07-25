package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const personalCompanyMaximumEmployees = 12

type personalCompanyRoleTemplateInput struct {
	TemplateKey      string          `json:"template_key"`
	Name             string          `json:"name"`
	Responsibilities string          `json:"responsibilities"`
	DefinitionOfDone string          `json:"definition_of_done"`
	RequiredSkills   json.RawMessage `json:"required_skills"`
	AllowedTools     json.RawMessage `json:"allowed_tools"`
	MaxRiskLevel     string          `json:"max_risk_level"`
	MaxConcurrency   int             `json:"max_concurrency"`
}

type personalCompanyRecruitmentPlanInput struct {
	RoleTemplateID  *uint           `json:"role_template_id"`
	Title           string          `json:"title"`
	CapabilityGap   string          `json:"capability_gap"`
	ExpectedBenefit string          `json:"expected_benefit"`
	MaxRiskLevel    string          `json:"max_risk_level"`
	ProposedTools   json.RawMessage `json:"proposed_tools"`
}

type personalCompanyCapabilityEvidenceInput struct {
	Capability        string     `json:"capability"`
	EvidenceURI       string     `json:"evidence_uri"`
	EvaluationSummary string     `json:"evaluation_summary"`
	Score             float64    `json:"score"`
	Confidence        float64    `json:"confidence"`
	ValidUntil        *time.Time `json:"valid_until"`
}

func (api *personalCompanyAPI) listRoleTemplates(c *gin.Context) {
	ctx, ok := api.personalCompanyContext(c)
	if !ok {
		return
	}
	company, err := loadPersonalCompany(ctx.userID, ctx.agentGroupID)
	if writePersonalCompanyLoadError(c, err) {
		return
	}
	var templates []model.CompanyRoleTemplate
	if err := model.DB.Where("personal_company_id = ?", company.ID).Order("name ASC").Find(&templates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load role templates"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"role_templates": templates})
}

func (api *personalCompanyAPI) createRoleTemplate(c *gin.Context) {
	ctx, ok := api.personalCompanyContext(c)
	if !ok {
		return
	}
	company, err := loadPersonalCompany(ctx.userID, ctx.agentGroupID)
	if writePersonalCompanyLoadError(c, err) {
		return
	}
	var input personalCompanyRoleTemplateInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.DefinitionOfDone) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Role name and definition_of_done are required"})
		return
	}
	key := normalizePersonalCompanyRoleKey(input.TemplateKey, input.Name)
	skills, validSkills := normalizedPersonalCompanyJSON(input.RequiredSkills, "[]")
	tools, validTools := normalizedPersonalCompanyJSON(input.AllowedTools, "[]")
	if !validSkills || !validTools {
		c.JSON(http.StatusBadRequest, gin.H{"error": "required_skills and allowed_tools must be valid JSON"})
		return
	}
	template := model.CompanyRoleTemplate{PersonalCompanyID: company.ID, TemplateKey: key, Name: truncatePersonalCompanyText(input.Name, 160), Responsibilities: strings.TrimSpace(input.Responsibilities), DefinitionOfDone: strings.TrimSpace(input.DefinitionOfDone), RequiredSkills: skills, AllowedTools: tools, MaxRiskLevel: normalizePersonalCompanyRiskLevel(input.MaxRiskLevel), MaxConcurrency: normalizePersonalCompanyConcurrency(input.MaxConcurrency), CreatedByUserID: ctx.userID}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&template).Error; err != nil {
			return err
		}
		return createPersonalCompanyAuditEvent(tx, company.ID, nil, "owner", ctx.userID, "role_template.created", fmt.Sprintf(`{"role_template_id":%d}`, template.ID))
	}); err != nil {
		if isPersonalCompanyUniqueError(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "A role template with this key already exists"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create role template"})
		}
		return
	}
	c.JSON(http.StatusCreated, gin.H{"role_template": template})
}

func (api *personalCompanyAPI) listRecruitmentPlans(c *gin.Context) {
	ctx, ok := api.personalCompanyContext(c)
	if !ok {
		return
	}
	company, err := loadPersonalCompany(ctx.userID, ctx.agentGroupID)
	if writePersonalCompanyLoadError(c, err) {
		return
	}
	var plans []model.CompanyRecruitmentPlan
	if err := model.DB.Where("personal_company_id = ?", company.ID).Order("created_at DESC").Find(&plans).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load recruitment plans"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"recruitment_plans": plans})
}

func (api *personalCompanyAPI) createRecruitmentPlan(c *gin.Context) {
	ctx, ok := api.personalCompanyContext(c)
	if !ok {
		return
	}
	company, err := loadPersonalCompany(ctx.userID, ctx.agentGroupID)
	if writePersonalCompanyLoadError(c, err) {
		return
	}
	var input personalCompanyRecruitmentPlanInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if strings.TrimSpace(input.Title) == "" || strings.TrimSpace(input.CapabilityGap) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Recruitment title and capability_gap are required"})
		return
	}
	if input.RoleTemplateID != nil {
		var template model.CompanyRoleTemplate
		if err := model.DB.Where("id = ? AND personal_company_id = ? AND active = ?", *input.RoleTemplateID, company.ID, true).First(&template).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Role template is not available"})
			return
		}
	}
	tools, valid := normalizedPersonalCompanyJSON(input.ProposedTools, "[]")
	if !valid {
		c.JSON(http.StatusBadRequest, gin.H{"error": "proposed_tools must be valid JSON"})
		return
	}
	plan := model.CompanyRecruitmentPlan{PersonalCompanyID: company.ID, RoleTemplateID: input.RoleTemplateID, Title: truncatePersonalCompanyText(input.Title, 200), CapabilityGap: strings.TrimSpace(input.CapabilityGap), ExpectedBenefit: strings.TrimSpace(input.ExpectedBenefit), MaxRiskLevel: normalizePersonalCompanyRiskLevel(input.MaxRiskLevel), ProposedTools: tools, Status: model.CompanyRecruitmentPlanProposed, CreatedByUserID: ctx.userID}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&plan).Error; err != nil {
			return err
		}
		return createPersonalCompanyAuditEvent(tx, company.ID, nil, "owner", ctx.userID, "recruitment_plan.proposed", fmt.Sprintf(`{"recruitment_plan_id":%d}`, plan.ID))
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create recruitment plan"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"recruitment_plan": plan})
}

func (api *personalCompanyAPI) approveRecruitmentPlan(c *gin.Context) {
	ctx, ok := api.personalCompanyContext(c)
	if !ok {
		return
	}
	company, err := loadPersonalCompany(ctx.userID, ctx.agentGroupID)
	if writePersonalCompanyLoadError(c, err) {
		return
	}
	planID, err := personalCompanyUintID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Recruitment plan not found"})
		return
	}
	var plan model.CompanyRecruitmentPlan
	if err := model.DB.Where("id = ? AND personal_company_id = ?", planID, company.ID).First(&plan).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Recruitment plan not found"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load recruitment plan"})
		return
	}
	if plan.Status != model.CompanyRecruitmentPlanProposed {
		c.JSON(http.StatusConflict, gin.H{"error": "Recruitment plan has already been decided"})
		return
	}
	if plan.MaxRiskLevel != "r0" && plan.MaxRiskLevel != "r1" {
		c.JSON(http.StatusConflict, gin.H{"error": "Plans requesting R2 or higher require a separate policy change"})
		return
	}
	var employee model.PersonalCompanyEmployee
	now := time.Now()
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		var employeeCount int64
		if err := tx.Model(&model.PersonalCompanyEmployee{}).Where("personal_company_id = ? AND status != ?", company.ID, model.PersonalCompanyEmployeeRetired).Count(&employeeCount).Error; err != nil {
			return err
		}
		if employeeCount >= personalCompanyMaximumEmployees {
			return errPersonalCompanyOrganizationLimit
		}
		employee = model.PersonalCompanyEmployee{PersonalCompanyID: company.ID, EmployeeKey: newPersonalCompanyID("employee"), Name: truncatePersonalCompanyText(plan.Title, 160), Role: "candidate", Status: model.PersonalCompanyEmployeeProbation, Version: 1, MaxRiskLevel: plan.MaxRiskLevel}
		if err := tx.Create(&employee).Error; err != nil {
			return err
		}
		version := model.CompanyEmployeeVersion{PersonalCompanyID: company.ID, EmployeeID: employee.ID, Version: 1, RoleTemplateID: plan.RoleTemplateID, ToolGrants: "[]", DataScope: "[]", SkillScope: "[]", ModelPolicy: `{}`, CreatedByUserID: ctx.userID}
		if err := tx.Create(&version).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.CompanyRecruitmentPlan{}).Where("id = ? AND status = ?", plan.ID, model.CompanyRecruitmentPlanProposed).Updates(map[string]interface{}{"status": model.CompanyRecruitmentPlanApproved, "employee_id": employee.ID, "decided_by_user_id": ctx.userID, "decided_at": now}).Error; err != nil {
			return err
		}
		return createPersonalCompanyAuditEvent(tx, company.ID, nil, "owner", ctx.userID, "recruitment_plan.approved", fmt.Sprintf(`{"recruitment_plan_id":%d,"employee_id":%d}`, plan.ID, employee.ID))
	}); err != nil {
		if errors.Is(err, errPersonalCompanyOrganizationLimit) {
			c.JSON(http.StatusConflict, gin.H{"error": "Personal Company organization limit reached"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to approve recruitment plan"})
		}
		return
	}
	c.JSON(http.StatusCreated, gin.H{"employee": employee, "status": model.CompanyRecruitmentPlanApproved})
}

func (api *personalCompanyAPI) recordCapabilityEvidence(c *gin.Context) {
	ctx, ok := api.personalCompanyContext(c)
	if !ok {
		return
	}
	company, err := loadPersonalCompany(ctx.userID, ctx.agentGroupID)
	if writePersonalCompanyLoadError(c, err) {
		return
	}
	employee, err := loadPersonalCompanyEmployee(company.ID, c.Param("id"))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Employee not found"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load employee"})
		return
	}
	var input personalCompanyCapabilityEvidenceInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if strings.TrimSpace(input.Capability) == "" || strings.TrimSpace(input.EvidenceURI) == "" || input.Score < 0 || input.Score > 1 || input.Confidence < 0 || input.Confidence > 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Capability, evidence_uri, score and confidence are required; score and confidence must be between 0 and 1"})
		return
	}
	var version model.CompanyEmployeeVersion
	if err := model.DB.Where("employee_id = ? AND personal_company_id = ? AND version = ?", employee.ID, company.ID, employee.Version).First(&version).Error; err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "Employee version is unavailable"})
		return
	}
	evidence := model.CompanyCapabilityEvidence{PersonalCompanyID: company.ID, EmployeeID: employee.ID, EmployeeVersionID: &version.ID, Capability: truncatePersonalCompanyText(input.Capability, 160), EvidenceURI: strings.TrimSpace(input.EvidenceURI), EvaluationSummary: strings.TrimSpace(input.EvaluationSummary), Score: input.Score, Confidence: input.Confidence, ValidUntil: input.ValidUntil, VerifiedByUserID: ctx.userID}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&evidence).Error; err != nil {
			return err
		}
		return createPersonalCompanyAuditEvent(tx, company.ID, nil, "owner", ctx.userID, "capability_evidence.recorded", fmt.Sprintf(`{"employee_id":%d,"evidence_id":%d}`, employee.ID, evidence.ID))
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to record capability evidence"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"capability_evidence": evidence})
}

func (api *personalCompanyAPI) promoteEmployee(c *gin.Context) {
	ctx, ok := api.personalCompanyContext(c)
	if !ok {
		return
	}
	company, err := loadPersonalCompany(ctx.userID, ctx.agentGroupID)
	if writePersonalCompanyLoadError(c, err) {
		return
	}
	employee, err := loadPersonalCompanyEmployee(company.ID, c.Param("id"))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Employee not found"})
		return
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load employee"})
		return
	}
	if employee.Status != model.PersonalCompanyEmployeeProbation {
		c.JSON(http.StatusConflict, gin.H{"error": "Only probationary employees can be promoted"})
		return
	}
	var qualifyingEvidence int64
	if err := model.DB.Model(&model.CompanyCapabilityEvidence{}).Where("personal_company_id = ? AND employee_id = ? AND score >= ? AND confidence >= ? AND (valid_until IS NULL OR valid_until > ?)", company.ID, employee.ID, 0.8, 0.7, time.Now()).Count(&qualifyingEvidence).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to evaluate capability evidence"})
		return
	}
	if qualifyingEvidence == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "Employee needs verified capability evidence before promotion"})
		return
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.PersonalCompanyEmployee{}).Where("id = ? AND status = ?", employee.ID, model.PersonalCompanyEmployeeProbation).Update("status", model.PersonalCompanyEmployeeActive).Error; err != nil {
			return err
		}
		return createPersonalCompanyAuditEvent(tx, company.ID, nil, "owner", ctx.userID, "employee.promoted", fmt.Sprintf(`{"employee_id":%d}`, employee.ID))
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to promote employee"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"employee_id": employee.ID, "status": model.PersonalCompanyEmployeeActive})
}

func loadPersonalCompanyEmployee(companyID uint, rawID string) (model.PersonalCompanyEmployee, error) {
	id, err := personalCompanyUintID(rawID)
	if err != nil {
		return model.PersonalCompanyEmployee{}, gorm.ErrRecordNotFound
	}
	var employee model.PersonalCompanyEmployee
	err = model.DB.Where("id = ? AND personal_company_id = ?", id, companyID).First(&employee).Error
	return employee, err
}
func personalCompanyUintID(value string) (uint, error) {
	id, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	if err != nil || id == 0 {
		return 0, errors.New("invalid ID")
	}
	return uint(id), nil
}
func normalizePersonalCompanyConcurrency(value int) int {
	if value < 1 {
		return 1
	}
	if value > 8 {
		return 8
	}
	return value
}
func normalizePersonalCompanyRoleKey(value, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		value = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(fallback), " ", "-"))
	}
	value = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return -1
	}, value)
	return truncatePersonalCompanyText(strings.Trim(value, "-"), 80)
}

var errPersonalCompanyOrganizationLimit = errors.New("personal company organization limit reached")
