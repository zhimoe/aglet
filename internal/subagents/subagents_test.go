package subagents

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeAgent(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverBuiltinsAndCustom(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, filepath.Join(dir, ".aglet", "agents", "reviewer.md"), `---
name: reviewer
description: Reviews code
tools: read_file, grep, mcp_*
model: deepseek
---
You review code.
`)
	writeAgent(t, filepath.Join(dir, ".aglet", "agents", "explore.md"), `---
description: Custom explorer
tools:
  - read_file
---
Look around.
`)

	list, warnings, err := Discover(dir, []string{".aglet/agents"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if len(list) != 3 {
		t.Fatalf("list = %#v", list)
	}
	reviewer, ok := Find(list, "reviewer")
	if !ok || reviewer.Model != "deepseek" || !slices.Equal(reviewer.Tools, []string{"read_file", "grep", "mcp_*"}) {
		t.Fatalf("reviewer = %#v", reviewer)
	}
	writeAgent(t, filepath.Join(dir, ".aglet", "agents", "claude.md"), "---\ntools: Read, Bash, TodoWrite\n---\nBody")
	list, _, err = Discover(dir, []string{".aglet/agents"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	claude, ok := Find(list, "claude")
	if !ok || !slices.Equal(claude.Tools, []string{"read_file", "run_command", "update_plan"}) {
		t.Fatalf("claude tools = %#v", claude.Tools)
	}
	explore, ok := Find(list, "explore")
	if !ok || explore.BuiltIn || explore.Description != "Custom explorer" || !slices.Equal(explore.Tools, []string{"read_file"}) {
		t.Fatalf("explore override = %#v", explore)
	}
	if general, ok := Find(list, "general"); !ok || !general.BuiltIn {
		t.Fatalf("general = %#v", general)
	}
}

func TestDiscoverWarnsOnMalformed(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, filepath.Join(dir, "agents", "bad.md"), "---\nunterminated")
	writeAgent(t, filepath.Join(dir, "agents", "bad name.md"), "Body")
	list, warnings, err := Discover(dir, []string{"agents"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("builtins missing: %#v", list)
	}
	if len(warnings) != 2 {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestRenderAvailable(t *testing.T) {
	text := RenderAvailable(Builtins())
	if text == "" || !slices.ContainsFunc(Builtins(), func(d Definition) bool { return d.Name == "explore" }) {
		t.Fatal("builtins missing")
	}
}
