package service

import (
	"encoding/json"
	"strings"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/shopspring/decimal"
)

// DepartmentModelAllowed applies the effective departmental model policy for a
// user. A deny policy always wins; when one or more allow policies apply, the
// requested model must be present in at least one allow-list. This predictable
// rule also handles employees who belong to multiple departments.
func DepartmentModelAllowed(userID uint, modelName string) (bool, error) {
	if userID == 0 {
		return true, nil
	}
	var departments []model.Department
	if err := model.DB.Model(&model.Department{}).
		Joins("JOIN department_members ON department_members.department_id = departments.id AND department_members.deleted_at IS NULL").
		Where("department_members.user_id = ? AND departments.deleted_at IS NULL", userID).
		Find(&departments).Error; err != nil {
		return false, err
	}
	modelName = strings.TrimSpace(strings.TrimPrefix(modelName, "models/"))
	hasAllowPolicy, allowed := false, false
	for _, department := range departments {
		var names []string
		if err := json.Unmarshal([]byte(department.ModelNames), &names); err != nil {
			continue
		}
		contains := false
		for _, name := range names {
			if strings.EqualFold(strings.TrimSpace(name), modelName) {
				contains = true
				break
			}
		}
		switch strings.ToLower(strings.TrimSpace(department.ModelPolicy)) {
		case "deny":
			if contains {
				return false, nil
			}
		case "allow":
			hasAllowPolicy = true
			allowed = allowed || contains
		}
	}
	return !hasAllowPolicy || allowed, nil
}

// EffectiveDepartmentMultiplier uses the lowest multiplier among a user's
// departments, matching the existing default group-multiplier selection rule.
func EffectiveDepartmentMultiplier(userID uint) (decimal.Decimal, error) {
	multiplier := decimal.NewFromInt(1)
	if userID == 0 {
		return multiplier, nil
	}
	var departments []model.Department
	if err := model.DB.Model(&model.Department{}).
		Joins("JOIN department_members ON department_members.department_id = departments.id AND department_members.deleted_at IS NULL").
		Where("department_members.user_id = ? AND departments.deleted_at IS NULL", userID).
		Find(&departments).Error; err != nil {
		return decimal.Zero, err
	}
	for _, department := range departments {
		if !department.Multiplier.IsZero() && department.Multiplier.LessThan(multiplier) {
			multiplier = department.Multiplier
		}
	}
	return multiplier, nil
}
