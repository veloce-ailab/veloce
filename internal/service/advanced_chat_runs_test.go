package service

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/veloce-ailab/veloce/internal/model"
)

func TestAdvancedChatModelRetryDelay(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: 0, want: 500 * time.Millisecond},
		{attempt: 1, want: time.Second},
		{attempt: 2, want: 2 * time.Second},
		{attempt: 6, want: assistantModelRetryMaxDelay},
	}
	for _, tc := range cases {
		if got := advancedChatModelRetryDelay(tc.attempt); got != tc.want {
			t.Fatalf("advancedChatModelRetryDelay(%d) = %s, want %s", tc.attempt, got, tc.want)
		}
	}
}

func TestRetryableAdvancedChatModelRequestError(t *testing.T) {
	if !retryableAdvancedChatModelRequestError(&ChatExecutorError{Status: http.StatusTooManyRequests, Message: "Upstream request failed"}) {
		t.Fatal("429 upstream errors should be retried")
	}
	if !retryableAdvancedChatModelRequestError(&ChatExecutorError{Status: http.StatusBadGateway, Message: "Failed to read upstream response"}) {
		t.Fatal("upstream response read failures should be retried")
	}
	if retryableAdvancedChatModelRequestError(&ChatExecutorError{Status: http.StatusInternalServerError, Message: "Failed to update balance"}) {
		t.Fatal("internal billing errors should not be retried")
	}
	if retryableAdvancedChatModelRequestError(errors.New("plain error")) {
		t.Fatal("plain errors should not be retried")
	}
}

func TestServerChatExecutorPreservesRoutingStateAcrossCalls(t *testing.T) {
	previous := serverChatProxyService
	serverChatProxyService = NewProxyService()
	defer func() { serverChatProxyService = previous }()

	userChannelID := uint(11)
	candidates := []model.ModelConfig{
		{Channel: model.Channel{ID: 1, UserChannelID: &userChannelID, UserChannel: model.UserChannel{RoutingAlgorithm: RoutingRoundRobin}}},
		{Channel: model.Channel{ID: 2, UserChannelID: &userChannelID, UserChannel: model.UserChannel{RoutingAlgorithm: RoutingRoundRobin}}},
	}

	first := serverChatExecutor().selectModelConfig(candidates, "gpt-test")
	second := serverChatExecutor().selectModelConfig(candidates, "gpt-test")
	if first.Channel.ID != 1 || second.Channel.ID != 2 {
		t.Fatalf("server chat executor should preserve routing state across calls, got %d then %d", first.Channel.ID, second.Channel.ID)
	}
}

func TestNormalizeConnectorTaskResultIncludesCommandFailureOutput(t *testing.T) {
	exitCode := 2
	input := advancedChatConnectorTaskResultInput{
		Success:  false,
		Stdout:   "stdout text",
		Stderr:   "stderr text",
		Error:    "command failed",
		ExitCode: &exitCode,
	}
	result := normalizeConnectorTaskResultText(input)
	if !strings.Contains(result, "stdout:\nstdout text") {
		t.Fatalf("result should include stdout, got %q", result)
	}
	if !strings.Contains(result, "stderr:\nstderr text") {
		t.Fatalf("result should include stderr, got %q", result)
	}
	message := normalizeConnectorTaskErrorMessage(input)
	if !strings.Contains(message, "command failed") || !strings.Contains(message, "exit code 2") {
		t.Fatalf("error message should include command error and exit code, got %q", message)
	}
}

