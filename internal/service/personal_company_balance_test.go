package service

import (
	"testing"

	"github.com/veloce-ailab/veloce/internal/model"
	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

func TestStudioOperationChargeAndBalanceFloor(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:personal-company-balance-test?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if err := db.AutoMigrate(&model.SystemSetting{}, &model.User{}, &model.PersonalCompany{}, &model.CompanyOutboxEvent{}, &model.CompanyAuditEvent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := model.SetSystemSettingWithDB(db, "system_mode", SystemModePersonal); err != nil {
		t.Fatalf("set personal mode: %v", err)
	}
	owner := model.User{Username: "studio-owner", Email: "studio-owner@example.test", Balance: decimal.NewFromInt(2)}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		return applyChatExecutorUsageCharge(tx, owner.ID, decimal.RequireFromString("0.25"), true)
	}); err != nil {
		t.Fatalf("charge studio operation: %v", err)
	}
	if err := db.First(&owner, owner.ID).Error; err != nil {
		t.Fatalf("reload owner: %v", err)
	}
	if got, want := owner.Balance.String(), "1.75"; got != want {
		t.Fatalf("charged balance = %s, want %s", got, want)
	}

	company := model.PersonalCompany{OwnerUserID: owner.ID, AgentGroupID: "studio", Name: "Studio", State: model.PersonalCompanyStateOperating, BalanceFloor: decimal.RequireFromString("1.75")}
	if err := db.Create(&company).Error; err != nil {
		t.Fatalf("create studio operation: %v", err)
	}
	previous := model.DB
	model.DB = db
	defer func() { model.DB = previous }()
	if err := pausePersonalCompanyForBalance(company, 9, owner.Balance); err != nil {
		t.Fatalf("pause for balance: %v", err)
	}
	if err := db.First(&company, company.ID).Error; err != nil {
		t.Fatalf("reload studio operation: %v", err)
	}
	if company.State != model.PersonalCompanyStateAttentionRequired || company.PausedAt == nil {
		t.Fatalf("studio state = %q, paused = %v", company.State, company.PausedAt)
	}
	var outboxCount int64
	if err := db.Model(&model.CompanyOutboxEvent{}).Where("personal_company_id = ? AND event_type = ?", company.ID, "balance.floor_reached").Count(&outboxCount).Error; err != nil || outboxCount != 1 {
		t.Fatalf("balance notifications = %d, error = %v", outboxCount, err)
	}
}
