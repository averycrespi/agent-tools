//go:build !darwin && !linux

package runtimes

import (
	"errors"
	"os"
	"os/exec"
)

func stdioProcessGroupsSupported() bool { return false }

func configureStdioProcess(*exec.Cmd) {}

func captureStdioProcessGroup(*os.Process) (int, bool) { return 0, false }

func signalStdioProcessGroup(*os.Process, int, bool) error {
	return errors.New("process group signals unsupported")
}