func TestAgentStudioRolePermissions(t *testing.T) {
	if advancedChatAgentStudioCanUseExecutionTools("chief") {
		t.Fatal("chief agents must not receive execution tools")
	}
	if advancedChatAgentStudioCanSplit("chief") {
		t.Fatal("chief agents must not split into sub-agents")
	}
	if !advancedChatAgentStudioCanDelegate("chief", true) {
		t.Fatal("chief agents should be able to delegate within an agent group")
	}
	if !advancedChatAgentStudioConnectorActionAllowed("chief", "list_files") || !advancedChatAgentStudioConnectorActionAllowed("chief", "read_file") || !advancedChatAgentStudioConnectorActionAllowed("chief", "run_command") {
		t.Fatal("chief agents should be able to inspect workspaces and run diagnostic commands")
	}
	if advancedChatAgentStudioConnectorActionAllowed("chief", "write_file") || advancedChatAgentStudioConnectorActionAllowed("chief", "replace_text") {
		t.Fatal("chief agents must not receive file mutation tools")
	}
	if !advancedChatAgentStudioCanUseExecutionTools("worker") || !advancedChatAgentStudioCanSplit("worker") {
		t.Fatal("worker agents should be able to execute and split")
	}
	if advancedChatAgentStudioCanUseExecutionTools("checker") || advancedChatAgentStudioCanSplit("checker") || advancedChatAgentStudioCanDelegate("checker", true) {
		t.Fatal("checker agents must not execute, split, or delegate")
	}
	if advancedChatAgentStudioConnectorActionAllowed("checker", "read_file") || advancedChatAgentStudioConnectorActionAllowed("checker", "write_file") {
		t.Fatal("checker agents must not receive connector tools")
	}
}

func TestAssistantModeDoesNotEnableAgentStudioCapabilities(t *testing.T) {
	prepared := preparedAdvancedChatAssistantRun{mode: advancedChatModeAssistant}
	studioRoleActive := prepared.mode == advancedChatModeAgentGroup && prepared.groupAgent != nil
	studioCanSplit := studioRoleActive && advancedChatAgentStudioCanSplit(advancedChatAgentStudioRole(prepared))
	studioCanDelegate := studioRoleActive && advancedChatAgentStudioCanDelegate(advancedChatAgentStudioRole(prepared), true)
	if studioRoleActive || studioCanSplit || studioCanDelegate {
		t.Fatal("assistant mode must not receive Agent Studio delegation or split capabilities")
	}
}

func TestNormalizeAdvancedChatAgentGroupRequiresOneChecker(t *testing.T) {
	base := advancedChatAgentGroupInput{
		ID:   "studio",
		Name: "Studio",
		Agents: []advancedChatGroupAgent{
			{ID: "chief", Name: "Chief", Type: "chief", ChatAgentID: "1"},
			{ID: "checker", Name: "Checker", Type: "checker", ChatAgentID: "2"},
			{ID: "worker", Name: "Worker", Type: "worker", ChatAgentID: "3"},
		},
	}
	if _, err := normalizeAdvancedChatAgentGroup(base); err != nil {
		t.Fatalf("expected one chief and one checker to be valid, got %v", err)
	}
	missingChecker := base
	missingChecker.Agents = []advancedChatGroupAgent{
		{ID: "chief", Name: "Chief", Type: "chief", ChatAgentID: "1"},
		{ID: "worker", Name: "Worker", Type: "worker", ChatAgentID: "3"},
	}
	if _, err := normalizeAdvancedChatAgentGroup(missingChecker); err == nil || !strings.Contains(err.Error(), "exactly one checker") {
		t.Fatalf("expected missing checker to fail, got %v", err)
	}
	twoCheckers := base
	twoCheckers.Agents = append(twoCheckers.Agents, advancedChatGroupAgent{ID: "checker2", Name: "Checker 2", Type: "checker", ChatAgentID: "4"})
	if _, err := normalizeAdvancedChatAgentGroup(twoCheckers); err == nil || !strings.Contains(err.Error(), "exactly one checker") {
		t.Fatalf("expected duplicate checker to fail, got %v", err)
	}
}

func TestAgentStudioCheckerDecisionRequiresApprovalTool(t *testing.T) {
	yes, opinion, err := advancedChatAgentStudioCheckerDecision(&ChatExecutorResult{ToolCalls: []ChatExecutorToolCall{{
		Name:      advancedChatAgentStudioApprovalToolName,
		Arguments: `{"decision":"yes","opinion":"scoped change"}`,
	}}})
	if err != nil || yes != "yes" || opinion != "scoped change" {
		t.Fatalf("expected yes decision with opinion, got yes=%v opinion=%q err=%v", yes, opinion, err)
	}
	no, _, err := advancedChatAgentStudioCheckerDecision(&ChatExecutorResult{ToolCalls: []ChatExecutorToolCall{{
		Name:      advancedChatAgentStudioApprovalToolName,
		Arguments: `{"decision":"no"}`,
	}}})
	if err != nil || no != "no" {
		t.Fatalf("expected no decision, got no=%v err=%v", no, err)
	}
	escalation, _, err := advancedChatAgentStudioCheckerDecision(&ChatExecutorResult{ToolCalls: []ChatExecutorToolCall{{
		Name:      advancedChatAgentStudioApprovalToolName,
		Arguments: `{"decision":"escalate"}`,
	}}})
	if err != nil || escalation != "escalate" {
		t.Fatalf("expected escalation decision, got decision=%q err=%v", escalation, err)
	}
	if _, _, err := advancedChatAgentStudioCheckerDecision(&ChatExecutorResult{Content: "yes"}); err == nil {
		t.Fatal("checker must call approval decision tool")
	}
}

