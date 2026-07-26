package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/veloce-ailab/veloce/internal/model"
	"gorm.io/gorm"
)

func setupSessionTasksTestDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:session-tasks-"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
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
	if err := db.AutoMigrate(&AdvancedChatSessionTask{}); err != nil {
		t.Fatalf("migrate tables: %v", err)
	}
	previous := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previous })
}

func sessionTasksFromResult(t *testing.T, result string) []AdvancedChatSessionTask {
	t.Helper()
	var payload struct {
		Tasks []AdvancedChatSessionTask `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		t.Fatalf("parse tool result: %v", err)
	}
	return payload.Tasks
}

func TestSessionTasksPlanUpdateFlow(t *testing.T) {
	setupSessionTasksTestDB(t)
	ctx := context.Background()
	input := AdvancedChatToolCallInput{UserID: 1, SessionID: "s1"}

	planInput := input
	planInput.Arguments = map[string]interface{}{
		"tasks": []interface{}{
			map[string]interface{}{"title": "研究现有实现", "description": "阅读代码"},
			map[string]interface{}{"title": "编写补丁"},
			map[string]interface{}{"title": "验证结果"},
		},
	}
	result, err := handleSessionTasksPlan(ctx, planInput)
	if err != nil {
		t.Fatalf("plan tasks: %v", err)
	}
	tasks := sessionTasksFromResult(t, result)
	if len(tasks) != 3 {
		t.Fatalf("expected 3 tasks, got %d", len(tasks))
	}
	if tasks[0].Status != sessionTaskStatusPending || tasks[0].Position != 1 || tasks[0].Title != "研究现有实现" {
		t.Fatalf("unexpected first task: %+v", tasks[0])
	}

	updateInput := input
	updateInput.Arguments = map[string]interface{}{"id": tasks[0].ID, "status": "in_progress"}
	result, err = handleSessionTasksUpdate(ctx, updateInput)
	if err != nil {
		t.Fatalf("update task: %v", err)
	}
	tasks = sessionTasksFromResult(t, result)
	if tasks[0].Status != sessionTaskStatusInProgress {
		t.Fatalf("expected in_progress, got %s", tasks[0].Status)
	}

	updateInput.Arguments = map[string]interface{}{"id": tasks[0].ID, "status": "completed", "note": "已完成"}
	result, err = handleSessionTasksUpdate(ctx, updateInput)
	if err != nil {
		t.Fatalf("complete task: %v", err)
	}
	tasks = sessionTasksFromResult(t, result)
	if tasks[0].Status != sessionTaskStatusCompleted || tasks[0].Note != "已完成" {
		t.Fatalf("unexpected completed task: %+v", tasks[0])
	}

	// Replacing the plan removes the previous tasks.
	planInput.Arguments = map[string]interface{}{
		"tasks": []interface{}{map[string]interface{}{"title": "新计划"}},
	}
	result, err = handleSessionTasksPlan(ctx, planInput)
	if err != nil {
		t.Fatalf("replace plan: %v", err)
	}
	tasks = sessionTasksFromResult(t, result)
	if len(tasks) != 1 || tasks[0].Title != "新计划" {
		t.Fatalf("expected replaced plan, got %+v", tasks)
	}
}

func TestSessionTasksScopedToSessionAndUser(t *testing.T) {
	setupSessionTasksTestDB(t)
	ctx := context.Background()

	planA := AdvancedChatToolCallInput{UserID: 1, SessionID: "sa", Arguments: map[string]interface{}{
		"tasks": []interface{}{map[string]interface{}{"title": "A 的任务"}},
	}}
	result, err := handleSessionTasksPlan(ctx, planA)
	if err != nil {
		t.Fatalf("plan for session A: %v", err)
	}
	taskA := sessionTasksFromResult(t, result)[0]

	// Another session cannot see or update session A's tasks.
	listB := AdvancedChatToolCallInput{UserID: 1, SessionID: "sb"}
	result, err = handleSessionTasksList(ctx, listB)
	if err != nil {
		t.Fatalf("list for session B: %v", err)
	}
	if len(sessionTasksFromResult(t, result)) != 0 {
		t.Fatal("session B must not see session A's tasks")
	}
	updateB := AdvancedChatToolCallInput{UserID: 2, SessionID: "sa", Arguments: map[string]interface{}{"id": taskA.ID, "status": "completed"}}
	if _, err := handleSessionTasksUpdate(ctx, updateB); err == nil {
		t.Fatal("another user must not update the task")
	}

	// Rejects invalid input.
	badPlan := AdvancedChatToolCallInput{UserID: 1, SessionID: "sa", Arguments: map[string]interface{}{"tasks": []interface{}{}}}
	if _, err := handleSessionTasksPlan(ctx, badPlan); err == nil {
		t.Fatal("empty plan must be rejected")
	}
	badStatus := AdvancedChatToolCallInput{UserID: 1, SessionID: "sa", Arguments: map[string]interface{}{"id": taskA.ID, "status": "done"}}
	if _, err := handleSessionTasksUpdate(ctx, badStatus); err == nil {
		t.Fatal("invalid status must be rejected")
	}
}
