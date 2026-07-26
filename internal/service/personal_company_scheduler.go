package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
	"gorm.io/gorm"
)

const personalCompanyChiefScheduleInterval = 10 * time.Second

var personalCompanyChiefSchedulerOnce sync.Once

func init() {
	RegisterStartupHook(startPersonalCompanyChiefScheduler)
}

// startPersonalCompanyChiefScheduler is the single control loop for an
// operating Studio. It consumes durable signals and advances internal work up
// to the Studio's configured concurrency limit.
func startPersonalCompanyChiefScheduler() error {
	personalCompanyChiefSchedulerOnce.Do(func() {
		// A single control loop per cluster, matching this scheduler's contract.
		RegisterScheduledJob(ScheduledJob{
			Name:        "personal_company_scheduler",
			Description: "推进运行中 Studio 的内部工作流",
			PrimaryOnly: true,
			Interval:    func() time.Duration { return personalCompanyChiefScheduleInterval },
			Run: func(ctx context.Context) (string, bool, error) {
				// Routine ticks stay out of the run log: this fires every 10s
				// whenever any Studio is operating.
				runPersonalCompanyChiefSchedule(ctx)
				return "", false, nil
			},
		})
	})
	return nil
}

func runPersonalCompanyChiefSchedule(ctx context.Context) {
	if model.DB == nil {
		return
	}
	var companies []model.PersonalCompany
	if err := model.DB.WithContext(ctx).Where("state = ?", model.PersonalCompanyStateOperating).Find(&companies).Error; err != nil {
		return
	}
	for _, company := range companies {
		_ = runPersonalCompanyChiefScheduleForCompany(ctx, company)
	}
}

func runPersonalCompanyChiefScheduleForCompany(ctx context.Context, company model.PersonalCompany) error {
	if company.ID == 0 || company.OwnerUserID == 0 || company.State != model.PersonalCompanyStateOperating {
		return nil
	}
	_, _ = RecoverExpiredPersonalCompanyWorkLeases(model.DB, company.ID, time.Now().UTC(), 2)
	if err := consumePersonalCompanyChiefSignals(ctx, company); err != nil {
		return err
	}
	active, err := personalCompanyActiveAttemptCount(ctx, company.ID)
	if err != nil {
		return err
	}
	maxConcurrent := normalizePersonalCompanyMaxConcurrentTasks(company.MaxConcurrentTasks)
	slots := maxConcurrent - active

	var verified model.CompanyWorkItem
	err = model.DB.WithContext(ctx).Where("personal_company_id = ? AND status = ?", company.ID, model.CompanyWorkStatusVerified).Order("updated_at ASC").First(&verified).Error
	if err == nil {
		return model.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&model.CompanyWorkItem{}).Where("id = ? AND status = ?", verified.ID, model.CompanyWorkStatusVerified).Update("status", model.CompanyWorkStatusDelivered).Error; err != nil {
				return err
			}
			if err := enqueuePersonalCompanySignal(tx, company, &verified.ID, "chief_scheduler", "work_item.delivered", fmt.Sprintf(`{"work_item_id":%d}`, verified.ID)); err != nil {
				return err
			}
			return createPersonalCompanyAuditEvent(tx, company.ID, &verified.ID, "chief", 0, "work_item.delivered", `{}`)
		})
	}
	if err != nil && err != gorm.ErrRecordNotFound {
		return err
	}
	for slots > 0 {
		var review model.CompanyWorkItem
		err = model.DB.WithContext(ctx).Where("personal_company_id = ? AND status = ?", company.ID, model.CompanyWorkStatusAwaitingReview).Order("priority DESC, created_at ASC").First(&review).Error
		if err == nil {
			if _, _, err = startPersonalCompanyReviewRun(company, company.OwnerUserID, review.ID); err != nil {
				return err
			}
			slots--
			continue
		}
		if err != gorm.ErrRecordNotFound {
			return err
		}
		var work model.CompanyWorkItem
		err = model.DB.WithContext(ctx).Where("personal_company_id = ? AND status = ?", company.ID, model.CompanyWorkStatusQueued).Order("priority DESC, created_at ASC").First(&work).Error
		if err == gorm.ErrRecordNotFound {
			return nil
		}
		if err != nil {
			return err
		}
		if _, _, err = startPersonalCompanyWorkRun(company, company.OwnerUserID, work.ID); err != nil {
			return err
		}
		slots--
	}
	return nil
}

func normalizePersonalCompanyMaxConcurrentTasks(value int) int {
	if value < 1 {
		return 1
	}
	if value > 8 {
		return 8
	}
	return value
}