func TestCommitDeltaRequiresApprovalUnlessAutoApproved(t *testing.T) {
	binding := advancedChatConnectorToolBinding{Action: "commit_delta"}
	if !advancedChatConnectorTaskRequiresApproval(binding, map[string]interface{}{"mutations": []interface{}{}}) {
		t.Fatal("commit_delta should require approval by default")
	}
	binding.AutoApprove = true
	if advancedChatConnectorTaskRequiresApproval(binding, map[string]interface{}{"mutations": []interface{}{}}) {
		t.Fatal("commit_delta should respect connector auto approval")
	}
	if advancedChatConnectorTaskRequiresApproval(advancedChatConnectorToolBinding{Action: "file_sha256"}, map[string]interface{}{"path": "main.go"}) {
		t.Fatal("file_sha256 should be treated as read-only and not require approval")
	}
}

func TestConnectorApprovalModes(t *testing.T) {
	fileChange := map[string]interface{}{"path": "main.go", "content": "package main"}
	command := map[string]interface{}{"command": "go test ./..."}

	manual := advancedChatConnectorToolBinding{Action: "write_file", ApprovalMode: advancedChatConnectorApprovalManual}
	if !advancedChatConnectorTaskRequiresApproval(manual, fileChange) {
		t.Fatal("manual mode should require approval for file changes")
	}

	fullAccess := advancedChatConnectorToolBinding{Action: "run_command", ApprovalMode: advancedChatConnectorApprovalFullAccess}
	if advancedChatConnectorTaskRequiresApproval(fullAccess, command) {
		t.Fatal("full access should not require approval for commands")
	}

	assistant := advancedChatConnectorToolBinding{
		Action:          "run_command",
		ApprovalMode:    advancedChatConnectorApprovalAssistant,
		CommandPrefixes: []string{"go test"},
	}
	if !advancedChatConnectorTaskRequiresApproval(assistant, command) {
		t.Fatal("assistant approval should review commands even when they match a manual allowlist")
	}
}

func TestWorkspaceGitHelpers(t *testing.T) {
	additions, deletions := parseAdvancedChatGitNumstat("12\t3\tfirst.go\n4\t0\tsecond.go\n-\t-\tbinary.dat")
	if additions != 16 || deletions != 3 {
		t.Fatalf("git numstat = +%d -%d, want +16 -3", additions, deletions)
	}
	branch, changedFiles := parseAdvancedChatGitStatus("## main...origin/main [ahead 1]\n M first.go\n?? second.go\n")
	if branch != "main" || changedFiles != 2 {
		t.Fatalf("git status = branch %q files %d, want main and 2", branch, changedFiles)
	}
	command, err := advancedChatWorkspaceGitActionCommand("commit", "Add workspace actions")
	if err != nil || command != "git add -A; git commit -m \"Add workspace actions\"" {
		t.Fatalf("commit command = %q, err=%v", command, err)
	}
	if _, err := advancedChatWorkspaceGitActionCommand("commit", "bad\nmessage"); err == nil {
		t.Fatal("commit message containing a newline should be rejected")
	}
	if !validAdvancedChatGitRef("feature/workspace-git") || validAdvancedChatGitRef("--output=/tmp/x") || validAdvancedChatGitRef("main; git push") {
		t.Fatal("git reference validation returned an unexpected result")
	}
}

