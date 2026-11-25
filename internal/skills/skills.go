package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/zhimoe/aglet/internal/config"
)

type Skill struct {
	Name        string
	Description string
	Path        string
	Body        string
	Hidden      bool
}

type frontmatter struct {
	Name                   string `yaml:"name"`
	Description            string `yaml:"description"`
	DisableModelInvocation bool   `yaml:"disable-model-invocation"`
}

// Discover loads SKILL.md files from the configured directories.
func Discover(workspace string, dirs []string, maxBytes int) ([]Skill, []string, error) {
	if maxBytes <= 0 {
		maxBytes = 64 * 1024
	}
	abs, err := filepath.Abs(config.ExpandPath(workspace))
	if err != nil {
		return nil, nil, err
	}
	var warnings []string
	seen := map[string]bool{}
	var out []Skill
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		p := config.ExpandPath(dir)
		if !filepath.IsAbs(p) {
			p = filepath.Join(abs, p)
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			continue
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			skillPath := filepath.Join(p, e.Name(), "SKILL.md")
			data, err := os.ReadFile(skillPath)
			if err != nil {
				continue
			}
			s, err := parse(e.Name(), skillPath, data, maxBytes)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: %v", skillPath, err))
				continue
			}
			if seen[s.Name] {
				warnings = append(warnings, fmt.Sprintf("duplicate skill %q ignored: %s", s.Name, skillPath))
				continue
			}
			seen[s.Name] = true
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, warnings, nil
}

func parse(defaultName, path string, data []byte, maxBytes int) (Skill, error) {
	text := strings.TrimPrefix(string(data), "\ufeff")
	var fm frontmatter
	body := text
	if strings.HasPrefix(text, "---") {
		rest := strings.TrimPrefix(text, "---")
		idx := strings.Index(rest, "\n---")
		if idx < 0 {
			return Skill{}, fmt.Errorf("unterminated frontmatter")
		}
		header := rest[:idx]
		body = strings.TrimPrefix(rest[idx+4:], "\n")
		if err := yaml.Unmarshal([]byte(header), &fm); err != nil {
			return Skill{}, fmt.Errorf("frontmatter: %w", err)
		}
	}
	name := strings.TrimSpace(fm.Name)
	if name == "" {
		name = defaultName
	}
	desc := strings.TrimSpace(fm.Description)
	if desc == "" {
		for _, line := range strings.Split(body, "\n") {
			line = strings.TrimSpace(strings.TrimLeft(line, "#"))
			if line != "" {
				desc = line
				break
			}
		}
	}
	body = strings.TrimSpace(body)
	if len(body) > maxBytes {
		body = body[:maxBytes] + "\n...[truncated]"
	}
	if name == "" {
		return Skill{}, fmt.Errorf("empty skill name")
	}
	return Skill{Name: name, Description: desc, Path: path, Body: body, Hidden: fm.DisableModelInvocation}, nil
}

func Find(list []Skill, name string) (Skill, bool) {
	for _, s := range list {
		if s.Name == name {
			return s, true
		}
	}
	return Skill{}, false
}

func RenderAvailable(list []Skill) string {
	var visible []Skill
	for _, s := range list {
		if !s.Hidden {
			visible = append(visible, s)
		}
	}
	if len(visible) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<available_skills>\n")
	for _, s := range visible {
		fmt.Fprintf(&b, "  <skill name=%q location=%q>%s</skill>\n", s.Name, s.Path, s.Description)
	}
	b.WriteString("</available_skills>\nCall load_skill with a skill name to read its full instructions before using it.\n")
	return b.String()
}
