//go:build unix

// Package proc provides platform-aware shell processes and process-group
// control shared by tools, hooks, and background jobs.
package proc

import (
	"context"
	"os/exec"
	"syscall"
)

// Command returns a shell command for the platform.
func Command(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", command)
}

// Configure puts the command in its own process group.
func Configure(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// KillGroup kills a process group started with Configure.
func KillGroup(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
