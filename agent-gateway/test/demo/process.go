//go:build e2e

package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
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

type commandResult struct {
	code int
	err  error
}

type child struct {
	label          string
	cmd            *exec.Cmd
	stdout, stderr boundedOutput
	exited         <-chan commandResult
	status         *os.File
	control        *os.File
	settled        bool
	cleanupErr     error
	code           int
}

func startChild(label string, argv, env []string) (*child, error) {
	// The shell reaps the command but remains a live group owner until teardown.
	// Arguments stay positional, and the command cannot inherit control pipes.
	const owner = `"$@" 3>&- 4>&- &
child=$!
trap '' TERM INT
wait "$child"
code=$?
printf '%s\n' "$code" >&3
IFS= read -r release <&4
kill -KILL -$$
`
	status, statusWriter, err := os.Pipe()
	if err != nil {
		return nil, errors.New(label + " status pipe failed")
	}
	defer func() { _ = statusWriter.Close() }()
	controlReader, control, err := os.Pipe()
	if err != nil {
		_ = status.Close()
		return nil, errors.New(label + " control pipe failed")
	}
	defer func() { _ = controlReader.Close() }()
	args := append([]string{"-c", owner, "demo-owner"}, argv...)
	c := &child{label: label, cmd: exec.Command("/bin/sh", args...), status: status, control: control, code: -1} //nolint:gosec // Fixed shell program; command arguments are positional, never interpolated.
	c.cmd.ExtraFiles = []*os.File{statusWriter, controlReader}
	c.cmd.Env = env
	c.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.cmd.Stdout = &c.stdout
	c.cmd.Stderr = &c.stderr
	c.cmd.WaitDelay = 2 * time.Second
	if err := c.cmd.Start(); err != nil {
		_ = status.Close()
		_ = control.Close()
		return nil, errors.New(label + " could not start")
	}
	exited := make(chan commandResult, 1)
	c.exited = exited
	go func() {
		scanner := bufio.NewScanner(status)
		scanner.Buffer(make([]byte, 16), 16)
		if !scanner.Scan() {
			exited <- commandResult{-1, errors.New("command status unavailable")}
			return
		}
		code, err := strconv.Atoi(scanner.Text())
		if err != nil || code < 0 || code > 255 {
			exited <- commandResult{-1, errors.New("invalid command status")}
			return
		}
		if code >= 128 {
			code = -1
		}
		exited <- commandResult{code: code}
	}()
	return c, nil
}
func (c *child) poll() (bool, error) {
	select {
	case result := <-c.exited:
		c.code = result.code
		c.exited = closedExit(result)
		return true, result.err
	default:
		return false, nil
	}
}
func closedExit(result commandResult) <-chan commandResult {
	ch := make(chan commandResult, 1)
	ch <- result
	return ch
}
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
	// Start applied Setpgid, and no Wait occurs until the group is fenced.
	// The direct child's reserved PID therefore still names our group.
	if c.settled {
		return errors.New(c.label + " process identity changed")
	}
	if err := syscall.Kill(-pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
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
	statusErr := err
	if !exited {
		if err = c.signal(syscall.SIGTERM); err != nil {
			return err
		}
		_, statusErr = c.wait(context.Background(), 5*time.Second, false)
	}
	// Do not reap the leader until its process group is fenced: its reserved PID
	// prevents another process from acquiring the identity we are signalling.
	if err = c.signal(syscall.SIGKILL); err != nil {
		return err
	}
	_ = c.control.Close()
	c.settled = true
	reaped := make(chan error, 1)
	go func() { reaped <- c.cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-reaped:
	case <-time.After(5 * time.Second):
		_ = c.status.Close()
		c.cleanupErr = errors.New(c.label + " could not be reaped")
		return c.cleanupErr
	}
	_ = c.status.Close()
	_, _ = c.wait(context.Background(), time.Second, false)
	if errors.Is(waitErr, exec.ErrWaitDelay) {
		c.cleanupErr = errors.New(c.label + " output pipes survived")
	}
	// Orphaned descendants can remain zombies until their new parent reaps them.
	deadline := time.Now().Add(2 * time.Second)
	for {
		err = syscall.Kill(-c.cmd.Process.Pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if (err != nil && !errors.Is(err, syscall.EPERM)) || !time.Now().Before(deadline) {
			c.cleanupErr = errors.New(c.label + " process group survived cleanup")
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if c.cleanupErr != nil {
		return c.cleanupErr
	}
	if c.stdout.exceeded() || c.stderr.exceeded() {
		return errors.New(c.label + " exceeded output bound")
	}
	if success && (!exited || statusErr != nil || c.code != 0) {
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
