package service

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

var (
	ErrEnterpriseQuotaExceeded = errors.New("enterprise quota exceeded")
	ErrInvalidQuotaScope       = errors.New("invalid quota scope")
)

type EnterpriseQuotaScope struct {
	OrganizationID uint
	ScopeType      string
	DepartmentID   *uint
	UserID         *uint
	TaskID         *uint
	PoolID         *uint
}

func (scope EnterpriseQuotaScope) normalized() (EnterpriseQuotaScope, error) {
	scope.ScopeType = strings.ToLower(strings.TrimSpace(scope.ScopeType))
	if scope.OrganizationID == 0 {
		return scope, ErrInvalidQuotaScope
	}
	count := 0
	if scope.DepartmentID != nil && *scope.DepartmentID != 0 {
		count++
	}
	if scope.UserID != nil && *scope.UserID != 0 {
		count++
	}
	if scope.TaskID != nil && *scope.TaskID != 0 {
		count++
	}
	if scope.PoolID != nil && *scope.PoolID != 0 {
		count++
	}
	switch scope.ScopeType {
	case model.QuotaScopeOrganization:
		if count != 0 {
			return scope, ErrInvalidQuotaScope
		}
	case model.QuotaScopeDepartment:
		if count != 1 || scope.DepartmentID == nil {
			return scope, ErrInvalidQuotaScope
		}
	case model.QuotaScopeUser:
		if count != 1 || scope.UserID == nil {
			return scope, ErrInvalidQuotaScope
		}
	case model.QuotaScopeTask:
		if count != 1 || scope.TaskID == nil {
			return scope, ErrInvalidQuotaScope
		}
	case model.QuotaScopePool:
		if count != 1 || scope.PoolID == nil {
			return scope, ErrInvalidQuotaScope
		}
	default:
		return scope, ErrInvalidQuotaScope
	}
	return scope, nil
}

func (scope EnterpriseQuotaScope) key() string {
	switch scope.ScopeType {
	case model.QuotaScopeDepartment:
		return "department:" + strconv.FormatUint(uint64(*scope.DepartmentID), 10)
	case model.QuotaScopeUser:
		return "user:" + strconv.FormatUint(uint64(*scope.UserID), 10)
	case model.QuotaScopeTask:
		return "task:" + strconv.FormatUint(uint64(*scope.TaskID), 10)
	case model.QuotaScopePool:
		return "pool:" + strconv.FormatUint(uint64(*scope.PoolID), 10)
	default:
		return "organization"
	}
}

func EnsureEnterpriseQuotaAccount(db *gorm.DB, scope EnterpriseQuotaScope) (model.QuotaAccount, error) {
	scope, err := scope.normalized()
	if err != nil {
		return model.QuotaAccount{}, err
	}
	account := model.QuotaAccount{OrganizationID: scope.OrganizationID, ScopeType: scope.ScopeType, ScopeKey: scope.key(), DepartmentID: scope.DepartmentID, UserID: scope.UserID, TaskID: scope.TaskID, PoolID: scope.PoolID}
	err = db.Where("organization_id = ? AND scope_type = ? AND scope_key = ?", account.OrganizationID, account.ScopeType, account.ScopeKey).FirstOrCreate(&account).Error
	return account, err
}

// AllocateEnterpriseQuota moves allocatable capacity from a parent account to
// a child account. The parent is never allowed below its consumed + reserved
// amount, which prevents departments or employees from overspending a budget.
func AllocateEnterpriseQuota(db *gorm.DB, parentID, childID, actorID uint, amount decimal.Decimal, referenceID string) error {
	if db == nil || parentID == 0 || childID == 0 || actorID == 0 || amount.LessThanOrEqual(decimal.Zero) || parentID == childID {
		return ErrInvalidQuotaScope
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var parent, child model.QuotaAccount
		if err := tx.First(&parent, parentID).Error; err != nil {
			return err
		}
		if err := tx.First(&child, childID).Error; err != nil {
			return err
		}
		if parent.OrganizationID != child.OrganizationID {
			return ErrInvalidQuotaScope
		}
		available := parent.LimitAmount.Sub(parent.ReservedAmount).Sub(parent.ConsumedAmount)
		if amount.GreaterThan(available) {
			return ErrEnterpriseQuotaExceeded
		}
		if err := tx.Model(&parent).Update("limit_amount", parent.LimitAmount.Sub(amount)).Error; err != nil {
			return err
		}
		if err := tx.Model(&child).Update("limit_amount", child.LimitAmount.Add(amount)).Error; err != nil {
			return err
		}
		return tx.Create([]model.QuotaLedger{
			{OrganizationID: parent.OrganizationID, AccountID: parent.ID, EntryType: model.QuotaLedgerAllocation, Amount: amount.Neg(), ReferenceType: "quota_allocation", ReferenceID: referenceID, CreatedByUserID: actorID},
			{OrganizationID: child.OrganizationID, AccountID: child.ID, EntryType: model.QuotaLedgerAllocation, Amount: amount, ReferenceType: "quota_allocation", ReferenceID: referenceID, CreatedByUserID: actorID},
		}).Error
	})
}

func ReserveEnterpriseTaskQuota(db *gorm.DB, accountID, taskID, actorID uint, amount decimal.Decimal, referenceID string) error {
	return enterpriseQuotaAdjust(db, accountID, taskID, actorID, amount, model.QuotaLedgerReservation, referenceID)
}

