package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

const (
	maxReadBytes  = 200 * 1024
	maxFileBytes  = 5 * 1024 * 1024
	maxListItems  = 500
	maxGlobItems  = 500
	maxReadLines  = 2000
	maxLineNumber = 5000
)

type readFileArgs struct {
	Path   string `json:"path" jsonschema:"File path relative to the workspace."`
	Offset int    `json:"offset,omitempty" jsonschema:"First line to read (1-based). Default 1."`
	Limit  int    `json:"limit,omitempty" jsonschema:"Maximum number of lines to read. Default 2000, max 5000."`
}

type readFileResult struct {
	Path      string `json:"path"`
	Content   string `json:"content"`
	Lines     int    `json:"lines"`
	Truncated bool   `json:"truncated"`
}

type writeFileArgs struct {
	Path    string `json:"path" jsonschema:"File path relative to the workspace."`
	Content string `json:"content" jsonschema:"Full file content to write."`
}

type writeFileResult struct {
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}

type editFileArgs struct {
	Path       string `json:"path" jsonschema:"File path relative to the workspace."`
	OldString  string `json:"old_string" jsonschema:"Exact text to replace."`
	NewString  string `json:"new_string" jsonschema:"Replacement text."`
	ReplaceAll bool   `json:"replace_all,omitempty" jsonschema:"Replace every occurrence instead of requiring exactly one."`
}

type editFileResult struct {
	Path         string `json:"path"`
	Replacements int    `json:"replacements"`
}

type listDirArgs struct {
	Path string `json:"path" jsonschema:"Directory path relative to the workspace. Default '.'."`
}

type listDirResult struct {
	Path      string `json:"path"`
	Entries   string `json:"entries"`
	Count     int    `json:"count"`
	Truncated bool   `json:"truncated"`
}

type globArgs struct {
	Pattern string `json:"pattern" jsonschema:"Glob pattern, supports ** (doublestar), relative to path."`
	Path    string `json:"path,omitempty" jsonschema:"Base directory relative to the workspace. Default '.'."`
}

type globResult struct {
	Matches   []string `json:"matches"`
	Count     int      `json:"count"`
	Truncated bool     `json:"truncated"`
}

func newReadFileTool(deps Deps) (tool.Tool, error) {
	handler := func(ctx adkagent.Context, args readFileArgs) (readFileResult, error) {
		if err := authorize(ctx, deps, "read_file", "Read "+args.Path, map[string]any{"path": args.Path}); err != nil {
			return readFileResult{}, err
		}
		abs, err := resolvePath(deps.Workspace, args.Path)
		if err != nil {
			return readFileResult{}, err
		}
		st, err := os.Stat(abs)
		if err != nil {
			return readFileResult{}, err
		}
		if st.IsDir() {
			return readFileResult{}, fmt.Errorf("%s is a directory", args.Path)
		}
		if st.Size() > maxFileBytes {
			return readFileResult{}, fmt.Errorf("file %s is too large (%d bytes, max %d)", args.Path, st.Size(), maxFileBytes)
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return readFileResult{}, err
		}
		text := strings.ReplaceAll(string(data), "\r\n", "\n")
		lines := strings.Split(text, "\n")
		start := args.Offset
		if start < 1 {
			start = 1
		}
		limit := args.Limit
		if limit <= 0 {
			limit = maxReadLines
		}
		if limit > maxLineNumber {
			limit = maxLineNumber
		}
		if start > len(lines) {
			start = len(lines) + 1
		}
		end := start - 1 + limit
		if end > len(lines) {
			end = len(lines)
		}
		var b strings.Builder
		for i := start - 1; i < end; i++ {
			fmt.Fprintf(&b, "%6d\t%s\n", i+1, lines[i])
		}
		content, truncated := truncateUTF8(b.String(), maxReadBytes)
		return readFileResult{
			Path:      args.Path,
			Content:   content,
			Lines:     end - start + 1,
			Truncated: truncated || end < len(lines),
		}, nil
	}
	return functiontool.New(functiontool.Config{
		Name:        "read_file",
		Description: "Read a UTF-8 text file from the workspace. Returns line-numbered content.",
	}, handler)
}

func newWriteFileTool(deps Deps) (tool.Tool, error) {
	handler := func(ctx adkagent.Context, args writeFileArgs) (writeFileResult, error) {
		summary := fmt.Sprintf("Write %d bytes to %s", len(args.Content), args.Path)
		if err := authorize(ctx, deps, "write_file", summary, map[string]any{"path": args.Path, "content": args.Content}); err != nil {
			return writeFileResult{}, err
		}
		abs, err := resolvePath(deps.Workspace, args.Path)
		if err != nil {
			return writeFileResult{}, err
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return writeFileResult{}, err
		}
		if err := os.WriteFile(abs, []byte(args.Content), 0o644); err != nil {
			return writeFileResult{}, err
		}
		return writeFileResult{Path: args.Path, Bytes: len(args.Content)}, nil
	}
	return functiontool.New(functiontool.Config{
		Name:        "write_file",
		Description: "Write full content to a file in the workspace, creating parent directories.",
	}, handler)
}

