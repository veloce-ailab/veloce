package middleware

import (
	"testing"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
)

func TestHasPermissionRespectsScopeAndExpiry(t *testing.T) {
	db, owner := tenantTestDatabase(t, "rbac")
	if err := model.EnsureEnterpriseTenantForUser(db, &owner); err != nil {
		t.Fatal(err)
	}
	user := model.User{Username: "rbac-member", Email: "rbac-member@example.com", APIKey: "rbac-member-key"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := model.EnsureEnterpriseTenantForUser(db, &user); err != nil {
		t.Fatal(err)
	}
	var org model.Organization
	if err := db.Where("slug = ?", model.EnterpriseOrganizationSlug).First(&org).Error; err != nil {
		t.Fatal(err)
	}
	workspace := model.Workspace{OrganizationID: org.ID, Slug: "team", Name: "Team", Type: model.WorkspaceTypeProject}
	if err := db.Create(&workspace).Error; err != nil {
		t.Fatal(err)
	}
	role := model.Role{OrganizationID: org.ID, Slug: "agent-manager", Name: "Agent manager"}
	var permission model.Permission
	if err := db.Where("code = ?", "agent.manage").First(&permission).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&role).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.RolePermission{RoleID: role.ID, PermissionID: permission.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.RoleBinding{OrganizationID: org.ID, UserID: user.ID, RoleID: role.ID, ScopeType: model.RoleBindingScopeWorkspace, ScopeID: workspace.ID, CreatedByUserID: user.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if HasPermission(db, user.ID, org.ID, 0, "agent.manage") {
		t.Fatal("workspace role granted organization access")
	}
	if !HasPermission(db, user.ID, org.ID, workspace.ID, "agent.manage") {
		t.Fatal("workspace role did not grant workspace access")
	}
	past := time.Now().Add(-time.Minute)
	if err := db.Model(&model.RoleBinding{}).Where("user_id = ? AND role_id = ?", user.ID, role.ID).Update("expires_at", past).Error; err != nil {
		t.Fatal(err)
	}
	if HasPermission(db, user.ID, org.ID, workspace.ID, "agent.manage") {
		t.Fatal("expired role granted access")
	}
}
