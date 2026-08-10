package service

import (
	"strings"

	"github.com/shopspring/decimal"
	"github.com/veloce-ailab/veloce/internal/model"
)

// NewUserGiftCredit returns the administrator-configured starting balance.
func NewUserGiftCredit() decimal.Decimal {
	value, err := decimal.NewFromString(strings.TrimSpace(model.GetSystemSetting("new_user_gift_credit", "0")))
	if err != nil || value.IsNegative() {
		return decimal.Zero
	}
	return value
}
