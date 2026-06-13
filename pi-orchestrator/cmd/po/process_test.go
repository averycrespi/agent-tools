package main

import (
	"os"
	osExec "os/exec"
	"path/filepath"
	"testing"
)

func TestConfigureSupervisorLogRedirectsStdoutAndStderr(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "supervisor.log")
	cmd := osExec.Command("test") //nolint:gosec

	logFile, err := configureSupervisorLog(cmd, logPath)
	if err != nil {
		t.Fatalf("configureSupervisorLog() error = %v", err)
	}
	defer logFile.Close() //nolint:errcheck
	if cmd.Stdout != logFile || cmd.Stderr != logFile {
		t.Fatalf("stdout/stderr not redirected to supervisor log")
	}
	if _, err := logFile.WriteString("hello\n"); err != nil {
		t.Fatalf("write log: %v", err)
	}
	data, err := os.ReadFile(logPath) //nolint:gosec
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if string(data) != "hello\n" {
		t.Fatalf("log = %q, want hello", data)
	}
}
