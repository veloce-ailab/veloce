package service

import (
	"strings"

	"github.com/veloce-ailab/veloce/internal/model"
	"gorm.io/gorm"
)

const (
	SystemModeOperation  = "operation"
	SystemModePersonal   = "personal"
	SystemModeEnterprise = model.EnterpriseSystemMode
)

func NormalizeSystemMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case SystemModePersonal:
		return SystemModePersonal
	case SystemModeEnterprise:
		return SystemModeEnterprise
	default:
		return SystemModeOperation
	}
}

func EnterpriseModeEnabled() bool {
	return CurrentSystemMode() == SystemModeEnterprise
}

func CurrentSystemMode() string {
	if model.DB == nil {
		return SystemModeOperation
	}
	return NormalizeSystemMode(model.GetSystemSetting("system_mode", SystemModeOperation))
}

func PersonalModeEnabled() bool {
	return CurrentSystemMode() == SystemModePersonal
}

func PersonalModeEnabledInTx(tx *gorm.DB) bool {
	if tx == nil {
		return PersonalModeEnabled()
	}
	return NormalizeSystemMode(model.GetSystemSettingWithDB(tx, "system_mode", SystemModeOperation)) == SystemModePersonal
}
