package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/middleware"
	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/veloce-ailab/veloce/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type EnterpriseAPI struct{}

type enterpriseWorkspaceResponse struct {
	Workspace model.Workspace `json:"workspace"`
	Role      string          `json:"role"`
}

type enterpriseContextInput struct {
	WorkspaceID   *uint  `json:"workspace_id"`
	WorkspaceSlug string `json:"workspace_slug"`
}
type enterpriseOrganizationInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}
type enterprisePortalInput struct {
	Enabled     bool     `json:"enabled"`
	Title       string   `json:"title"`
	Message     string   `json:"message"`
	Widgets     []string `json:"widgets"`
	PageLayouts string   `json:"page_layouts"`
}
type enterprisePortalConfig struct {
	Enabled     bool     `json:"enabled"`
	Title       string   `json:"title"`
	Message     string   `json:"message"`
	Widgets     []string `json:"widgets"`
	PageLayouts string   `json:"page_layouts"`
}

type enterpriseRoleInput struct {
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
}

type enterpriseBindingInput struct {
	UserID       uint   `json:"user_id"`
	DepartmentID uint   `json:"department_id"`
	RoleID       uint   `json:"role_id"`
	ScopeType    string `json:"scope_type"`
	ScopeID      uint   `json:"scope_id"`
}
type enterpriseDepartmentInput struct {
	Slug        string                 `json:"slug"`
	Name        string                 `json:"name"`
	ParentID    *uint                  `json:"parent_id"`
	Multiplier  string                 `json:"multiplier"`
	ModelPolicy string                 `json:"model_policy"`
	ModelNames  []string               `json:"model_names"`
	Settings    map[string]interface{} `json:"settings"`
}

type enterpriseTaskInput struct {
	Title              string     `json:"title"`
	Description        string     `json:"description"`
	DepartmentID       *uint      `json:"department_id"`
	OwnerUserID        *uint      `json:"owner_user_id"`
	AssigneeUserIDs    []uint     `json:"assignee_user_ids"`
	ParticipantUserIDs []uint     `json:"participant_user_ids"`
	Priority           int        `json:"priority"`
	DueAt              *time.Time `json:"due_at"`
}

type enterpriseTaskStatusInput struct {
	Status string `json:"status"`
}
type enterpriseTaskParticipantInput struct {
	UserID uint   `json:"user_id"`
	Role   string `json:"role"`
}
type enterpriseTaskDepartmentInput struct {
	DepartmentID uint `json:"department_id"`
}
type enterpriseMemberInput struct {
	Role   *string `json:"role"`
	Status *string `json:"status"`
}
type enterpriseCreateMemberInput struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role"`
}
type enterpriseMemberDepartmentsInput struct {
	DepartmentIDs []uint `json:"department_ids"`
}
type enterpriseDepartmentRolesInput struct {
	RoleIDs []uint `json:"role_ids"`
}

type enterpriseDeviceInput struct {
	ExternalDeviceID    string `json:"external_device_id"`
	Name                string `json:"name"`
	Kind                string `json:"kind"`
	OwnerUserID         *uint  `json:"owner_user_id"`
	ManagedByEnterprise bool   `json:"managed_by_enterprise"`
}
type enterpriseConnectorTokenInput struct {
	Name        string `json:"name"`
	OwnerUserID *uint  `json:"owner_user_id"`
	Mode        string `json:"mode"`
	ListenPort  int    `json:"listen_port"`
}
type enterpriseDeviceAssignmentInput struct {
	DeviceID       uint       `json:"device_id"`
	UserID         *uint      `json:"user_id"`
	DepartmentID   *uint      `json:"department_id"`
	TaskID         *uint      `json:"task_id"`
	AllowedTools   []string   `json:"allowed_tools"`
	Classification string     `json:"classification"`
	ExpiresAt      *time.Time `json:"expires_at"`
}
type enterpriseQuotaAccountInput struct {
	ScopeType    string `json:"scope_type"`
	DepartmentID *uint  `json:"department_id"`
	UserID       *uint  `json:"user_id"`
	TaskID       *uint  `json:"task_id"`
	PoolID       *uint  `json:"pool_id"`
	InitialLimit string `json:"initial_limit"`
}
type enterpriseQuotaAllocationInput struct {
	ParentAccountID uint   `json:"parent_account_id"`
	ChildAccountID  uint   `json:"child_account_id"`
	Amount          string `json:"amount"`
	ReferenceID     string `json:"reference_id"`
}
type enterprisePoolBudgetInput struct {
	PoolID uint   `json:"pool_id"`
	Amount string `json:"amount"`
}
type enterpriseUserBudgetInput struct {
	UserID uint   `json:"user_id"`
	Amount string `json:"amount"`
}
type enterpriseSharedPoolInput struct {
	ScopeType    string `json:"scope_type"`
	DepartmentID *uint  `json:"department_id"`
	TaskID       *uint  `json:"task_id"`
	Name         string `json:"name"`
}
type enterprisePoolResourceInput struct {
	ID string `json:"id"`
}
type enterpriseSharedSessionMessageInput struct {
	Content string `json:"content"`
	Title   string `json:"title"`
}
type enterpriseSharedPoolSessionInput struct {
	AgentID       string `json:"agent_id"`
	ModelName     string `json:"model_name"`
	UserChannelID uint   `json:"user_channel_id"`
}

func (api *EnterpriseAPI) GetOrganization(c *gin.Context) {
	if _, ok := enterpriseCurrentUser(c); !ok || !enterpriseFeatureAvailable(c) {
		return
	}
	tenant, ok := middleware.CurrentTenantContext(c)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Enterprise tenant context is unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"organization": tenant.Organization, "role": tenant.OrganizationMember.Role})
}

func (api *EnterpriseAPI) UpdateOrganization(c *gin.Context) {
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	var input enterpriseOrganizationInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid organization settings"})
		return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" || len([]rune(name)) > 160 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Organization name must be between 1 and 160 characters"})
		return
	}
	description := strings.TrimSpace(input.Description)
	if len([]rune(description)) > 1000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Organization description is too long"})
		return
	}
	if err := model.DB.Model(&model.Organization{}).Where("id = ?", tenant.Organization.ID).Updates(map[string]interface{}{"name": name, "description": description}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update organization settings"})
		return
	}
	tenant.Organization.Name, tenant.Organization.Description = name, description
	enterpriseAuditCurrent(c, "organization_updated", fmt.Sprintf("更新企业资料：%s", name), fmt.Sprintf(`{"organization_id":%d}`, tenant.Organization.ID))
	c.JSON(http.StatusOK, gin.H{"organization": tenant.Organization})
}

func (api *EnterpriseAPI) GetPortal(c *gin.Context) {
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	config := enterprisePortalConfigFor(tenant.Organization.ID)
	var runningTasks, assignedTasks, employees, departments, devices int64
	model.DB.Model(&model.EnterpriseTask{}).Where("organization_id = ? AND status = ?", tenant.Organization.ID, model.EnterpriseTaskStatusRunning).Count(&runningTasks)
	model.DB.Model(&model.EnterpriseTask{}).Where("organization_id = ? AND status = ?", tenant.Organization.ID, model.EnterpriseTaskStatusAssigned).Count(&assignedTasks)
	model.DB.Model(&model.OrganizationMember{}).Joins("JOIN users ON users.id = organization_members.user_id").Where("organization_members.organization_id = ? AND users.username NOT LIKE ? AND users.email NOT LIKE ?", tenant.Organization.ID, "enterprise-pool-%", "%@internal.invalid").Count(&employees)
	model.DB.Model(&model.Department{}).Where("organization_id = ?", tenant.Organization.ID).Count(&departments)
	model.DB.Model(&model.EnterpriseDevice{}).Where("organization_id = ? AND status = ?", tenant.Organization.ID, model.EnterpriseDeviceStatusActive).Count(&devices)
	c.JSON(http.StatusOK, gin.H{"organization": tenant.Organization, "portal": config, "summary": gin.H{"running_tasks": runningTasks, "assigned_tasks": assignedTasks, "employees": employees, "departments": departments, "devices": devices}})
}

func (api *EnterpriseAPI) UpdatePortal(c *gin.Context) {
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	var input enterprisePortalInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid portal settings"})
		return
	}
	config := enterprisePortalConfigFor(tenant.Organization.ID)
	config.Enabled, config.Title, config.Message, config.Widgets = input.Enabled, strings.TrimSpace(input.Title), strings.TrimSpace(input.Message), enterprisePortalWidgets(input.Widgets)
	if len([]rune(config.Title)) > 160 || len([]rune(config.Message)) > 1000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Portal content is too long"})
		return
	}
	data, err := json.Marshal(config)
	if err != nil || model.SetSystemSettingWithDB(model.DB, enterprisePortalSettingKey(tenant.Organization.ID), string(data)) != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save portal settings"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"portal": config})
	enterpriseAuditCurrent(c, "portal_updated", "更新企业门户配置", fmt.Sprintf(`{"organization_id":%d}`, tenant.Organization.ID))
}

