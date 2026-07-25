package service

import (
	"context"
	"testing"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestPersonalCompanyChiefSchedulerDeliversVerifiedWork(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:personal-company-chief-scheduler-test?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&model.PersonalCompany{}, &model.CompanyWorkItem{}, &model.CompanyWorkAttempt{}, &model.CompanySignal{}, &model.CompanyOutboxEvent{}, &model.CompanyAuditEvent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	company := model.PersonalCompany{OwnerUserID: 17, AgentGroupID: "studio", Name: "Studio", State: model.PersonalCompanyStateOperating}
	if err := db.Create(&company).Error; err != nil {
		t.Fatalf("create company: %v", err)
	}
	work := model.CompanyWorkItem{PersonalCompanyID: company.ID, OwnerUserID: company.OwnerUserID, Title: "Verified", DefinitionOfDone: "Done", Status: model.CompanyWorkStatusVerified, RiskLevel: "r0", IdempotencyKey: "verified-work"}
	if err := db.Create(&work).Error; err != nil {
		t.Fatalf("create work: %v", err)
	}
	previous := model.DB
	model.DB = db
	defer func() { model.DB = previous }()
	if err := runPersonalCompanyChiefScheduleForCompany(context.Background(), company); err != nil {
		t.Fatalf("run chief schedule: %v", err)
	}
	if err := db.First(&work, work.ID).Error; err != nil {
		t.Fatalf("reload work: %v", err)
	}
	if work.Status != model.CompanyWorkStatusDelivered {
		t.Fatalf("work status = %q, want delivered", work.Status)
	}
}

func TestNormalizePersonalCompanyMaxConcurrentTasks(t *testing.T) {
	if got := normalizePersonalCompanyMaxConcurrentTasks(0); got != 1 {
		t.Fatalf("zero concurrency = %d, want 1", got)
	}
	if got := normalizePersonalCompanyMaxConcurrentTasks(3); got != 3 {
		t.Fatalf("configured concurrency = %d, want 3", got)
	}
	if got := normalizePersonalCompanyMaxConcurrentTasks(9); got != 8 {
		t.Fatalf("high concurrency = %d, want 8", got)
	}
}

func TestInterruptPersonalCompanyWorkRequeuesExecution(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:personal-company-chief-pause-test?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&model.PersonalCompany{}, &model.CompanyWorkItem{}, &model.CompanyWorkAttempt{}, &model.CompanySignal{}, &model.CompanyOutboxEvent{}, &model.CompanyAuditEvent{}, &AdvancedChatRun{}, &AdvancedChatMessage{}, &AdvancedChatRunEvent{}, &AdvancedChatConnectorTask{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	company := model.PersonalCompany{OwnerUserID: 18, AgentGroupID: "studio", Name: "Studio", State: model.PersonalCompanyStatePaused}
	if err := db.Create(&company).Error; err != nil {
		t.Fatalf("create company: %v", err)
	}
	work := model.CompanyWorkItem{PersonalCompanyID: company.ID, OwnerUserID: company.OwnerUserID, Title: "Running", DefinitionOfDone: "Done", Status: model.CompanyWorkStatusExecuting, RiskLevel: "r0", IdempotencyKey: "running-work"}
	if err := db.Create(&work).Error; err != nil {
		t.Fatalf("create work: %v", err)
	}
	attempt := model.CompanyWorkAttempt{WorkItemID: work.ID, AttemptNumber: 1, Kind: model.CompanyWorkAttemptKindExecution, Status: model.CompanyWorkStatusExecuting}
	if err := db.Create(&attempt).Error; err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	previous := model.DB
	model.DB = db
	defer func() { model.DB = previous }()
	if err := interruptPersonalCompanyWork(company); err != nil {
		t.Fatalf("interrupt work: %v", err)
	}
	if err := db.First(&attempt, attempt.ID).Error; err != nil {
		t.Fatalf("reload attempt: %v", err)
	}
	if attempt.Status != model.CompanyWorkStatusCancelled {
		t.Fatalf("attempt status = %q, want cancelled", attempt.Status)
	}
	if err := db.First(&work, work.ID).Error; err != nil {
		t.Fatalf("reload work: %v", err)
	}
	if work.Status != model.CompanyWorkStatusQueued {
		t.Fatalf("work status = %q, want queued", work.Status)
	}
}
