package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/veloce-ailab/veloce/internal/cache"
	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrWalletIdempotencyConflict = errors.New("wallet idempotency key was already used with different parameters")
	ErrWalletInvalidSettlement   = errors.New("invalid wallet settlement")
	ErrWalletLimitExceeded       = errors.New("wallet participation limit exceeded")
)

const walletMetadataMaxBytes = 64 << 10

type WalletSettlementInput struct {
	UserID         uint
	Source         string
	PluginID       string
	IdempotencyKey string
	DebitAmount    decimal.Decimal
	CreditAmount   decimal.Decimal
	ReferenceType  string
	ReferenceID    string
	Description    string
	Metadata       map[string]interface{}
	Limits         []WalletSettlementLimit
}

type WalletSettlementLimit struct {
	Key string
	Max int
}

type WalletSettlementResult struct {
	Transaction model.WalletTransaction
	Replay      bool
}

// SettleWallet atomically applies a debit and credit and persists the matching
// immutable ledger row. The idempotency key is scoped by user and source.
func SettleWallet(ctx context.Context, input WalletSettlementInput) (WalletSettlementResult, error) {
	if model.DB == nil {
		return WalletSettlementResult{}, errors.New("database is not initialized")
	}
	normalized, metadataJSON, requestHash, err := normalizeWalletSettlement(input)
	if err != nil {
		return WalletSettlementResult{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	release := cache.AcquireUserBillingLock(ctx, normalized.UserID)
	defer release()

	if existing, found, err := findWalletTransaction(model.DB.WithContext(ctx), normalized); err != nil {
		return WalletSettlementResult{}, err
	} else if found {
		return replayWalletTransaction(existing, requestHash)
	}

	var result WalletSettlementResult
	err = model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		settled, settleErr := settleWalletInTx(tx, normalized, metadataJSON, requestHash)
		result = settled
		return settleErr
	})
	if err != nil {
		// Another instance may have committed the same key while this transaction
		// was in flight. Resolve that race as an idempotent replay.
		if existing, found, lookupErr := findWalletTransaction(model.DB.WithContext(ctx), normalized); lookupErr == nil && found {
			return replayWalletTransaction(existing, requestHash)
		}
		cache.InvalidateUserBillingBalance(ctx, normalized.UserID)
		return WalletSettlementResult{}, err
	}
	cache.StoreUserBillingBalance(ctx, normalized.UserID, result.Transaction.BalanceAfter)
	return result, nil
}

func settleWalletInTx(tx *gorm.DB, input WalletSettlementInput, metadataJSON, requestHash string) (WalletSettlementResult, error) {
	var user model.User
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "balance").First(&user, input.UserID).Error; err != nil {
		return WalletSettlementResult{}, err
	}
	if existing, found, err := findWalletTransaction(tx, input); err != nil {
		return WalletSettlementResult{}, err
	} else if found {
		return replayWalletTransaction(existing, requestHash)
	}
	for _, limit := range input.Limits {
		var used int64
		if err := tx.Model(&model.WalletLimitUsage{}).Where("user_id = ? AND source = ? AND limit_key = ?", input.UserID, input.Source, limit.Key).Count(&used).Error; err != nil {
			return WalletSettlementResult{}, err
		}
		if used >= int64(limit.Max) {
			return WalletSettlementResult{}, fmt.Errorf("%w: %s", ErrWalletLimitExceeded, limit.Key)
		}
	}
	before := user.Balance
	update := tx.Model(&model.User{}).
		Where("id = ? AND balance >= ?", input.UserID, input.DebitAmount).
		UpdateColumn("balance", gorm.Expr("balance - ? + ?", input.DebitAmount, input.CreditAmount))
	if update.Error != nil {
		return WalletSettlementResult{}, update.Error
	}
	if update.RowsAffected == 0 {
		return WalletSettlementResult{}, ErrInsufficientBalance
	}
	var updated model.User
	if err := tx.Select("id", "balance").First(&updated, input.UserID).Error; err != nil {
		return WalletSettlementResult{}, err
	}

	entry := model.WalletTransaction{
		UserID:         input.UserID,
		Source:         input.Source,
		IdempotencyKey: input.IdempotencyKey,
		PluginID:       input.PluginID,
		DebitAmount:    input.DebitAmount,
		CreditAmount:   input.CreditAmount,
		BalanceBefore:  before,
		BalanceAfter:   updated.Balance,
		ReferenceType:  input.ReferenceType,
		ReferenceID:    input.ReferenceID,
		Description:    input.Description,
		RequestHash:    requestHash,
		MetadataJSON:   metadataJSON,
	}
	if err := tx.Create(&entry).Error; err != nil {
		return WalletSettlementResult{}, err
	}
	if len(input.Limits) > 0 {
		usages := make([]model.WalletLimitUsage, 0, len(input.Limits))
		for _, limit := range input.Limits {
			usages = append(usages, model.WalletLimitUsage{WalletTransactionID: entry.ID, UserID: input.UserID, Source: input.Source, LimitKey: limit.Key})
		}
		if err := tx.Create(&usages).Error; err != nil {
			return WalletSettlementResult{}, err
		}
	}
	return WalletSettlementResult{Transaction: entry}, nil
}

