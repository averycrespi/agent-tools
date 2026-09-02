//go:build darwin || linux

package testutil

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureTestProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func captureTestProcessGroup(process *os.Process) (int, bool) {
	if process == nil {
		return 0, false
	}
	groupID, err := syscall.Getpgid(process.Pid)
	return groupID, err == nil && groupID == process.Pid
}

func revalidateTestProcessGroup(process *os.Process, expectedGroupID int) bool {
	groupID, ok := captureTestProcessGroup(process)
	return ok && groupID == expectedGroupID
}

func signalTestProcessGroup(groupID int, signal syscall.Signal) error {
	return syscall.Kill(-groupID, signal)
}

func testProcessGroupAlive(groupID int) bool {
	err := syscall.Kill(-groupID, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
