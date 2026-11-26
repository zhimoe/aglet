// Package commands discovers and expands user-defined slash commands.
package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/zhimoe/aglet/internal/config"
	"github.com/zhimoe/aglet/internal/frontmatter"
)

// Command is a Markdown prompt template invoked as /name.
type Command struct {
	Name         string
	Description  string
	ArgumentHint string
	Path         string
	Body         string
}

type commandFrontmatter struct {
	Description  string     `yaml:"description"`
	ArgumentHint hintString `yaml:"argument-hint"`
}

// hintString accepts both `argument-hint: "[base]"` and the unquoted YAML
// sequence form `argument-hint: [base]` used by Claude commands.
type hintString string

func (h *hintString) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		*h = hintString(node.Value)
		return nil
	case yaml.SequenceNode:
		var parts []string
		if err := node.Decode(&parts); err != nil {
			return err
		}
		*h = hintString("[" + strings.Join(parts, ", ") + "]")
		return nil
	default:
		return fmt.Errorf("argument-hint must be a string or a list")
	}
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_:-]*$`)

// Discover loads commands from the configured directories. Relative
// directories are resolved against the workspace. Missing directories are
// ignored; malformed files produce warnings.
func Discover(workspace string, dirs []string, maxBytes int) ([]Command, []string, error) {
	if maxBytes <= 0 {
		maxBytes = 64 * 1024
	}
	abs, err := filepath.Abs(config.ExpandPath(workspace))
	if err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	var warnings []string
	var out []Command
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		root := config.ExpandPath(dir)
		if !filepath.IsAbs(root) {
			root = filepath.Join(abs, root)
		}
		if st, err := os.Stat(root); err != nil || !st.IsDir() {
			continue
		}
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".md") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: %v", path, err))
				return nil
			}
			cmd, err := parse(root, path, data, maxBytes)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: %v", path, err))
				return nil
			}
			if seen[cmd.Name] {
				warnings = append(warnings, fmt.Sprintf("duplicate command %q ignored: %s", cmd.Name, path))
				return nil
			}
			seen[cmd.Name] = true
			out = append(out, cmd)
			return nil
		})
		if err != nil {
			return nil, warnings, err
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, warnings, nil
}

func parse(root, path string, data []byte, maxBytes int) (Command, error) {
	doc, err := frontmatter.Split(data)
	if err != nil {
		return Command{}, err
	}
	var fm commandFrontmatter
	if doc.HasFrontmatter {
		if err := yaml.Unmarshal([]byte(doc.Header), &fm); err != nil {
			return Command{}, fmt.Errorf("frontmatter: %w", err)
		}
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return Command{}, err
	}
	name := strings.TrimSuffix(rel, filepath.Ext(rel))
	name = strings.ReplaceAll(name, string(filepath.Separator), ":")
	name = strings.TrimSpace(name)
	if !namePattern.MatchString(name) {
		return Command{}, fmt.Errorf("invalid command name %q", name)
	}
	body := strings.TrimSpace(doc.Body)
	if len(body) > maxBytes {
		body = body[:maxBytes] + "\n...[truncated]"
	}
	desc := strings.TrimSpace(fm.Description)
	if desc == "" {
		for line := range strings.SplitSeq(body, "\n") {
			line = strings.TrimSpace(strings.TrimLeft(line, "#"))
			if line != "" {
				desc = line
				break
			}
		}
	}
	return Command{
		Name:         name,
		Description:  desc,
		ArgumentHint: strings.TrimSpace(string(fm.ArgumentHint)),
		Path:         path,
		Body:         body,
	}, nil
}

// Find returns the command with the exact name.
func Find(list []Command, name string) (Command, bool) {
	for _, c := range list {
		if c.Name == name {
			return c, true
		}
	}
	return Command{}, false
}

// ParseInvocation splits a leading "/name args" invocation.
func ParseInvocation(line string) (name, args string, ok bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "/") {
		return "", "", false
	}
	rest := strings.TrimSpace(line[1:])
	if rest == "" {
		return "", "", false
	}
	name, args, _ = strings.Cut(rest, " ")
	return name, strings.TrimSpace(args), true
}

const dollarSentinel = "\x00aglet-dollar\x00"

// Expand substitutes $ARGUMENTS and $1..$9, then appends args when the body
// has no placeholder. A literal dollar is written as \$.
func Expand(c Command, args string) string {
	args = strings.TrimSpace(args)
	fields := strings.Fields(args)
	body := strings.ReplaceAll(c.Body, `\$`, dollarSentinel)
	replaced := false
	for i := 1; i <= 9; i++ {
		placeholder := "$" + strconv.Itoa(i)
		if !strings.Contains(body, placeholder) {
			continue
		}
		value := ""
		if i-1 < len(fields) {
			value = fields[i-1]
		}
		body = strings.ReplaceAll(body, placeholder, value)
		replaced = true
	}
	if strings.Contains(body, "$ARGUMENTS") {
		body = strings.ReplaceAll(body, "$ARGUMENTS", args)
		replaced = true
	}
	body = strings.ReplaceAll(body, dollarSentinel, "$")
	if !replaced && args != "" {
		body += "\n\n" + args
	}
	return body
}