func TestWorkspaceDirectoryParsers(t *testing.T) {
	directories := parseAdvancedChatWorkspaceDirectories("dir\tapi\t0\nfile\tgo.mod\t120\ndir\tweb\t0", "/workspace", false)
	if len(directories) != 2 || directories[0].Path != "/workspace/api" || directories[1].Path != "/workspace/web" {
		t.Fatalf("unexpected directory listing: %#v", directories)
	}
	drives := parseAdvancedChatWindowsDriveDirectories("C:\\\r\nD:\\\r\n(no Windows drives found)")
	if len(drives) != 2 || drives[0].Path != "C:\\" || drives[1].Path != "D:\\" {
		t.Fatalf("unexpected Windows drive listing: %#v", drives)
	}
	if got := advancedChatWorkspaceDirectoryJoin("C:\\", "repo", true); got != "C:\\repo" {
		t.Fatalf("Windows directory join = %q", got)
	}
}

func TestNormalizeStaticSiteDomainAndFiles(t *testing.T) {
	domain, err := normalizeAdvancedChatStaticSiteDomain("https://Site.Example.COM/path")
	if err != nil {
		t.Fatalf("expected domain to normalize, got %v", err)
	}
	if domain != "site.example.com" {
		t.Fatalf("domain = %q, want site.example.com", domain)
	}
	if _, err := normalizeAdvancedChatStaticSiteDomain("example.com:8080"); err == nil {
		t.Fatal("domain with port should be rejected")
	}
	files, total, err := normalizeAdvancedChatStaticSiteFiles([]interface{}{
		map[string]interface{}{"path": "index.html", "content": base64.StdEncoding.EncodeToString([]byte("<html></html>"))},
	})
	if err != nil {
		t.Fatalf("expected files to normalize, got %v", err)
	}
	if len(files) != 1 || files[0]["path"] != "index.html" || total != len("<html></html>") {
		t.Fatalf("unexpected files=%v total=%d", files, total)
	}
	if _, _, err := normalizeAdvancedChatStaticSiteFiles([]interface{}{
		map[string]interface{}{"path": "../secret.txt", "content": base64.StdEncoding.EncodeToString([]byte("x"))},
	}); err == nil {
		t.Fatal("path traversal should be rejected")
	}
	if _, _, err := normalizeAdvancedChatStaticSiteFiles([]interface{}{
		map[string]interface{}{"path": "index.html", "content": "not base64"},
	}); err == nil {
		t.Fatal("invalid base64 should be rejected")
	}
}

func TestNormalizeConnectorModeDefaultsToPlatform(t *testing.T) {
	if got := normalizeAdvancedChatConnectorMode(""); got != advancedChatConnectorModePlatform {
		t.Fatalf("empty mode = %q, want platform", got)
	}
	if got := normalizeAdvancedChatConnectorListenPort(0, ""); got != 0 {
		t.Fatalf("platform default port = %d, want 0", got)
	}
	if got := normalizeAdvancedChatConnectorListenPort(0, advancedChatConnectorModeWebServer); got != advancedChatStaticSiteDefaultListenPort {
		t.Fatalf("web server default port = %d, want %d", got, advancedChatStaticSiteDefaultListenPort)
	}
}

func TestAcceptedConnectorApprovalConflict(t *testing.T) {
	status, ok := acceptedAdvancedChatConnectorApprovalConflict(advancedChatConnectorTaskDecisionConflict{Status: advancedChatConnectorTaskStatusCompleted}, true)
	if !ok || status != advancedChatConnectorTaskStatusCompleted {
		t.Fatalf("completed approved conflict should be accepted, got status=%q ok=%v", status, ok)
	}
	if _, ok := acceptedAdvancedChatConnectorApprovalConflict(advancedChatConnectorTaskDecisionConflict{Status: advancedChatConnectorTaskStatusFailed}, true); ok {
		t.Fatal("failed approval conflict should not be accepted")
	}
	if _, ok := acceptedAdvancedChatConnectorApprovalConflict(advancedChatConnectorTaskDecisionConflict{Status: advancedChatConnectorTaskStatusQueued}, false); ok {
		t.Fatal("rejected approval conflict should not be accepted")
	}
}

func TestAgentStudioReplaceMutationsUseBaseSHA(t *testing.T) {
	mutations := advancedChatAgentStudioMutationsFromReplaceArguments(map[string]interface{}{
		"path":     "main.go",
		"old_text": "old",
		"new_text": "new",
	}, func(path string) string {
		if path != "main.go" {
			t.Fatalf("unexpected path %q", path)
		}
		return sha256Hex("base")
	})
	if len(mutations) != 1 {
		t.Fatalf("expected one mutation, got %d", len(mutations))
	}
	if mutations[0].BaseSHA256 != sha256Hex("base") {
		t.Fatalf("base sha = %q, want %q", mutations[0].BaseSHA256, sha256Hex("base"))
	}
}

