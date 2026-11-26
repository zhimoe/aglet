package commands

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscover(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".aglet", "commands", "review.md"), `---
description: Review a diff
argument-hint: "[base]"
---
Review $ARGUMENTS for bugs.
`)
	writeFile(t, filepath.Join(dir, ".aglet", "commands", "git", "status.md"), "Show git status.\n")
	writeFile(t, filepath.Join(dir, ".aglet", "commands", "fix.md"), "---\nargument-hint: [issue]\n---\nFix $1.\n")
	writeFile(t, filepath.Join(dir, ".aglet", "commands", "bad name.md"), "nope")

	list, warnings, err := Discover(dir, []string{".aglet/commands"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("commands = %#v (warnings %v)", list, warnings)
	}
	if list[0].Name != "fix" || list[0].ArgumentHint != "[issue]" {
		t.Fatalf("fix = %#v", list[0])
	}
	if list[1].Name != "git:status" || list[2].Name != "review" {
		t.Fatalf("names = %q %q", list[1].Name, list[2].Name)
	}
	if list[2].Description != "Review a diff" || list[2].ArgumentHint != "[base]" {
		t.Fatalf("review = %#v", list[2])
	}
	if len(warnings) == 0 {
		t.Fatal("expected warning for bad command name")
	}
}

func TestDiscoverDuplicateKeepsFirst(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a", "same.md"), "first")
	writeFile(t, filepath.Join(dir, "b", "same.md"), "second")
	list, warnings, err := Discover(dir, []string{"a", "b"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Body != "first" {
		t.Fatalf("list = %#v", list)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestParseInvocation(t *testing.T) {
	name, args, ok := ParseInvocation("  /git:review HEAD~1  ")
	if !ok || name != "git:review" || args != "HEAD~1" {
		t.Fatalf("got %q %q %v", name, args, ok)
	}
	if _, _, ok := ParseInvocation("plain"); ok {
		t.Fatal("plain prompt should not parse")
	}
	if _, _, ok := ParseInvocation("/"); ok {
		t.Fatal("bare slash should not parse")
	}
}

func TestExpand(t *testing.T) {
	c := Command{Body: "A $ARGUMENTS B $1 C $2 D \\$ARGUMENTS"}
	got := Expand(c, "one two")
	want := "A one two B one C two D $ARGUMENTS"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	appended := Expand(Command{Body: "Do it."}, "with detail")
	if appended != "Do it.\n\nwith detail" {
		t.Fatalf("append = %q", appended)
	}
	if got := Expand(Command{Body: "Arg: $1"}, ""); got != "Arg: " {
		t.Fatalf("empty positional = %q", got)
	}
}
