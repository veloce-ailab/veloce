package service

import (
	"testing"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestEnterpriseDeviceAssignmentsRespectTargetExpiryAndRevocation(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:enterprise-device-service?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Organization{}, &model.Department{}, &model.OrganizationMember{}, &model.EnterpriseTask{}, &model.EnterpriseDevice{}, &model.EnterpriseDeviceAssignment{}); err != nil {
		t.Fatal(err)
	}
	user := model.User{Username: "device-user", Email: "device@example.com", APIKey: "device-key"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	org := model.Organization{Slug: "device-org", Name: "Devices", CreatedByUserID: user.ID}
	if err := db.Create(&org).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.OrganizationMember{OrganizationID: org.ID, UserID: user.ID, Status: model.OrganizationMemberStatusActive}).Error; err != nil {
		t.Fatal(err)
	}
	task := model.EnterpriseTask{OrganizationID: org.ID, CreatedByUserID: user.ID, OwnerUserID: user.ID, Title: "Task"}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	device := model.EnterpriseDevice{OrganizationID: org.ID, ExternalDeviceID: "device-1", Name: "Workstation"}
	if err := db.Create(&device).Error; err != nil {
		t.Fatal(err)
	}
	assignment, err := AssignEnterpriseDevice(db, EnterpriseDeviceAssignmentInput{OrganizationID: org.ID, DeviceID: device.ID, TaskID: &task.ID, AllowedTools: []string{"files.read", "files.read"}, AssignedBy: user.ID, ExpiresAt: timePtr(time.Now().Add(time.Hour))})
	if err != nil {
		t.Fatal(err)
	}
	items, err := ActiveEnterpriseDeviceAssignments(db, org.ID, user.ID, nil, &task.ID)
	if err != nil || len(items) != 1 {
		t.Fatalf("active assignments: %d, %v", len(items), err)
	}
	if err := RevokeEnterpriseDeviceAssignment(db, org.ID, assignment.ID, user.ID); err != nil {
		t.Fatal(err)
	}
	items, err = ActiveEnterpriseDeviceAssignments(db, org.ID, user.ID, nil, &task.ID)
	if err != nil || len(items) != 0 {
		t.Fatalf("revoked assignments: %d, %v", len(items), err)
	}
}

func timePtr(value time.Time) *time.Time { return &value }
