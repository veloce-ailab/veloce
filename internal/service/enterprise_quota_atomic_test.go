package service

import (
	"errors"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/veloce-ailab/veloce/internal/model"
	"gorm.io/gorm"
)

func newQuotaAtomicDB(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.Organization{}, &model.EnterpriseTask{}, &model.QuotaAccount{}, &model.QuotaLedger{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func quotaAccountAmounts(t *testing.T, db *gorm.DB, accountID uint) model.QuotaAccount {
	t.Helper()
	var account model.QuotaAccount
	if err := db.First(&account, accountID).Error; err != nil {
		t.Fatal(err)
	}
	return account
}

// The parent's remaining budget must be enforced by the UPDATE itself, so a
// transfer that no longer fits is rejected instead of silently overdrawing.
func TestAllocateEnterpriseQuotaRejectsOverdraw(t *testing.T) {
	db := newQuotaAtomicDB(t, "file:enterprise-quota-atomic-allocate?mode=memory&cache=shared")
	org := model.Organization{Slug: "atomic", Name: "Atomic", CreatedByUserID: 1}
	if err := db.Create(&org).Error; err != nil {
		t.Fatal(err)
	}
	parent, err := EnsureEnterpriseQuotaAccount(db, EnterpriseQuotaScope{OrganizationID: org.ID, ScopeType: model.QuotaScopeOrganization})
	if err != nil {
		t.Fatal(err)
	}
	task := model.EnterpriseTask{OrganizationID: org.ID, CreatedByUserID: 1, OwnerUserID: 1, Title: "Task"}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	child, err := EnsureEnterpriseQuotaAccount(db, EnterpriseQuotaScope{OrganizationID: org.ID, ScopeType: model.QuotaScopeTask, TaskID: &task.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.QuotaAccount{}).Where("id = ?", parent.ID).Update("limit_amount", decimal.NewFromInt(10)).Error; err != nil {
		t.Fatal(err)
	}

	if err := AllocateEnterpriseQuota(db, parent.ID, child.ID, 1, decimal.NewFromInt(6), "first"); err != nil {
		t.Fatalf("first allocation error = %v, want nil", err)
	}
	// Only 4 remains, so a second transfer of 6 must fail rather than pushing the
	// parent negative or crediting the child twice.
	if err := AllocateEnterpriseQuota(db, parent.ID, child.ID, 1, decimal.NewFromInt(6), "second"); !errors.Is(err, ErrEnterpriseQuotaExceeded) {
		t.Fatalf("second allocation error = %v, want ErrEnterpriseQuotaExceeded", err)
	}

	parentAccount := quotaAccountAmounts(t, db, parent.ID)
	if !parentAccount.LimitAmount.Equal(decimal.NewFromInt(4)) {
		t.Fatalf("parent limit = %s, want 4", parentAccount.LimitAmount)
	}
	childAccount := quotaAccountAmounts(t, db, child.ID)
	if !childAccount.LimitAmount.Equal(decimal.NewFromInt(6)) {
		t.Fatalf("child limit = %s, want 6", childAccount.LimitAmount)
	}
}

// Releasing more than is reserved must be refused; otherwise reserved_amount
// goes negative and inflates the account's available quota.
func TestReleaseEnterpriseTaskQuotaCannotGoNegative(t *testing.T) {
	db := newQuotaAtomicDB(t, "file:enterprise-quota-atomic-release?mode=memory&cache=shared")
	org := model.Organization{Slug: "atomic-release", Name: "Atomic", CreatedByUserID: 1}
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
	if err := db.Model(&model.QuotaAccount{}).Where("id = ?", account.ID).Update("limit_amount", decimal.NewFromInt(10)).Error; err != nil {
		t.Fatal(err)
	}
	if err := ReserveEnterpriseTaskQuota(db, account.ID, task.ID, 1, decimal.NewFromInt(5), "reserve"); err != nil {
		t.Fatal(err)
	}

	if err := ReleaseEnterpriseTaskQuota(db, account.ID, task.ID, 1, decimal.NewFromInt(5), "release"); err != nil {
		t.Fatalf("release error = %v, want nil", err)
	}
	// The reservation is already gone; a replayed release must not subtract again.
	if err := ReleaseEnterpriseTaskQuota(db, account.ID, task.ID, 1, decimal.NewFromInt(5), "replay"); !errors.Is(err, ErrEnterpriseQuotaExceeded) {
		t.Fatalf("replayed release error = %v, want ErrEnterpriseQuotaExceeded", err)
	}

	settled := quotaAccountAmounts(t, db, account.ID)
	if settled.ReservedAmount.IsNegative() {
		t.Fatalf("reserved amount = %s, want it never negative", settled.ReservedAmount)
	}
	if !settled.ReservedAmount.IsZero() {
		t.Fatalf("reserved amount = %s, want 0", settled.ReservedAmount)
	}
}

func TestConsumeEnterpriseTaskQuotaRequiresReservation(t *testing.T) {
	db := newQuotaAtomicDB(t, "file:enterprise-quota-atomic-consume?mode=memory&cache=shared")
	org := model.Organization{Slug: "atomic-consume", Name: "Atomic", CreatedByUserID: 1}
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
	if err := db.Model(&model.QuotaAccount{}).Where("id = ?", account.ID).Update("limit_amount", decimal.NewFromInt(10)).Error; err != nil {
		t.Fatal(err)
	}
	if err := ReserveEnterpriseTaskQuota(db, account.ID, task.ID, 1, decimal.NewFromInt(3), "reserve"); err != nil {
		t.Fatal(err)
	}

	if err := ConsumeEnterpriseTaskQuota(db, account.ID, task.ID, 1, decimal.NewFromInt(4), "over"); !errors.Is(err, ErrEnterpriseQuotaExceeded) {
		t.Fatalf("consuming more than reserved error = %v, want ErrEnterpriseQuotaExceeded", err)
	}
	settled := quotaAccountAmounts(t, db, account.ID)
	if !settled.ReservedAmount.Equal(decimal.NewFromInt(3)) || !settled.ConsumedAmount.IsZero() {
		t.Fatalf("account amounts = reserved %s consumed %s, want 3 and 0", settled.ReservedAmount, settled.ConsumedAmount)
	}
}
