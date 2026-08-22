package testutil

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

type BinaryRunner struct {
	timeout        time.Duration
	maxOutputBytes int
}

type ProcessResult struct {
	ExitCode        int
	Stdout          []byte
	Stderr          []byte
	StdoutTruncated bool
	StderrTruncated bool
}

func NewBinaryRunner(timeout time.Duration, maxOutputBytes int) (*BinaryRunner, error) {
	if timeout <= 0 {
		return nil, fmt.Errorf("runner timeout must be positive")
	}
	if maxOutputBytes <= 0 {
		return nil, fmt.Errorf("runner output bound must be positive")
	}
	return &BinaryRunner{timeout: timeout, maxOutputBytes: maxOutputBytes}, nil
}

func (runner *BinaryRunner) Run(ctx context.Context, binary string, args ...string) (ProcessResult, error) {
	runContext, cancel := context.WithTimeout(ctx, runner.timeout)
	defer cancel()

	command := exec.CommandContext(runContext, binary, args...) //nolint:gosec // Test harness intentionally executes the caller-selected binary.
	stdout := newBoundedWriter(runner.maxOutputBytes)
	stderr := newBoundedWriter(runner.maxOutputBytes)
	command.Stdout = stdout
	command.Stderr = stderr

	runErr := command.Run()
	result := ProcessResult{
		ExitCode:        exitCode(runErr),
		Stdout:          stdout.Bytes(),
		Stderr:          stderr.Bytes(),
		StdoutTruncated: stdout.Truncated(),
		StderrTruncated: stderr.Truncated(),
	}
	if runContext.Err() != nil {
		return result, runContext.Err()
	}
	return result, runErr
}

func exitCode(runErr error) int {
	if runErr == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(runErr, &exitError) {
		return exitError.ExitCode()
	}
	return -1
}

type boundedWriter struct {
	buffer    bytes.Buffer
	maximum   int
	truncated bool
}

func newBoundedWriter(maximum int) *boundedWriter {
	return &boundedWriter{maximum: maximum}
}

func (writer *boundedWriter) Write(value []byte) (int, error) {
	originalLength := len(value)
	remaining := writer.maximum - writer.buffer.Len()
	if remaining <= 0 {
		writer.truncated = writer.truncated || originalLength > 0
		return originalLength, nil
	}
	if len(value) > remaining {
		value = value[:remaining]
		writer.truncated = true
	}
	_, _ = writer.buffer.Write(value)
	return originalLength, nil
}

func (writer *boundedWriter) Bytes() []byte {
	return append([]byte(nil), writer.buffer.Bytes()...)
}

func (writer *boundedWriter) Truncated() bool {
	return writer.truncated
}
