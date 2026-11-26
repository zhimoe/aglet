//go:build unix

package tools

import (
	"os/exec"

	"github.com/zhimoe/aglet/internal/proc"
)

func setProcessGroup(cmd *exec.Cmd) { proc.Configure(cmd) }

func killProcessGroup(pid int) { proc.KillGroup(pid) }