func TestValidateAgentStudioMutationsDetectsConflicts(t *testing.T) {
	if err := validateAdvancedChatAgentStudioMutations([]advancedChatAgentStudioMutation{{
		Action:  "replace_text",
		Path:    "main.go",
		OldText: "old",
		NewText: "new",
	}}); err != nil {
		t.Fatalf("expected valid mutation, got %v", err)
	}
	if err := validateAdvancedChatAgentStudioMutations([]advancedChatAgentStudioMutation{
		{Action: "replace_text", Path: "main.go", OldText: "old", NewText: "new"},
		{Action: "replace_text", Path: "main.go", OldText: "old", NewText: "other"},
	}); err == nil || !strings.Contains(err.Error(), "conflicting replace_text") {
		t.Fatalf("expected replace conflict, got %v", err)
	}
	if err := validateAdvancedChatAgentStudioMutations([]advancedChatAgentStudioMutation{
		{Action: "write_file", Path: "main.go", Content: "one"},
		{Action: "write_file", Path: "main.go", Content: "two"},
	}); err == nil || !strings.Contains(err.Error(), "conflicting write_file") {
		t.Fatalf("expected write conflict, got %v", err)
	}
	if err := validateAdvancedChatAgentStudioMutations([]advancedChatAgentStudioMutation{{
		Action: "replace_text",
		Path:   "main.go",
	}}); err == nil || !strings.Contains(err.Error(), "old_text is required") {
		t.Fatalf("expected old_text validation error, got %v", err)
	}
	if err := validateAdvancedChatAgentStudioMutations([]advancedChatAgentStudioMutation{{
		Action: "delete_file",
		Path:   "main.go",
	}}); err != nil {
		t.Fatalf("expected delete_file mutation to validate, got %v", err)
	}
	if err := validateAdvancedChatAgentStudioMutations([]advancedChatAgentStudioMutation{
		{Action: "write_file", Path: "main.go", Content: "next"},
		{Action: "delete_file", Path: "main.go"},
	}); err == nil || !strings.Contains(err.Error(), "conflicting delete_file") {
		t.Fatalf("expected delete conflict, got %v", err)
	}
}

func TestAgentStudioSandboxArguments(t *testing.T) {
	sandboxID := advancedChatAgentStudioSandboxID("run/1", "group A", "worker")
	if sandboxID != "run-1-group-A-worker" {
		t.Fatalf("unexpected sandbox id %q", sandboxID)
	}
	arguments := map[string]interface{}{"command": "go test ./..."}
	next := advancedChatAgentStudioSandboxArguments(arguments, sandboxID)
	if next[advancedChatAgentStudioSandboxIDArg] != sandboxID {
		t.Fatalf("sandbox id was not injected: %+v", next)
	}
	if next[advancedChatAgentStudioSandboxBackendArg] != "appcontainer" {
		t.Fatalf("sandbox backend was not injected: %+v", next)
	}
	if _, exists := arguments[advancedChatAgentStudioSandboxIDArg]; exists {
		t.Fatal("sandbox argument injection must not mutate the original argument map")
	}
}

func TestDelegatedAgentMessagesDropPendingToolCalls(t *testing.T) {
	messages := []ChatExecutorMessage{
		{Role: "user", Content: "do work"},
		{Role: "assistant", Content: "", ToolCalls: []map[string]interface{}{{"id": "call-1", "type": "function"}}},
	}
	sanitized := advancedChatMessagesForDelegatedAgent(messages)
	if len(sanitized) != 1 {
		t.Fatalf("expected pending empty assistant tool call message to be dropped, got %d messages", len(sanitized))
	}
	if len(messages[1].ToolCalls) == 0 {
		t.Fatal("sanitizing delegated messages must not mutate the original message slice")
	}

	messages = []ChatExecutorMessage{
		{Role: "user", Content: "do work"},
		{Role: "assistant", Content: "I will delegate this.", ToolCalls: []map[string]interface{}{{"id": "call-1", "type": "function"}}},
	}
	sanitized = advancedChatMessagesForDelegatedAgent(messages)
	if len(sanitized) != 2 {
		t.Fatalf("expected assistant text to be preserved, got %d messages", len(sanitized))
	}
	if len(sanitized[1].ToolCalls) != 0 || sanitized[1].Content != "I will delegate this." {
		t.Fatalf("expected pending tool calls to be stripped while preserving text, got %+v", sanitized[1])
	}
}

