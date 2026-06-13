//go:build !windows

package main

import (
	osExec "os/exec"
	"syscall"
)

func detachCommand(cmd *osExec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
