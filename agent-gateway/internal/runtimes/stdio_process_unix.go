//go:build darwin || linux

package runtimes

import (
	"fmt"
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

func signalStdioProcessGroup(process *os.Process, expectedGroupID int, force bool) error {
	if process == nil {
		return fmt.Errorf("process identity unavailable")
	}
	groupID, err := syscall.Getpgid(process.Pid)
	if err != nil {
		return fmt.Errorf("inspect process group %d: %w", process.Pid, err)
	}
	if groupID != process.Pid || groupID != expectedGroupID {
		return fmt.Errorf("process group identity mismatch for %d; signal not sent", process.Pid)
	}
	signal := syscall.SIGTERM
	if force {
		signal = syscall.SIGKILL
	}
	return syscall.Kill(-groupID, signal)
}