func personalCompanyActiveAttemptCount(ctx context.Context, companyID uint) (int, error) {
	var count int64
	err := model.DB.WithContext(ctx).Table("company_work_attempts AS attempts").
		Joins("JOIN company_work_items AS work ON work.id = attempts.work_item_id").
		Where("work.personal_company_id = ? AND attempts.status = ?", companyID, model.CompanyWorkStatusExecuting).
		Count(&count).Error
	return int(count), err
}

func consumePersonalCompanyChiefSignals(ctx context.Context, company model.PersonalCompany) error {
	return model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var signals []model.CompanySignal
		if err := tx.Where("personal_company_id = ? AND status = ?", company.ID, model.CompanySignalStatusInbox).Order("created_at ASC").Limit(100).Find(&signals).Error; err != nil {
			return err
		}
		for _, signal := range signals {
			if err := tx.Model(&model.CompanySignal{}).Where("id = ? AND status = ?", signal.ID, model.CompanySignalStatusInbox).Update("status", model.CompanySignalStatusTriaged).Error; err != nil {
				return err
			}
			if err := createPersonalCompanyAuditEvent(tx, company.ID, signal.WorkItemID, "chief", 0, "chief.signal_received", fmt.Sprintf(`{"signal_id":%d,"source":%q}`, signal.ID, signal.Source)); err != nil {
				return err
			}
		}
		return nil
	})
}

func enqueuePersonalCompanySignal(tx *gorm.DB, company model.PersonalCompany, workItemID *uint, source, eventType, payload string) error {
	key := fmt.Sprintf("chief:%s:%s", source, newPersonalCompanyID("signal"))
	signal := model.CompanySignal{PersonalCompanyID: company.ID, OwnerUserID: company.OwnerUserID, Source: source, DeduplicationKey: key, Payload: payload, Status: model.CompanySignalStatusInbox, WorkItemID: workItemID}
	if err := tx.Create(&signal).Error; err != nil {
		return err
	}
	outbox := model.CompanyOutboxEvent{PersonalCompanyID: company.ID, EventKey: key, EventType: eventType, Payload: payload, Status: model.CompanyOutboxStatusPending}
	return tx.Create(&outbox).Error
}

// interruptPersonalCompanyWork makes pausing a Studio a hard execution
// boundary. Attempts are first made non-runnable, then their model runs are
// cancelled so a concurrent completion cannot publish a late result.
func interruptPersonalCompanyWork(company model.PersonalCompany) error {
	if company.ID == 0 {
		return nil
	}
	type activeAttempt struct {
		ID                uint
		WorkItemID        uint
		Kind              string
		AdvancedChatRunID string
	}
	attempts := []activeAttempt{}
	if err := model.DB.Table("company_work_attempts AS attempts").
		Select("attempts.id, attempts.work_item_id, attempts.kind, attempts.advanced_chat_run_id").
		Joins("JOIN company_work_items AS work ON work.id = attempts.work_item_id").
		Where("work.personal_company_id = ? AND attempts.status = ?", company.ID, model.CompanyWorkStatusExecuting).
		Find(&attempts).Error; err != nil {
		return err
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		for _, attempt := range attempts {
			if err := tx.Model(&model.CompanyWorkAttempt{}).Where("id = ? AND status = ?", attempt.ID, model.CompanyWorkStatusExecuting).Updates(map[string]interface{}{"status": model.CompanyWorkStatusCancelled, "finished_at": now, "result_summary": "Studio operations paused"}).Error; err != nil {
				return err
			}
			nextStatus := model.CompanyWorkStatusQueued
			if attempt.Kind == model.CompanyWorkAttemptKindReview {
				nextStatus = model.CompanyWorkStatusAwaitingReview
			}
			if err := tx.Model(&model.CompanyWorkItem{}).Where("id = ? AND status = ?", attempt.WorkItemID, model.CompanyWorkStatusExecuting).Update("status", nextStatus).Error; err != nil {
				return err
			}
			if err := enqueuePersonalCompanySignal(tx, company, &attempt.WorkItemID, "company_pause", "work_item."+nextStatus, fmt.Sprintf(`{"attempt_id":%d}`, attempt.ID)); err != nil {
				return err
			}
			if err := createPersonalCompanyAuditEvent(tx, company.ID, &attempt.WorkItemID, "owner", company.OwnerUserID, "work_attempt.interrupted", fmt.Sprintf(`{"attempt_id":%d}`, attempt.ID)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	for _, attempt := range attempts {
		if attempt.AdvancedChatRunID != "" {
			_, _, _, _ = stopAdvancedChatRun(attempt.AdvancedChatRunID, company.OwnerUserID)
		}
	}
	return nil
}
