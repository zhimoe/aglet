package diff

import (
	"strings"
	"testing"
)

func TestUnified(t *testing.T) {
	out := Unified("a.txt", "one\ntwo\n", "one\nthree\n", 3)
	if !strings.Contains(out, "--- a.txt") || !strings.Contains(out, "+++ a.txt") {
		t.Fatalf("missing headers: %q", out)
	}
	if !strings.Contains(out, "-two") || !strings.Contains(out, "+three") {
		t.Fatalf("missing changes: %q", out)
	}
	if Unified("a.txt", "same", "same", 3) != "" {
		t.Fatal("expected empty diff")
	}
	newFile := Unified("new.txt", "", "hello\n", 3)
	if !strings.Contains(newFile, "--- /dev/null") || !strings.Contains(newFile, "@@ -0,0 +1 @@") || !strings.Contains(newFile, "+hello") {
		t.Fatalf("new file diff = %q", newFile)
	}
	if strings.Contains(newFile, "\n-\n") {
		t.Fatalf("new file diff should not remove a phantom empty line: %q", newFile)
	}
}
