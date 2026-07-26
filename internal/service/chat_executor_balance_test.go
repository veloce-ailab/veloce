package service

import (
	"net/http"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/shopspring/decimal"
	"github.com/veloce-ailab/veloce/internal/model"
	"gorm.io/gorm"
)

func setupChatExecutorBalanceTestDB(t *testing.T, systemMode string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:chat-executor-balance-"+systemMode+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("access database pool: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.AutoMigrate(&model.SystemSetting{}, &model.Department{}, &model.DepartmentMember{}, &model.UserGroupMembership{}, &model.UserChannel{}, &model.Channel{}, &model.Model{}, &model.ModelConfig{}); err != nil {
		t.Fatalf("migrate tables: %v", err)
	}
	if err := model.SetSystemSettingWithDB(db, "system_mode", systemMode); err != nil {
		t.Fatalf("set system mode: %v", err)
	}
	previous := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previous })
}

func TestExecuteServerChatCompletionSkipsBalanceCheckInPersonalMode(t *testing.T) {
	setupChatExecutorBalanceTestDB(t, SystemModePersonal)
	user := &model.User{ID: 1, Balance: decimal.Zero}

	_, err := ExecuteServerChatCompletion(nil, user, ChatExecutorRequest{ModelName: "test-model"})
	if err == nil {
		t.Fatal("expected an error without any configured channel")
	}
	execErr, ok := err.(*ChatExecutorError)
	if !ok {
		t.Fatalf("unexpected error type: %T", err)
	}
	if execErr.Status == http.StatusPaymentRequired {
		t.Fatalf("personal mode must not reject zero balance, got: %s", execErr.Message)
	}
}

func TestExecuteServerChatCompletionKeepsBalanceCheckInOperationMode(t *testing.T) {
	setupChatExecutorBalanceTestDB(t, SystemModeOperation)
	user := &model.User{ID: 1, Balance: decimal.Zero}

	_, err := ExecuteServerChatCompletion(nil, user, ChatExecutorRequest{ModelName: "test-model"})
	execErr, ok := err.(*ChatExecutorError)
	if !ok {
		t.Fatalf("unexpected error type: %T", err)
	}
	if execErr.Status != http.StatusPaymentRequired {
		t.Fatalf("operation mode must reject zero balance, got status %d: %s", execErr.Status, execErr.Message)
	}
}

func TestExecuteServerChatCompletionKeepsBalanceCheckForChargedPersonalRuns(t *testing.T) {
	setupChatExecutorBalanceTestDB(t, SystemModePersonal)
	user := &model.User{ID: 1, Balance: decimal.Zero}

	_, err := ExecuteServerChatCompletion(nil, user, ChatExecutorRequest{ModelName: "test-model", ChargeBalance: true})
	execErr, ok := err.(*ChatExecutorError)
	if !ok {
		t.Fatalf("unexpected error type: %T", err)
	}
	if execErr.Status != http.StatusPaymentRequired {
		t.Fatalf("charged personal-mode runs must reject zero balance, got status %d: %s", execErr.Status, execErr.Message)
	}
}
