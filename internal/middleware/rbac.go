package middleware

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// PermissionMiddleware checks a resource.action capability in the current
// organization and, when selected, the current workspace. Platform IsAdmin is
// an explicit break-glass bypass; ordinary enterprise administrators do not
// receive this privilege unless their role grants the capability.
func PermissionMiddleware(code string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !model.EnterpriseModeEnabledWithDB(model.DB) {
			c.Next()
			return
		}
		tenant, ok := CurrentTenantContext(c)
		if !ok {
			c.JSON(http.StatusForbidden, gin.H{"error": "Enterprise tenant context is required"})
			c.Abort()
			return
		}
		userValue, exists := c.Get("user")
		user, ok := userValue.(*model.User)
		if !exists || !ok || user == nil {
			c.JSON(http.StatusForbidden, gin.H{"error": "Permission is not granted"})
			c.Abort()
			return
		}
		if user.IsAdmin {
			c.Next()
			return
		}
		if !HasPermission(model.DB, user.ID, tenant.Organization.ID, workspaceID(tenant), code) {
			c.JSON(http.StatusForbidden, gin.H{"error": "Permission is not granted"})
			c.Abort()
			return
		}
		c.Next()
	}
}

func workspaceID(tenant *TenantContext) uint {
	if tenant == nil || tenant.Workspace == nil {
		return 0
	}
	return tenant.Workspace.ID
}

// HasPermission resolves active role bindings at organization and workspace
// scope. It is deliberately query based until a cache with explicit invalidation
// is introduced; authorization changes therefore take effect immediately.
func HasPermission(db *gorm.DB, userID, organizationID, workspaceID uint, code string) bool {
	if db == nil || userID == 0 || organizationID == 0 || strings.TrimSpace(code) == "" {
		return false
	}
	query := db.Table("role_bindings").
		Joins("JOIN roles ON roles.id = role_bindings.role_id AND roles.deleted_at IS NULL").
		Joins("JOIN role_permissions ON role_permissions.role_id = roles.id").
		Joins("JOIN permissions ON permissions.id = role_permissions.permission_id").
		Where("role_bindings.user_id = ? AND role_bindings.organization_id = ? AND role_bindings.deleted_at IS NULL", userID, organizationID).
		Where("permissions.code = ?", strings.ToLower(strings.TrimSpace(code))).
		Where("role_bindings.expires_at IS NULL OR role_bindings.expires_at > ?", time.Now())
	if workspaceID == 0 {
		query = query.Where("role_bindings.scope_type = ? AND role_bindings.scope_id = ?", model.RoleBindingScopeOrganization, organizationID)
	} else {
		query = query.Where("(role_bindings.scope_type = ? AND role_bindings.scope_id = ?) OR (role_bindings.scope_type = ? AND role_bindings.scope_id = ?)", model.RoleBindingScopeOrganization, organizationID, model.RoleBindingScopeWorkspace, workspaceID)
	}
	var count int64
	if query.Count(&count).Error == nil && count > 0 {
		return true
	}
	departmentQuery := db.Table("department_role_bindings").Joins("JOIN department_members ON department_members.department_id = department_role_bindings.department_id AND department_members.deleted_at IS NULL").Joins("JOIN roles ON roles.id = department_role_bindings.role_id AND roles.deleted_at IS NULL").Joins("JOIN role_permissions ON role_permissions.role_id = roles.id").Joins("JOIN permissions ON permissions.id = role_permissions.permission_id").Where("department_role_bindings.organization_id = ? AND department_members.user_id = ? AND department_role_bindings.deleted_at IS NULL AND permissions.code = ?", organizationID, userID, strings.ToLower(strings.TrimSpace(code)))
	return departmentQuery.Count(&count).Error == nil && count > 0
}

func EffectivePermissions(db *gorm.DB, userID, organizationID, workspaceID uint) []string {
	if db == nil || userID == 0 || organizationID == 0 {
		return []string{}
	}
	var codes []string
	query := db.Table("role_bindings").
		Distinct("permissions.code").
		Joins("JOIN roles ON roles.id = role_bindings.role_id AND roles.deleted_at IS NULL").
		Joins("JOIN role_permissions ON role_permissions.role_id = roles.id").
		Joins("JOIN permissions ON permissions.id = role_permissions.permission_id").
		Where("role_bindings.user_id = ? AND role_bindings.organization_id = ? AND role_bindings.deleted_at IS NULL", userID, organizationID).
		Where("role_bindings.expires_at IS NULL OR role_bindings.expires_at > ?", time.Now())
	if workspaceID == 0 {
		query = query.Where("role_bindings.scope_type = ? AND role_bindings.scope_id = ?", model.RoleBindingScopeOrganization, organizationID)
	} else {
		query = query.Where("(role_bindings.scope_type = ? AND role_bindings.scope_id = ?) OR (role_bindings.scope_type = ? AND role_bindings.scope_id = ?)", model.RoleBindingScopeOrganization, organizationID, model.RoleBindingScopeWorkspace, workspaceID)
	}
	query.Order("permissions.code ASC").Pluck("permissions.code", &codes)
	var departmentCodes []string
	db.Table("department_role_bindings").Distinct("permissions.code").Joins("JOIN department_members ON department_members.department_id = department_role_bindings.department_id AND department_members.deleted_at IS NULL").Joins("JOIN roles ON roles.id = department_role_bindings.role_id AND roles.deleted_at IS NULL").Joins("JOIN role_permissions ON role_permissions.role_id = roles.id").Joins("JOIN permissions ON permissions.id = role_permissions.permission_id").Where("department_role_bindings.organization_id = ? AND department_members.user_id = ? AND department_role_bindings.deleted_at IS NULL", organizationID, userID).Pluck("permissions.code", &departmentCodes)
	seen := map[string]bool{}
	for _, code := range append(codes, departmentCodes...) {
		seen[code] = true
	}
	codes = codes[:0]
	for code := range seen {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}
