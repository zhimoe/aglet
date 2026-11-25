package tools

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

const (
	maxGrepMatches = 200
	maxGrepFile    = 2 * 1024 * 1024
	maxGrepBytes   = 1024 * 1024
)

type grepArgs struct {
	Pattern    string `json:"pattern" jsonschema:"Regular expression to search for."`
	Path       string `json:"path,omitempty" jsonschema:"File or directory relative to the workspace. Default '.'."`
	Glob       string `json:"glob,omitempty" jsonschema:"Optional doublestar glob filter, e.g. '**/*.go'."`
	IgnoreCase bool   `json:"ignore_case,omitempty" jsonschema:"Case-insensitive matching."`
}

type grepResult struct {
	Matches   string `json:"matches"`
	Count     int    `json:"count"`
	Truncated bool   `json:"truncated"`
}

func newGrepTool(deps Deps) (tool.Tool, error) {
	handler := func(ctx adkagent.Context, args grepArgs) (grepResult, error) {
		if args.Pattern == "" {
			return grepResult{}, fmt.Errorf("pattern is required")
		}
		p := args.Path
		if p == "" {
			p = "."
		}
		summary := fmt.Sprintf("Grep %q in %s", args.Pattern, p)
		if err := authorize(ctx, deps, "grep", summary, map[string]any{
			"pattern": args.Pattern, "path": p, "glob": args.Glob, "ignore_case": args.IgnoreCase,
		}); err != nil {
			return grepResult{}, err
		}
		abs, err := resolvePath(deps.Workspace, p)
		if err != nil {
			return grepResult{}, err
		}
		if out, ok, err := runRipgrep(ctx, abs, args); ok {
			if err != nil {
				return grepResult{}, err
			}
			matches, truncated := truncateUTF8(out, maxGrepBytes)
			count := strings.Count(strings.TrimSpace(out), "\n") + 1
			if strings.TrimSpace(out) == "" {
				count = 0
			}
			if count > maxGrepMatches {
				count = maxGrepMatches
			}
			return grepResult{Matches: matches, Count: count, Truncated: truncated}, nil
		}
		return walkGrep(abs, args)
	}
	return functiontool.New(functiontool.Config{
		Name:        "grep",
		Description: "Search file contents with a regular expression. Uses ripgrep when available.",
	}, handler)
}

func runRipgrep(ctx context.Context, abs string, args grepArgs) (string, bool, error) {
	rg, err := exec.LookPath("rg")
	if err != nil {
		return "", false, nil
	}
	cmdArgs := []string{"--line-number", "--no-heading", "--color", "never", "--max-count", "50", "-e", args.Pattern}
	if args.IgnoreCase {
		cmdArgs = append(cmdArgs, "-i")
	}
	if args.Glob != "" {
		cmdArgs = append(cmdArgs, "--glob", args.Glob)
	}
	cmdArgs = append(cmdArgs, "--", abs)
	cmd := exec.CommandContext(ctx, rg, cmdArgs...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	if err == nil {
		return stdout.String(), true, nil
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
		return "", true, nil
	}
	// rg failed for another reason (bad pattern, permission, etc.).
	if stderr.Len() > 0 {
		return "", true, fmt.Errorf("ripgrep: %s", strings.TrimSpace(stderr.String()))
	}
	return "", false, nil
}

func walkGrep(abs string, args grepArgs) (grepResult, error) {
	flags := ""
	if args.IgnoreCase {
		flags = "(?i)"
	}
	re, err := regexp.Compile(flags + args.Pattern)
	if err != nil {
		return grepResult{}, fmt.Errorf("invalid pattern: %w", err)
	}
	var lines []string
	truncated := false
	err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".hg", ".svn", "node_modules", "vendor", ".venv", "__pycache__":
				return filepath.SkipDir
			}
			return nil
		}
		if len(lines) >= maxGrepMatches {
			truncated = true
			return filepath.SkipAll
		}
		rel, err := filepath.Rel(abs, path)
		if err != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if args.Glob != "" {
			ok, err := doublestar.Match(args.Glob, relSlash)
			if err != nil || !ok {
				return nil
			}
		}
		info, err := d.Info()
		if err != nil || info.Size() > maxGrepFile {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if bytes.IndexByte(data, 0) >= 0 {
			return nil
		}
		for i, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
			if re.MatchString(line) {
				lines = append(lines, fmt.Sprintf("%s:%d:%s", relSlash, i+1, line))
				if len(lines) >= maxGrepMatches {
					truncated = true
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		return grepResult{}, err
	}
	sort.Strings(lines)
	out, cut := truncateUTF8(strings.Join(lines, "\n"), maxGrepBytes)
	return grepResult{Matches: out, Count: len(lines), Truncated: truncated || cut}, nil
}
