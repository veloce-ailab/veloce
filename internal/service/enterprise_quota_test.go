package service

import (
	"errors"
	"testing"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func TestEnterpriseTaskQuotaReservationConsumptionAndRelease(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:enterprise-quota-service?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Organization{}, &model.EnterpriseTask{}, &model.QuotaAccount{}, &model.QuotaLedger{}); err != nil {
		t.Fatal(err)
	}
	org := model.Organization{Slug: "quota-service", Name: "Quota", CreatedByUserID: 1}
	if err := db.Create(&org).Error; err != nil {
		t.Fatal(err)
	}
	task := model.EnterpriseTask{OrganizationID: org.ID, CreatedByUserID: 1, OwnerUserID: 1, Title: "Task"}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	account, err := EnsureEnterpriseQuotaAccount(db, EnterpriseQuotaScope{OrganizationID: org.ID, ScopeType: model.QuotaScopeTask, TaskID: &task.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&account).Update("limit_amount", decimal.NewFromInt(10)).Error; err != nil {
		t.Fatal(err)
	}
	if err := ReserveEnterpriseTaskQuota(db, account.ID, task.ID, 1, decimal.NewFromInt(7), "reserve"); err != nil {
		t.Fatal(err)
	}
	if err := ConsumeEnterpriseTaskQuota(db, account.ID, task.ID, 1, decimal.NewFromInt(4), "consume"); err != nil {
		t.Fatal(err)
	}
	if err := ReleaseEnterpriseTaskQuota(db, account.ID, task.ID, 1, decimal.NewFromInt(3), "release"); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&account, account.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !account.ReservedAmount.IsZero() || !account.ConsumedAmount.Equal(decimal.NewFromInt(4)) {
		t.Fatalf("unexpected account %+v", account)
	}
	if err := ReserveEnterpriseTaskQuota(db, account.ID, task.ID, 1, decimal.NewFromInt(7), "overflow"); !errors.Is(err, ErrEnterpriseQuotaExceeded) {
		t.Fatalf("expected quota exceeded, got %v", err)
	}
}

func TestEnterprisePoolQuotaUsesPoolAsSubjectAndEmployeeAsActor(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:enterprise-pool-quota-service?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Organization{}, &model.EnterpriseSharedPool{}, &model.QuotaAccount{}, &model.QuotaLedger{}); err != nil {
		t.Fatal(err)
	}
	org := model.Organization{Slug: "pool-quota-service", Name: "Pool quota", CreatedByUserID: 1}
	if err := db.Create(&org).Error; err != nil {
		t.Fatal(err)
	}
	pool := model.EnterpriseSharedPool{OrganizationID: org.ID, ScopeType: model.EnterprisePoolScopeTask, ScopeKey: "task:1", Name: "Task pool", CreatedByUserID: 1}
	if err := db.Create(&pool).Error; err != nil {
		t.Fatal(err)
	}
	account, err := EnsureEnterpriseQuotaAccount(db, EnterpriseQuotaScope{OrganizationID: org.ID, ScopeType: model.QuotaScopePool, PoolID: &pool.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&account).Update("limit_amount", decimal.NewFromInt(10)).Error; err != nil {
		t.Fatal(err)
	}
	if err := ReserveEnterprisePoolQuota(db, account.ID, pool.ID, 42, decimal.NewFromInt(7), "reserve"); err != nil {
		t.Fatal(err)
	}
	if err := ConsumeEnterprisePoolQuota(db, account.ID, pool.ID, 42, decimal.NewFromInt(4), "consume"); err != nil {
		t.Fatal(err)
	}
	if err := ReserveEnterprisePoolQuota(db, account.ID, pool.ID, 42, decimal.NewFromInt(7), "overflow"); !errors.Is(err, ErrEnterpriseQuotaExceeded) {
		t.Fatalf("expected quota exceeded, got %v", err)
	}
	var ledger model.QuotaLedger
	if err := db.Where("account_id = ? AND entry_type = ?", account.ID, model.QuotaLedgerConsumption).First(&ledger).Error; err != nil {
		t.Fatal(err)
	}
	if ledger.PoolID == nil || *ledger.PoolID != pool.ID || ledger.CreatedByUserID != 42 {
		t.Fatalf("unexpected pool ledger %+v", ledger)
	}
}
