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

func captureStdioProcessGroup(process *os.Process) (int, bool) {
	if process == nil {
		return 0, false
	}
	groupID, err := syscall.Getpgid(process.Pid)
	return groupID, err == nil && groupID == process.Pid
}

func signalStdioProcessGroup(process *os.Process, expectedGroupID int, force bool) bool {
	groupID, verified := captureStdioProcessGroup(process)
	if !verified || groupID != expectedGroupID {
		return false
	}
	signal := syscall.SIGTERM
	if force {
		signal = syscall.SIGKILL
	}
	return syscall.Kill(-groupID, signal) == nil
}