func TestAgentStudioSubAgentStatusHelpers(t *testing.T) {
	payload := map[string]interface{}{
		"task_id":    "task-1",
		"agent_id":   "split-a",
		"agent_name": "Split A",
	}
	if !advancedChatAgentStudioSubAgentMatches(payload, "task-1") {
		t.Fatal("status query should match task id")
	}
	if !advancedChatAgentStudioSubAgentMatches(payload, "split-a") {
		t.Fatal("status query should match agent id")
	}
	if !advancedChatAgentStudioSubAgentMatches(payload, "Split A") {
		t.Fatal("status query should match agent name")
	}
	if advancedChatAgentStudioSubAgentMatches(payload, "other") {
		t.Fatal("status query should not match unrelated ids")
	}
	if key := advancedChatAgentStudioSubAgentKey(payload); key != "task-1" {
		t.Fatalf("expected task id to be the stable key, got %q", key)
	}
}

func TestMergeAdvancedChatToolCallDetailListPreservesStreamedWorkerCalls(t *testing.T) {
	current := []advancedChatCompletionToolCall{
		{ID: "delegate-1", Name: "agent_delegate", Server: "Agent Studio", Tool: "agent_delegate", Status: "running"},
		{ID: "worker-read-1", Name: "workspace_read_file", Server: "local", Tool: "read_file", Status: "ok", Result: "read result"},
	}
	final := []advancedChatCompletionToolCall{
		{ID: "delegate-1", Name: "agent_delegate", Server: "Agent Studio", Tool: "agent_delegate", Status: "ok", Result: "[worker] done"},
	}
	merged := mergeAdvancedChatToolCallDetailList(current, final)
	if len(merged) != 2 {
		t.Fatalf("expected merged list to preserve streamed worker calls, got %d items", len(merged))
	}
	if merged[0].Status != "ok" || merged[0].Result != "[worker] done" {
		t.Fatalf("expected final delegate status to override running, got status=%q result=%q", merged[0].Status, merged[0].Result)
	}
	if merged[1].ID != "worker-read-1" || merged[1].Status != "ok" {
		t.Fatalf("expected worker tool call to be preserved, got %+v", merged[1])
	}
}

func TestMergeAdvancedChatToolCallDetailsPreservesExistingResult(t *testing.T) {
	current := []advancedChatCompletionToolCall{{
		ID:        "read-1",
		Name:      "workspace_read_file",
		Server:    "local",
		Tool:      "read_file",
		Status:    "ok",
		Arguments: map[string]interface{}{"path": "package.json"},
		Result:    "file content",
	}}
	merged := mergeAdvancedChatToolCallDetails(current, advancedChatCompletionToolCall{
		ID:     "read-1",
		Name:   "workspace_read_file",
		Server: "local",
		Tool:   "read_file",
		Status: "ok",
	})
	if len(merged) != 1 {
		t.Fatalf("expected one merged tool call, got %d", len(merged))
	}
	if merged[0].Result != "file content" {
		t.Fatalf("empty result should not overwrite existing result, got %q", merged[0].Result)
	}
	if stringFromMap(merged[0].Arguments, "path") != "package.json" {
		t.Fatalf("empty arguments should not overwrite existing arguments, got %+v", merged[0].Arguments)
	}
}

func TestCancelActiveAdvancedChatToolCalls(t *testing.T) {
	details := []advancedChatCompletionToolCall{
		{ID: "running", Name: "agent_delegate", Status: "running"},
		{ID: "approval", Name: "workspace_write_file", Status: "approval_required"},
		{ID: "done", Name: "workspace_read_file", Status: "ok", Result: "kept"},
	}
	updated, changed := cancelActiveAdvancedChatToolCalls(details, "Cancelled by user.")
	if !changed {
		t.Fatal("expected active tool calls to be marked changed")
	}
	if updated[0].Status != "error" || updated[1].Status != "error" {
		t.Fatalf("expected active calls to become error, got %q and %q", updated[0].Status, updated[1].Status)
	}
	if updated[2].Status != "ok" || updated[2].Result != "kept" {
		t.Fatalf("completed calls should not be changed, got %+v", updated[2])
	}
}

