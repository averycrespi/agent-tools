//go:build e2e

package demo

import (
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestServeDemoLifecycle(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	runner, err := testutil.NewBinaryRunner(4*time.Minute, 1024*1024)
	require.NoError(t, err)
	result, err := runner.Run(t.Context(), "python3", filepath.Join(filepath.Dir(source), "..", "serve-demo.py"))
	require.NoError(t, err, "%s\n%s", result.Stdout, result.Stderr)
	require.Zero(t, result.ExitCode)
	require.False(t, result.StdoutTruncated)
	require.False(t, result.StderrTruncated)
	require.True(t, result.Cleanup.Reaped)
	require.False(t, result.Cleanup.Survived)
}
