package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverFrontmatterAndFind(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, ".agents", "skills")
	writeSkill(t, root, "git-review", `---
name: review
description: Review diffs
---
# Review
Do the review.
`)
	writeSkill(t, root, "plain", "Plain instruction line\nmore")

	list, warnings, err := Discover(dir, []string{".agents/skills"}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if len(list) != 2 {
		t.Fatalf("list = %#v", list)
	}
	s, ok := Find(list, "review")
	if !ok || s.Description != "Review diffs" || !strings.Contains(s.Body, "Do the review") {
		t.Fatalf("skill = %#v", s)
	}
	if p, _ := Find(list, "plain"); p.Description != "Plain instruction line" {
		t.Fatalf("plain description = %q", p.Description)
	}
}

func TestDuplicateAndMalformedWarnings(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "a", "---\nname: same\ndescription: first\n---\nfirst")
	writeSkill(t, root, "b", "---\nname: same\ndescription: second\n---\nsecond")
	writeSkill(t, root, "bad", "---\nname: bad\nno closing")
	list, warnings, err := Discover(root, []string{"."}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "same" {
		t.Fatalf("list = %#v", list)
	}
	if len(warnings) < 2 {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestRenderAvailableHidesHidden(t *testing.T) {
	out := RenderAvailable([]Skill{
		{Name: "shown", Description: "visible", Path: "/a"},
		{Name: "hidden", Description: "hidden", Path: "/b", Hidden: true},
	})
	if !strings.Contains(out, `<skill name="shown"`) || strings.Contains(out, "hidden") {
		t.Fatalf("render = %q", out)
	}
}