func (api *EnterpriseAPI) UpdatePortalLayout(c *gin.Context) {
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	var input struct {
		PageLayouts string `json:"page_layouts"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid portal layout"})
		return
	}
	if input.PageLayouts != "" {
		var layouts map[string]interface{}
		if json.Unmarshal([]byte(input.PageLayouts), &layouts) != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid portal page layout"})
			return
		}
	}
	config := enterprisePortalConfigFor(tenant.Organization.ID)
	config.PageLayouts = input.PageLayouts
	data, err := json.Marshal(config)
	if err != nil || model.SetSystemSettingWithDB(model.DB, enterprisePortalSettingKey(tenant.Organization.ID), string(data)) != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save portal layout"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"portal": config})
	enterpriseAuditCurrent(c, "portal_layout_updated", "更新企业门户仪表盘布局", fmt.Sprintf(`{"organization_id":%d}`, tenant.Organization.ID))
}

func (api *EnterpriseAPI) ListWorkspaces(c *gin.Context) {
	user, ok := enterpriseCurrentUser(c)
	if !ok || !enterpriseFeatureAvailable(c) {
		return
	}
	tenant, ok := middleware.CurrentTenantContext(c)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Enterprise tenant context is unavailable"})
		return
	}
	var memberships []model.WorkspaceMember
	if err := model.DB.Preload("Workspace").
		Joins("JOIN workspaces ON workspaces.id = workspace_members.workspace_id AND workspaces.deleted_at IS NULL").
		Where("workspace_members.user_id = ? AND workspace_members.deleted_at IS NULL", user.ID).
		Where("workspaces.organization_id = ? AND workspaces.status = ?", tenant.Organization.ID, model.WorkspaceStatusActive).
		Order("workspace_members.id ASC").Find(&memberships).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list workspaces"})
		return
	}
	items := make([]enterpriseWorkspaceResponse, 0, len(memberships))
	for _, membership := range memberships {
		if membership.Workspace.ID == 0 {
			continue
		}
		items = append(items, enterpriseWorkspaceResponse{Workspace: membership.Workspace, Role: membership.Role})
	}
	c.JSON(http.StatusOK, gin.H{"workspaces": items})
}

func (api *EnterpriseAPI) SelectContext(c *gin.Context) {
	user, ok := enterpriseCurrentUser(c)
	if !ok || !enterpriseFeatureAvailable(c) {
		return
	}
	var input enterpriseContextInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid enterprise context"})
		return
	}
	selection := middleware.TenantSelection{
		WorkspaceSlug: strings.TrimSpace(input.WorkspaceSlug),
	}
	if input.WorkspaceID != nil {
		selection.WorkspaceID = strconv.FormatUint(uint64(*input.WorkspaceID), 10)
	}
	tenant, status, err := middleware.ResolveTenantContext(model.DB, user.ID, selection)
	if err != nil {
		c.JSON(status, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, tenant)
}

func (api *EnterpriseAPI) PreviewPermissions(c *gin.Context) {
	user, ok := enterpriseCurrentUser(c)
	if !ok || !enterpriseFeatureAvailable(c) {
		return
	}
	tenant, ok := middleware.CurrentTenantContext(c)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Enterprise tenant context is unavailable"})
		return
	}
	workspaceID := uint(0)
	if tenant.Workspace != nil {
		workspaceID = tenant.Workspace.ID
	}
	c.JSON(http.StatusOK, gin.H{"permissions": middleware.EffectivePermissions(model.DB, user.ID, tenant.Organization.ID, workspaceID)})
}

func (api *EnterpriseAPI) ListPermissions(c *gin.Context) {
	if !enterpriseFeatureAvailable(c) {
		return
	}
	var permissions []model.Permission
	if err := model.DB.Order("resource ASC, action ASC").Find(&permissions).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list permissions"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"permissions": permissions})
}

func (api *EnterpriseAPI) ListMembers(c *gin.Context) {
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	if err := model.EnsureEnterpriseTenantForExistingUsers(model.DB); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to synchronize employee accounts"})
		return
	}
	var members []model.OrganizationMember
	if err := model.DB.Preload("User").Joins("JOIN users ON users.id = organization_members.user_id").
		Where("organization_members.organization_id = ? AND users.username NOT LIKE ? AND users.email NOT LIKE ?", tenant.Organization.ID, "enterprise-pool-%", "%@internal.invalid").
		Order("organization_members.id ASC").Find(&members).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list members"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"members": members})
}

func (api *EnterpriseAPI) CreateMember(c *gin.Context) {
	user, ok := enterpriseCurrentUser(c)
	if !ok {
		return
	}
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	var input enterpriseCreateMemberInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid employee account"})
		return
	}
	if strings.TrimSpace(input.Username) == "" || strings.TrimSpace(input.Email) == "" || input.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Username, email, and password are required"})
		return
	}
	var account model.User
	input.Username = strings.TrimSpace(input.Username)
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	if len([]rune(input.Username)) < 3 || len([]rune(input.Username)) > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Username must be between 3 and 100 characters"})
		return
	}
	if input.Email == "" || !strings.Contains(input.Email, "@") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Valid email is required"})
		return
	}
	if len(input.Password) < 8 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Password must be at least 8 characters"})
		return
	}
	var count int64
	if err := model.DB.Model(&model.User{}).Where("username = ? OR email = ?", input.Username, input.Email).Count(&count).Error; err != nil || count > 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Username or email already exists"})
		return
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to secure password"})
		return
	}
	group, err := model.EnsureDefaultGroup()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to prepare user group"})
		return
	}
	apiKey, _, err := service.GenerateAPIKey()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create employee account"})
		return
	}
	account = model.User{Username: input.Username, Email: input.Email, PasswordHash: string(passwordHash), EmailVerified: true, GroupID: group.ID, APIKey: apiKey}
	if err := model.DB.Create(&account).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to create employee account"})
		return
	}
	if err := model.DB.Where(&model.UserGroupMembership{UserID: account.ID, GroupID: group.ID}).FirstOrCreate(&model.UserGroupMembership{UserID: account.ID, GroupID: group.ID}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to initialize employee account"})
		return
	}
	if enterprisePoolAccount(account) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Internal pool accounts cannot become employees"})
		return
	}
	role := model.NormalizeOrganizationMemberRole(input.Role)
	joinedAt := time.Now()
	member := model.OrganizationMember{}
	if err := model.DB.Unscoped().Where("organization_id = ? AND user_id = ?", tenant.Organization.ID, account.ID).First(&member).Error; err == nil {
		if member.DeletedAt.Valid {
			if err := model.DB.Unscoped().Model(&member).Updates(map[string]interface{}{"deleted_at": nil, "role": role, "status": model.OrganizationMemberStatusActive, "joined_at": joinedAt}).Error; err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to restore member"})
				return
			}
		} else if err := model.DB.Model(&member).Updates(map[string]interface{}{"role": role, "status": model.OrganizationMemberStatusActive, "joined_at": joinedAt}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to initialize employee membership"})
			return
		}
	} else if errors.Is(err, gorm.ErrRecordNotFound) {
		member = model.OrganizationMember{OrganizationID: tenant.Organization.ID, UserID: account.ID, Role: role, Status: model.OrganizationMemberStatusActive, JoinedAt: &joinedAt}
		if err := model.DB.Create(&member).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to add member"})
			return
		}
	} else {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to add member"})
		return
	}
	if err := model.EnsureOrganizationRoleBinding(model.DB, tenant.Organization.ID, account.ID, user.ID, model.BuiltinRoleMember); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to grant member permissions"})
		return
	}
	if err := model.DB.Preload("User").First(&member, member.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load member"})
		return
	}
	enterpriseAudit(c, user.ID, "member_created", fmt.Sprintf("创建员工账号“%s”", account.Username), fmt.Sprintf(`{"user_id":%d,"member_id":%d}`, account.ID, member.ID))
	c.JSON(http.StatusCreated, member)
}

func (api *EnterpriseAPI) DeleteMember(c *gin.Context) {
	actor, _ := enterpriseCurrentUser(c)
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	userID, err := parseEnterpriseID(c.Param("user_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var member model.OrganizationMember
	if err := model.DB.Where("organization_id = ? AND user_id = ?", tenant.Organization.ID, userID).First(&member).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Member not found"})
		return
	}
	if member.Role == model.OrganizationMemberRoleOwner {
		var owners int64
		model.DB.Model(&model.OrganizationMember{}).Where("organization_id = ? AND role = ?", tenant.Organization.ID, model.OrganizationMemberRoleOwner).Count(&owners)
		if owners <= 1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Cannot remove the last organization owner"})
			return
		}
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("organization_id = ? AND user_id = ?", tenant.Organization.ID, userID).Delete(&model.DepartmentMember{}).Error; err != nil {
			return err
		}
		if err := tx.Where("organization_id = ? AND user_id = ?", tenant.Organization.ID, userID).Delete(&model.RoleBinding{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ? AND task_id IN (?)", userID, tx.Model(&model.EnterpriseTask{}).Select("id").Where("organization_id = ?", tenant.Organization.ID)).Delete(&model.EnterpriseTaskAssignment{}).Error; err != nil {
			return err
		}
		return tx.Delete(&member).Error
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to remove member"})
		return
	}
	enterpriseAudit(c, actor.ID, "member_deleted", fmt.Sprintf("移除员工 #%d", userID), fmt.Sprintf(`{"user_id":%d}`, userID))
	c.JSON(http.StatusOK, gin.H{"message": "Member removed"})
}

func (api *EnterpriseAPI) UpdateMember(c *gin.Context) {
	actor, _ := enterpriseCurrentUser(c)
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	userID, err := parseEnterpriseID(c.Param("user_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var input enterpriseMemberInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid member"})
		return
	}
	updates := map[string]interface{}{}
	if input.Role != nil {
		updates["role"] = model.NormalizeOrganizationMemberRole(*input.Role)
	}
	if input.Status != nil {
		updates["status"] = model.NormalizeOrganizationMemberStatus(*input.Status)
	}
	if len(updates) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "No member changes"})
		return
	}
	var member model.OrganizationMember
	if err := model.DB.Where("organization_id = ? AND user_id = ?", tenant.Organization.ID, userID).First(&member).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Member not found"})
		return
	}
	if err := model.DB.Model(&member).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update member"})
		return
	}
	model.DB.First(&member, member.ID)
	enterpriseAudit(c, actor.ID, "member_updated", fmt.Sprintf("更新员工 #%d", userID), fmt.Sprintf(`{"user_id":%d}`, userID))
	c.JSON(http.StatusOK, member)
}

func (api *EnterpriseAPI) ListMemberDepartments(c *gin.Context) {
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	userID, err := parseEnterpriseID(c.Param("user_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var items []model.DepartmentMember
	if err := model.DB.Where("organization_id = ? AND user_id = ?", tenant.Organization.ID, userID).Order("department_id ASC").Find(&items).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list member departments"})
		return
	}
	departmentIDs := make([]uint, 0, len(items))
	for _, item := range items {
		departmentIDs = append(departmentIDs, item.DepartmentID)
	}
	c.JSON(http.StatusOK, gin.H{"department_ids": departmentIDs})
}

func (api *EnterpriseAPI) ReplaceMemberDepartments(c *gin.Context) {
	user, ok := enterpriseCurrentUser(c)
	if !ok {
		return
	}
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	userID, err := parseEnterpriseID(c.Param("user_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var input enterpriseMemberDepartmentsInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid member departments"})
		return
	}
	if !enterpriseActiveMember(tenant.Organization.ID, userID) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Member not found"})
		return
	}
	ids := uniqueEnterpriseUserIDs(input.DepartmentIDs)
	if len(ids) > 0 {
		var count int64
		if err := model.DB.Model(&model.Department{}).Where("organization_id = ? AND id IN ?", tenant.Organization.ID, ids).Count(&count).Error; err != nil || count != int64(len(ids)) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Department not found"})
			return
		}
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("organization_id = ? AND user_id = ?", tenant.Organization.ID, userID).Delete(&model.DepartmentMember{}).Error; err != nil {
			return err
		}
		for _, departmentID := range ids {
			if err := tx.Create(&model.DepartmentMember{OrganizationID: tenant.Organization.ID, DepartmentID: departmentID, UserID: userID}).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update member departments"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"department_ids": ids, "updated_by": user.ID})
	enterpriseAudit(c, user.ID, "member_departments_updated", fmt.Sprintf("更新员工 #%d 的部门归属", userID), fmt.Sprintf(`{"user_id":%d,"department_ids":%v}`, userID, ids))
}

func (api *EnterpriseAPI) ListDepartmentRoles(c *gin.Context) {
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	departmentID, err := parseEnterpriseID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var bindings []model.DepartmentRoleBinding
	if err := model.DB.Where("organization_id = ? AND department_id = ?", tenant.Organization.ID, departmentID).Find(&bindings).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list department roles"})
		return
	}
	roleIDs := make([]uint, 0, len(bindings))
	for _, binding := range bindings {
		roleIDs = append(roleIDs, binding.RoleID)
	}
	c.JSON(http.StatusOK, gin.H{"role_ids": roleIDs})
}

func (api *EnterpriseAPI) ReplaceDepartmentRoles(c *gin.Context) {
	user, ok := enterpriseCurrentUser(c)
	if !ok {
		return
	}
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	departmentID, err := parseEnterpriseID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var input enterpriseDepartmentRolesInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid department roles"})
		return
	}
	var department model.Department
	if err := model.DB.Where("id = ? AND organization_id = ?", departmentID, tenant.Organization.ID).First(&department).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Department not found"})
		return
	}
	roleIDs := uniqueEnterpriseUserIDs(input.RoleIDs)
	if len(roleIDs) > 0 {
		var count int64
		if err := model.DB.Model(&model.Role{}).Where("organization_id = ? AND id IN ?", tenant.Organization.ID, roleIDs).Count(&count).Error; err != nil || count != int64(len(roleIDs)) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Role not found"})
			return
		}
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("organization_id = ? AND department_id = ?", tenant.Organization.ID, departmentID).Delete(&model.DepartmentRoleBinding{}).Error; err != nil {
			return err
		}
		for _, roleID := range roleIDs {
			if err := tx.Create(&model.DepartmentRoleBinding{OrganizationID: tenant.Organization.ID, DepartmentID: departmentID, RoleID: roleID, CreatedByUserID: user.ID}).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update department roles"})
		return
	}
	enterpriseAudit(c, user.ID, "department_roles_updated", "", fmt.Sprintf(`{"department_id":%d,"role_ids":%v}`, department.ID, roleIDs))
	c.JSON(http.StatusOK, gin.H{"role_ids": roleIDs})
}

func (api *EnterpriseAPI) ListDepartments(c *gin.Context) {
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	var departments []model.Department
	if err := model.DB.Where("organization_id = ?", tenant.Organization.ID).Order("name ASC").Find(&departments).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list departments"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"departments": departments})
}
func (api *EnterpriseAPI) CreateDepartment(c *gin.Context) {
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	var input enterpriseDepartmentInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid department"})
		return
	}
	department, err := enterpriseDepartmentFromInput(tenant.Organization.ID, 0, input)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := model.DB.Create(&department).Error; err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "Failed to create department"})
		return
	}
	enterpriseAuditCurrent(c, "department_created", fmt.Sprintf("创建部门“%s”", department.Name), fmt.Sprintf(`{"department_id":%d}`, department.ID))
	c.JSON(http.StatusCreated, department)
}
func (api *EnterpriseAPI) UpdateDepartment(c *gin.Context) {
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	id, err := parseEnterpriseID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var input enterpriseDepartmentInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid department"})
		return
	}
	department, err := enterpriseDepartmentFromInput(tenant.Organization.ID, id, input)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := model.DB.Model(&department).Updates(map[string]interface{}{"slug": department.Slug, "name": department.Name, "parent_id": department.ParentID, "multiplier": department.Multiplier, "model_policy": department.ModelPolicy, "model_names": department.ModelNames, "settings": department.Settings}).Error; err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "Failed to update department"})
		return
	}
	enterpriseAuditCurrent(c, "department_updated", fmt.Sprintf("更新部门“%s”", department.Name), fmt.Sprintf(`{"department_id":%d}`, department.ID))
	c.JSON(http.StatusOK, department)
}
func (api *EnterpriseAPI) DeleteDepartment(c *gin.Context) {
	var department model.Department
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	id, err := parseEnterpriseID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	model.DB.Where("id = ? AND organization_id = ?", id, tenant.Organization.ID).First(&department)
	result := model.DB.Where("id = ? AND organization_id = ?", id, tenant.Organization.ID).Delete(&model.Department{})
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete department"})
		return
	}
	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Department not found"})
		return
	}
	enterpriseAuditCurrent(c, "department_deleted", fmt.Sprintf("删除部门“%s”", department.Name), fmt.Sprintf(`{"department_id":%d}`, id))
	c.JSON(http.StatusOK, gin.H{"message": "Department deleted"})
}

func (api *EnterpriseAPI) ListRoles(c *gin.Context) {
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	var roles []model.Role
	if err := model.DB.Where("organization_id = ?", tenant.Organization.ID).Order("builtin DESC, name ASC").Find(&roles).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list roles"})
		return
	}
	items := make([]gin.H, 0, len(roles))
	for i := range roles {
		roles[i].Description = strings.TrimSpace(roles[i].Description)
		var codes []string
		model.DB.Table("role_permissions").Select("permissions.code").Joins("JOIN permissions ON permissions.id = role_permissions.permission_id").Where("role_permissions.role_id = ?", roles[i].ID).Order("permissions.code ASC").Scan(&codes)
		items = append(items, gin.H{"id": roles[i].ID, "name": roles[i].Name, "slug": roles[i].Slug, "description": roles[i].Description, "builtin": roles[i].Builtin, "permissions": codes})
	}
	c.JSON(http.StatusOK, gin.H{"roles": items})
}

func (api *EnterpriseAPI) CreateRole(c *gin.Context) {
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	var input enterpriseRoleInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid role"})
		return
	}
	role, err := enterpriseCreateOrUpdateRole(tenant.Organization.ID, 0, input)
	if err != nil {
		enterpriseWriteRoleError(c, err)
		return
	}
	enterpriseAuditCurrent(c, "role_created", fmt.Sprintf("创建权限组“%s”", role.Name), fmt.Sprintf(`{"role_id":%d}`, role.ID))
	c.JSON(http.StatusCreated, role)
}

func (api *EnterpriseAPI) UpdateRole(c *gin.Context) {
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	roleID, err := parseEnterpriseID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var input enterpriseRoleInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid role"})
		return
	}
	role, err := enterpriseCreateOrUpdateRole(tenant.Organization.ID, roleID, input)
	if err != nil {
		enterpriseWriteRoleError(c, err)
		return
	}
	enterpriseAuditCurrent(c, "role_updated", fmt.Sprintf("更新权限组“%s”", role.Name), fmt.Sprintf(`{"role_id":%d}`, role.ID))
	c.JSON(http.StatusOK, role)
}

func (api *EnterpriseAPI) ListRoleBindings(c *gin.Context) {
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	var bindings []model.RoleBinding
	if err := model.DB.Preload("Role").Preload("User").Joins("JOIN users ON users.id = role_bindings.user_id").
		Where("role_bindings.organization_id = ? AND users.username NOT LIKE ? AND users.email NOT LIKE ?", tenant.Organization.ID, "enterprise-pool-%", "%@internal.invalid").
		Order("role_bindings.id ASC").Find(&bindings).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list role bindings"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"bindings": bindings})
}

func (api *EnterpriseAPI) CreateRoleBinding(c *gin.Context) {
	user, ok := enterpriseCurrentUser(c)
	if !ok {
		return
	}
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	var input enterpriseBindingInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid role binding"})
		return
	}
	input.ScopeType = model.NormalizeRoleBindingScope(input.ScopeType)
	if (input.UserID == 0) == (input.DepartmentID == 0) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Select exactly one employee or department"})
		return
	}
	if input.DepartmentID != 0 {
		input.ScopeType, input.ScopeID = model.RoleBindingScopeOrganization, tenant.Organization.ID
	}
	if !model.RoleBindingScopeValid(input.ScopeType, input.ScopeID, tenant.Organization.ID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid role binding scope"})
		return
	}
	if input.UserID != 0 && !enterpriseActiveMember(tenant.Organization.ID, input.UserID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Active organization member not found"})
		return
	}
	var role model.Role
	if err := model.DB.Where("id = ? AND organization_id = ?", input.RoleID, tenant.Organization.ID).First(&role).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Role not found"})
		return
	}
	if input.ScopeType == model.RoleBindingScopeWorkspace {
		var workspace model.Workspace
		if err := model.DB.Where("id = ? AND organization_id = ?", input.ScopeID, tenant.Organization.ID).First(&workspace).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Workspace not found"})
			return
		}
	}
	if input.DepartmentID != 0 {
		var department model.Department
		if err := model.DB.Where("id = ? AND organization_id = ?", input.DepartmentID, tenant.Organization.ID).First(&department).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Department not found"})
			return
		}
		binding := model.DepartmentRoleBinding{OrganizationID: tenant.Organization.ID, DepartmentID: input.DepartmentID, RoleID: input.RoleID, CreatedByUserID: user.ID}
		if err := model.DB.Where("organization_id = ? AND department_id = ? AND role_id = ?", binding.OrganizationID, binding.DepartmentID, binding.RoleID).FirstOrCreate(&binding).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to grant department role"})
			return
		}
		enterpriseAudit(c, user.ID, "role_granted", fmt.Sprintf("向部门“%s”授予权限组“%s”", department.Name, role.Name), fmt.Sprintf(`{"department_id":%d,"role_id":%d}`, department.ID, role.ID))
		c.JSON(http.StatusCreated, binding)
		return
	}
	binding := model.RoleBinding{OrganizationID: tenant.Organization.ID, UserID: input.UserID, RoleID: input.RoleID, ScopeType: input.ScopeType, ScopeID: input.ScopeID, CreatedByUserID: user.ID}
	if err := model.DB.Where("organization_id = ? AND user_id = ? AND role_id = ? AND scope_type = ? AND scope_id = ?", binding.OrganizationID, binding.UserID, binding.RoleID, binding.ScopeType, binding.ScopeID).FirstOrCreate(&binding).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to grant role"})
		return
	}
	enterpriseAudit(c, user.ID, "role_granted", fmt.Sprintf("向员工 #%d 授予权限组“%s”", binding.UserID, role.Name), fmt.Sprintf(`{"user_id":%d,"role_id":%d}`, binding.UserID, role.ID))
	c.JSON(http.StatusCreated, binding)
}

func (api *EnterpriseAPI) DeleteRoleBinding(c *gin.Context) {
	actor, _ := enterpriseCurrentUser(c)
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	id, err := parseEnterpriseID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	result := model.DB.Where("id = ? AND organization_id = ?", id, tenant.Organization.ID).Delete(&model.RoleBinding{})
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to revoke role"})
		return
	}
	if result.RowsAffected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "Role binding not found"})
		return
	}
	enterpriseAudit(c, actor.ID, "role_revoked", fmt.Sprintf("撤销权限授予 #%d", id), fmt.Sprintf(`{"binding_id":%d}`, id))
	c.JSON(http.StatusOK, gin.H{"message": "Role revoked"})
}

func (api *EnterpriseAPI) ListTasks(c *gin.Context) {
	user, ok := enterpriseCurrentUser(c)
	if !ok {
		return
	}
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	var tasks []model.EnterpriseTask
	query := model.DB.Preload("Owner").Where("organization_id = ?", tenant.Organization.ID).
		Where("created_by_user_id = ? OR owner_user_id = ? OR id IN (?)", user.ID, user.ID, model.DB.Model(&model.EnterpriseTaskAssignment{}).Select("task_id").Where("user_id = ?", user.ID)).
		Order("updated_at DESC")
	if err := query.Find(&tasks).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list tasks"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"tasks": tasks})
}
func (api *EnterpriseAPI) ListManagedTasks(c *gin.Context) {
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	var tasks []model.EnterpriseTask
	if err := model.DB.Preload("Owner").Where("organization_id = ?", tenant.Organization.ID).Order("updated_at DESC").Find(&tasks).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list tasks"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"tasks": tasks})
}

func (api *EnterpriseAPI) CreateTask(c *gin.Context) {
	user, ok := enterpriseCurrentUser(c)
	if !ok {
		return
	}
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	var input enterpriseTaskInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid task"})
		return
	}
	input.Title, input.Description = strings.TrimSpace(input.Title), strings.TrimSpace(input.Description)
	if input.Title == "" || len([]rune(input.Title)) > 200 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Task title is required and must not exceed 200 characters"})
		return
	}
	ownerID := user.ID
	if input.OwnerUserID != nil {
		ownerID = *input.OwnerUserID
	}
	if !enterpriseActiveMember(tenant.Organization.ID, ownerID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Task owner must be an active organization member"})
		return
	}
	if input.DepartmentID != nil {
		var department model.Department
		if err := model.DB.Where("id = ? AND organization_id = ?", *input.DepartmentID, tenant.Organization.ID).First(&department).Error; err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Department not found"})
			return
		}
	}
	if input.Priority < 0 || input.Priority > 5 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Task priority must be between 0 and 5"})
		return
	}
	task := model.EnterpriseTask{OrganizationID: tenant.Organization.ID, DepartmentID: input.DepartmentID, CreatedByUserID: user.ID, OwnerUserID: ownerID, Title: input.Title, Description: input.Description, Priority: input.Priority, DueAt: input.DueAt, Status: model.EnterpriseTaskStatusAssigned}
	if tenant.Workspace != nil {
		task.WorkspaceID = &tenant.Workspace.ID
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&task).Error; err != nil {
			return err
		}
		assignments := []model.EnterpriseTaskAssignment{{TaskID: task.ID, UserID: ownerID, Role: model.EnterpriseTaskAssignmentOwner, AssignedBy: user.ID}}
		for _, assigneeID := range uniqueEnterpriseUserIDs(input.AssigneeUserIDs) {
			if assigneeID != ownerID && enterpriseActiveMemberWithDB(tx, tenant.Organization.ID, assigneeID) {
				assignments = append(assignments, model.EnterpriseTaskAssignment{TaskID: task.ID, UserID: assigneeID, Role: model.EnterpriseTaskAssignmentAssignee, AssignedBy: user.ID})
			}
		}
		for _, participantID := range uniqueEnterpriseUserIDs(input.ParticipantUserIDs) {
			if participantID != ownerID && enterpriseActiveMemberWithDB(tx, tenant.Organization.ID, participantID) {
				assignments = append(assignments, model.EnterpriseTaskAssignment{TaskID: task.ID, UserID: participantID, Role: model.EnterpriseTaskAssignmentParticipant, AssignedBy: user.ID})
			}
		}
		if err := tx.Create(&assignments).Error; err != nil {
			return err
		}
		if task.DepartmentID != nil {
			if err := tx.Where("organization_id = ? AND task_id = ? AND department_id = ?", task.OrganizationID, task.ID, *task.DepartmentID).FirstOrCreate(&model.EnterpriseTaskDepartment{OrganizationID: task.OrganizationID, TaskID: task.ID, DepartmentID: *task.DepartmentID, AddedBy: user.ID}).Error; err != nil {
				return err
			}
		}
		return ensureEnterpriseSharedPool(tx, tenant.Organization.ID, model.EnterprisePoolScopeTask, nil, &task.ID, task.Title, user.ID)
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create task"})
		return
	}
	c.JSON(http.StatusCreated, task)
	enterpriseAudit(c, user.ID, "task_created", fmt.Sprintf("创建任务“%s”（优先级 P%d）", task.Title, task.Priority), fmt.Sprintf(`{"task_id":%d}`, task.ID))
}

func (api *EnterpriseAPI) ListSharedPools(c *gin.Context) {
	user, ok := enterpriseCurrentUser(c)
	if !ok {
		return
	}
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	var pools []model.EnterpriseSharedPool
	if err := model.DB.Where("organization_id = ?", tenant.Organization.ID).Order("updated_at DESC").Find(&pools).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list shared pools"})
		return
	}
	items := make([]model.EnterpriseSharedPool, 0, len(pools))
	for _, pool := range pools {
		if enterpriseCanAccessSharedPool(user, tenant.Organization.ID, pool) {
			items = append(items, pool)
		}
	}
	c.JSON(http.StatusOK, gin.H{"pools": items})
}
func (api *EnterpriseAPI) CreateSharedPool(c *gin.Context) {
	user, ok := enterpriseCurrentUser(c)
	if !ok {
		return
	}
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	var input enterpriseSharedPoolInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid shared pool"})
		return
	}
	if strings.EqualFold(strings.TrimSpace(input.ScopeType), model.EnterprisePoolScopeTask) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Task pools are created automatically with their task"})
		return
	}
	pool, err := enterpriseSharedPoolFromInput(tenant.Organization.ID, input, user.ID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("organization_id = ? AND scope_type = ? AND scope_key = ?", pool.OrganizationID, pool.ScopeType, pool.ScopeKey).FirstOrCreate(&pool).Error; err != nil {
			return err
		}
		return ensureEnterpriseSharedPoolResources(tx, &pool)
	}); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "Failed to create shared pool"})
		return
	}
	enterpriseAudit(c, user.ID, "shared_pool_created", fmt.Sprintf("创建共享资源池“%s”", pool.Name), fmt.Sprintf(`{"pool_id":%d,"scope_type":"%s"}`, pool.ID, pool.ScopeType))
	c.JSON(http.StatusCreated, pool)
}
func (api *EnterpriseAPI) ListSharedPoolSessions(c *gin.Context) {
	pool, ok := enterpriseSharedPoolAccess(c)
	if !ok {
		return
	}
	var rows []model.EnterpriseSharedSession
	if err := model.DB.Where("pool_id = ?", pool.ID).Order("created_at DESC").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list shared sessions"})
		return
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.SessionID)
	}
	var sessions []service.AdvancedChatSession
	if len(ids) > 0 {
		if err := model.DB.Where("id IN ?", ids).Order("updated_at DESC").Find(&sessions).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load shared sessions"})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"sessions": sessions})
}

// CreateSharedPoolSession creates a session whose owner is the pool resource
// principal. It deliberately does not create a personal source session: a
// "new session here" action must not leave an uncategorized duplicate behind.
func (api *EnterpriseAPI) CreateSharedPoolSession(c *gin.Context) {
	user, ok := enterpriseCurrentUser(c)
	if !ok {
		return
	}
	pool, ok := enterpriseSharedPoolAccess(c)
	if !ok {
		return
	}
	var input enterpriseSharedPoolSessionInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid shared session"})
		return
	}
	var session service.AdvancedChatSession
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := ensureEnterpriseSharedPoolResources(tx, &pool); err != nil {
			return err
		}
		session = service.AdvancedChatSession{
			ID:                       enterprisePoolResourceID("session"),
			UserID:                   pool.ResourceUserID,
			Title:                    "New session",
			RunMode:                  "chat",
			AgentID:                  strings.TrimSpace(input.AgentID),
			SkillIDs:                 "[]",
			MCPServerIDs:             "[]",
			ConnectorApprovalMode:    "manual",
			ConnectorCommandPrefixes: "[]",
			ModelName:                strings.TrimSpace(input.ModelName),
			UserChannelID:            input.UserChannelID,
		}
		if err := tx.Create(&session).Error; err != nil {
			return err
		}
		return tx.Create(&model.EnterpriseSharedSession{PoolID: pool.ID, SessionID: session.ID, SharedBy: user.ID}).Error
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create shared session"})
		return
	}
	enterpriseAudit(c, user.ID, "pool_session_created", "", fmt.Sprintf(`{"pool_id":%d,"session_id":"%s"}`, pool.ID, session.ID))
	c.JSON(http.StatusCreated, gin.H{"session": session})
}
func (api *EnterpriseAPI) ListSharedPoolDevices(c *gin.Context) {
	pool, ok := enterpriseSharedPoolAccess(c)
	if !ok {
		return
	}
	assignments := model.DB.Where("organization_id = ? AND status = ? AND (expires_at IS NULL OR expires_at > ?)", pool.OrganizationID, model.EnterpriseDeviceAssignmentActive, time.Now())
	if pool.ScopeType == model.EnterprisePoolScopeTask && pool.TaskID != nil {
		assignments = assignments.Where("scope_type = ? AND task_id = ?", model.EnterpriseDeviceAssignmentTask, *pool.TaskID)
	} else if pool.ScopeType == model.EnterprisePoolScopeDepartment && pool.DepartmentID != nil {
		assignments = assignments.Where("scope_type = ? AND department_id = ?", model.EnterpriseDeviceAssignmentDepartment, *pool.DepartmentID)
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid shared pool scope"})
		return
	}
	var rows []model.EnterpriseDeviceAssignment
	if err := assignments.Order("created_at DESC").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list shared pool devices"})
		return
	}
	deviceIDs := make([]uint, 0, len(rows))
	for _, row := range rows {
		deviceIDs = append(deviceIDs, row.DeviceID)
	}
	var devices []model.EnterpriseDevice
	if len(deviceIDs) > 0 {
		if err := model.DB.Where("organization_id = ? AND id IN ? AND status = ?", pool.OrganizationID, deviceIDs, model.EnterpriseDeviceStatusActive).Find(&devices).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load shared pool devices"})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"devices": devices})
}
func (api *EnterpriseAPI) GetSharedPoolSession(c *gin.Context) {
	pool, ok := enterpriseSharedPoolAccess(c)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(c.Param("session_id"))
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Session id is required"})
		return
	}
	var binding model.EnterpriseSharedSession
	if err := model.DB.Where("pool_id = ? AND session_id = ?", pool.ID, sessionID).First(&binding).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Shared session not found"})
		return
	}
	var session service.AdvancedChatSession
	if err := model.DB.Where("id = ?", sessionID).First(&session).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Session not found"})
		return
	}
	var messages []service.AdvancedChatMessage
	if err := model.DB.Where("session_id = ?", session.ID).Order("sort_order ASC, created_at ASC").Find(&messages).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load shared session"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"session": session, "messages": messages})
}
func (api *EnterpriseAPI) AppendSharedPoolSessionMessage(c *gin.Context) {
	user, ok := enterpriseCurrentUser(c)
	if !ok {
		return
	}
	pool, ok := enterpriseSharedPoolAccess(c)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(c.Param("session_id"))
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Session id is required"})
		return
	}
	var input enterpriseSharedSessionMessageInput
	if err := c.ShouldBindJSON(&input); err != nil || strings.TrimSpace(input.Content) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Message content is required"})
		return
	}
	if len([]rune(input.Content)) > 200000 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Message is too long"})
		return
	}
	var session service.AdvancedChatSession
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		var binding model.EnterpriseSharedSession
		if err := tx.Where("pool_id = ? AND session_id = ?", pool.ID, sessionID).First(&binding).Error; err != nil {
			return err
		}
		if err := tx.Where("id = ?", sessionID).First(&session).Error; err != nil {
			return err
		}
		var last service.AdvancedChatMessage
		nextOrder := 0
		if err := tx.Where("session_id = ?", sessionID).Order("sort_order DESC, created_at DESC").First(&last).Error; err == nil {
			nextOrder = last.SortOrder + 1
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		message := service.AdvancedChatMessage{
			ID:           "shared-" + strconv.FormatInt(time.Now().UnixNano(), 36),
			SessionID:    sessionID,
			UserID:       user.ID,
			Role:         "user",
			Content:      strings.TrimSpace(input.Content),
			ContentParts: "[]",
			ToolCalls:    "[]",
			SortOrder:    nextOrder,
		}
		if err := tx.Create(&message).Error; err != nil {
			return err
		}
		if session.Title == "New session" || session.Title == "Assistant session" {
			title := strings.TrimSpace(input.Title)
			if title != "" {
				if len([]rune(title)) > 200 {
					title = string([]rune(title)[:200])
				}
				session.Title = title
			}
		}
		return tx.Model(&session).Updates(map[string]interface{}{"title": session.Title, "updated_at": time.Now()}).Error
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "Shared session not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to append shared message"})
		return
	}
	var messages []service.AdvancedChatMessage
	if err := model.DB.Where("session_id = ?", session.ID).Order("sort_order ASC, created_at ASC").Find(&messages).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load shared session"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"session": session, "messages": messages})
}
func (api *EnterpriseAPI) ShareSessionToPool(c *gin.Context) {
	user, ok := enterpriseCurrentUser(c)
	if !ok {
		return
	}
	pool, ok := enterpriseSharedPoolAccess(c)
	if !ok {
		return
	}
	var input enterprisePoolResourceInput
	if err := c.ShouldBindJSON(&input); err != nil || strings.TrimSpace(input.ID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Session id is required"})
		return
	}
	var session service.AdvancedChatSession
	if err := model.DB.Where("id = ? AND user_id = ?", strings.TrimSpace(input.ID), user.ID).First(&session).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Personal session not found"})
		return
	}
	var entry model.EnterpriseSharedSession
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := ensureEnterpriseSharedPoolResources(tx, &pool); err != nil {
			return err
		}
		var existing model.EnterpriseSharedSession
		if err := tx.Where("pool_id = ? AND source_session_id = ?", pool.ID, session.ID).First(&existing).Error; err == nil {
			entry = existing
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		cloneID := enterprisePoolResourceID("session")
		clone := session
		clone.ID = cloneID
		clone.UserID = pool.ResourceUserID
		clone.FolderID = ""
		if err := tx.Create(&clone).Error; err != nil {
			return err
		}
		var messages []service.AdvancedChatMessage
		if err := tx.Where("session_id = ?", session.ID).Order("sort_order ASC, created_at ASC").Find(&messages).Error; err != nil {
			return err
		}
		for index := range messages {
			messages[index].ID = fmt.Sprintf("%s-%d", enterprisePoolResourceID("message"), index)
			messages[index].SessionID = cloneID
			messages[index].SortOrder = index
		}
		if len(messages) > 0 {
			if err := tx.Create(&messages).Error; err != nil {
				return err
			}
		}
		entry = model.EnterpriseSharedSession{PoolID: pool.ID, SessionID: cloneID, SourceSessionID: session.ID, SharedBy: user.ID}
		return tx.Create(&entry).Error
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to share session"})
		return
	}
	enterpriseAudit(c, user.ID, "pool_session_shared", fmt.Sprintf("将会话“%s”共享到资源池“%s”", session.Title, pool.Name), fmt.Sprintf(`{"pool_id":%d,"session_id":"%s"}`, pool.ID, entry.SessionID))
	c.JSON(http.StatusCreated, entry)
}
func (api *EnterpriseAPI) ListSharedPoolFiles(c *gin.Context) {
	pool, ok := enterpriseSharedPoolAccess(c)
	if !ok {
		return
	}
	var rows []model.EnterpriseSharedFile
	if err := model.DB.Where("pool_id = ?", pool.ID).Order("created_at DESC").Find(&rows).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list shared files"})
		return
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.FileID)
	}
	var files []service.AdvancedChatFile
	if len(ids) > 0 {
		if err := model.DB.Where("id IN ?", ids).Order("created_at DESC").Find(&files).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load shared files"})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"files": files})
}
func (api *EnterpriseAPI) ShareFileToPool(c *gin.Context) {
	user, ok := enterpriseCurrentUser(c)
	if !ok {
		return
	}
	pool, ok := enterpriseSharedPoolAccess(c)
	if !ok {
		return
	}
	var input enterprisePoolResourceInput
	if err := c.ShouldBindJSON(&input); err != nil || strings.TrimSpace(input.ID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "File id is required"})
		return
	}
	var file service.AdvancedChatFile
	if err := model.DB.Where("id = ? AND user_id = ?", strings.TrimSpace(input.ID), user.ID).First(&file).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Personal file not found"})
		return
	}
	var entry model.EnterpriseSharedFile
	if err := model.DB.Transaction(func(tx *gorm.DB) error { return ensureEnterpriseSharedPoolResources(tx, &pool) }); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to share file"})
		return
	}
	if err := model.DB.Where("pool_id = ? AND source_file_id = ?", pool.ID, file.ID).First(&entry).Error; err == nil {
		c.JSON(http.StatusCreated, entry)
		return
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to share file"})
		return
	}
	data, err := service.ReadAdvancedChatFileData(file)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read personal file"})
		return
	}
	clone, _, _, err := service.StoreAdvancedChatPoolFile(pool.ResourceUserID, file.Name, file.MIMEType, data)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create pool file"})
		return
	}
	entry = model.EnterpriseSharedFile{PoolID: pool.ID, FileID: clone.ID, SourceFileID: file.ID, SharedBy: user.ID}
	if err := model.DB.Create(&entry).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to share file"})
		return
	}
	enterpriseAudit(c, user.ID, "pool_file_shared", fmt.Sprintf("将文件“%s”共享到资源池“%s”", file.Name, pool.Name), fmt.Sprintf(`{"pool_id":%d,"file_id":"%s"}`, pool.ID, clone.ID))
	c.JSON(http.StatusCreated, entry)
}
func (api *EnterpriseAPI) DownloadSharedPoolFile(c *gin.Context) {
	pool, ok := enterpriseSharedPoolAccess(c)
	if !ok {
		return
	}
	fileID := strings.TrimSpace(c.Param("file_id"))
	if fileID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "File id is required"})
		return
	}
	var binding model.EnterpriseSharedFile
	if err := model.DB.Where("pool_id = ? AND file_id = ?", pool.ID, fileID).First(&binding).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Shared file not found"})
		return
	}
	var file service.AdvancedChatFile
	if err := model.DB.Where("id = ?", fileID).First(&file).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "File not found"})
		return
	}
	data, err := service.ReadAdvancedChatFileData(file)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read shared file"})
		return
	}
	name := strings.ReplaceAll(file.Name, `"`, "")
	if name == "" {
		name = "file"
	}
	c.Header("Content-Disposition", `attachment; filename="`+name+`"`)
	c.Data(http.StatusOK, file.MIMEType, data)
}

func (api *EnterpriseAPI) GetSharedPoolFileContent(c *gin.Context) {
	pool, ok := enterpriseSharedPoolAccess(c)
	if !ok {
		return
	}
	fileID := strings.TrimSpace(c.Param("file_id"))
	if fileID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "File id is required"})
		return
	}
	var binding model.EnterpriseSharedFile
	if err := model.DB.Where("pool_id = ? AND file_id = ?", pool.ID, fileID).First(&binding).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Shared file not found"})
		return
	}
	var file service.AdvancedChatFile
	if err := model.DB.Where("id = ?", fileID).First(&file).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "File not found"})
		return
	}
	textRunes := []rune(file.TextExtract)
	truncated := false
	if len(textRunes) > 20000 {
		textRunes = textRunes[:20000]
		truncated = true
	}
	c.JSON(http.StatusOK, gin.H{
		"id":        file.ID,
		"text":      string(textRunes),
		"binary":    strings.TrimSpace(file.TextExtract) == "",
		"truncated": truncated || len([]rune(file.TextExtract)) >= 100000,
	})
}

