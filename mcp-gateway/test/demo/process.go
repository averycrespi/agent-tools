//go:build e2e

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

const outputLimit = 1024 * 1024

type boundedOutput struct {
	mu       sync.Mutex
	data     []byte
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	room := outputLimit - len(b.data)
	if n > room {
		b.overflow = true
		p = p[:room]
	}
	b.data = append(b.data, p...)
	return n, nil
}
func (b *boundedOutput) bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.data...)
}
func (b *boundedOutput) exceeded() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.overflow }

type child struct {
	label          string
	cmd            *exec.Cmd
	stdout, stderr boundedOutput
	exited         <-chan error
	settled        bool
	cleanupErr     error
	code           int
}

func startChild(label string, argv, env []string) (*child, error) {
	c := &child{label: label, cmd: exec.Command(argv[0], argv[1:]...)} //nolint:gosec // Only fixed developer commands and owned binary paths reach this supervisor.
	c.cmd.Env = env
	c.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.cmd.Stdout = &c.stdout
	c.cmd.Stderr = &c.stderr
	c.cmd.WaitDelay = 2 * time.Second
	if err := c.cmd.Start(); err != nil {
		return nil, errors.New(label + " could not start")
	}
	c.exited = observeExit(c.cmd.Process.Pid)
	return c, nil
}
func (c *child) poll() (bool, error) {
	select {
	case err := <-c.exited:
		c.exited = closedExit(err)
		return true, err
	default:
		return false, nil
	}
}
func closedExit(err error) <-chan error { ch := make(chan error, 1); ch <- err; return ch }
func (c *child) check(ctx context.Context) error {
	if ctx.Err() != nil {
		return errors.New("interrupted")
	}
	if c.stdout.exceeded() || c.stderr.exceeded() {
		return errors.New(c.label + " exceeded output bound")
	}
	if exited, err := c.poll(); exited || err != nil {
		return errors.New(c.label + " exited unexpectedly")
	}
	return nil
}
func (c *child) signal(sig syscall.Signal) error {
	pid := c.cmd.Process.Pid
	pgid, err := syscall.Getpgid(pid)
	if err != nil || pgid != pid {
		return errors.New(c.label + " process identity changed")
	}
	if err = syscall.Kill(-pid, sig); err != nil {
		return errors.New(c.label + " group signal failed")
	}
	return nil
}
func (c *child) wait(ctx context.Context, timeout time.Duration, stopOnOverflow bool) (bool, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if yes, err := c.poll(); yes {
			return true, err
		}
		select {
		case <-ctx.Done():
			return false, nil
		case <-timer.C:
			return false, nil
		case <-tick.C:
			if stopOnOverflow && (c.stdout.exceeded() || c.stderr.exceeded()) {
				return false, nil
			}
		}
	}
}
func (c *child) finish(ctx context.Context, timeout time.Duration, success bool) error {
	if c.settled {
		return c.cleanupErr
	}
	exited, err := c.wait(ctx, timeout, true)
	if err != nil {
		return errors.New(c.label + " exit observation failed")
	}
	if !exited {
		if err = c.signal(syscall.SIGTERM); err != nil {
			return err
		}
		if _, err = c.wait(context.Background(), 5*time.Second, false); err != nil {
			return errors.New(c.label + " exit observation failed")
		}
	}
	// Do not reap the leader until its process group is fenced: its reserved PID
	// prevents another process from acquiring the identity we are signalling.
	if err = c.signal(syscall.SIGKILL); err != nil {
		return err
	}
	if yes, waitErr := c.wait(context.Background(), 5*time.Second, false); !yes || waitErr != nil {
		return errors.New(c.label + " could not be reaped")
	}
	waitErr := c.cmd.Wait()
	c.settled = true
	c.code = c.cmd.ProcessState.ExitCode()
	if errors.Is(waitErr, exec.ErrWaitDelay) {
		c.cleanupErr = errors.New(c.label + " output pipes survived")
	}
	if err = syscall.Kill(-c.cmd.Process.Pid, 0); !errors.Is(err, syscall.ESRCH) {
		c.cleanupErr = errors.New(c.label + " process group survived cleanup")
	}
	if c.cleanupErr != nil {
		return c.cleanupErr
	}
	if c.stdout.exceeded() || c.stderr.exceeded() {
		return errors.New(c.label + " exceeded output bound")
	}
	if success && (!exited || waitErr != nil) {
		return errors.New(c.label + " failed or timed out (child output suppressed)")
	}
	return nil
}

func writePrivate(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600) //nolint:gosec // The supervisor selects an owned-root sink; exclusive creation never overwrites a file.
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	return errors.Join(err, f.Close())
}

var _ io.Writer = (*boundedOutput)(nil)
