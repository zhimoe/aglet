package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	ctx := context.Background()
	steps := [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	}
	for _, args := range steps {
		if _, err := runGit(ctx, dir, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	path := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(path, []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, dir, "add", "a.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, dir, "commit", "-m", "initial"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	status, err := runGit(ctx, dir, "status", "--short")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "a.txt") {
		t.Fatalf("status = %q", status)
	}
	diff, err := runGit(ctx, dir, "diff", "--no-color")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "+two") {
		t.Fatalf("diff = %q", diff)
	}
	log, err := runGit(ctx, dir, "log", "--oneline", "-n", "5")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log, "initial") {
		t.Fatalf("log = %q", log)
	}

	notRepo := t.TempDir()
	if _, err := runGit(ctx, notRepo, "status"); err == nil {
		t.Fatal("expected error outside a git repo")
	}
}