func (api *EnterpriseAPI) UpdateTaskStatus(c *gin.Context) {
	user, ok := enterpriseCurrentUser(c)
	if !ok {
		return
	}
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	id, err := parseEnterpriseID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var input enterpriseTaskStatusInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid task status"})
		return
	}
	status := model.NormalizeEnterpriseTaskStatus(input.Status)
	var task model.EnterpriseTask
	if err := model.DB.Where("id = ? AND organization_id = ?", id, tenant.Organization.ID).First(&task).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}
	if task.OwnerUserID != user.ID && task.CreatedByUserID != user.ID && !enterpriseTaskAssignedTo(task.ID, user.ID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Task access denied"})
		return
	}
	if status == model.EnterpriseTaskStatusCompleted && task.OwnerUserID != user.ID && !user.IsAdmin {
		status = model.EnterpriseTaskStatusReview
	}
	if (status == model.EnterpriseTaskStatusCompleted || status == model.EnterpriseTaskStatusCancelled) && task.OwnerUserID != user.ID && !user.IsAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "Only the task leader can confirm completion or cancellation"})
		return
	}
	updates := map[string]interface{}{"status": status}
	if status == model.EnterpriseTaskStatusRunning {
		updates["started_at"] = time.Now()
	}
	if status == model.EnterpriseTaskStatusCompleted || status == model.EnterpriseTaskStatusCancelled {
		updates["completed_at"] = time.Now()
	}
	if err := model.DB.Model(&task).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update task"})
		return
	}
	model.DB.First(&task, task.ID)
	enterpriseAudit(c, user.ID, "task_status_changed", fmt.Sprintf("将任务“%s”状态更新为 %s", task.Title, status), fmt.Sprintf(`{"task_id":%d,"status":"%s"}`, task.ID, status))
	c.JSON(http.StatusOK, task)
}

