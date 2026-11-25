package instructions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAncestorAndExplicit(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ONLYHERE.md"), []byte("root rules"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "RULES.md"), []byte("sub rules"), 0o644); err != nil {
		t.Fatal(err)
	}
	sections, err := Load(dir, []string{"ONLYHERE.md"}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 1 || !strings.Contains(sections[0].Content, "root rules") {
		t.Fatalf("sections = %#v", sections)
	}
	sections, err = Load(dir, []string{"sub/RULES.md"}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 1 || !strings.Contains(sections[0].Content, "sub rules") {
		t.Fatalf("explicit sections = %#v", sections)
	}
	if sections, _ := Load(dir, []string{}, 1024); len(sections) != 0 {
		t.Fatalf("empty files should disable, got %#v", sections)
	}
}

func TestLoadTruncates(t *testing.T) {
	dir := t.TempDir()
	name := "BIG_RULES.md"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Repeat("x", 100)), 0o644); err != nil {
		t.Fatal(err)
	}
	sections, err := Load(dir, []string{name}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(sections) != 1 || !sections[0].Truncated || len(sections[0].Content) != 20 {
		t.Fatalf("sections = %#v", sections)
	}
}

func TestRender(t *testing.T) {
	out := Render([]Section{{Path: "/x/AGENTS.md", Content: "hello"}})
	if !strings.Contains(out, `<project_instructions path="/x/AGENTS.md">`) || !strings.Contains(out, "hello") {
		t.Fatalf("render = %q", out)
	}
}
