//go:build windows

package tools

import (
	"context"
	"os/exec"

	"github.com/zhimoe/aglet/internal/proc"
)

func newShellCommand(ctx context.Context, command string) *exec.Cmd {
	return proc.Command(ctx, command)
}