func (api *EnterpriseAPI) GetTaskDetail(c *gin.Context) {
	user, ok := enterpriseCurrentUser(c)
	if !ok {
		return
	}
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	id, err := parseEnterpriseID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var task model.EnterpriseTask
	if err := model.DB.Where("id = ? AND organization_id = ?", id, tenant.Organization.ID).First(&task).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}
	if task.OwnerUserID != user.ID && task.CreatedByUserID != user.ID && !enterpriseTaskAssignedTo(task.ID, user.ID) && !user.IsAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "Task access denied"})
		return
	}
	api.writeTaskDetail(c, task)
}

func (api *EnterpriseAPI) GetManagedTaskDetail(c *gin.Context) {
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	id, err := parseEnterpriseID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var task model.EnterpriseTask
	if err := model.DB.Where("id = ? AND organization_id = ?", id, tenant.Organization.ID).First(&task).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}
	api.writeTaskDetail(c, task)
}

func (api *EnterpriseAPI) writeTaskDetail(c *gin.Context, task model.EnterpriseTask) {
	var assignments []model.EnterpriseTaskAssignment
	var departments []model.EnterpriseTaskDepartment
	var devices []model.EnterpriseDeviceAssignment
	var pool model.EnterpriseSharedPool
	var quota model.QuotaAccount
	model.DB.Preload("User").Where("task_id = ?", task.ID).Order("role ASC, id ASC").Find(&assignments)
	model.DB.Preload("Department").Where("task_id = ?", task.ID).Find(&departments)
	model.DB.Where("task_id = ? AND status = ?", task.ID, model.EnterpriseDeviceAssignmentActive).Find(&devices)
	model.DB.Where("organization_id = ? AND scope_type = ? AND task_id = ?", task.OrganizationID, model.EnterprisePoolScopeTask, task.ID).First(&pool)
	if pool.ID != 0 {
		model.DB.Where("organization_id = ? AND pool_id = ?", task.OrganizationID, pool.ID).First(&quota)
	}
	c.JSON(http.StatusOK, gin.H{"task": task, "assignments": assignments, "departments": departments, "device_assignments": devices, "pool": pool, "quota_account": quota})
}

