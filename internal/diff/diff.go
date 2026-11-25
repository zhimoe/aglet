package diff

import (
	"strings"
	"unicode/utf8"

	"github.com/pmezard/go-difflib/difflib"
)

const (
	maxLines = 2000
	maxBytes = 256 * 1024
)

// Unified returns a unified diff between oldText and newText, or "" when equal.
func Unified(path, oldText, newText string, contextLines int) string {
	oldText = strings.ReplaceAll(oldText, "\r\n", "\n")
	newText = strings.ReplaceAll(newText, "\r\n", "\n")
	if oldText == newText {
		return ""
	}
	if contextLines < 0 {
		contextLines = 3
	}
	from := path
	var a []string
	if oldText == "" {
		from = "/dev/null"
	} else {
		a = splitLines(oldText)
	}
	d := difflib.UnifiedDiff{
		A:        a,
		B:        splitLines(newText),
		FromFile: from,
		ToFile:   path,
		Context:  contextLines,
	}
	var b strings.Builder
	if err := difflib.WriteUnifiedDiff(&b, d); err != nil {
		return ""
	}
	return truncate(b.String())
}

// splitLines keeps a trailing newline from producing a phantom empty line.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return difflib.SplitLines(strings.TrimSuffix(s, "\n"))
}

func truncate(s string) string {
	if len(s) > maxBytes {
		cut := maxBytes
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "\n...[diff truncated]"
	}
	lines := strings.Split(s, "\n")
	if len(lines) > maxLines {
		s = strings.Join(lines[:maxLines], "\n") + "\n...[diff truncated]"
	}
	return s
}
