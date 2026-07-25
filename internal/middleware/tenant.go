package middleware

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	tenantContextKey    = "enterprise_tenant_context"
	organizationIDKey   = "enterprise_organization_id"
	workspaceIDKey      = "enterprise_workspace_id"
	workspaceIDHeader   = "X-Workspace-ID"
	workspaceSlugHeader = "X-Workspace-Slug"
)

type TenantContext struct {
	Organization       model.Organization       `json:"organization"`
	OrganizationMember model.OrganizationMember `json:"organization_member"`
	Workspace          *model.Workspace         `json:"workspace,omitempty"`
	WorkspaceMember    *model.WorkspaceMember   `json:"workspace_member,omitempty"`
}

// TenantContextMiddleware resolves and validates the current organization and
// workspace after authentication. It is a no-op while enterprise features are
// disabled, preserving the existing community behavior.
func TenantContextMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !model.EnterpriseModeEnabledWithDB(model.DB) {
			c.Next()
			return
		}
		value, exists := c.Get("user")
		user, ok := value.(*model.User)
		if !exists || !ok || user == nil || user.ID == 0 {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
			c.Abort()
			return
		}
		tenant, status, err := ResolveTenantContext(model.DB, user.ID, TenantSelection{
			WorkspaceID:   strings.TrimSpace(c.GetHeader(workspaceIDHeader)),
			WorkspaceSlug: strings.TrimSpace(c.GetHeader(workspaceSlugHeader)),
		})
		if err != nil {
			c.JSON(status, gin.H{"error": err.Error()})
			c.Abort()
			return
		}
		c.Set(tenantContextKey, tenant)
		c.Set(organizationIDKey, tenant.Organization.ID)
		if tenant.Workspace != nil {
			c.Set(workspaceIDKey, tenant.Workspace.ID)
		}
		c.Next()
	}
}

type TenantSelection struct {
	WorkspaceID   string
	WorkspaceSlug string
}

func ResolveTenantContext(db *gorm.DB, userID uint, selection TenantSelection) (*TenantContext, int, error) {
	if db == nil {
		return nil, http.StatusInternalServerError, errors.New("enterprise database is unavailable")
	}
	organizationMember, organization, status, err := resolveOrganization(db, userID)
	if err != nil {
		return nil, status, err
	}
	workspaceMember, workspace, status, err := resolveWorkspace(db, userID, organization.ID, selection)
	if err != nil {
		return nil, status, err
	}
	return &TenantContext{
		Organization:       organization,
		OrganizationMember: organizationMember,
		Workspace:          workspace,
		WorkspaceMember:    workspaceMember,
	}, http.StatusOK, nil
}

func CurrentTenantContext(c *gin.Context) (*TenantContext, bool) {
	value, exists := c.Get(tenantContextKey)
	if !exists {
		return nil, false
	}
	tenant, ok := value.(*TenantContext)
	return tenant, ok && tenant != nil
}

// OrganizationRoleMiddleware authorizes enterprise organization roles. A
// platform IsAdmin is a deliberate break-glass operator and may bypass tenant
// roles; enterprise administrators must still hold the required tenant role.
func OrganizationRoleMiddleware(roles ...string) gin.HandlerFunc {
	allowed := normalizedRoleSet(roles)
	return func(c *gin.Context) {
		tenant, ok := CurrentTenantContext(c)
		if !ok {
			c.JSON(http.StatusForbidden, gin.H{"error": "Enterprise tenant context is required"})
			c.Abort()
			return
		}
		if value, exists := c.Get("user"); exists {
			if user, ok := value.(*model.User); ok && user != nil && user.IsAdmin {
				c.Next()
				return
			}
		}
		if _, ok := allowed[NormalizeOrganizationRole(tenant.OrganizationMember.Role)]; !ok {
			c.JSON(http.StatusForbidden, gin.H{"error": "Organization role is not permitted"})
			c.Abort()
			return
		}
		c.Next()
	}
}

