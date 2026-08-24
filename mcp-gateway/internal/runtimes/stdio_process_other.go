//go:build !darwin && !linux

package runtimes

import (
	"os"
	"os/exec"
)

func stdioProcessGroupsSupported() bool { return false }

func configureStdioProcess(*exec.Cmd) {}

func killStdioProcessGroup(process *os.Process) {
	if process != nil {
		_ = process.Kill()
	}
}
