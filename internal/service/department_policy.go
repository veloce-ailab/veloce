package service

import "github.com/shopspring/decimal"

// Department policies were part of the retired enterprise feature. API
// forwarding now has no department-level restrictions or multipliers.
func DepartmentModelAllowed(uint, string) (bool, error) { return true, nil }

func EffectiveDepartmentMultiplier(uint) (decimal.Decimal, error) { return decimal.NewFromInt(1), nil }
