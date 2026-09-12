//go:build !darwin && !linux

package testutil

import (
	"os"
	"os/exec"
	"syscall"
)

func configureTestProcessGroup(*exec.Cmd) {}

func captureTestProcessGroup(process *os.Process) (int, bool) {
	if process == nil {
		return 0, false
	}
	return process.Pid, true
}

func revalidateTestProcessGroup(process *os.Process, expectedGroupID int) bool {
	return process != nil && process.Pid == expectedGroupID
}

func signalTestProcessGroup(_ int, _ syscall.Signal) error {
	return syscall.ENOTSUP
}

func testProcessGroupAlive(int) bool { return false }
