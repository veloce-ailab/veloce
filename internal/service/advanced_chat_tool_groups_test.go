package service

import "testing"

func TestNormalizeAdvancedChatDisabledToolGroups(t *testing.T) {
	groups := normalizeAdvancedChatDisabledToolGroups([]string{" Workspace ", "web", "web", "unknown", "MEMORY"})
	if len(groups) != 3 || groups[0] != "workspace" || groups[1] != "web" || groups[2] != "memory" {
		t.Fatalf("unexpected groups: %v", groups)
	}
	if got := normalizeAdvancedChatDisabledToolGroups(nil); len(got) != 0 {
		t.Fatalf("nil input must normalize to empty, got %v", got)
	}
}

func TestAdvancedChatToolGroupForTool(t *testing.T) {
	cases := map[string]string{
		"workspace_read_file":           advancedChatToolGroupWorkspace,
		"workspace_run_command":         advancedChatToolGroupWorkspace,
		"workspace_list_windows_drives": advancedChatToolGroupWorkspace,
		"workspace_web_search":          advancedChatToolGroupWeb,
		"workspace_web_fetch":           advancedChatToolGroupWeb,
		"deploy_static_site":            advancedChatToolGroupSites,
		"tasks_plan":                    advancedChatToolGroupTasks,
		"tasks_update":                  advancedChatToolGroupTasks,
		"ask_user":                      advancedChatToolGroupAskUser,
		"memory_upsert":                 advancedChatToolGroupMemory,
		"studio_create_work":            "",
		"some_mcp_tool":                 "",
	}
	for name, want := range cases {
		if got := advancedChatToolGroupForTool(name); got != want {
			t.Fatalf("group for %q = %q, want %q", name, got, want)
		}
	}
}

func TestFilterAdvancedChatToolsByDisabledGroups(t *testing.T) {
	tools := []ChatExecutorTool{
		{Name: "workspace_read_file"},
		{Name: "workspace_run_command"},
		{Name: "workspace_web_search"},
		{Name: "tasks_plan"},
		{Name: "ask_user"},
		{Name: "custom_mcp_tool"},
	}
	filtered := filterAdvancedChatToolsByDisabledGroups(tools, []string{advancedChatToolGroupWorkspace, advancedChatToolGroupAskUser})
	names := map[string]bool{}
	for _, tool := range filtered {
		names[tool.Name] = true
	}
	if len(filtered) != 3 || !names["workspace_web_search"] || !names["tasks_plan"] || !names["custom_mcp_tool"] {
		t.Fatalf("unexpected filtered tools: %+v", filtered)
	}
	if got := filterAdvancedChatToolsByDisabledGroups(tools, nil); len(got) != len(tools) {
		t.Fatal("no disabled groups must keep all tools")
	}
}

func TestRuntimeExtensionsHonorDisabledGroups(t *testing.T) {
	tasksExt, err := sessionTasksRuntimeExtension(nil, AdvancedChatRuntimeContext{SessionID: "s1", DisabledToolGroups: []string{advancedChatToolGroupTasks}})
	if err != nil || len(tasksExt.Tools) != 0 || tasksExt.SystemPrompt != "" {
		t.Fatalf("tasks extension must be empty when disabled: %+v, %v", tasksExt, err)
	}
	askExt, err := askUserRuntimeExtension(nil, AdvancedChatRuntimeContext{SessionID: "s1", DisabledToolGroups: []string{advancedChatToolGroupAskUser}})
	if err != nil || len(askExt.Tools) != 0 || askExt.SystemPrompt != "" {
		t.Fatalf("ask_user extension must be empty when disabled: %+v, %v", askExt, err)
	}
	enabledExt, err := sessionTasksRuntimeExtension(nil, AdvancedChatRuntimeContext{SessionID: "s1"})
	if err != nil || len(enabledExt.Tools) == 0 {
		t.Fatalf("tasks extension must stay available when not disabled: %v", err)
	}
}
