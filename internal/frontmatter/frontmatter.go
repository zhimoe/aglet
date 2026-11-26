// Package frontmatter splits Markdown files with a YAML frontmatter header.
package frontmatter

import (
	"fmt"
	"strings"
)

// Document is a Markdown file split into its optional YAML header and body.
type Document struct {
	Header         string
	Body           string
	HasFrontmatter bool
}

// Split separates an optional leading YAML frontmatter block from the body.
// It accepts a leading BOM, CRLF line endings, and an empty header.
func Split(data []byte) (Document, error) {
	text := strings.TrimPrefix(string(data), "\xef\xbb\xbf")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return Document{Body: text}, nil
	}
	rest := strings.TrimPrefix(text, "---\n")
	header, body, ok := cutClosingDelimiter(rest)
	if !ok {
		return Document{}, fmt.Errorf("unterminated frontmatter")
	}
	return Document{
		Header:         header,
		Body:           body,
		HasFrontmatter: true,
	}, nil
}

// cutClosingDelimiter finds a line consisting exactly of "---".
func cutClosingDelimiter(s string) (header, body string, ok bool) {
	if strings.HasPrefix(s, "---\n") {
		return "", s[4:], true
	}
	if s == "---" {
		return "", "", true
	}
	offset := 0
	for {
		idx := strings.Index(s[offset:], "\n---")
		if idx < 0 {
			return "", "", false
		}
		start := offset + idx
		end := start + len("\n---")
		if end == len(s) || s[end] == '\n' {
			return s[:start], strings.TrimPrefix(s[end:], "\n"), true
		}
		offset = start + 1
	}
}
