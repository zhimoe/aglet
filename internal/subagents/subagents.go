// Package subagents discovers subagent definitions used by the task tool.
package subagents

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/zhimoe/aglet/internal/config"
	"github.com/zhimoe/aglet/internal/frontmatter"
)

// Definition describes a subagent role.
type Definition struct {
	Name        string
	Description string
	Tools       []string // glob patterns; empty means every local and MCP tool
	Model       string   // "" or "inherit" uses the parent model
	Body        string
	Path        string
	BuiltIn     bool
}

type definitionFrontmatter struct {
	Name        string     `yaml:"name"`
	Description string     `yaml:"description"`
	Tools       stringList `yaml:"tools"`
	Model       string     `yaml:"model"`
}

type stringList []string

func (s *stringList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		value := strings.TrimSpace(node.Value)
		if value == "" {
			*s = nil
			return nil
		}
		for part := range strings.SplitSeq(value, ",") {
			if part = strings.TrimSpace(part); part != "" {
				*s = append(*s, part)
			}
		}
		return nil
	case yaml.SequenceNode:
		var out []string
		if err := node.Decode(&out); err != nil {
			return err
		}
		for _, part := range out {
			if part = strings.TrimSpace(part); part != "" {
				*s = append(*s, part)
			}
		}
		return nil
	default:
		return fmt.Errorf("tools must be a string or a list")
	}
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// Builtins are always available, even when no directories exist.
func Builtins() []Definition {
	return []Definition{
		{
			Name:        "general",
			Description: "General-purpose subagent with every local and MCP tool.",
			Body: `You are a general-purpose subagent working inside aglet. Complete the assigned task using the available tools.
You share the workspace with the parent agent but cannot see its conversation. Do not ask the user questions.
Return a concise final report with the concrete result, files changed, and how you verified it.`,
			BuiltIn: true,
		},
		{
			Name:        "explore",
			Description: "Read-only explorer for codebase research and web lookups.",
			Tools: []string{
				"read_file", "list_dir", "glob", "grep",
				"git_status", "git_diff", "git_log",
				"load_skill", "web_fetch", "web_search",
			},
			Body: `You are a read-only exploration subagent working inside aglet. Search and read the codebase and web to answer the task.
Do not modify files and do not run commands. Do not ask the user questions.
Return concrete findings with file paths, line references, and relevant URLs.`,
			BuiltIn: true,
		},
	}
}

// Discover loads custom definitions and merges the built-ins. Custom
// definitions override a built-in with the same name.
func Discover(workspace string, dirs []string, maxBytes int) ([]Definition, []string, error) {
	if maxBytes <= 0 {
		maxBytes = 64 * 1024
	}
	abs, err := filepath.Abs(config.ExpandPath(workspace))
	if err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	var warnings []string
	var out []Definition
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
			def, err := parse(path, data, maxBytes)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: %v", path, err))
				return nil
			}
			if seen[def.Name] {
				warnings = append(warnings, fmt.Sprintf("duplicate subagent %q ignored: %s", def.Name, path))
				return nil
			}
			seen[def.Name] = true
			out = append(out, def)
			return nil
		})
		if err != nil {
			return nil, warnings, err
		}
	}
	for _, def := range Builtins() {
		if !seen[def.Name] {
			seen[def.Name] = true
			out = append(out, def)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, warnings, nil
}

func parse(path string, data []byte, maxBytes int) (Definition, error) {
	doc, err := frontmatter.Split(data)
	if err != nil {
		return Definition{}, err
	}
	var fm definitionFrontmatter
	if doc.HasFrontmatter {
		if err := yaml.Unmarshal([]byte(doc.Header), &fm); err != nil {
			return Definition{}, fmt.Errorf("frontmatter: %w", err)
		}
	}
	name := strings.TrimSpace(fm.Name)
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if !namePattern.MatchString(name) {
		return Definition{}, fmt.Errorf("invalid subagent name %q", name)
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
	tools := make([]string, 0, len(fm.Tools))
	for _, tool := range fm.Tools {
		tools = append(tools, normalizeToolName(tool))
	}
	return Definition{
		Name:        name,
		Description: desc,
		Tools:       tools,
		Model:       strings.TrimSpace(fm.Model),
		Body:        body,
		Path:        path,
	}, nil
}

// normalizeToolName maps Claude Code tool names to aglet's tool names.
func normalizeToolName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "read":
		return "read_file"
	case "write":
		return "write_file"
	case "edit", "multiedit":
		return "edit_file"
	case "bash":
		return "run_command"
	case "ls":
		return "list_dir"
	case "webfetch":
		return "web_fetch"
	case "websearch":
		return "web_search"
	case "todowrite":
		return "update_plan"
	case "skill":
		return "load_skill"
	case "bashoutput":
		return "bash_output"
	case "killshell":
		return "kill_shell"
	default:
		return strings.TrimSpace(name)
	}
}

// Find returns the definition with the exact name.
func Find(list []Definition, name string) (Definition, bool) {
	for _, def := range list {
		if def.Name == name {
			return def, true
		}
	}
	return Definition{}, false
}

// RenderAvailable returns the system-prompt index of subagent types.
func RenderAvailable(list []Definition) string {
	if len(list) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<available_subagents>\n")
	for _, def := range list {
		fmt.Fprintf(&b, "  <subagent name=%q>%s</subagent>\n", def.Name, def.Description)
	}
	b.WriteString("</available_subagents>\nCall task with subagent_type, description, and a self-contained prompt to delegate work.\n")
	return b.String()
}
