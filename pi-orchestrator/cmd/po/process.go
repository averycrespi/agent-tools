package main

import (
	"fmt"
	"os"
	osExec "os/exec"
)

var defaultStartSupervisor = func(logPath string, args ...string) (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, err
	}
	cmd := osExec.Command(exe, append([]string{"supervisor"}, args...)...) //nolint:gosec
	logFile, err := configureSupervisorLog(cmd, logPath)
	if err != nil {
		return 0, err
	}
	if logFile != nil {
		defer logFile.Close() //nolint:errcheck
	}
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

func configureSupervisorLog(cmd *osExec.Cmd, logPath string) (*os.File, error) {
	if logPath == "" {
		return nil, nil
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // Path is po-owned supervisor log metadata created for the workflow run.
	if err != nil {
		return nil, fmt.Errorf("open supervisor log: %w", err)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	return logFile, nil
}

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