func ConsumeEnterpriseTaskQuota(db *gorm.DB, accountID, taskID, actorID uint, amount decimal.Decimal, referenceID string) error {
	if amount.LessThanOrEqual(decimal.Zero) {
		return ErrInvalidQuotaScope
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var account model.QuotaAccount
		if err := tx.First(&account, accountID).Error; err != nil {
			return err
		}
		if account.TaskID == nil || *account.TaskID != taskID || account.ReservedAmount.LessThan(amount) {
			return ErrEnterpriseQuotaExceeded
		}
		if err := tx.Model(&account).Updates(map[string]interface{}{"reserved_amount": account.ReservedAmount.Sub(amount), "consumed_amount": account.ConsumedAmount.Add(amount)}).Error; err != nil {
			return err
		}
		return tx.Create(&model.QuotaLedger{OrganizationID: account.OrganizationID, AccountID: account.ID, TaskID: &taskID, EntryType: model.QuotaLedgerConsumption, Amount: amount, ReferenceType: "quota_consumption", ReferenceID: referenceID, CreatedByUserID: actorID}).Error
	})
}

func ReleaseEnterpriseTaskQuota(db *gorm.DB, accountID, taskID, actorID uint, amount decimal.Decimal, referenceID string) error {
	return enterpriseQuotaAdjust(db, accountID, taskID, actorID, amount, model.QuotaLedgerRelease, referenceID)
}

func ReserveEnterprisePoolQuota(db *gorm.DB, accountID, poolID, actorID uint, amount decimal.Decimal, referenceID string) error {
	return enterprisePoolQuotaAdjust(db, accountID, poolID, actorID, amount, model.QuotaLedgerReservation, referenceID)
}

func ConsumeEnterprisePoolQuota(db *gorm.DB, accountID, poolID, actorID uint, amount decimal.Decimal, referenceID string) error {
	if db == nil || accountID == 0 || poolID == 0 || actorID == 0 || amount.LessThanOrEqual(decimal.Zero) {
		return ErrInvalidQuotaScope
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var account model.QuotaAccount
		if err := tx.First(&account, accountID).Error; err != nil {
			return err
		}
		if account.PoolID == nil || *account.PoolID != poolID || account.ReservedAmount.LessThan(amount) {
			return ErrEnterpriseQuotaExceeded
		}
		if err := tx.Model(&account).Updates(map[string]interface{}{"reserved_amount": account.ReservedAmount.Sub(amount), "consumed_amount": account.ConsumedAmount.Add(amount)}).Error; err != nil {
			return err
		}
		return tx.Create(&model.QuotaLedger{OrganizationID: account.OrganizationID, AccountID: account.ID, PoolID: &poolID, EntryType: model.QuotaLedgerConsumption, Amount: amount, ReferenceType: "pool_quota_consumption", ReferenceID: referenceID, CreatedByUserID: actorID}).Error
	})
}

func enterprisePoolQuotaAdjust(db *gorm.DB, accountID, poolID, actorID uint, amount decimal.Decimal, entryType, referenceID string) error {
	if db == nil || accountID == 0 || poolID == 0 || actorID == 0 || amount.LessThanOrEqual(decimal.Zero) {
		return ErrInvalidQuotaScope
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var account model.QuotaAccount
		if err := tx.First(&account, accountID).Error; err != nil {
			return err
		}
		if account.PoolID == nil || *account.PoolID != poolID {
			return ErrInvalidQuotaScope
		}
		available := account.LimitAmount.Sub(account.ReservedAmount).Sub(account.ConsumedAmount)
		if amount.GreaterThan(available) {
			return ErrEnterpriseQuotaExceeded
		}
		if err := tx.Model(&account).Update("reserved_amount", account.ReservedAmount.Add(amount)).Error; err != nil {
			return err
		}
		return tx.Create(&model.QuotaLedger{OrganizationID: account.OrganizationID, AccountID: account.ID, PoolID: &poolID, EntryType: entryType, Amount: amount, ReferenceType: "pool_quota", ReferenceID: referenceID, CreatedByUserID: actorID}).Error
	})
}

func enterpriseQuotaAdjust(db *gorm.DB, accountID, taskID, actorID uint, amount decimal.Decimal, entryType, referenceID string) error {
	if db == nil || accountID == 0 || taskID == 0 || actorID == 0 || amount.LessThanOrEqual(decimal.Zero) {
		return ErrInvalidQuotaScope
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var account model.QuotaAccount
		if err := tx.First(&account, accountID).Error; err != nil {
			return err
		}
		if account.TaskID == nil || *account.TaskID != taskID {
			return ErrInvalidQuotaScope
		}
		if entryType == model.QuotaLedgerReservation {
			available := account.LimitAmount.Sub(account.ReservedAmount).Sub(account.ConsumedAmount)
			if amount.GreaterThan(available) {
				return ErrEnterpriseQuotaExceeded
			}
			if err := tx.Model(&account).Update("reserved_amount", account.ReservedAmount.Add(amount)).Error; err != nil {
				return err
			}
		} else {
			if amount.GreaterThan(account.ReservedAmount) {
				return fmt.Errorf("%w: release exceeds reservation", ErrEnterpriseQuotaExceeded)
			}
			if err := tx.Model(&account).Update("reserved_amount", account.ReservedAmount.Sub(amount)).Error; err != nil {
				return err
			}
		}
		return tx.Create(&model.QuotaLedger{OrganizationID: account.OrganizationID, AccountID: account.ID, TaskID: &taskID, EntryType: entryType, Amount: amount, ReferenceType: "task_quota", ReferenceID: referenceID, CreatedByUserID: actorID}).Error
	})
}