func (api *EnterpriseAPI) AddTaskParticipant(c *gin.Context) {
	user, ok := enterpriseCurrentUser(c)
	if !ok {
		return
	}
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	taskID, err := parseEnterpriseID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var input enterpriseTaskParticipantInput
	if err := c.ShouldBindJSON(&input); err != nil || input.UserID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Participant is required"})
		return
	}
	if !enterpriseActiveMember(tenant.Organization.ID, input.UserID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Participant must be an active organization member"})
		return
	}
	role := strings.ToLower(strings.TrimSpace(input.Role))
	if role != model.EnterpriseTaskAssignmentAssignee {
		role = model.EnterpriseTaskAssignmentParticipant
	}
	var task model.EnterpriseTask
	if err := model.DB.Where("id = ? AND organization_id = ?", taskID, tenant.Organization.ID).First(&task).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}
	assignment := model.EnterpriseTaskAssignment{TaskID: task.ID, UserID: input.UserID, Role: role, AssignedBy: user.ID}
	if err := model.DB.Where("task_id = ? AND user_id = ? AND role = ?", task.ID, input.UserID, role).FirstOrCreate(&assignment).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to add participant"})
		return
	}
	var participant model.User
	model.DB.First(&participant, input.UserID)
	enterpriseAudit(c, user.ID, "task_participant_added", fmt.Sprintf("将员工“%s”加入任务“%s”", participant.Username, task.Title), fmt.Sprintf(`{"task_id":%d,"user_id":%d,"role":"%s"}`, task.ID, input.UserID, role))
	c.JSON(http.StatusCreated, assignment)
}

func (api *EnterpriseAPI) DeleteTaskParticipant(c *gin.Context) {
	actor, _ := enterpriseCurrentUser(c)
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	taskID, err := parseEnterpriseID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	userID, err := parseEnterpriseID(c.Param("user_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var task model.EnterpriseTask
	if err := model.DB.Where("id = ? AND organization_id = ?", taskID, tenant.Organization.ID).First(&task).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}
	if task.OwnerUserID == userID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Task leader cannot be removed"})
		return
	}
	result := model.DB.Where("task_id = ? AND user_id = ?", task.ID, userID).Delete(&model.EnterpriseTaskAssignment{})
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to remove participant"})
		return
	}
	var participant model.User
	model.DB.First(&participant, userID)
	enterpriseAudit(c, actor.ID, "task_participant_removed", fmt.Sprintf("将员工“%s”移出任务“%s”", participant.Username, task.Title), fmt.Sprintf(`{"task_id":%d,"user_id":%d}`, task.ID, userID))
	c.JSON(http.StatusOK, gin.H{"message": "Participant removed"})
}

func (api *EnterpriseAPI) AddTaskDepartment(c *gin.Context) {
	user, ok := enterpriseCurrentUser(c)
	if !ok {
		return
	}
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	taskID, err := parseEnterpriseID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var input enterpriseTaskDepartmentInput
	if err := c.ShouldBindJSON(&input); err != nil || input.DepartmentID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Department is required"})
		return
	}
	var department model.Department
	if err := model.DB.Where("id = ? AND organization_id = ?", input.DepartmentID, tenant.Organization.ID).First(&department).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Department not found"})
		return
	}
	var task model.EnterpriseTask
	if err := model.DB.Where("id = ? AND organization_id = ?", taskID, tenant.Organization.ID).First(&task).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Task not found"})
		return
	}
	item := model.EnterpriseTaskDepartment{OrganizationID: tenant.Organization.ID, TaskID: task.ID, DepartmentID: input.DepartmentID, AddedBy: user.ID}
	if err := model.DB.Where("organization_id = ? AND task_id = ? AND department_id = ?", tenant.Organization.ID, task.ID, input.DepartmentID).FirstOrCreate(&item).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to add department"})
		return
	}
	enterpriseAudit(c, user.ID, "task_department_added", fmt.Sprintf("将部门“%s”加入任务“%s”", department.Name, task.Title), fmt.Sprintf(`{"task_id":%d,"department_id":%d}`, task.ID, department.ID))
	c.JSON(http.StatusCreated, item)
}

