package service

import (
	"errors"
	"testing"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestPersonalCompanyWorkQueueLeasesAndRecovers(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:personal-company-dispatch-test?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&model.PersonalCompany{}, &model.CompanyWorkItem{}, &model.CompanyWorkAttempt{}, &model.CompanySignal{}, &model.CompanyOutboxEvent{}, &model.CompanyAuditEvent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	company := model.PersonalCompany{OwnerUserID: 7, Name: "Studio", State: model.PersonalCompanyStateOperating}
	if err := db.Create(&company).Error; err != nil {
		t.Fatalf("create company: %v", err)
	}
	work := model.CompanyWorkItem{PersonalCompanyID: company.ID, OwnerUserID: 7, Title: "Research", DefinitionOfDone: "Cited result", Status: model.CompanyWorkStatusPlanned, RiskLevel: "r0", IdempotencyKey: "dispatch-test"}
	if err := db.Create(&work).Error; err != nil {
		t.Fatalf("create work: %v", err)
	}
	if err := QueuePersonalCompanyWorkItem(db, company, work.ID, 7); err != nil {
		t.Fatalf("queue work: %v", err)
	}
	if err := QueuePersonalCompanyWorkItem(db, company, work.ID, 7); !errors.Is(err, ErrPersonalCompanyWorkNotQueueable) {
		t.Fatalf("second queue error = %v, want not queueable", err)
	}
	var signals, outbox int64
	if err := db.Model(&model.CompanySignal{}).Count(&signals).Error; err != nil || signals != 1 {
		t.Fatalf("signals = %d, error = %v", signals, err)
	}
	if err := db.Model(&model.CompanyOutboxEvent{}).Count(&outbox).Error; err != nil || outbox != 1 {
		t.Fatalf("outbox = %d, error = %v", outbox, err)
	}
	now := time.Now().UTC()
	attempt, err := LeaseNextPersonalCompanyWorkItem(db, company.ID, now, time.Minute)
	if err != nil {
		t.Fatalf("lease work: %v", err)
	}
	if err := RenewPersonalCompanyWorkLease(db, attempt.ID, attempt.LeaseToken, now, 2*time.Minute); err != nil {
		t.Fatalf("renew lease: %v", err)
	}
	if count, err := RecoverExpiredPersonalCompanyWorkLeases(db, company.ID, now.Add(time.Minute), 1); err != nil || count != 0 {
		t.Fatalf("early recovery = %d, %v", count, err)
	}
	if count, err := RecoverExpiredPersonalCompanyWorkLeases(db, company.ID, now.Add(3*time.Minute), 1); err != nil || count != 1 {
		t.Fatalf("first recovery = %d, %v", count, err)
	}
	attempt, err = LeaseNextPersonalCompanyWorkItem(db, company.ID, now.Add(3*time.Minute), time.Minute)
	if err != nil {
		t.Fatalf("second lease: %v", err)
	}
	if count, err := RecoverExpiredPersonalCompanyWorkLeases(db, company.ID, now.Add(5*time.Minute), 1); err != nil || count != 1 {
		t.Fatalf("dead-letter recovery = %d, %v", count, err)
	}
	if err := db.First(&work, work.ID).Error; err != nil {
		t.Fatalf("reload work: %v", err)
	}
	if work.Status != model.CompanyWorkStatusDeadLetter {
		t.Fatalf("work status = %q, want dead_letter", work.Status)
	}
}
