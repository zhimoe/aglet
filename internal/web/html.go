package web

import (
	"bytes"
	"errors"
	"io"
	"strings"

	"golang.org/x/net/html"
)

// HTMLToText extracts a title and readable text from an HTML document.
func HTMLToText(data []byte) (string, string, error) {
	z := html.NewTokenizer(bytes.NewReader(data))
	var title, body textBuilder
	skipDepth := 0
	preDepth := 0
	inTitle := false
	href := ""
	for {
		tokenType := z.Next()
		switch tokenType {
		case html.ErrorToken:
			if errors.Is(z.Err(), io.EOF) {
				return cleanText(title.String()), cleanText(body.String()), nil
			}
			return "", "", z.Err()
		case html.TextToken:
			text := string(z.Text())
			if skipDepth > 0 {
				continue
			}
			if inTitle {
				title.appendString(text)
				continue
			}
			appendText(&body, text, preDepth > 0)
		case html.StartTagToken, html.SelfClosingTagToken:
			nameBytes, hasAttr := z.TagName()
			tag := string(nameBytes)
			if skipTags[tag] {
				if tokenType == html.StartTagToken {
					skipDepth++
				}
				continue
			}
			if skipDepth > 0 {
				continue
			}
			switch tag {
			case "title":
				inTitle = true
				continue
			case "pre":
				preDepth++
				newline(&body)
				continue
			case "br":
				newline(&body)
				continue
			case "hr":
				newline(&body)
				body.appendString("---")
				newline(&body)
				continue
			case "h1", "h2", "h3", "h4", "h5", "h6":
				newline(&body)
				body.appendString(strings.Repeat("#", int(tag[1]-'0')) + " ")
				continue
			case "li":
				newline(&body)
				body.appendString("- ")
				continue
			case "a":
				href = ""
				if hasAttr {
					for {
						key, value, more := z.TagAttr()
						if string(key) == "href" {
							href = string(value)
						}
						if !more {
							break
						}
					}
				}
				continue
			}
			if blockTags[tag] {
				newline(&body)
			}
		case html.EndTagToken:
			nameBytes, _ := z.TagName()
			tag := string(nameBytes)
			if skipTags[tag] {
				if skipDepth > 0 {
					skipDepth--
				}
				continue
			}
			if skipDepth > 0 {
				continue
			}
			switch tag {
			case "title":
				inTitle = false
				continue
			case "pre":
				if preDepth > 0 {
					preDepth--
				}
				newline(&body)
				continue
			case "a":
				if href != "" {
					body.appendString(" (" + href + ")")
					href = ""
				}
				continue
			}
			if blockTags[tag] {
				newline(&body)
			}
		}
	}
}

var skipTags = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true, "svg": true, "iframe": true, "object": true,
}

var blockTags = map[string]bool{
	"p": true, "div": true, "section": true, "article": true, "header": true, "footer": true,
	"main": true, "aside": true, "nav": true, "ul": true, "ol": true, "table": true, "tr": true,
	"blockquote": true, "form": true, "figure": true, "figcaption": true, "dl": true, "dt": true, "dd": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
}

type textBuilder struct {
	b    strings.Builder
	last byte
}

func (t *textBuilder) appendString(s string) {
	if s == "" {
		return
	}
	t.b.WriteString(s)
	t.last = s[len(s)-1]
}

func (t *textBuilder) appendByte(c byte) {
	t.b.WriteByte(c)
	t.last = c
}

func (t *textBuilder) Len() int       { return t.b.Len() }
func (t *textBuilder) String() string { return t.b.String() }

func appendText(t *textBuilder, text string, pre bool) {
	if pre {
		t.appendString(text)
		return
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return
	}
	if t.Len() > 0 && t.last != ' ' && t.last != '\n' {
		t.appendByte(' ')
	}
	t.appendString(strings.Join(fields, " "))
}

func newline(t *textBuilder) {
	if t.Len() == 0 || t.last == '\n' {
		return
	}
	t.appendByte('\n')
}

func cleanText(s string) string {
	lines := strings.Split(s, "\n")
	out := lines[:0]
	blank := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			if len(out) == 0 || blank {
				continue
			}
			blank = true
			out = append(out, "")
			continue
		}
		blank = false
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
