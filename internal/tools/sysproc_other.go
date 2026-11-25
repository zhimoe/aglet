//go:build !unix && !windows

package tools

import "os/exec"

func setProcessGroup(cmd *exec.Cmd) {}

func killProcessGroup(pid int) {}
