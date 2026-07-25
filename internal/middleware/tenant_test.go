package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestResolveTenantContextUsesSingleEnterpriseAndPersonalWorkspace(t *testing.T) {
	db, user := tenantTestDatabase(t, "defaults")
	if err := model.EnsureEnterpriseTenantForUser(db, &user); err != nil {
		t.Fatalf("bootstrap enterprise tenant: %v", err)
	}

	tenant, status, err := ResolveTenantContext(db, user.ID, TenantSelection{})
	if err != nil || status != http.StatusOK {
		t.Fatalf("resolve default tenant: status=%d err=%v", status, err)
	}
	if tenant.Organization.Slug != model.EnterpriseOrganizationSlug {
		t.Fatalf("organization slug = %q", tenant.Organization.Slug)
	}
	if tenant.Workspace == nil || tenant.Workspace.Type != model.WorkspaceTypePersonal {
		t.Fatalf("expected personal workspace, got %+v", tenant.Workspace)
	}
}

func TestResolveTenantContextRejectsAnotherEmployeesPersonalWorkspace(t *testing.T) {
	db, user := tenantTestDatabase(t, "unauthorized")
	if err := model.EnsureEnterpriseTenantForUser(db, &user); err != nil {
		t.Fatalf("bootstrap enterprise tenant: %v", err)
	}
	otherUser := model.User{Username: "tenant-other-user", Email: "tenant-other@example.com", APIKey: "tenant-other-key"}
	if err := db.Create(&otherUser).Error; err != nil {
		t.Fatalf("create other user: %v", err)
	}
	if err := model.EnsureEnterpriseTenantForUser(db, &otherUser); err != nil {
		t.Fatalf("bootstrap other employee: %v", err)
	}

	var otherWorkspace model.Workspace
	if err := db.Where("created_by_user_id = ?", otherUser.ID).First(&otherWorkspace).Error; err != nil {
		t.Fatalf("find other workspace: %v", err)
	}
	if _, status, err := ResolveTenantContext(db, user.ID, TenantSelection{WorkspaceID: fmt.Sprint(otherWorkspace.ID)}); err == nil || status != http.StatusForbidden {
		t.Fatalf("unauthorized workspace result: status=%d err=%v", status, err)
	}
}

func TestResolveTenantContextRejectsInvalidIDs(t *testing.T) {
	db, user := tenantTestDatabase(t, "invalid-id")
	if err := model.EnsureEnterpriseTenantForUser(db, &user); err != nil {
		t.Fatalf("bootstrap enterprise tenant: %v", err)
	}
	if _, status, err := ResolveTenantContext(db, user.ID, TenantSelection{WorkspaceID: "0"}); err == nil || status != http.StatusBadRequest {
		t.Fatalf("invalid workspace id result: status=%d err=%v", status, err)
	}
}

func TestResolveTenantContextRejectsSuspendedMembershipAndOrganization(t *testing.T) {
	db, user := tenantTestDatabase(t, "suspended")
	if err := model.EnsureEnterpriseTenantForUser(db, &user); err != nil {
		t.Fatalf("bootstrap enterprise tenant: %v", err)
	}
	var membership model.OrganizationMember
	if err := db.Where("user_id = ?", user.ID).First(&membership).Error; err != nil {
		t.Fatalf("find membership: %v", err)
	}
	if err := db.Model(&membership).Update("status", model.OrganizationMemberStatusSuspended).Error; err != nil {
		t.Fatalf("suspend membership: %v", err)
	}
	if _, status, err := ResolveTenantContext(db, user.ID, TenantSelection{}); err == nil || status != http.StatusForbidden {
		t.Fatalf("suspended membership result: status=%d err=%v", status, err)
	}

	if err := db.Model(&membership).Update("status", model.OrganizationMemberStatusActive).Error; err != nil {
		t.Fatalf("reactivate membership: %v", err)
	}
	if err := db.Model(&model.Organization{}).Where("id = ?", membership.OrganizationID).Update("status", model.OrganizationStatusSuspended).Error; err != nil {
		t.Fatalf("suspend organization: %v", err)
	}
	if _, status, err := ResolveTenantContext(db, user.ID, TenantSelection{}); err == nil || status != http.StatusForbidden {
		t.Fatalf("suspended organization result: status=%d err=%v", status, err)
	}
}

func TestOrganizationRoleMiddlewareGrantsPlatformAdminBreakGlassAccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Set("user", &model.User{ID: 1, IsAdmin: true})
	context.Set(tenantContextKey, &TenantContext{
		Organization:       model.Organization{ID: 1},
		OrganizationMember: model.OrganizationMember{Role: model.OrganizationMemberRoleMember},
	})

	OrganizationRoleMiddleware(model.OrganizationMemberRoleOwner, model.OrganizationMemberRoleAdmin)(context)
	if context.IsAborted() {
		t.Fatalf("platform admin tenant-role result: aborted=%t status=%d", context.IsAborted(), recorder.Code)
	}
}

func TestEnterpriseRoleMiddlewareAllowsMatchingRoles(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Set(tenantContextKey, &TenantContext{
		Organization:       model.Organization{ID: 1},
		OrganizationMember: model.OrganizationMember{Role: model.OrganizationMemberRoleOwner},
		Workspace:          &model.Workspace{ID: 2},
		WorkspaceMember:    &model.WorkspaceMember{Role: model.WorkspaceMemberRoleAdmin},
	})

	OrganizationRoleMiddleware(model.OrganizationMemberRoleOwner)(context)
	if context.IsAborted() {
		t.Fatalf("matching organization role was rejected: status=%d", recorder.Code)
	}
	WorkspaceRoleMiddleware(model.WorkspaceMemberRoleAdmin)(context)
	if context.IsAborted() {
		t.Fatalf("matching workspace role was rejected: status=%d", recorder.Code)
	}
}

func tenantTestDatabase(t *testing.T, name string) (*gorm.DB, model.User) {
	t.Helper()
	dsn := fmt.Sprintf("file:tenant-middleware-%s?mode=memory&cache=shared", name)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{},
		&model.Organization{},
		&model.Department{},
		&model.Workspace{},
		&model.OrganizationMember{},
		&model.WorkspaceMember{},
		&model.Permission{},
		&model.Role{},
		&model.RolePermission{},
		&model.RoleBinding{},
	); err != nil {
		t.Fatalf("migrate enterprise models: %v", err)
	}
	user := model.User{Username: "tenant-user-" + name, Email: name + "@example.com", APIKey: "tenant-key-" + name}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return db, user
}
