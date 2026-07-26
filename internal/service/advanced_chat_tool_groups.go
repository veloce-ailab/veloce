package service

import "strings"

// Built-in tools are switchable per session as feature groups. A session
// stores the groups the user turned OFF (empty means everything stays on),
// and the completion pipeline drops the matching tools before calling the
// model. MCP and Studio tools are managed elsewhere and are not grouped here.
const (
	advancedChatToolGroupWorkspace = "workspace"
	advancedChatToolGroupWeb       = "web"
	advancedChatToolGroupSites     = "sites"
	advancedChatToolGroupTasks     = "tasks"
	advancedChatToolGroupAskUser   = "ask_user"
	advancedChatToolGroupMemory    = "memory"
)

var advancedChatToolGroupNames = []string{
	advancedChatToolGroupWorkspace,
	advancedChatToolGroupWeb,
	advancedChatToolGroupSites,
	advancedChatToolGroupTasks,
	advancedChatToolGroupAskUser,
	advancedChatToolGroupMemory,
}

func normalizeAdvancedChatDisabledToolGroups(values []string) []string {
	known := map[string]bool{}
	for _, name := range advancedChatToolGroupNames {
		known[name] = true
	}
	seen := map[string]bool{}
	groups := make([]string, 0, len(values))
	for _, value := range values {
		group := strings.ToLower(strings.TrimSpace(value))
		if !known[group] || seen[group] {
			continue
		}
		seen[group] = true
		groups = append(groups, group)
	}
	return groups
}

func advancedChatToolGroupForTool(name string) string {
	switch name {
	case advancedChatConnectorToolListFiles,
		advancedChatConnectorToolReadFile,
		advancedChatConnectorToolWriteFile,
		advancedChatConnectorToolReplaceText,
		advancedChatConnectorToolRunCommand,
		advancedChatConnectorToolWindowsDrives:
		return advancedChatToolGroupWorkspace
	case advancedChatConnectorToolWebSearch, advancedChatConnectorToolWebFetch:
		return advancedChatToolGroupWeb
	case "list_static_sites", "deploy_static_site", "set_static_site_enabled", "delete_static_site":
		return advancedChatToolGroupSites
	case sessionTasksPlanToolName, sessionTasksUpdateToolName, sessionTasksListToolName:
		return advancedChatToolGroupTasks
	case askUserToolName:
		return advancedChatToolGroupAskUser
	}
	if strings.HasPrefix(name, "memory_") {
		return advancedChatToolGroupMemory
	}
	return ""
}

// AdvancedChatMemoryToolsDisabled lets extension packages (premium memory)
// honor the per-session "memory" feature switch.
func AdvancedChatMemoryToolsDisabled(disabledGroups []string) bool {
	return advancedChatToolGroupDisabled(disabledGroups, advancedChatToolGroupMemory)
}

func advancedChatToolGroupDisabled(disabledGroups []string, group string) bool {
	if group == "" {
		return false
	}
	for _, disabled := range disabledGroups {
		if disabled == group {
			return true
		}
	}
	return false
}

func filterAdvancedChatToolsByDisabledGroups(tools []ChatExecutorTool, disabledGroups []string) []ChatExecutorTool {
	if len(disabledGroups) == 0 || len(tools) == 0 {
		return tools
	}
	filtered := make([]ChatExecutorTool, 0, len(tools))
	for _, tool := range tools {
		if advancedChatToolGroupDisabled(disabledGroups, advancedChatToolGroupForTool(tool.Name)) {
			continue
		}
		filtered = append(filtered, tool)
	}
	return filtered
}
