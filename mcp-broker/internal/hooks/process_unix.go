//go:build darwin || linux

package hooks

import (
	"os"
	"os/exec"
	"syscall"
)

func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateProcess(process *os.Process) {
	_ = syscall.Kill(-process.Pid, syscall.SIGTERM)
	_ = process.Signal(syscall.SIGTERM)
}

func killProcess(process *os.Process) {
	_ = syscall.Kill(-process.Pid, syscall.SIGKILL)
	_ = process.Kill()
}
