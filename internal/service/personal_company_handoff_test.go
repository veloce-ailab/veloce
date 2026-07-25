package service

import (
	"errors"
	"testing"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestPersonalCompanyHandoffRequiresExplicitDecision(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:personal-company-handoff-test?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&model.PersonalCompany{}, &model.CompanyWorkItem{}, &model.PersonalCompanyEmployee{}, &model.CompanyWorkAttempt{}, &model.CompanyHandoffPackage{}, &model.CompanyOutboxEvent{}, &model.CompanyAuditEvent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	company := model.PersonalCompany{OwnerUserID: 8, Name: "Studio", State: model.PersonalCompanyStateOperating}
	if err := db.Create(&company).Error; err != nil {
		t.Fatalf("create company: %v", err)
	}
	from := model.PersonalCompanyEmployee{PersonalCompanyID: company.ID, EmployeeKey: "researcher", Name: "Research", Role: "research", Status: model.PersonalCompanyEmployeeActive}
	to := model.PersonalCompanyEmployee{PersonalCompanyID: company.ID, EmployeeKey: "reviewer", Name: "Review", Role: "reviewer", Status: model.PersonalCompanyEmployeeActive}
	if err := db.Create(&from).Error; err != nil {
		t.Fatalf("create source employee: %v", err)
	}
	if err := db.Create(&to).Error; err != nil {
		t.Fatalf("create receiving employee: %v", err)
	}
	work := model.CompanyWorkItem{PersonalCompanyID: company.ID, OwnerUserID: company.OwnerUserID, Title: "Research", DefinitionOfDone: "Cited result", Status: model.CompanyWorkStatusAwaitingReview, RiskLevel: "r0", IdempotencyKey: "handoff-test"}
	if err := db.Create(&work).Error; err != nil {
		t.Fatalf("create work: %v", err)
	}
	attempt := model.CompanyWorkAttempt{WorkItemID: work.ID, AttemptNumber: 1, Status: model.CompanyWorkStatusAwaitingReview}
	if err := db.Create(&attempt).Error; err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	handoff, err := CreatePersonalCompanyHandoff(db, company, company.OwnerUserID, work.ID, personalCompanyHandoffInput{WorkAttemptID: &attempt.ID, FromEmployeeID: &from.ID, ToEmployeeID: to.ID, CompletionSummary: "Research package", Evidence: `["https://example.test/source"]`, Risks: `["Need verification"]`, NextSteps: "Review citations"})
	if err != nil {
		t.Fatalf("create handoff: %v", err)
	}
	if handoff.Status != model.CompanyHandoffStatusPending {
		t.Fatalf("handoff status = %q, want pending", handoff.Status)
	}
	if _, err := CreatePersonalCompanyHandoff(db, company, company.OwnerUserID, work.ID, personalCompanyHandoffInput{ToEmployeeID: to.ID, CompletionSummary: "Missing JSON", Evidence: "not-json", NextSteps: "Review"}); !errors.Is(err, ErrPersonalCompanyHandoffInvalid) {
		t.Fatalf("invalid handoff error = %v, want invalid", err)
	}
	handoff, err = DecidePersonalCompanyHandoff(db, company.ID, company.OwnerUserID, handoff.ID, model.CompanyHandoffStatusAccepted, "Ready to review")
	if err != nil {
		t.Fatalf("accept handoff: %v", err)
	}
	if handoff.Status != model.CompanyHandoffStatusAccepted || handoff.AcceptedAt == nil {
		t.Fatalf("accepted handoff = %#v", handoff)
	}
	if _, err := DecidePersonalCompanyHandoff(db, company.ID, company.OwnerUserID, handoff.ID, model.CompanyHandoffStatusRejected, "too late"); !errors.Is(err, ErrPersonalCompanyHandoffDecided) {
		t.Fatalf("second decision error = %v, want already decided", err)
	}
	var reloadedWork model.CompanyWorkItem
	if err := db.First(&reloadedWork, work.ID).Error; err != nil {
		t.Fatalf("reload work: %v", err)
	}
	if reloadedWork.Status != model.CompanyWorkStatusAwaitingReview {
		t.Fatalf("work status = %q, handoff decision must not advance it", reloadedWork.Status)
	}
	var outboxCount int64
	if err := db.Model(&model.CompanyOutboxEvent{}).Count(&outboxCount).Error; err != nil || outboxCount != 2 {
		t.Fatalf("outbox events = %d, error = %v", outboxCount, err)
	}
}
