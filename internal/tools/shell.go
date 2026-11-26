package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

const (
	defaultCommandTimeout = 120 * time.Second
	maxCommandTimeout     = 600 * time.Second
	maxCommandOutput      = 1024 * 1024
)

type runCommandArgs struct {
	Command         string `json:"command" jsonschema:"Shell command to run."`
	Cwd             string `json:"cwd,omitempty" jsonschema:"Working directory relative to the workspace. Default workspace root."`
	TimeoutSec      int    `json:"timeout_sec,omitempty" jsonschema:"Timeout in seconds. Default 120, max 600 for foreground; for background 0 means no timeout."`
	RunInBackground bool   `json:"run_in_background,omitempty" jsonschema:"Start the command as a background job and return a job id. Poll with bash_output and stop with kill_shell."`
	Description     string `json:"description,omitempty" jsonschema:"Short description of what the command does."`
}

// ShellRequest is the frontend-facing shell escape request.
type ShellRequest struct {
	Command    string
	Cwd        string
	TimeoutSec int
}

type ShellResult struct {
	Command   string `json:"command"`
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout"`
	TimedOut  bool   `json:"timed_out"`
	Truncated bool   `json:"truncated"`
	JobID     string `json:"job_id,omitempty"`
	Status    string `json:"status,omitempty"`
}

// runCommandResult is kept as the tool schema name.
type runCommandResult = ShellResult

type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.truncated {
		return len(p), nil
	}
	remain := b.limit - b.buf.Len()
	if remain <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remain {
		_, _ = b.buf.Write(p[:remain])
		b.truncated = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) String() string { return b.buf.String() }

// RunShell executes a command after policy approval. TUI shell escape and the
// run_command tool share this implementation.
func RunShell(ctx context.Context, deps Deps, req ShellRequest) (ShellResult, error) {
	if strings.TrimSpace(req.Command) == "" {
		return ShellResult{}, fmt.Errorf("command is required")
	}
	cwd := req.Cwd
	if cwd == "" {
		cwd = "."
	}
	if err := authorize(ctx, deps, "run_command", "Run: "+req.Command, map[string]any{
		"command": req.Command, "cwd": cwd, "timeout_sec": req.TimeoutSec,
	}); err != nil {
		return ShellResult{}, err
	}
	dir, err := resolvePath(deps.Workspace, cwd)
	if err != nil {
		return ShellResult{}, err
	}
	timeout := time.Duration(req.TimeoutSec) * time.Second
	if req.TimeoutSec <= 0 {
		timeout = defaultCommandTimeout
	}
	if timeout > maxCommandTimeout {
		timeout = maxCommandTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := newShellCommand(runCtx, req.Command)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	out := &limitedBuffer{limit: maxCommandOutput}
	cmd.Stdout = out
	cmd.Stderr = out
	setProcessGroup(cmd)

	if err := cmd.Start(); err != nil {
		return ShellResult{}, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-runCtx.Done():
		killProcessGroup(cmd.Process.Pid)
		<-done
		res := ShellResult{
			Command: req.Command, Stdout: out.String(), Status: JobTimedOut,
			TimedOut: true, Truncated: out.truncated, ExitCode: -1,
		}
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		return res, nil
	case err := <-done:
		res := ShellResult{Command: req.Command, Stdout: out.String(), Status: JobExited, Truncated: out.truncated}
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				res.ExitCode = ee.ExitCode()
			} else {
				return res, err
			}
		}
		return res, nil
	}
}

func newRunCommandTool(deps Deps) (tool.Tool, error) {
	handler := func(ctx adkagent.Context, args runCommandArgs) (runCommandResult, error) {
		if args.RunInBackground {
			return runBackgroundCommand(ctx, deps, args)
		}
		return RunShell(ctx, deps, ShellRequest{
			Command: args.Command, Cwd: args.Cwd, TimeoutSec: args.TimeoutSec,
		})
	}
	return functiontool.New(functiontool.Config{
		Name: "run_command",
		Description: "Run a shell command in the workspace. Requires approval. Returns combined stdout/stderr and exit code. " +
			"Set run_in_background=true for long-running servers or tests; poll with bash_output and stop with kill_shell.",
	}, handler)
}

const maxBackgroundTimeout = 24 * time.Hour

func runBackgroundCommand(ctx adkagent.Context, deps Deps, args runCommandArgs) (runCommandResult, error) {
	if deps.Jobs == nil {
		return runCommandResult{}, fmt.Errorf("background jobs are not available")
	}
	cwd := args.Cwd
	if cwd == "" {
		cwd = "."
	}
	summary := "Run in background: " + args.Command
	if args.Description != "" {
		summary = "Run in background (" + args.Description + "): " + args.Command
	}
	if err := authorize(ctx, deps, "run_command", summary, map[string]any{
		"command": args.Command, "cwd": cwd, "timeout_sec": args.TimeoutSec, "run_in_background": true,
	}); err != nil {
		return runCommandResult{}, err
	}
	dir, err := resolvePath(deps.Workspace, cwd)
	if err != nil {
		return runCommandResult{}, err
	}
	timeout := time.Duration(args.TimeoutSec) * time.Second
	if timeout < 0 {
		timeout = 0
	}
	if timeout > maxBackgroundTimeout {
		timeout = maxBackgroundTimeout
	}
	snap, err := deps.Jobs.Start(dir, args.Command, timeout)
	if err != nil {
		return runCommandResult{}, err
	}
	return runCommandResult{
		Command: args.Command, JobID: snap.ID, Status: snap.Status, ExitCode: 0,
	}, nil
}
