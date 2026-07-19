//go:build darwin || linux

package exec_test

import (
	"context"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	execclient "github.com/averycrespi/agent-tools/worktree-sync/internal/exec"
)

func TestInteractiveChildSharesTerminalProcessGroup(t *testing.T) {
	if path := os.Getenv("WTS_PROCESS_GROUP_HELPER"); path != "" {
		err := os.WriteFile(path, []byte(strconv.Itoa(syscall.Getpgrp())), 0o600)
		require.NoError(t, err)
		return
	}

	path := t.TempDir() + "/process-group"
	t.Setenv("WTS_PROCESS_GROUP_HELPER", path)
	err := (execclient.OSRunner{}).Interactive(context.Background(), "", os.Args[0], "-test.run=^TestInteractiveChildSharesTerminalProcessGroup$")
	require.NoError(t, err)
	value, err := os.ReadFile(path)
	require.NoError(t, err)
	childGroup, err := strconv.Atoi(strings.TrimSpace(string(value)))
	require.NoError(t, err)
	require.Equal(t, syscall.Getpgrp(), childGroup)
}
