//go:build darwin || linux

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

type commandRunner func(context.Context, string, ...string) ([]byte, int, error)

type capture struct {
	mu       sync.Mutex
	data     []byte
	overflow bool
}

func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	room := definitionLimit - len(c.data)
	if n > room {
		c.overflow = true
		p = p[:room]
	}
	c.data = append(c.data, p...)
	return n, nil
}
func (c *capture) exceeded() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.overflow }

// runCommand keeps the child unreaped until the group has been fenced. Only
// package-owned utility children can be signalled, never launchd's Gateway PID.
func runCommand(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	switch name {
	case "/bin/launchctl", "/bin/ps":
	default:
		return nil, -1, errors.New("unsupported service utility")
	}
	return runOwned(ctx, name, args...)
}
func runOwned(ctx context.Context, name string, args ...string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, -1, err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, -1, err
	}
	command := exec.Command(name, args...) //nolint:gosec // Production caller allowlists absolute system utilities and owns literal argv; tests inject isolated fixtures.
	command.Env = []string{"PATH=" + utilityPath, "LC_ALL=C"}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Stdout = writer
	command.Stderr = writer
	if err = command.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, -1, err
	}
	_ = writer.Close()
	var output capture
	copied := make(chan error, 1)
	go func() { _, e := io.Copy(&output, reader); copied <- e }()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	var cause error
	for {
		exited, e := childExited(command.Process.Pid)
		if e != nil {
			cause = fmt.Errorf("owned utility exit observation failed: %w", e)
			break
		}
		if exited {
			break
		}
		if output.exceeded() {
			cause = errors.New("utility output exceeds bound")
			break
		}
		select {
		case <-ctx.Done():
			cause = ctx.Err()
		case <-tick.C:
		}
		if cause != nil {
			break
		}
	}
	// Setpgid succeeded in Start and Wait has never been called: this PID cannot
	// have been recycled, even if the group leader has already exited.
	if e := syscall.Kill(-command.Process.Pid, syscall.SIGKILL); e != nil && !errors.Is(e, syscall.ESRCH) {
		if e = groupCleanupError(command.Process.Pid, e); e != nil {
			cause = errors.Join(cause, fmt.Errorf("utility group cleanup failed: %w", e))
		}
	}
	reaped := make(chan error, 1)
	go func() { reaped <- command.Wait() }()
	code := -1
	select {
	case e := <-reaped:
		if command.ProcessState != nil {
			code = command.ProcessState.ExitCode()
		}
		var exit *exec.ExitError
		if e != nil && !errors.As(e, &exit) {
			cause = errors.Join(cause, e)
		}
	case <-time.After(5 * time.Second):
		cause = errors.Join(cause, errors.New("utility reap unconfirmed"))
	}
	select {
	case e := <-copied:
		cause = errors.Join(cause, e)
	case <-time.After(time.Second):
		cause = errors.Join(cause, errors.New("utility output cleanup unconfirmed"))
		_ = reader.Close()
		<-copied
	}
	_ = reader.Close()
	output.mu.Lock()
	defer output.mu.Unlock()
	if output.overflow {
		cause = errors.Join(cause, errors.New("utility output exceeds bound"))
	}
	if cause != nil {
		return nil, -1, cause
	}
	return append([]byte(nil), output.data...), code, nil
}
