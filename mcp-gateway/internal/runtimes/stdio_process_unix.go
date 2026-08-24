//go:build darwin || linux

package runtimes

import (
	"os"
	"os/exec"
	"syscall"
)

func stdioProcessGroupsSupported() bool { return true }

func configureStdioProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killStdioProcessGroup(process *os.Process) {
	if process == nil {
		return
	}
	_ = syscall.Kill(-process.Pid, syscall.SIGKILL)
	_ = process.Kill()
}
