package service

import (
	"errors"
	"fmt"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"gorm.io/gorm"
)

var (
	ErrPersonalCompanyWorkNotQueueable = errors.New("personal company work item is not queueable")
	ErrPersonalCompanyNoQueuedWork     = errors.New("no queued personal company work")
)

// RenewPersonalCompanyWorkLease is the worker heartbeat. The token condition
// prevents a stale process from extending a lease reclaimed by another worker.
func RenewPersonalCompanyWorkLease(db *gorm.DB, attemptID uint, leaseToken string, now time.Time, leaseDuration time.Duration) error {
	if db == nil || attemptID == 0 || leaseToken == "" || leaseDuration <= 0 {
		return ErrPersonalCompanyNoQueuedWork
	}
	expiresAt := now.Add(leaseDuration)
	result := db.Model(&model.CompanyWorkAttempt{}).Where("id = ? AND lease_token = ? AND status = ?", attemptID, leaseToken, model.CompanyWorkStatusExecuting).Update("lease_expires_at", expiresAt)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrPersonalCompanyNoQueuedWork
	}
	return nil
}

// RecoverExpiredPersonalCompanyWorkLeases requeues expired attempts until the
// limit is exhausted. It performs no external side effect and is safe to run
// after every worker restart.
func RecoverExpiredPersonalCompanyWorkLeases(db *gorm.DB, companyID uint, now time.Time, retryLimit int) (int, error) {
	if db == nil || companyID == 0 {
		return 0, ErrPersonalCompanyNoQueuedWork
	}
	if retryLimit < 0 {
		retryLimit = 0
	}
	var recovered int
	err := db.Transaction(func(tx *gorm.DB) error {
		var attempts []model.CompanyWorkAttempt
		if err := tx.Joins("JOIN company_work_items ON company_work_items.id = company_work_attempts.work_item_id").Where("company_work_items.personal_company_id = ? AND company_work_attempts.status = ? AND company_work_attempts.lease_expires_at < ?", companyID, model.CompanyWorkStatusExecuting, now).Find(&attempts).Error; err != nil {
			return err
		}
		for _, attempt := range attempts {
			var totalAttempts int64
			if err := tx.Model(&model.CompanyWorkAttempt{}).Where("work_item_id = ?", attempt.WorkItemID).Count(&totalAttempts).Error; err != nil {
				return err
			}
			attemptStatus, workStatus := model.CompanyWorkStatusRetryableFailure, model.CompanyWorkStatusQueued
			if int(totalAttempts) > retryLimit {
				attemptStatus, workStatus = model.CompanyWorkStatusDeadLetter, model.CompanyWorkStatusDeadLetter
			}
			if err := tx.Model(&model.CompanyWorkAttempt{}).Where("id = ? AND status = ? AND lease_expires_at < ?", attempt.ID, model.CompanyWorkStatusExecuting, now).Updates(map[string]interface{}{"status": attemptStatus, "finished_at": now}).Error; err != nil {
				return err
			}
			if err := tx.Model(&model.CompanyWorkItem{}).Where("id = ? AND status = ?", attempt.WorkItemID, model.CompanyWorkStatusExecuting).Update("status", workStatus).Error; err != nil {
				return err
			}
			if err := createPersonalCompanyAuditEvent(tx, companyID, &attempt.WorkItemID, "worker", 0, "work_attempt.lease_expired", fmt.Sprintf(`{"attempt_id":%d,"status":%q}`, attempt.ID, attemptStatus)); err != nil {
				return err
			}
			recovered++
		}
		return nil
	})
	return recovered, err
}

