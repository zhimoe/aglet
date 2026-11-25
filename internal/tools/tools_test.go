package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePath(t *testing.T) {
	dir := t.TempDir()
	got, err := resolvePath(dir, "a/b.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, dir) {
		t.Fatalf("path %q not under %q", got, dir)
	}
	if _, err := resolvePath(dir, "../escape"); err == nil {
		t.Fatal("expected escape error")
	}
	if _, err := resolvePath(dir, "/etc/passwd"); err == nil {
		t.Fatal("expected absolute-outside error")
	}
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	if _, err := resolvePath(dir, "link.txt"); err == nil {
		t.Fatal("expected symlink escape error")
	}
}

func TestTruncateUTF8(t *testing.T) {
	s := strings.Repeat("你好", 100)
	out, cut := truncateUTF8(s, 10)
	if !cut {
		t.Fatal("expected truncation")
	}
	if !strings.HasPrefix(s, strings.TrimSuffix(out, "\n...[truncated]")) {
		t.Fatalf("bad truncation: %q", out)
	}
}

func TestLimitedBuffer(t *testing.T) {
	b := &limitedBuffer{limit: 5}
	n, err := b.Write([]byte("abcdef"))
	if err != nil || n != 6 {
		t.Fatalf("write = %d, %v", n, err)
	}
	if b.String() != "abcde" || !b.truncated {
		t.Fatalf("buf = %q truncated=%v", b.String(), b.truncated)
	}
}

func TestWalkGrep(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello foo\nbar"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.md"), []byte("foo again"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := walkGrep(dir, grepArgs{Pattern: "foo"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Count != 2 || !strings.Contains(res.Matches, "a.txt:1") || !strings.Contains(res.Matches, "b.md:1") {
		t.Fatalf("result = %#v", res)
	}
	res, err = walkGrep(dir, grepArgs{Pattern: "foo", Glob: "**/*.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Count != 1 || !strings.Contains(res.Matches, "a.txt") {
		t.Fatalf("glob result = %#v", res)
	}
}
