package service

import "testing"

func TestPluginGitHubRepository(t *testing.T) {
	owner, repository, ok := pluginGitHubRepository("https://github.com/veloce-ailab/example-plugin.git")
	if !ok || owner != "veloce-ailab" || repository != "example-plugin" {
		t.Fatalf("pluginGitHubRepository() = %q, %q, %v", owner, repository, ok)
	}
	for _, value := range []string{
		"http://github.com/veloce-ailab/example-plugin",
		"https://github.com/veloce-ailab/example-plugin/releases",
		"https://example.com/veloce-ailab/example-plugin",
		"https://github.com/veloce-ailab/example-plugin?x=1",
	} {
		if _, _, ok := pluginGitHubRepository(value); ok {
			t.Fatalf("pluginGitHubRepository(%q) unexpectedly succeeded", value)
		}
	}
}
