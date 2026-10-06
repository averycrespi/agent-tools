//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCLIEncryptedBackupRefusalIsNotUnknownWrite(t *testing.T) {
	h := newGatewayHarness(t)
	h.Start()
	defer func() {
		if h.process != nil {
			h.Stop(syscall.SIGTERM)
		}
	}()
	bearer := filepath.Join(t.TempDir(), "bearer")
	require.NoError(t, os.WriteFile(bearer, []byte(h.bearer+"\n"), 0o600))
	result := runOnlineCLI(t, h, bearer, false, "backup", "create", "--idempotency-key", "refused", "--output", "json")
	require.Equal(t, 5, result.ExitCode)
	require.Empty(t, result.Stdout)
	require.Contains(t, string(result.Stderr), `"code":"encrypted_backup_unsupported"`)
	require.Contains(t, string(result.Stderr), `"uncertain":false`)
	entries, err := os.ReadDir(filepath.Join(h.root, "backups"))
	require.NoError(t, err)
	require.Empty(t, entries)
}
