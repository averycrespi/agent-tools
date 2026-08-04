//go:build e2e && (darwin || linux)

package e2e_test

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestE2E_SIGTERMTerminatesActiveHookProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "child.pid")
	script := filepath.Join(dir, "descendant-hook.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nsleep 30 &\necho $! > \"$1\"\nwait\n"), 0o700))
	s := newTestStack(t, stackOpts{
		Tools: defaultTools,
		Hooks: testHooks(testHookHandlerConfig{Command: script, Args: []string{pidPath}, TimeoutSeconds: 60}),
	})

	go func() { _, _ = s.callTool("echo.say_hello", map[string]any{}) }()
	_ = s.waitForPending(5 * time.Second)
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(pidPath)
		return err == nil && strings.TrimSpace(string(data)) != ""
	}, 5*time.Second, 20*time.Millisecond)
	pidData, err := os.ReadFile(pidPath)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	require.NoError(t, err)

	require.NoError(t, s.stopBroker(syscall.SIGTERM, 2*time.Second))
	require.Eventually(t, func() bool {
		err := syscall.Kill(pid, 0)
		return errors.Is(err, syscall.ESRCH)
	}, time.Second, 10*time.Millisecond)
}
