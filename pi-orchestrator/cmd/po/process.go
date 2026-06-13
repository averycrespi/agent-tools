package main

import (
	"fmt"
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

var defaultKillSupervisor = func(pid int) error {
	if pid <= 0 {
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find supervisor process: %w", err)
	}
	if err := proc.Kill(); err != nil {
		return fmt.Errorf("kill supervisor process %d: %w", pid, err)
	}
	return nil
}

var killSupervisor = defaultKillSupervisor
