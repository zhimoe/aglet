//go:build !unix && !windows

// Package proc provides platform-aware shell processes and process-group
// control shared by tools, hooks, and background jobs.
package proc

import (
	"context"
	"os/exec"
)

// Command returns a shell command for the platform.
func Command(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", command)
}

// Configure is a no-op on unsupported platforms.
func Configure(*exec.Cmd) {}

// KillGroup is a no-op on unsupported platforms.
func KillGroup(int) {}
