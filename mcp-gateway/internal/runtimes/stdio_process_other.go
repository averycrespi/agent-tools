//go:build !darwin && !linux

package runtimes

import (
	"os"
	"os/exec"
)

func stdioProcessGroupsSupported() bool { return false }

func configureStdioProcess(*exec.Cmd) {}

func captureStdioProcessGroup(*os.Process) (int, bool) { return 0, false }

func signalStdioProcessGroup(*os.Process, int, bool) bool { return false }
