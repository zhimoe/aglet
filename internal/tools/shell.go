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
	Command    string `json:"command" jsonschema:"Shell command to run."`
	Cwd        string `json:"cwd,omitempty" jsonschema:"Working directory relative to the workspace. Default workspace root."`
	TimeoutSec int    `json:"timeout_sec,omitempty" jsonschema:"Timeout in seconds. Default 120, max 600."`
}

type runCommandResult struct {
	Command   string `json:"command"`
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout"`
	TimedOut  bool   `json:"timed_out"`
	Truncated bool   `json:"truncated"`
}

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

func newRunCommandTool(deps Deps) (tool.Tool, error) {
	handler := func(ctx adkagent.Context, args runCommandArgs) (runCommandResult, error) {
		if strings.TrimSpace(args.Command) == "" {
			return runCommandResult{}, fmt.Errorf("command is required")
		}
		cwd := args.Cwd
		if cwd == "" {
			cwd = "."
		}
		if err := authorize(ctx, deps, "run_command", "Run: "+args.Command, map[string]any{
			"command": args.Command, "cwd": cwd, "timeout_sec": args.TimeoutSec,
		}); err != nil {
			return runCommandResult{}, err
		}
		dir, err := resolvePath(deps.Workspace, cwd)
		if err != nil {
			return runCommandResult{}, err
		}
		timeout := time.Duration(args.TimeoutSec) * time.Second
		if args.TimeoutSec <= 0 {
			timeout = defaultCommandTimeout
		}
		if timeout > maxCommandTimeout {
			timeout = maxCommandTimeout
		}
		runCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		cmd := newShellCommand(runCtx, args.Command)
		cmd.Dir = dir
		cmd.Env = os.Environ()
		out := &limitedBuffer{limit: maxCommandOutput}
		cmd.Stdout = out
		cmd.Stderr = out
		setProcessGroup(cmd)

		if err := cmd.Start(); err != nil {
			return runCommandResult{}, err
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()

		select {
		case <-runCtx.Done():
			killProcessGroup(cmd.Process.Pid)
			<-done
			res := runCommandResult{
				Command: args.Command, Stdout: out.String(),
				TimedOut: true, Truncated: out.truncated, ExitCode: -1,
			}
			if ctx.Err() != nil {
				return res, ctx.Err()
			}
			return res, nil
		case err := <-done:
			res := runCommandResult{Command: args.Command, Stdout: out.String(), Truncated: out.truncated}
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
	return functiontool.New(functiontool.Config{
		Name:        "run_command",
		Description: "Run a shell command in the workspace. Requires approval. Returns combined stdout/stderr and exit code.",
	}, handler)
}
