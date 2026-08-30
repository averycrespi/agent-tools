//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/admin"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAdminAuthorityRealBinaryIsOneTimeAndResetIsAtomic(t *testing.T) {
	ctx := context.Background()
	binary := gatewayBinary(t)
	root := filepath.Join(t.TempDir(), "gateway")
	initialPath := filepath.Join(t.TempDir(), "initial")
	resetPath := filepath.Join(t.TempDir(), "reset")
	runner, err := testutil.NewBinaryRunner(10*time.Second, 4096)
	require.NoError(t, err)

	initialized, err := runner.Run(ctx, binary, "initialize", "--data-dir", root, "--secret-output", initialPath, "--output", "json")
	require.NoError(t, err)
	assert.Equal(t, 0, initialized.ExitCode)
	assert.Empty(t, initialized.Stderr)
	var initialResult map[string]any
	require.NoError(t, json.Unmarshal(initialized.Stdout, &initialResult))
	assert.Equal(t, "1", initialResult["revision"])
	initialBearer := readBearer(t, initialPath)
	assert.NotContains(t, string(initialized.Stdout), initialBearer)

	reset, err := runner.Run(ctx, binary, "admin-reset", "--data-dir", root, "--secret-output", resetPath, "--output", "json")
	require.NoError(t, err)
	assert.Equal(t, 0, reset.ExitCode)
	assert.Empty(t, reset.Stderr)
	resetBearer := readBearer(t, resetPath)
	assert.NotEqual(t, initialBearer, resetBearer)
	assert.NotContains(t, string(reset.Stdout), resetBearer)

	failed, err := runner.Run(ctx, binary, "admin-reset", "--data-dir", root, "--secret-output", resetPath, "--output", "json")
	assert.Error(t, err)
	assert.Equal(t, 2, failed.ExitCode)
	assert.Empty(t, failed.Stdout)
	var problem struct {
		Code      string `json:"code"`
		ExitCode  int    `json:"exit_code"`
		Uncertain bool   `json:"uncertain"`
	}
	require.NoError(t, json.Unmarshal(failed.Stderr, &problem))
	assert.Equal(t, "secret_output_unavailable", problem.Code)
	assert.Equal(t, 2, problem.ExitCode)
	assert.False(t, problem.Uncertain)

	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	defer func() { require.NoError(t, ownership.Close()) }()
	store, err := storage.Open(ctx, ownership)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	service := admin.NewService(store, wallClock{}, rand.Reader)
	_, err = service.Authenticate(ctx, initialBearer)
	assert.ErrorIs(t, err, admin.ErrAuthenticationRequired)
	_, err = service.Authenticate(ctx, resetBearer)
	require.NoError(t, err)

	database, err := os.ReadFile(ownership.Layout().Database)
	require.NoError(t, err)
	assert.False(t, bytes.Contains(database, []byte(initialBearer)))
	assert.False(t, bytes.Contains(database, []byte(resetBearer)))
	assertCanaryAbsent(t, initialBearer, root, initialized, reset, failed)
	assertCanaryAbsent(t, resetBearer, root, initialized, reset, failed)
}

func assertCanaryAbsent(t *testing.T, canary, root string, results ...testutil.ProcessResult) {
	t.Helper()
	scanner, err := testutil.NewCanaryScanner([]byte(canary))
	require.NoError(t, err)
	for index, result := range results {
		require.NoError(t, scanner.Scan("stdout", bytes.NewReader(result.Stdout)), "result %d", index)
		require.NoError(t, scanner.Scan("stderr/log", bytes.NewReader(result.Stderr)), "result %d", index)
	}
	require.NoError(t, scanner.Scan("argv", bytes.NewBufferString("initialize admin-reset --data-dir --secret-output")))
	require.NoError(t, filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || !info.Mode().IsRegular() {
			return walkErr
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		scanErr := scanner.Scan("data file", file)
		return errors.Join(scanErr, file.Close())
	}))
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

func readBearer(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, byte('\n'), contents[len(contents)-1])
	return string(bytes.TrimSuffix(contents, []byte("\n")))
}