func normalizeWalletSettlement(input WalletSettlementInput) (WalletSettlementInput, string, string, error) {
	input.Source = strings.TrimSpace(input.Source)
	input.PluginID = strings.TrimSpace(input.PluginID)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.ReferenceType = strings.TrimSpace(input.ReferenceType)
	input.ReferenceID = strings.TrimSpace(input.ReferenceID)
	input.Description = strings.TrimSpace(input.Description)
	if input.UserID == 0 || input.Source == "" || input.IdempotencyKey == "" {
		return input, "", "", fmt.Errorf("%w: user, source, and idempotency key are required", ErrWalletInvalidSettlement)
	}
	if len(input.Source) > 100 || len(input.PluginID) > 80 || len(input.IdempotencyKey) > 160 || len(input.ReferenceType) > 80 || len(input.ReferenceID) > 160 || len(input.Description) > 500 {
		return input, "", "", fmt.Errorf("%w: one or more fields exceed their maximum length", ErrWalletInvalidSettlement)
	}
	if err := validateWalletAmount(input.DebitAmount); err != nil {
		return input, "", "", fmt.Errorf("%w: debit amount: %v", ErrWalletInvalidSettlement, err)
	}
	if err := validateWalletAmount(input.CreditAmount); err != nil {
		return input, "", "", fmt.Errorf("%w: credit amount: %v", ErrWalletInvalidSettlement, err)
	}
	if input.DebitAmount.IsZero() && input.CreditAmount.IsZero() {
		return input, "", "", fmt.Errorf("%w: debit and credit cannot both be zero", ErrWalletInvalidSettlement)
	}
	seenLimits := map[string]struct{}{}
	for index := range input.Limits {
		input.Limits[index].Key = strings.TrimSpace(input.Limits[index].Key)
		limit := input.Limits[index]
		if limit.Key == "" || len(limit.Key) > 160 || limit.Max <= 0 || limit.Max > 1_000_000 {
			return input, "", "", fmt.Errorf("%w: invalid participation limit", ErrWalletInvalidSettlement)
		}
		if _, exists := seenLimits[limit.Key]; exists {
			return input, "", "", fmt.Errorf("%w: duplicate participation limit", ErrWalletInvalidSettlement)
		}
		seenLimits[limit.Key] = struct{}{}
	}
	metadata, err := json.Marshal(input.Metadata)
	if err != nil {
		return input, "", "", fmt.Errorf("%w: metadata must be valid JSON", ErrWalletInvalidSettlement)
	}
	if len(metadata) > walletMetadataMaxBytes {
		return input, "", "", fmt.Errorf("%w: metadata is too large", ErrWalletInvalidSettlement)
	}
	if string(metadata) == "null" {
		metadata = []byte("{}")
	}
	fingerprint, _ := json.Marshal(map[string]interface{}{
		"user_id": input.UserID, "source": input.Source, "plugin_id": input.PluginID,
		"idempotency_key": input.IdempotencyKey, "debit": input.DebitAmount.StringFixed(6),
		"credit": input.CreditAmount.StringFixed(6), "reference_type": input.ReferenceType,
		"reference_id": input.ReferenceID, "description": input.Description, "metadata": json.RawMessage(metadata), "limits": input.Limits,
	})
	hash := sha256.Sum256(fingerprint)
	return input, string(metadata), hex.EncodeToString(hash[:]), nil
}

func validateWalletAmount(amount decimal.Decimal) error {
	if amount.IsNegative() {
		return errors.New("must not be negative")
	}
	if amount.Exponent() < -6 {
		return errors.New("supports at most 6 decimal places")
	}
	if amount.GreaterThanOrEqual(decimal.New(1, 14)) {
		return errors.New("exceeds decimal(20,6) range")
	}
	return nil
}

func findWalletTransaction(db *gorm.DB, input WalletSettlementInput) (model.WalletTransaction, bool, error) {
	var entry model.WalletTransaction
	err := db.Where("user_id = ? AND source = ? AND idempotency_key = ?", input.UserID, input.Source, input.IdempotencyKey).Limit(1).Find(&entry).Error
	return entry, entry.ID != 0, err
}

func replayWalletTransaction(entry model.WalletTransaction, requestHash string) (WalletSettlementResult, error) {
	if entry.RequestHash != requestHash {
		return WalletSettlementResult{}, ErrWalletIdempotencyConflict
	}
	return WalletSettlementResult{Transaction: entry, Replay: true}, nil
}
