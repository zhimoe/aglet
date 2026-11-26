//go:build windows

// Package proc provides platform-aware shell processes and process-group
// control shared by tools, hooks, and background jobs.
package proc

import (
	"context"
	"os/exec"
	"strconv"
)

// Command returns a shell command for the platform.
func Command(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "cmd.exe", "/C", command)
}

// Configure is a no-op on Windows, which has no Unix process groups.
func Configure(*exec.Cmd) {}

// KillGroup uses taskkill's tree mode to stop cmd.exe and its children.
func KillGroup(pid int) {
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}