func TestAgentStudioDeltaLogVirtualRead(t *testing.T) {
	delta := &advancedChatAgentStudioDeltaLog{}
	delta.append(advancedChatAgentStudioMutation{Action: "replace_text", Path: "main.go", OldText: "old", NewText: "new"})
	if got := delta.applyToPath("main.go", "old value"); got != "new value" {
		t.Fatalf("virtual read should apply deferred replacement, got %q", got)
	}
	delta.append(advancedChatAgentStudioMutation{Action: "write_file", Path: "main.go", Content: "final"})
	if got := delta.applyToPath("main.go", "old value"); got != "final" {
		t.Fatalf("virtual read should apply deferred write last, got %q", got)
	}
}

func TestAgentStudioSandboxReportMutations(t *testing.T) {
	report := `stdout

SandboxChangeReport:
` + "```json" + `
{
  "sandbox_id": "run-worker",
  "backend": "portable-copy",
  "workspace": "C:\\repo",
  "changed": true,
  "files": [{"path": "package.json", "change": "modified"}],
  "mutations": [{
    "action": "write_file",
    "path": "package.json",
    "content": "{\"dependencies\":{\"express\":\"^4.18.3\",\"typeorm\":\"^0.3.20\"}}",
    "overwrite": true,
    "create_dirs": true,
    "base_sha256": "abc"
  }]
}
` + "```"
	mutations := advancedChatAgentStudioSandboxMutations(report)
	if len(mutations) != 1 {
		t.Fatalf("expected one sandbox mutation, got %d", len(mutations))
	}
	if mutations[0].Action != "write_file" || mutations[0].Path != "package.json" || !strings.Contains(mutations[0].Content, "typeorm") {
		t.Fatalf("unexpected sandbox mutation: %+v", mutations[0])
	}
	delta := &advancedChatAgentStudioDeltaLog{}
	appendAdvancedChatAgentStudioDeltaMutations(delta, mutations)
	if got := delta.applyToPath("package.json", "{}"); !strings.Contains(got, "typeorm") {
		t.Fatalf("sandbox mutation was not appended to delta, got %q", got)
	}
}

func TestAgentStudioCommitBindingPrefersWorkspaceFileBinding(t *testing.T) {
	readBinding := advancedChatConnectorToolBinding{Action: "read_file", DeviceID: "read-device"}
	writeBinding := advancedChatConnectorToolBinding{Action: "write_file", DeviceID: "write-device"}
	binding, ok := advancedChatAgentStudioCommitBinding(map[string]advancedChatConnectorToolBinding{
		advancedChatConnectorToolReadFile:  readBinding,
		advancedChatConnectorToolWriteFile: writeBinding,
	})
	if !ok {
		t.Fatal("expected commit binding")
	}
	if binding.DeviceID != "write-device" {
		t.Fatalf("expected write binding to be preferred, got %+v", binding)
	}
}

func TestNormalizeAdvancedChatGroupAgentsPreservesMemberTools(t *testing.T) {
	agents := normalizeAdvancedChatGroupAgents([]advancedChatGroupAgent{{
		ID:           "worker",
		Name:         "Worker",
		Type:         "worker",
		ChatAgentID:  "1",
		SkillIDs:     []string{"skill-a", "skill-a", "skill-b"},
		MCPServerIDs: []string{"mcp-a", "", "mcp-b", "mcp-a"},
	}})
	if len(agents) != 1 {
		t.Fatalf("expected one normalized agent, got %d", len(agents))
	}
	if got := strings.Join(agents[0].SkillIDs, ","); got != "skill-a,skill-b" {
		t.Fatalf("skill ids were not preserved and deduplicated, got %q", got)
	}
	if got := strings.Join(agents[0].MCPServerIDs, ","); got != "mcp-a,mcp-b" {
		t.Fatalf("mcp server ids were not preserved and deduplicated, got %q", got)
	}
	if !advancedChatPreparedAgentStream(&AdvancedChatAgent{Stream: true}) {
		t.Fatal("prepared agent stream helper should return true for streaming agents")
	}
	if advancedChatPreparedAgentStream(nil) {
		t.Fatal("prepared group agent stream helper should return false without an agent")
	}
}