func WorkspaceRoleMiddleware(roles ...string) gin.HandlerFunc {
	allowed := normalizedRoleSet(roles)
	return func(c *gin.Context) {
		tenant, ok := CurrentTenantContext(c)
		if !ok || tenant.WorkspaceMember == nil {
			c.JSON(http.StatusForbidden, gin.H{"error": "Enterprise workspace context is required"})
			c.Abort()
			return
		}
		if value, exists := c.Get("user"); exists {
			if user, ok := value.(*model.User); ok && user != nil && user.IsAdmin {
				c.Next()
				return
			}
		}
		if _, ok := allowed[NormalizeWorkspaceRole(tenant.WorkspaceMember.Role)]; !ok {
			c.JSON(http.StatusForbidden, gin.H{"error": "Workspace role is not permitted"})
			c.Abort()
			return
		}
		c.Next()
	}
}

func NormalizeOrganizationRole(value string) string {
	return model.NormalizeOrganizationMemberRole(value)
}

func NormalizeWorkspaceRole(value string) string {
	return model.NormalizeWorkspaceMemberRole(value)
}

func normalizedRoleSet(roles []string) map[string]struct{} {
	result := make(map[string]struct{}, len(roles))
	for _, role := range roles {
		role = strings.ToLower(strings.TrimSpace(role))
		if role != "" {
			result[role] = struct{}{}
		}
	}
	return result
}

func resolveOrganization(db *gorm.DB, userID uint) (model.OrganizationMember, model.Organization, int, error) {
	query := db.Model(&model.OrganizationMember{}).
		Joins("JOIN organizations ON organizations.id = organization_members.organization_id AND organizations.deleted_at IS NULL").
		Where("organization_members.user_id = ? AND organization_members.status = ? AND organization_members.deleted_at IS NULL", userID, model.OrganizationMemberStatusActive).
		Where("organizations.status = ? AND organizations.slug = ?", model.OrganizationStatusActive, model.EnterpriseOrganizationSlug)

	var membership model.OrganizationMember
	if err := query.Order("organization_members.id ASC").First(&membership).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.OrganizationMember{}, model.Organization{}, http.StatusForbidden, errors.New("organization access denied")
		}
		return model.OrganizationMember{}, model.Organization{}, http.StatusInternalServerError, errors.New("failed to resolve organization")
	}
	var organization model.Organization
	if err := db.First(&organization, membership.OrganizationID).Error; err != nil {
		return model.OrganizationMember{}, model.Organization{}, http.StatusInternalServerError, errors.New("failed to load organization")
	}
	return membership, organization, http.StatusOK, nil
}

func resolveWorkspace(db *gorm.DB, userID, organizationID uint, selection TenantSelection) (*model.WorkspaceMember, *model.Workspace, int, error) {
	query := db.Model(&model.WorkspaceMember{}).
		Joins("JOIN workspaces ON workspaces.id = workspace_members.workspace_id AND workspaces.deleted_at IS NULL").
		Where("workspace_members.user_id = ? AND workspace_members.deleted_at IS NULL", userID).
		Where("workspaces.organization_id = ? AND workspaces.status = ?", organizationID, model.WorkspaceStatusActive)

	explicit := selection.WorkspaceID != "" || selection.WorkspaceSlug != ""
	if selection.WorkspaceID != "" {
		workspaceID, err := parsePositiveID(selection.WorkspaceID, "workspace")
		if err != nil {
			return nil, nil, http.StatusBadRequest, err
		}
		query = query.Where("workspaces.id = ?", workspaceID)
	} else if selection.WorkspaceSlug != "" {
		query = query.Where("workspaces.slug = ?", selection.WorkspaceSlug)
	}

	var membership model.WorkspaceMember
	err := query.Order("CASE WHEN workspaces.type = 'personal' THEN 0 ELSE 1 END").Order("workspace_members.id ASC").First(&membership).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if explicit {
			return nil, nil, http.StatusForbidden, errors.New("workspace access denied")
		}
		return nil, nil, http.StatusOK, nil
	}
	if err != nil {
		return nil, nil, http.StatusInternalServerError, errors.New("failed to resolve workspace")
	}
	var workspace model.Workspace
	if err := db.First(&workspace, membership.WorkspaceID).Error; err != nil {
		return nil, nil, http.StatusInternalServerError, errors.New("failed to load workspace")
	}
	return &membership, &workspace, http.StatusOK, nil
}

func parsePositiveID(value, resource string) (uint64, error) {
	id, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	if err != nil || id == 0 {
		return 0, errors.New(resource + " id must be a positive integer")
	}
	return id, nil
}