func newEditFileTool(deps Deps) (tool.Tool, error) {
	handler := func(ctx adkagent.Context, args editFileArgs) (editFileResult, error) {
		if args.OldString == "" {
			return editFileResult{}, fmt.Errorf("old_string must not be empty")
		}
		summary := fmt.Sprintf("Edit %s (replace %q)", args.Path, short(args.OldString, 80))
		if err := authorize(ctx, deps, "edit_file", summary, map[string]any{
			"path": args.Path, "old_string": args.OldString, "new_string": args.NewString,
		}); err != nil {
			return editFileResult{}, err
		}
		abs, err := resolvePath(deps.Workspace, args.Path)
		if err != nil {
			return editFileResult{}, err
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return editFileResult{}, err
		}
		text := string(data)
		count := strings.Count(text, args.OldString)
		if count == 0 {
			return editFileResult{}, fmt.Errorf("old_string not found in %s", args.Path)
		}
		if count > 1 && !args.ReplaceAll {
			return editFileResult{}, fmt.Errorf("old_string occurs %d times in %s; pass replace_all=true or provide a more specific string", count, args.Path)
		}
		updated := strings.ReplaceAll(text, args.OldString, args.NewString)
		if err := os.WriteFile(abs, []byte(updated), 0o644); err != nil {
			return editFileResult{}, err
		}
		return editFileResult{Path: args.Path, Replacements: count}, nil
	}
	return functiontool.New(functiontool.Config{
		Name:        "edit_file",
		Description: "Replace exact text in a file. Requires old_string to be unique unless replace_all is true.",
	}, handler)
}

func newListDirTool(deps Deps) (tool.Tool, error) {
	handler := func(ctx adkagent.Context, args listDirArgs) (listDirResult, error) {
		p := args.Path
		if p == "" {
			p = "."
		}
		if err := authorize(ctx, deps, "list_dir", "List "+p, map[string]any{"path": p}); err != nil {
			return listDirResult{}, err
		}
		abs, err := resolvePath(deps.Workspace, p)
		if err != nil {
			return listDirResult{}, err
		}
		entries, err := os.ReadDir(abs)
		if err != nil {
			return listDirResult{}, err
		}
		var b strings.Builder
		n := 0
		for _, e := range entries {
			if n >= maxListItems {
				break
			}
			info, err := e.Info()
			if err != nil {
				fmt.Fprintf(&b, "? %s\n", e.Name())
			} else if e.IsDir() {
				fmt.Fprintf(&b, "d %s/\n", e.Name())
			} else {
				fmt.Fprintf(&b, "f %s (%d bytes)\n", e.Name(), info.Size())
			}
			n++
		}
		return listDirResult{Path: p, Entries: b.String(), Count: n, Truncated: n >= maxListItems}, nil
	}
	return functiontool.New(functiontool.Config{
		Name:        "list_dir",
		Description: "List entries in a workspace directory.",
	}, handler)
}

func newGlobTool(deps Deps) (tool.Tool, error) {
	handler := func(ctx adkagent.Context, args globArgs) (globResult, error) {
		base := args.Path
		if base == "" {
			base = "."
		}
		if err := authorize(ctx, deps, "glob", "Glob "+args.Pattern+" in "+base, map[string]any{"pattern": args.Pattern, "path": base}); err != nil {
			return globResult{}, err
		}
		if strings.Contains(args.Pattern, "..") {
			return globResult{}, fmt.Errorf("pattern must not contain '..'")
		}
		abs, err := resolvePath(deps.Workspace, base)
		if err != nil {
			return globResult{}, err
		}
		matches, err := doublestar.Glob(os.DirFS(abs), args.Pattern)
		if err != nil {
			return globResult{}, err
		}
		sort.Strings(matches)
		truncated := len(matches) > maxGlobItems
		if truncated {
			matches = matches[:maxGlobItems]
		}
		return globResult{Matches: matches, Count: len(matches), Truncated: truncated}, nil
	}
	return functiontool.New(functiontool.Config{
		Name:        "glob",
		Description: "Find files matching a doublestar glob pattern (supports **) inside the workspace.",
	}, handler)
}

func resolvePath(workspace, p string) (string, error) {
	if p == "" {
		p = "."
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(workspace, p)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	root := workspace
	if real, err := filepath.EvalSymlinks(workspace); err == nil {
		root = real
	}
	if !within(workspace, abs) {
		return "", fmt.Errorf("path %q is outside workspace %s", p, workspace)
	}
	// Resolve symlinks for the target (or its parent, for a new file) so a
	// symlink inside the workspace cannot escape it.
	check := abs
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		check = real
	} else {
		check = evalNearest(abs)
	}
	if !within(root, check) {
		return "", fmt.Errorf("path %q resolves outside workspace %s", p, workspace)
	}
	return abs, nil
}

// evalNearest resolves symlinks in the nearest existing ancestor and appends
// the not-yet-existing suffix. This handles new files/directories.
func evalNearest(p string) string {
	orig := p
	for {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			rel, err := filepath.Rel(p, orig)
			if err != nil {
				return orig
			}
			return filepath.Join(real, rel)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return orig
		}
		p = parent
	}
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func truncateUTF8(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n...[truncated]", true
}

func short(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
