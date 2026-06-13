package main

import (
	"os"
	osExec "os/exec"
)

var defaultStartSupervisor = func(args ...string) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	cmd := osExec.Command(exe, append([]string{"supervisor"}, args...)...) //nolint:gosec
	detachCommand(cmd)
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		return pid, err
	}
	return pid, nil
}

var startSupervisor = defaultStartSupervisor
