//go:build windows

package tools

import (
	"os/exec"
	"strconv"
)

func setProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup uses taskkill's tree mode because Windows has no Unix
// process groups; this also cleans up children spawned by cmd.exe.
func killProcessGroup(pid int) {
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}
