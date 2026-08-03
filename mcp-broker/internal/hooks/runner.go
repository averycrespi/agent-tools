package hooks

import (
	"context"
	"os"
	"os/exec"
	"time"
)

const terminateGracePeriod = 250 * time.Millisecond

type commandRunner struct{}

func (commandRunner) Run(ctx context.Context, handler Handler, payload []byte) RunStatus {
	if ctx.Err() != nil {
		return statusForContext(ctx)
	}

	stdin, stdinWriter, err := os.Pipe()
	if err != nil {
		return RunStatusFailed
	}
	cmd := exec.Command(handler.Command, handler.Args...) //nolint:gosec // handlers come only from trusted startup configuration
	cmd.Env = handler.Env
	cmd.Stdin = stdin
	configureProcess(cmd)
	if ctx.Err() != nil {
		_ = stdin.Close()
		_ = stdinWriter.Close()
		return statusForContext(ctx)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdinWriter.Close()
		return RunStatusFailed
	}
	_ = stdin.Close()

	writerDone := make(chan struct{})
	go func() {
		_, _ = stdinWriter.Write(payload)
		_ = stdinWriter.Close()
		close(writerDone)
	}()

	waited := make(chan error, 1)
	go func() {
		waited <- cmd.Wait()
	}()

	select {
	case waitErr := <-waited:
		_ = stdinWriter.Close()
		<-writerDone
		if ctx.Err() != nil {
			stopProcess(cmd.Process, nil, true)
			return statusForContext(ctx)
		}
		if waitErr != nil {
			return RunStatusFailed
		}
		return RunStatusSucceeded
	case <-ctx.Done():
		_ = stdinWriter.Close()
		stopProcess(cmd.Process, waited, false)
		<-writerDone
		return statusForContext(ctx)
	}
}

func stopProcess(process *os.Process, waited <-chan error, directExited bool) {
	terminateProcess(process)
	timer := time.NewTimer(terminateGracePeriod)
	if directExited {
		<-timer.C
		killProcess(process)
		return
	}

	select {
	case <-waited:
		directExited = true
		<-timer.C
	case <-timer.C:
	}
	killProcess(process)
	if !directExited {
		<-waited
	}
}

func statusForContext(ctx context.Context) RunStatus {
	if ctx.Err() == context.DeadlineExceeded {
		return RunStatusTimedOut
	}
	return RunStatusCanceled
}