func (api *EnterpriseAPI) DeleteTaskDepartment(c *gin.Context) {
	actor, _ := enterpriseCurrentUser(c)
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	taskID, err := parseEnterpriseID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	departmentID, err := parseEnterpriseID(c.Param("department_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	result := model.DB.Where("organization_id = ? AND task_id = ? AND department_id = ?", tenant.Organization.ID, taskID, departmentID).Delete(&model.EnterpriseTaskDepartment{})
	if result.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to remove department"})
		return
	}
	var department model.Department
	var task model.EnterpriseTask
	model.DB.First(&department, departmentID)
	model.DB.First(&task, taskID)
	enterpriseAudit(c, actor.ID, "task_department_removed", fmt.Sprintf("将部门“%s”移出任务“%s”", department.Name, task.Title), fmt.Sprintf(`{"task_id":%d,"department_id":%d}`, taskID, departmentID))
	c.JSON(http.StatusOK, gin.H{"message": "Department removed"})
}

func (api *EnterpriseAPI) ListDevices(c *gin.Context) {
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	var devices []model.EnterpriseDevice
	if err := model.DB.Where("organization_id = ?", tenant.Organization.ID).Order("created_at DESC").Find(&devices).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list devices"})
		return
	}
	connectorIDs := make([]string, 0, len(devices))
	for _, device := range devices {
		if device.Kind == "connector" && device.ExternalDeviceID != "" {
			connectorIDs = append(connectorIDs, device.ExternalDeviceID)
		}
	}
	connectors := map[string]service.AdvancedChatConnectorDevice{}
	if len(connectorIDs) > 0 {
		var records []service.AdvancedChatConnectorDevice
		if err := model.DB.Where("id IN ?", connectorIDs).Find(&records).Error; err == nil {
			for _, item := range records {
				connectors[item.ID] = item
			}
		}
	}
	items := make([]gin.H, 0, len(devices))
	for _, device := range devices {
		item := gin.H{"id": device.ID, "external_device_id": device.ExternalDeviceID, "name": device.Name, "kind": device.Kind, "owner_user_id": device.OwnerUserID, "managed_by_enterprise": device.ManagedByEnterprise, "status": device.Status}
		if connector, exists := connectors[device.ExternalDeviceID]; exists {
			item["connector_status"] = connector.Status
			item["online"] = connector.Status == "online" && connector.LastSeenAt != nil && connector.LastSeenAt.After(time.Now().Add(-90*time.Second))
			item["hostname"] = connector.Hostname
			item["os"] = connector.OS
			item["last_seen_at"] = connector.LastSeenAt
		}
		items = append(items, item)
	}
	c.JSON(http.StatusOK, gin.H{"devices": items})
}
func (api *EnterpriseAPI) CreateConnectorCommand(c *gin.Context) {
	user, ok := enterpriseCurrentUser(c)
	if !ok {
		return
	}
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	var input enterpriseConnectorTokenInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid connector"})
		return
	}
	ownerID := user.ID
	if input.OwnerUserID != nil {
		ownerID = *input.OwnerUserID
	}
	if !enterpriseActiveMember(tenant.Organization.ID, ownerID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Connector owner must be an active organization member"})
		return
	}
	var connector service.AdvancedChatConnectorDevice
	var token string
	var device model.EnterpriseDevice
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		var createErr error
		connector, token, createErr = service.CreateEnterpriseConnectorToken(tx, ownerID, input.Name, input.Mode, input.ListenPort)
		if createErr != nil {
			return createErr
		}
		device = model.EnterpriseDevice{OrganizationID: tenant.Organization.ID, ExternalDeviceID: connector.ID, Name: connector.Name, Kind: "connector", OwnerUserID: &ownerID, ManagedByEnterprise: true, Status: model.EnterpriseDeviceStatusActive}
		return tx.Create(&device).Error
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create enterprise connector"})
		return
	}
	enterpriseAudit(c, user.ID, "connector_command_created", fmt.Sprintf("生成设备“%s”的连接命令", device.Name), fmt.Sprintf(`{"device_id":%d,"owner_user_id":%d}`, device.ID, ownerID))
	c.JSON(http.StatusCreated, gin.H{"token": token, "device": device, "mode": connector.Mode, "listen_port": connector.ListenPort})
}
func (api *EnterpriseAPI) RotateConnectorCommand(c *gin.Context) {
	user, _ := enterpriseCurrentUser(c)
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	id, err := parseEnterpriseID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var device model.EnterpriseDevice
	if err := model.DB.Where("id = ? AND organization_id = ? AND kind = ?", id, tenant.Organization.ID, "connector").First(&device).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Connector device not found"})
		return
	}
	connector, token, err := service.RotateEnterpriseConnectorToken(model.DB, device.ExternalDeviceID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to regenerate connector command"})
		return
	}
	enterpriseAudit(c, user.ID, "connector_command_rotated", fmt.Sprintf("重新生成设备“%s”的连接命令", device.Name), fmt.Sprintf(`{"device_id":%d}`, device.ID))
	c.JSON(http.StatusOK, gin.H{"token": token, "device": device, "mode": connector.Mode, "listen_port": connector.ListenPort})
}
func (api *EnterpriseAPI) CreateDevice(c *gin.Context) {
	user, ok := enterpriseCurrentUser(c)
	if !ok {
		return
	}
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	var input enterpriseDeviceInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid device"})
		return
	}
	input.ExternalDeviceID, input.Name, input.Kind = strings.TrimSpace(input.ExternalDeviceID), strings.TrimSpace(input.Name), strings.TrimSpace(input.Kind)
	if input.ExternalDeviceID == "" || input.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Device id and name are required"})
		return
	}
	if input.Kind == "" {
		input.Kind = "connector"
	}
	device := model.EnterpriseDevice{OrganizationID: tenant.Organization.ID, ExternalDeviceID: input.ExternalDeviceID, Name: input.Name, Kind: input.Kind, OwnerUserID: input.OwnerUserID, ManagedByEnterprise: input.ManagedByEnterprise, Status: model.EnterpriseDeviceStatusActive}
	if err := model.DB.Create(&device).Error; err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "Failed to create device"})
		return
	}
	enterpriseAudit(c, user.ID, "device_created", fmt.Sprintf("登记企业设备“%s”", device.Name), fmt.Sprintf(`{"device_id":%d}`, device.ID))
	c.JSON(http.StatusCreated, device)
}
func (api *EnterpriseAPI) UpdateDevice(c *gin.Context) {
	user, _ := enterpriseCurrentUser(c)
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	id, err := parseEnterpriseID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var input enterpriseDeviceInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid device"})
		return
	}
	var device model.EnterpriseDevice
	if err := model.DB.Where("id = ? AND organization_id = ?", id, tenant.Organization.ID).First(&device).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Device not found"})
		return
	}
	updates := map[string]interface{}{}
	if value := strings.TrimSpace(input.Name); value != "" {
		updates["name"] = value
	}
	if value := strings.TrimSpace(input.Kind); value != "" {
		updates["kind"] = value
	}
	if input.OwnerUserID != nil {
		if !enterpriseActiveMember(tenant.Organization.ID, *input.OwnerUserID) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Device owner must be an active organization member"})
			return
		}
		updates["owner_user_id"] = *input.OwnerUserID
	}
	updates["managed_by_enterprise"] = input.ManagedByEnterprise
	if err := model.DB.Model(&device).Updates(updates).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update device"})
		return
	}
	model.DB.First(&device, id)
	enterpriseAudit(c, user.ID, "device_updated", fmt.Sprintf("更新企业设备“%s”", device.Name), fmt.Sprintf(`{"device_id":%d}`, device.ID))
	c.JSON(http.StatusOK, device)
}
func (api *EnterpriseAPI) DeleteDevice(c *gin.Context) {
	user, ok := enterpriseCurrentUser(c)
	if !ok {
		return
	}
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	id, err := parseEnterpriseID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var device model.EnterpriseDevice
	if err := model.DB.Where("id = ? AND organization_id = ?", id, tenant.Organization.ID).First(&device).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Device not found"})
		return
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("organization_id = ? AND device_id = ?", tenant.Organization.ID, device.ID).Delete(&model.EnterpriseDeviceAssignment{}).Error; err != nil {
			return err
		}
		if device.Kind == "connector" && device.ExternalDeviceID != "" {
			if err := tx.Where("device_id = ?", device.ExternalDeviceID).Delete(&service.AdvancedChatConnectorTask{}).Error; err != nil {
				return err
			}
			if err := tx.Where("id = ?", device.ExternalDeviceID).Delete(&service.AdvancedChatConnectorDevice{}).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&device).Error
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to delete device"})
		return
	}
	enterpriseAudit(c, user.ID, "device_deleted", "", fmt.Sprintf(`{"device_id":%d}`, device.ID))
	c.JSON(http.StatusOK, gin.H{"message": "Device deleted"})
}
func (api *EnterpriseAPI) ListDeviceAssignments(c *gin.Context) {
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	var assignments []model.EnterpriseDeviceAssignment
	if err := model.DB.Preload("User").Where("organization_id = ?", tenant.Organization.ID).Order("created_at DESC").Find(&assignments).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list device assignments"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"assignments": assignments})
}
func (api *EnterpriseAPI) AssignDevice(c *gin.Context) {
	user, ok := enterpriseCurrentUser(c)
	if !ok {
		return
	}
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	var input enterpriseDeviceAssignmentInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid device assignment"})
		return
	}
	assignment, err := service.AssignEnterpriseDevice(model.DB, service.EnterpriseDeviceAssignmentInput{OrganizationID: tenant.Organization.ID, DeviceID: input.DeviceID, DepartmentID: input.DepartmentID, UserID: input.UserID, TaskID: input.TaskID, AllowedTools: input.AllowedTools, Classification: input.Classification, AssignedBy: user.ID, ExpiresAt: input.ExpiresAt})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	enterpriseAudit(c, user.ID, "device_assigned", fmt.Sprintf("分配设备 #%d", assignment.DeviceID), fmt.Sprintf(`{"assignment_id":%d,"device_id":%d,"scope_type":"%s"}`, assignment.ID, assignment.DeviceID, assignment.ScopeType))
	c.JSON(http.StatusCreated, assignment)
}
func (api *EnterpriseAPI) RevokeDeviceAssignment(c *gin.Context) {
	user, ok := enterpriseCurrentUser(c)
	if !ok {
		return
	}
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	id, err := parseEnterpriseID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := service.RevokeEnterpriseDeviceAssignment(model.DB, tenant.Organization.ID, id, user.ID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Device assignment not found or already revoked"})
		return
	}
	enterpriseAudit(c, user.ID, "device_assignment_revoked", fmt.Sprintf("撤销设备分配 #%d", id), fmt.Sprintf(`{"assignment_id":%d}`, id))
	c.JSON(http.StatusOK, gin.H{"message": "Device assignment revoked"})
}
func (api *EnterpriseAPI) ListQuotaAccounts(c *gin.Context) {
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	var accounts []model.QuotaAccount
	if err := model.DB.Where("organization_id = ?", tenant.Organization.ID).Order("scope_type ASC, id ASC").Find(&accounts).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list quota accounts"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"accounts": accounts})
}
func (api *EnterpriseAPI) CreateQuotaAccount(c *gin.Context) {
	user, ok := enterpriseCurrentUser(c)
	if !ok {
		return
	}
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	var input enterpriseQuotaAccountInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid quota account"})
		return
	}
	scope := service.EnterpriseQuotaScope{OrganizationID: tenant.Organization.ID, ScopeType: input.ScopeType, DepartmentID: input.DepartmentID, UserID: input.UserID, TaskID: input.TaskID, PoolID: input.PoolID}
	if err := validateEnterpriseQuotaScope(tenant.Organization.ID, scope); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	initialLimit := strings.TrimSpace(input.InitialLimit)
	amount := decimal.Zero
	if initialLimit != "" {
		var parseErr error
		amount, parseErr = decimal.NewFromString(initialLimit)
		if parseErr != nil || amount.LessThanOrEqual(decimal.Zero) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Initial limit must be a positive decimal"})
			return
		}
		if strings.ToLower(strings.TrimSpace(input.ScopeType)) != model.QuotaScopeOrganization {
			c.JSON(http.StatusBadRequest, gin.H{"error": "Only the organization account can receive an initial limit"})
			return
		}
	}
	account, err := service.EnsureEnterpriseQuotaAccount(model.DB, scope)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if initialLimit != "" {
		if err := model.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&model.QuotaAccount{}).
				Where("id = ?", account.ID).
				UpdateColumn("limit_amount", gorm.Expr("limit_amount + ?", amount)).Error; err != nil {
				return err
			}
			return tx.Create(&model.QuotaLedger{OrganizationID: tenant.Organization.ID, AccountID: account.ID, EntryType: model.QuotaLedgerAllocation, Amount: amount, ReferenceType: "initial_quota", CreatedByUserID: user.ID}).Error
		}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to initialize quota"})
			return
		}
		model.DB.First(&account, account.ID)
	}
	enterpriseAudit(c, user.ID, "quota_account_created", "登记组织预算账户", fmt.Sprintf(`{"account_id":%d,"scope_type":"%s","initial_limit":"%s"}`, account.ID, account.ScopeType, amount))
	c.JSON(http.StatusCreated, account)
}
func (api *EnterpriseAPI) AllocateQuota(c *gin.Context) {
	user, ok := enterpriseCurrentUser(c)
	if !ok {
		return
	}
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	var input enterpriseQuotaAllocationInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid quota allocation"})
		return
	}
	amount, err := decimal.NewFromString(strings.TrimSpace(input.Amount))
	if err != nil || amount.LessThanOrEqual(decimal.Zero) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Allocation amount must be a positive decimal"})
		return
	}
	var count int64
	model.DB.Model(&model.QuotaAccount{}).Where("organization_id = ? AND id IN ?", tenant.Organization.ID, []uint{input.ParentAccountID, input.ChildAccountID}).Count(&count)
	if count != 2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Quota accounts must belong to this organization"})
		return
	}
	var parent, child model.QuotaAccount
	model.DB.First(&parent, input.ParentAccountID)
	model.DB.First(&child, input.ChildAccountID)
	if parent.ScopeType != model.QuotaScopeOrganization || child.ScopeType != model.QuotaScopePool {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Only organization budget may be allocated to a pool"})
		return
	}
	if err := service.AllocateEnterpriseQuota(model.DB, input.ParentAccountID, input.ChildAccountID, user.ID, amount, strings.TrimSpace(input.ReferenceID)); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	enterpriseAudit(c, user.ID, "budget_allocated", fmt.Sprintf("分配组织预算 %s 到资源池", amount), fmt.Sprintf(`{"parent_account_id":%d,"child_account_id":%d,"amount":"%s"}`, input.ParentAccountID, input.ChildAccountID, amount))
	c.JSON(http.StatusOK, gin.H{"message": "Quota allocated"})
}

