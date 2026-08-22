package testutil

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
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

type RunningProcess struct {
	command  *exec.Cmd
	ctx      context.Context
	cancel   context.CancelFunc
	stdout   *boundedWriter
	stderr   *boundedWriter
	waitOnce sync.Once
	result   ProcessResult
	err      error
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
	process, err := runner.Start(ctx, binary, args...)
	if err != nil {
		return ProcessResult{ExitCode: -1}, err
	}
	return process.Wait()
}

func (runner *BinaryRunner) Start(ctx context.Context, binary string, args ...string) (*RunningProcess, error) {
	runContext, cancel := context.WithTimeout(ctx, runner.timeout)
	command := exec.CommandContext(runContext, binary, args...) //nolint:gosec // Test harness intentionally executes the caller-selected binary.
	process := &RunningProcess{
		command: command, ctx: runContext, cancel: cancel,
		stdout: newBoundedWriter(runner.maxOutputBytes), stderr: newBoundedWriter(runner.maxOutputBytes),
	}
	command.Stdout, command.Stderr = process.stdout, process.stderr
	if err := command.Start(); err != nil {
		cancel()
		return nil, err
	}
	return process, nil
}

func (process *RunningProcess) StdoutReady() <-chan struct{} { return process.stdout.FirstWrite() }

func (process *RunningProcess) Signal(signal os.Signal) error {
	if process.command.Process == nil {
		return fmt.Errorf("process is not running")
	}
	return process.command.Process.Signal(signal)
}

func (process *RunningProcess) Wait() (ProcessResult, error) {
	process.waitOnce.Do(func() {
		runErr := process.command.Wait()
		process.result = ProcessResult{
			ExitCode: exitCode(runErr), Stdout: process.stdout.Bytes(), Stderr: process.stderr.Bytes(),
			StdoutTruncated: process.stdout.Truncated(), StderrTruncated: process.stderr.Truncated(),
		}
		if process.ctx.Err() != nil {
			process.err = process.ctx.Err()
		} else {
			process.err = runErr
		}
		process.cancel()
	})
	return process.result, process.err
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
	buffer     bytes.Buffer
	maximum    int
	truncated  bool
	firstWrite chan struct{}
	readyOnce  sync.Once
}

func newBoundedWriter(maximum int) *boundedWriter {
	return &boundedWriter{maximum: maximum, firstWrite: make(chan struct{})}
}

func (writer *boundedWriter) Write(value []byte) (int, error) {
	originalLength := len(value)
	if originalLength > 0 {
		writer.readyOnce.Do(func() { close(writer.firstWrite) })
	}
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

func (writer *boundedWriter) FirstWrite() <-chan struct{} { return writer.firstWrite }
