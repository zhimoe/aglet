//go:build !unix

package tools

import "os/exec"

func setProcessGroup(cmd *exec.Cmd) {}

func killProcessGroup(pid int) {}