func (api *EnterpriseAPI) FundPoolFromPersonalBalance(c *gin.Context) {
	user, ok := enterpriseCurrentUser(c)
	if !ok {
		return
	}
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	var input enterprisePoolBudgetInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid pool funding"})
		return
	}
	amount, err := decimal.NewFromString(strings.TrimSpace(input.Amount))
	if err != nil || amount.LessThanOrEqual(decimal.Zero) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Amount must be a positive decimal"})
		return
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		var pool model.EnterpriseSharedPool
		if err := tx.Where("id = ? AND organization_id = ?", input.PoolID, tenant.Organization.ID).First(&pool).Error; err != nil {
			return err
		}
		if !enterpriseCanAccessSharedPool(user, tenant.Organization.ID, pool) {
			return errors.New("Pool access denied")
		}
		var account model.QuotaAccount
		if err := tx.Where("organization_id = ? AND pool_id = ?", tenant.Organization.ID, pool.ID).First(&account).Error; err != nil {
			return err
		}
		// Debit conditionally: reading the balance and then writing an absolute
		// value lets two concurrent transfers both pass the check, deduct the
		// personal balance once and credit the pool twice.
		debit := tx.Model(&model.User{}).
			Where("id = ? AND balance >= ?", user.ID, amount).
			UpdateColumn("balance", gorm.Expr("balance - ?", amount))
		if debit.Error != nil {
			return debit.Error
		}
		if debit.RowsAffected == 0 {
			return errors.New("Insufficient personal balance")
		}
		if err := tx.Model(&model.QuotaAccount{}).
			Where("id = ?", account.ID).
			UpdateColumn("limit_amount", gorm.Expr("limit_amount + ?", amount)).Error; err != nil {
			return err
		}
		return tx.Create(&model.QuotaLedger{OrganizationID: tenant.Organization.ID, AccountID: account.ID, PoolID: &pool.ID, EntryType: model.QuotaLedgerAllocation, Amount: amount, ReferenceType: "personal_balance_to_pool", CreatedByUserID: user.ID}).Error
	}); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	enterpriseAudit(c, user.ID, "personal_balance_allocated", fmt.Sprintf("将个人余额 %s 分配到资源池", amount), fmt.Sprintf(`{"pool_id":%d,"amount":"%s"}`, input.PoolID, amount))
	c.JSON(http.StatusOK, gin.H{"message": "Personal balance allocated to pool"})
}

func (api *EnterpriseAPI) FundPoolFromOrganizationBudget(c *gin.Context) {
	api.moveOrganizationBudget(c, false)
}
func (api *EnterpriseAPI) ReclaimPoolToOrganizationBudget(c *gin.Context) {
	api.moveOrganizationBudget(c, true)
}
func (api *EnterpriseAPI) moveOrganizationBudget(c *gin.Context, reclaim bool) {
	user, ok := enterpriseCurrentUser(c)
	if !ok {
		return
	}
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	var input enterprisePoolBudgetInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid pool budget transfer"})
		return
	}
	amount, err := decimal.NewFromString(strings.TrimSpace(input.Amount))
	if err != nil || amount.LessThanOrEqual(decimal.Zero) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Amount must be a positive decimal"})
		return
	}
	org, err := service.EnsureEnterpriseQuotaAccount(model.DB, service.EnterpriseQuotaScope{OrganizationID: tenant.Organization.ID, ScopeType: model.QuotaScopeOrganization})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to load organization budget"})
		return
	}
	var pool model.EnterpriseSharedPool
	if err := model.DB.Where("id = ? AND organization_id = ?", input.PoolID, tenant.Organization.ID).First(&pool).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Pool not found"})
		return
	}
	var poolAccount model.QuotaAccount
	if err := model.DB.Where("organization_id = ? AND pool_id = ?", tenant.Organization.ID, pool.ID).First(&poolAccount).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Pool budget not found"})
		return
	}
	parent, child := org.ID, poolAccount.ID
	if reclaim {
		parent, child = poolAccount.ID, org.ID
	}
	if err := service.AllocateEnterpriseQuota(model.DB, parent, child, user.ID, amount, "organization_pool_budget"); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if reclaim {
		enterpriseAudit(c, user.ID, "budget_reclaimed", fmt.Sprintf("从任务池“%s”回收组织预算 %s", pool.Name, amount), fmt.Sprintf(`{"pool_id":%d,"amount":"%s"}`, pool.ID, amount))
	} else {
		enterpriseAudit(c, user.ID, "budget_allocated", fmt.Sprintf("向任务池“%s”分配组织预算 %s", pool.Name, amount), fmt.Sprintf(`{"pool_id":%d,"amount":"%s"}`, pool.ID, amount))
	}
	c.JSON(http.StatusOK, gin.H{"message": "Organization budget transferred"})
}

func (api *EnterpriseAPI) GrantOrganizationBudgetToUser(c *gin.Context) {
	user, ok := enterpriseCurrentUser(c)
	if !ok {
		return
	}
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	var input enterpriseUserBudgetInput
	if err := c.ShouldBindJSON(&input); err != nil || input.UserID == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid employee budget grant"})
		return
	}
	amount, err := decimal.NewFromString(strings.TrimSpace(input.Amount))
	if err != nil || amount.LessThanOrEqual(decimal.Zero) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Amount must be a positive decimal"})
		return
	}
	if !enterpriseActiveMember(tenant.Organization.ID, input.UserID) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Employee not found"})
		return
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		org, err := service.EnsureEnterpriseQuotaAccount(tx, service.EnterpriseQuotaScope{OrganizationID: tenant.Organization.ID, ScopeType: model.QuotaScopeOrganization})
		if err != nil {
			return err
		}
		var employee model.User
		if err := tx.First(&employee, input.UserID).Error; err != nil {
			return err
		}
		// Enforce the organization's remaining budget inside the UPDATE so that
		// concurrent grants cannot each read the same available amount and hand
		// out the budget more than once.
		debit := tx.Model(&model.QuotaAccount{}).
			Where("id = ? AND limit_amount >= reserved_amount + consumed_amount + ?", org.ID, amount).
			UpdateColumn("limit_amount", gorm.Expr("limit_amount - ?", amount))
		if debit.Error != nil {
			return debit.Error
		}
		if debit.RowsAffected == 0 {
			return service.ErrEnterpriseQuotaExceeded
		}
		if err := tx.Model(&model.User{}).
			Where("id = ?", employee.ID).
			UpdateColumn("balance", gorm.Expr("balance + ?", amount)).Error; err != nil {
			return err
		}
		return tx.Create(&model.QuotaLedger{OrganizationID: tenant.Organization.ID, AccountID: org.ID, EntryType: model.QuotaLedgerAllocation, Amount: amount.Neg(), ReferenceType: "organization_budget_to_user", ReferenceID: strconv.FormatUint(uint64(employee.ID), 10), CreatedByUserID: user.ID}).Error
	}); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var employee model.User
	model.DB.First(&employee, input.UserID)
	enterpriseAudit(c, user.ID, "budget_granted", fmt.Sprintf("向员工“%s”发放组织预算 %s", employee.Username, amount), fmt.Sprintf(`{"user_id":%d,"amount":"%s"}`, employee.ID, amount))
	c.JSON(http.StatusOK, gin.H{"message": "Organization budget granted"})
}
func (api *EnterpriseAPI) ListQuotaLedger(c *gin.Context) {
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return
	}
	query := model.DB.Where("organization_id = ?", tenant.Organization.ID)
	if raw := c.Query("account_id"); raw != "" {
		id, err := parseEnterpriseID(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		query = query.Where("account_id = ?", id)
	}
	if raw := c.Query("task_id"); raw != "" {
		id, err := parseEnterpriseID(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		query = query.Where("task_id = ?", id)
	}
	if raw := c.Query("pool_id"); raw != "" {
		id, err := parseEnterpriseID(raw)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		query = query.Where("pool_id = ?", id)
	}
	var entries []model.QuotaLedger
	if err := query.Order("id DESC").Limit(200).Find(&entries).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list quota ledger"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"entries": entries})
}

func validateEnterpriseQuotaScope(organizationID uint, scope service.EnterpriseQuotaScope) error {
	scopeType := strings.ToLower(strings.TrimSpace(scope.ScopeType))
	if scopeType == model.QuotaScopeDepartment || scopeType == model.QuotaScopeUser || scopeType == model.QuotaScopeTask {
		return errors.New("Allocate quota to a shared pool; departments, employees, and tasks are not quota subjects")
	}
	if scope.DepartmentID != nil {
		var item model.Department
		if err := model.DB.Where("id = ? AND organization_id = ?", *scope.DepartmentID, organizationID).First(&item).Error; err != nil {
			return errors.New("Department not found")
		}
	}
	if scope.UserID != nil && !enterpriseActiveMember(organizationID, *scope.UserID) {
		return errors.New("Quota employee must be an active organization member")
	}
	if scope.TaskID != nil {
		var item model.EnterpriseTask
		if err := model.DB.Where("id = ? AND organization_id = ?", *scope.TaskID, organizationID).First(&item).Error; err != nil {
			return errors.New("Task not found")
		}
	}
	if scope.PoolID != nil {
		var item model.EnterpriseSharedPool
		if err := model.DB.Where("id = ? AND organization_id = ?", *scope.PoolID, organizationID).First(&item).Error; err != nil {
			return errors.New("Shared pool not found")
		}
	}
	return nil
}

func enterpriseSharedPoolAccess(c *gin.Context) (model.EnterpriseSharedPool, bool) {
	user, ok := enterpriseCurrentUser(c)
	if !ok {
		return model.EnterpriseSharedPool{}, false
	}
	tenant, ok := enterpriseTenant(c)
	if !ok {
		return model.EnterpriseSharedPool{}, false
	}
	id, err := parseEnterpriseID(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return model.EnterpriseSharedPool{}, false
	}
	var pool model.EnterpriseSharedPool
	if err := model.DB.Where("id = ? AND organization_id = ?", id, tenant.Organization.ID).First(&pool).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Shared pool not found"})
		return model.EnterpriseSharedPool{}, false
	}
	if !enterpriseCanAccessSharedPool(user, tenant.Organization.ID, pool) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Shared pool access denied"})
		return model.EnterpriseSharedPool{}, false
	}
	return pool, true
}
func enterpriseCanAccessSharedPool(user *model.User, organizationID uint, pool model.EnterpriseSharedPool) bool {
	if user == nil || user.ID == 0 {
		return false
	}
	if pool.ScopeType == model.EnterprisePoolScopeTask && pool.TaskID != nil {
		var task model.EnterpriseTask
		if model.DB.Where("id = ? AND organization_id = ?", *pool.TaskID, organizationID).First(&task).Error != nil || task.Status != model.EnterpriseTaskStatusRunning {
			return false
		}
		// A task pool is an active-task workspace, not an archive. This applies
		// to super administrators too, so finished and not-yet-started tasks do
		// not appear as folders in sessions or files.
		if user.IsAdmin {
			return true
		}
		if enterpriseTaskAssignedTo(*pool.TaskID, user.ID) {
			return true
		}
		var count int64
		return model.DB.Model(&model.EnterpriseTask{}).Where("id = ? AND organization_id = ? AND (created_by_user_id = ? OR owner_user_id = ?)", *pool.TaskID, organizationID, user.ID, user.ID).Count(&count).Error == nil && count > 0
	}
	if user.IsAdmin {
		return true
	}
	if pool.ScopeType == model.EnterprisePoolScopeDepartment && pool.DepartmentID != nil {
		var count int64
		return model.DB.Model(&model.DepartmentMember{}).Where("organization_id = ? AND department_id = ? AND user_id = ?", organizationID, *pool.DepartmentID, user.ID).Count(&count).Error == nil && count > 0
	}
	return false
}
func enterpriseSharedPoolFromInput(organizationID uint, input enterpriseSharedPoolInput, userID uint) (model.EnterpriseSharedPool, error) {
	scope := strings.ToLower(strings.TrimSpace(input.ScopeType))
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return model.EnterpriseSharedPool{}, errors.New("Shared pool name is required")
	}
	pool := model.EnterpriseSharedPool{OrganizationID: organizationID, ScopeType: scope, DepartmentID: input.DepartmentID, TaskID: input.TaskID, Name: name, CreatedByUserID: userID}
	switch scope {
	case model.EnterprisePoolScopeTask:
		if input.TaskID == nil || input.DepartmentID != nil {
			return pool, errors.New("Task pool requires exactly one task")
		}
		var task model.EnterpriseTask
		if err := model.DB.Where("id = ? AND organization_id = ?", *input.TaskID, organizationID).First(&task).Error; err != nil {
			return pool, errors.New("Task not found")
		}
		pool.ScopeKey = "task:" + strconv.FormatUint(uint64(*input.TaskID), 10)
	case model.EnterprisePoolScopeDepartment:
		if input.DepartmentID == nil || input.TaskID != nil {
			return pool, errors.New("Department pool requires exactly one department")
		}
		var department model.Department
		if err := model.DB.Where("id = ? AND organization_id = ?", *input.DepartmentID, organizationID).First(&department).Error; err != nil {
			return pool, errors.New("Department not found")
		}
		pool.ScopeKey = "department:" + strconv.FormatUint(uint64(*input.DepartmentID), 10)
	default:
		return pool, errors.New("Unsupported shared pool scope")
	}
	return pool, nil
}
func ensureEnterpriseSharedPool(db *gorm.DB, organizationID uint, scope string, departmentID, taskID *uint, name string, userID uint) error {
	pool := model.EnterpriseSharedPool{OrganizationID: organizationID, ScopeType: scope, DepartmentID: departmentID, TaskID: taskID, Name: strings.TrimSpace(name), CreatedByUserID: userID}
	if scope == model.EnterprisePoolScopeTask && taskID != nil {
		pool.ScopeKey = "task:" + strconv.FormatUint(uint64(*taskID), 10)
	} else if scope == model.EnterprisePoolScopeDepartment && departmentID != nil {
		pool.ScopeKey = "department:" + strconv.FormatUint(uint64(*departmentID), 10)
	} else {
		return errors.New("Invalid shared pool scope")
	}
	if err := db.Where("organization_id = ? AND scope_type = ? AND scope_key = ?", pool.OrganizationID, pool.ScopeType, pool.ScopeKey).FirstOrCreate(&pool).Error; err != nil {
		return err
	}
	return ensureEnterpriseSharedPoolResources(db, &pool)
}