// QueuePersonalCompanyWorkItem moves an authorized low-risk work item into a
// durable queue. It records both a signal and an outbox event atomically; it
// intentionally does not invoke an agent or a tool.
func QueuePersonalCompanyWorkItem(db *gorm.DB, company model.PersonalCompany, workItemID, actorUserID uint) error {
	if db == nil || company.ID == 0 || actorUserID == 0 {
		return ErrPersonalCompanyWorkNotQueueable
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var workItem model.CompanyWorkItem
		if err := tx.Where("id = ? AND personal_company_id = ? AND owner_user_id = ?", workItemID, company.ID, actorUserID).First(&workItem).Error; err != nil {
			return err
		}
		if company.State != model.PersonalCompanyStateOperating || !personalCompanyWorkMayQueue(workItem) {
			return ErrPersonalCompanyWorkNotQueueable
		}
		result := tx.Model(&model.CompanyWorkItem{}).Where("id = ? AND status IN ?", workItem.ID, []string{model.CompanyWorkStatusPlanned, model.CompanyWorkStatusAuthorized}).Update("status", model.CompanyWorkStatusQueued)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrPersonalCompanyWorkNotQueueable
		}
		if err := enqueuePersonalCompanySignal(tx, company, &workItem.ID, "work_item", "work_item.queued", fmt.Sprintf(`{"work_item_id":%d}`, workItem.ID)); err != nil {
			return err
		}
		return createPersonalCompanyAuditEvent(tx, company.ID, &workItem.ID, "owner", actorUserID, "work_item.queued", `{}`)
	})
}

// LeaseNextPersonalCompanyWorkItem creates a single recoverable attempt.
// Expired leases may be reclaimed by a later worker; completed side effects
// still require their own idempotency keys before a runtime is connected.
func LeaseNextPersonalCompanyWorkItem(db *gorm.DB, companyID uint, now time.Time, leaseDuration time.Duration) (model.CompanyWorkAttempt, error) {
	if db == nil || companyID == 0 || leaseDuration <= 0 {
		return model.CompanyWorkAttempt{}, ErrPersonalCompanyNoQueuedWork
	}
	var leased model.CompanyWorkAttempt
	err := db.Transaction(func(tx *gorm.DB) error {
		var workItem model.CompanyWorkItem
		if err := tx.Where("personal_company_id = ? AND status = ?", companyID, model.CompanyWorkStatusQueued).Order("priority DESC, created_at ASC").First(&workItem).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPersonalCompanyNoQueuedWork
			}
			return err
		}
		result := tx.Model(&model.CompanyWorkItem{}).Where("id = ? AND status = ?", workItem.ID, model.CompanyWorkStatusQueued).Update("status", model.CompanyWorkStatusExecuting)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrPersonalCompanyNoQueuedWork
		}
		var attemptCount int64
		if err := tx.Model(&model.CompanyWorkAttempt{}).Where("work_item_id = ?", workItem.ID).Count(&attemptCount).Error; err != nil {
			return err
		}
		startedAt := now
		expiresAt := now.Add(leaseDuration)
		leased = model.CompanyWorkAttempt{WorkItemID: workItem.ID, AttemptNumber: int(attemptCount) + 1, Kind: model.CompanyWorkAttemptKindExecution, Status: model.CompanyWorkStatusExecuting, LeaseToken: newPersonalCompanyID("lease"), LeaseExpiresAt: &expiresAt, StartedAt: &startedAt, InputSnapshot: workItem.InputSnapshot}
		if err := tx.Create(&leased).Error; err != nil {
			return err
		}
		return createPersonalCompanyAuditEvent(tx, companyID, &workItem.ID, "worker", 0, "work_attempt.leased", fmt.Sprintf(`{"attempt_id":%d}`, leased.ID))
	})
	return leased, err
}

func personalCompanyWorkMayQueue(workItem model.CompanyWorkItem) bool {
	switch workItem.RiskLevel {
	case "r0", "r1", "r2":
		return workItem.Status == model.CompanyWorkStatusPlanned || workItem.Status == model.CompanyWorkStatusAuthorized
	case "r3":
		return workItem.Status == model.CompanyWorkStatusAuthorized
	default:
		return false
	}
}
