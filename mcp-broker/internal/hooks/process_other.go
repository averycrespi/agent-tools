//go:build !darwin && !linux

package hooks

import (
	"os"
	"os/exec"
)

func configureProcess(_ *exec.Cmd) {}

func terminateProcess(process *os.Process) {
	_ = process.Kill()
}

func killProcess(process *os.Process) {
	_ = process.Kill()
}