// ensureEnterpriseSharedPoolResources establishes the pool as both resource
// principal and quota subject. Employees remain actors in its audit trail.
func ensureEnterpriseSharedPoolResources(db *gorm.DB, pool *model.EnterpriseSharedPool) error {
	if err := ensureEnterpriseSharedPoolIdentity(db, pool); err != nil {
		return err
	}
	_, err := service.EnsureEnterpriseQuotaAccount(db, service.EnterpriseQuotaScope{
		OrganizationID: pool.OrganizationID,
		ScopeType:      model.QuotaScopePool,
		PoolID:         &pool.ID,
	})
	return err
}

func ensureEnterpriseSharedPoolIdentity(db *gorm.DB, pool *model.EnterpriseSharedPool) error {
	if db == nil || pool == nil || pool.ID == 0 {
		return errors.New("Invalid shared pool")
	}
	if pool.ResourceUserID != 0 {
		return nil
	}
	username := fmt.Sprintf("enterprise-pool-%d", pool.ID)
	email := fmt.Sprintf("enterprise-pool-%d@internal.invalid", pool.ID)
	defaultGroup := model.Group{Name: "user"}
	if err := db.Where("name = ?", defaultGroup.Name).FirstOrCreate(&defaultGroup).Error; err != nil {
		return err
	}
	resourceUser := model.User{Username: username, Email: email, GroupID: defaultGroup.ID, APIKey: fmt.Sprintf("pool-internal-%d", pool.ID), EmailVerified: false}
	if err := db.Where("username = ?", username).FirstOrCreate(&resourceUser).Error; err != nil {
		return err
	}
	if err := db.Model(&model.EnterpriseSharedPool{}).Where("id = ? AND resource_user_id = ?", pool.ID, 0).Update("resource_user_id", resourceUser.ID).Error; err != nil {
		return err
	}
	pool.ResourceUserID = resourceUser.ID
	return nil
}

func enterprisePoolResourceID(kind string) string {
	return fmt.Sprintf("pool-%s-%x", strings.TrimSpace(kind), time.Now().UnixNano())
}

func enterpriseDepartmentFromInput(organizationID, id uint, input enterpriseDepartmentInput) (model.Department, error) {
	input.Name, input.Slug = strings.TrimSpace(input.Name), strings.ToLower(strings.TrimSpace(input.Slug))
	if input.Name == "" || input.Slug == "" {
		return model.Department{}, errors.New("Department name and slug are required")
	}
	if input.ParentID != nil {
		if *input.ParentID == id {
			return model.Department{}, errors.New("Department cannot be its own parent")
		}
		var parent model.Department
		if err := model.DB.Where("id = ? AND organization_id = ?", *input.ParentID, organizationID).First(&parent).Error; err != nil {
			return model.Department{}, errors.New("Parent department not found")
		}
	}
	multiplier := decimal.NewFromInt(1)
	if value := strings.TrimSpace(input.Multiplier); value != "" {
		var err error
		multiplier, err = decimal.NewFromString(value)
		if err != nil || multiplier.LessThanOrEqual(decimal.Zero) {
			return model.Department{}, errors.New("Department multiplier must be a positive decimal")
		}
	}
	policy := strings.ToLower(strings.TrimSpace(input.ModelPolicy))
	if policy == "" {
		policy = "inherit"
	}
	if policy != "inherit" && policy != "allow" && policy != "deny" {
		return model.Department{}, errors.New("Department model policy must be inherit, allow, or deny")
	}
	modelNames := uniqueEnterpriseStrings(input.ModelNames)
	if policy != "inherit" && len(modelNames) == 0 {
		return model.Department{}, errors.New("Department model policy requires at least one model")
	}
	modelNamesJSON, err := json.Marshal(modelNames)
	if err != nil {
		return model.Department{}, errors.New("Invalid department model names")
	}
	settingsJSON, err := json.Marshal(input.Settings)
	if err != nil {
		return model.Department{}, errors.New("Invalid department settings")
	}
	if input.Settings == nil {
		settingsJSON = []byte("{}")
	}
	department := model.Department{ID: id, OrganizationID: organizationID, Name: input.Name, Slug: input.Slug, ParentID: input.ParentID, Multiplier: multiplier, ModelPolicy: policy, ModelNames: string(modelNamesJSON), Settings: string(settingsJSON)}
	if id != 0 {
		if err := model.DB.Where("id = ? AND organization_id = ?", id, organizationID).First(&department).Error; err != nil {
			return model.Department{}, errors.New("Department not found")
		}
		department.Name, department.Slug, department.ParentID = input.Name, input.Slug, input.ParentID
		if strings.TrimSpace(input.Multiplier) != "" {
			department.Multiplier = multiplier
		}
		if strings.TrimSpace(input.ModelPolicy) != "" {
			department.ModelPolicy, department.ModelNames = policy, string(modelNamesJSON)
		}
		if input.Settings != nil {
			department.Settings = string(settingsJSON)
		}
	}
	return department, nil
}

func uniqueEnterpriseStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; !exists {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	return result
}

func enterpriseActiveMember(organizationID, userID uint) bool {
	return enterpriseActiveMemberWithDB(model.DB, organizationID, userID)
}
func enterprisePoolAccount(user model.User) bool {
	return strings.HasPrefix(user.Username, "enterprise-pool-") || strings.HasSuffix(user.Email, "@internal.invalid")
}
func enterpriseActiveMemberWithDB(db *gorm.DB, organizationID, userID uint) bool {
	var count int64
	return db.Model(&model.OrganizationMember{}).Joins("JOIN users ON users.id = organization_members.user_id").
		Where("organization_members.organization_id = ? AND organization_members.user_id = ? AND organization_members.status = ? AND users.username NOT LIKE ? AND users.email NOT LIKE ?", organizationID, userID, model.OrganizationMemberStatusActive, "enterprise-pool-%", "%@internal.invalid").
		Count(&count).Error == nil && count == 1
}
func enterpriseTaskAssignedTo(taskID, userID uint) bool {
	var count int64
	return model.DB.Model(&model.EnterpriseTaskAssignment{}).Where("task_id = ? AND user_id = ?", taskID, userID).Count(&count).Error == nil && count > 0
}
func uniqueEnterpriseUserIDs(values []uint) []uint {
	seen := map[uint]struct{}{}
	result := make([]uint, 0, len(values))
	for _, value := range values {
		if value != 0 {
			if _, ok := seen[value]; !ok {
				seen[value] = struct{}{}
				result = append(result, value)
			}
		}
	}
	return result
}

func enterpriseTenant(c *gin.Context) (*middleware.TenantContext, bool) {
	if !enterpriseFeatureAvailable(c) {
		return nil, false
	}
	tenant, ok := middleware.CurrentTenantContext(c)
	if !ok {
		c.JSON(http.StatusForbidden, gin.H{"error": "Enterprise tenant context is required"})
	}
	return tenant, ok
}

// enterpriseAudit persists a stable event code and structured metadata only.
// Human-readable copy belongs to the presentation layer, where it can be
// localized without making audit records dependent on UI wording.
func enterpriseAudit(c *gin.Context, userID uint, action, _ string, metadata string) {
	if userID == 0 {
		return
	}
	service.RecordAuditLog(service.AuditLogInput{LogType: service.AuditLogTypeAdmin, Action: action, Resource: enterpriseAuditResource(action), UserID: &userID, Method: c.Request.Method, Path: c.Request.URL.Path, IPAddress: c.ClientIP(), UserAgent: c.Request.UserAgent(), StatusCode: http.StatusOK, Metadata: metadata})
}

func enterpriseAuditResource(action string) string {
	for _, resource := range []string{"organization", "portal", "member", "department", "role", "task", "device", "connector", "quota", "budget", "shared_pool", "pool"} {
		if strings.HasPrefix(action, resource+"_") {
			return "enterprise." + resource
		}
	}
	return "enterprise"
}

func enterpriseAuditCurrent(c *gin.Context, action, message, metadata string) {
	user, ok := enterpriseCurrentUser(c)
	if ok {
		enterpriseAudit(c, user.ID, action, message, metadata)
	}
}

func enterprisePortalSettingKey(organizationID uint) string {
	return fmt.Sprintf("enterprise_portal_%d", organizationID)
}

func enterprisePortalConfigFor(organizationID uint) enterprisePortalConfig {
	config := enterprisePortalConfig{Title: "企业门户", Widgets: []string{"tasks", "organization", "devices"}}
	raw := model.GetSystemSettingWithDB(model.DB, enterprisePortalSettingKey(organizationID), "")
	if raw != "" && json.Unmarshal([]byte(raw), &config) == nil {
		config.Title = strings.TrimSpace(config.Title)
		config.Message = strings.TrimSpace(config.Message)
		config.Widgets = enterprisePortalWidgets(config.Widgets)
	}
	return config
}

func enterprisePortalWidgets(values []string) []string {
	allowed := map[string]bool{"tasks": true, "organization": true, "devices": true}
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if allowed[value] && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func enterpriseCreateOrUpdateRole(organizationID, roleID uint, input enterpriseRoleInput) (model.Role, error) {
	input.Slug = strings.ToLower(strings.TrimSpace(input.Slug))
	input.Name = strings.TrimSpace(input.Name)
	if input.Slug == "" || input.Name == "" {
		return model.Role{}, errors.New("role slug and name are required")
	}
	role := model.Role{}
	if roleID != 0 {
		if err := model.DB.Where("id = ? AND organization_id = ?", roleID, organizationID).First(&role).Error; err != nil {
			return role, err
		}
		if role.Builtin {
			return role, errors.New("built-in roles cannot be changed")
		}
	} else {
		role = model.Role{OrganizationID: organizationID, Slug: input.Slug, Name: input.Name, Description: strings.TrimSpace(input.Description)}
	}
	return role, model.DB.Transaction(func(tx *gorm.DB) error {
		if roleID == 0 {
			if err := tx.Create(&role).Error; err != nil {
				return err
			}
		} else if err := tx.Model(&role).Updates(map[string]interface{}{"slug": input.Slug, "name": input.Name, "description": strings.TrimSpace(input.Description)}).Error; err != nil {
			return err
		}
		if input.Permissions == nil {
			return nil
		}
		var permissions []model.Permission
		if len(input.Permissions) > 0 {
			if err := tx.Where("code IN ?", input.Permissions).Find(&permissions).Error; err != nil {
				return err
			}
			if len(permissions) != len(input.Permissions) {
				return errors.New("one or more permissions do not exist")
			}
		}
		if err := tx.Where("role_id = ?", role.ID).Delete(&model.RolePermission{}).Error; err != nil {
			return err
		}
		for _, permission := range permissions {
			if err := tx.Create(&model.RolePermission{RoleID: role.ID, PermissionID: permission.ID}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func enterpriseWriteRoleError(c *gin.Context, err error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Role not found"})
		return
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
}
func parseEnterpriseID(value string) (uint, error) {
	id, err := strconv.ParseUint(value, 10, 64)
	if err != nil || id == 0 {
		return 0, errors.New("id must be a positive integer")
	}
	return uint(id), nil
}

func enterpriseFeatureAvailable(c *gin.Context) bool {
	if service.EnterpriseFeaturesEnabled() {
		return true
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "Enterprise features are disabled"})
	return false
}

func enterpriseCurrentUser(c *gin.Context) (*model.User, bool) {
	value, exists := c.Get("user")
	user, ok := value.(*model.User)
	if !exists || !ok || user == nil || user.ID == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return nil, false
	}
	return user, true
}
