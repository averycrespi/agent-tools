package testutil

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
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
	Cleanup         ProcessCleanup
}

type RunningProcess struct {
	command  *exec.Cmd
	cancel   context.CancelFunc
	stdout   *boundedWriter
	stderr   *boundedWriter
	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
	mu       sync.Mutex
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
	return runner.RunInDir(ctx, "", binary, args...)
}

func (runner *BinaryRunner) RunInDir(ctx context.Context, directory, binary string, args ...string) (ProcessResult, error) {
	process, err := runner.start(ctx, directory, binary, args...)
	if err != nil {
		return ProcessResult{ExitCode: -1}, err
	}
	return process.Wait()
}

func (runner *BinaryRunner) Start(ctx context.Context, binary string, args ...string) (*RunningProcess, error) {
	return runner.start(ctx, "", binary, args...)
}

func (runner *BinaryRunner) start(ctx context.Context, directory, binary string, args ...string) (*RunningProcess, error) {
	runContext, cancel := context.WithTimeout(ctx, runner.timeout)
	command := exec.Command(binary, args...) //nolint:gosec // Test harness intentionally executes the caller-selected binary.
	command.Dir = directory
	configureTestProcessGroup(command)
	process := &RunningProcess{
		command: command, cancel: cancel,
		stdout: newBoundedWriter(runner.maxOutputBytes), stderr: newBoundedWriter(runner.maxOutputBytes),
		stop: make(chan struct{}), done: make(chan struct{}),
	}
	command.Stdout, command.Stderr = process.stdout, process.stderr
	if err := command.Start(); err != nil {
		cancel()
		return nil, err
	}
	groupID, owned := captureTestProcessGroup(command.Process)
	if !owned {
		_ = command.Process.Kill()
		_ = command.Wait()
		cancel()
		return nil, fmt.Errorf("capture owned process group: process %d is not its group leader", command.Process.Pid)
	}
	ledger, err := cleanupLedgerFromEnvironment()
	if err != nil {
		_ = signalTestProcessGroup(groupID, syscall.SIGKILL)
		_ = command.Wait()
		cancel()
		return nil, err
	}
	var registration *cleanupRegistration
	if ledger != nil {
		registration, err = ledger.register(command.Process.Pid, groupID)
		if err != nil {
			_ = signalTestProcessGroup(groupID, syscall.SIGKILL)
			_ = command.Wait()
			cancel()
			return nil, err
		}
	}
	exited := make(chan processExit, 1)
	go func() { exited <- processExit{err: command.Wait()} }()
	go func() {
		cleanup, runErr := superviseProcess(runContext, newProcessSupervisor(command, groupID, exited), process.stop)
		process.mu.Lock()
		process.result = ProcessResult{
			ExitCode: exitCode(runErr), Stdout: process.stdout.Bytes(), Stderr: process.stderr.Bytes(),
			StdoutTruncated: process.stdout.Truncated(), StderrTruncated: process.stderr.Truncated(), Cleanup: cleanup,
		}
		process.err = runErr
		process.mu.Unlock()
		registration.settle()
		cancel()
		close(process.done)
	}()
	return process, nil
}

func (process *RunningProcess) StdoutReady() <-chan struct{} { return process.stdout.FirstWrite() }

func (process *RunningProcess) Signal(signal os.Signal) error {
	if process.command.Process == nil {
		return fmt.Errorf("process is not running")
	}
	return process.command.Process.Signal(signal)
}

func (process *RunningProcess) Stop() error {
	select {
	case <-process.done:
		return nil
	default:
		process.stopOnce.Do(func() { close(process.stop) })
	}
	<-process.done
	process.mu.Lock()
	defer process.mu.Unlock()
	if process.result.Cleanup.Survived {
		return fmt.Errorf("process cleanup left survivors")
	}
	return nil
}

func (process *RunningProcess) Wait() (ProcessResult, error) {
	<-process.done
	process.mu.Lock()
	defer process.mu.Unlock()
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
