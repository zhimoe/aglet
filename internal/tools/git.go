package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

const (
	gitTimeout    = 30 * time.Second
	maxGitBytes   = 64 * 1024
	maxGitLogRows = 50
)

type gitStatusArgs struct{}

type gitDiffArgs struct {
	Staged bool   `json:"staged,omitempty" jsonschema:"Show staged changes instead of unstaged changes."`
	Path   string `json:"path,omitempty" jsonschema:"Optional path or directory to limit the diff."`
	Base   string `json:"base,omitempty" jsonschema:"Optional git revision to diff against, e.g. main."`
}

type gitLogArgs struct {
	Limit int    `json:"limit,omitempty" jsonschema:"Number of commits (default 10, max 50)."`
	Path  string `json:"path,omitempty" jsonschema:"Optional path to limit history."`
}

type gitCommitArgs struct {
	Message string `json:"message" jsonschema:"Commit message."`
	All     bool   `json:"all,omitempty" jsonschema:"Stage all changes with git add -A before committing."`
}

type gitTextResult struct {
	Output    string `json:"output"`
	Truncated bool   `json:"truncated"`
}

func newGitStatusTool(deps Deps) (tool.Tool, error) {
	handler := func(ctx adkagent.Context, _ gitStatusArgs) (gitTextResult, error) {
		if err := authorize(ctx, deps, "git_status", "git status", nil); err != nil {
			return gitTextResult{}, err
		}
		out, err := runGit(ctx, deps.Workspace, "status", "--short", "--branch")
		return gitTextResult{Output: out}, err
	}
	return functiontool.New(functiontool.Config{
		Name:        "git_status",
		Description: "Show git working tree status (short format).",
	}, handler)
}

func newGitDiffTool(deps Deps) (tool.Tool, error) {
	handler := func(ctx adkagent.Context, args gitDiffArgs) (gitTextResult, error) {
		summary := "git diff"
		if args.Staged {
			summary = "git diff --staged"
		}
		if err := authorize(ctx, deps, "git_diff", summary, map[string]any{
			"staged": args.Staged, "path": args.Path, "base": args.Base,
		}); err != nil {
			return gitTextResult{}, err
		}
		cmdArgs := []string{"diff", "--no-color"}
		if args.Staged {
			cmdArgs = append(cmdArgs, "--staged")
		}
		if args.Base != "" {
			cmdArgs = append(cmdArgs, args.Base)
		}
		if args.Path != "" {
			if strings.Contains(args.Path, "..") {
				return gitTextResult{}, fmt.Errorf("path must not contain '..'")
			}
			cmdArgs = append(cmdArgs, "--", args.Path)
		}
		out, err := runGit(ctx, deps.Workspace, cmdArgs...)
		return gitTextResult{Output: out}, err
	}
	return functiontool.New(functiontool.Config{
		Name:        "git_diff",
		Description: "Show a git diff (unstaged or staged) as text.",
	}, handler)
}

func newGitLogTool(deps Deps) (tool.Tool, error) {
	handler := func(ctx adkagent.Context, args gitLogArgs) (gitTextResult, error) {
		limit := args.Limit
		if limit <= 0 {
			limit = 10
		}
		if limit > maxGitLogRows {
			limit = maxGitLogRows
		}
		if err := authorize(ctx, deps, "git_log", "git log", map[string]any{"limit": limit, "path": args.Path}); err != nil {
			return gitTextResult{}, err
		}
		cmdArgs := []string{"log", "--oneline", "-n", fmt.Sprint(limit)}
		if args.Path != "" {
			if strings.Contains(args.Path, "..") {
				return gitTextResult{}, fmt.Errorf("path must not contain '..'")
			}
			cmdArgs = append(cmdArgs, "--", args.Path)
		}
		out, err := runGit(ctx, deps.Workspace, cmdArgs...)
		return gitTextResult{Output: out}, err
	}
	return functiontool.New(functiontool.Config{
		Name:        "git_log",
		Description: "Show recent commit history in one-line format.",
	}, handler)
}

func newGitCommitTool(deps Deps) (tool.Tool, error) {
	handler := func(ctx adkagent.Context, args gitCommitArgs) (gitTextResult, error) {
		if strings.TrimSpace(args.Message) == "" {
			return gitTextResult{}, fmt.Errorf("commit message is required")
		}
		if err := authorize(ctx, deps, "git_commit", "Commit: "+args.Message, map[string]any{
			"message": args.Message, "all": args.All,
		}); err != nil {
			return gitTextResult{}, err
		}
		if args.All {
			if _, err := runGit(ctx, deps.Workspace, "add", "-A"); err != nil {
				return gitTextResult{}, err
			}
		}
		out, err := runGit(ctx, deps.Workspace, "commit", "-m", args.Message)
		return gitTextResult{Output: out}, err
	}
	return functiontool.New(functiontool.Config{
		Name:        "git_commit",
		Description: "Create a git commit. Requires approval; optionally stages all changes first.",
	}, handler)
}

func runGit(ctx context.Context, workspace string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", args...)
	cmd.Dir = workspace
	out, err := cmd.CombinedOutput()
	text, truncated := truncateUTF8(string(out), maxGitBytes)
	_ = truncated
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return text, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
		}
		return text, fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return text, nil
}
