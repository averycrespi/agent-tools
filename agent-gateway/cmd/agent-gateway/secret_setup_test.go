package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/stretchr/testify/require"
)

func TestStoppedSecretSetupPreservesExistingInstallation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	bearer := filepath.Join(root, "admin-bearer")
	initializeLegacyBackupFixture(t, root, bearer)
	before := treeBytes(t, root)
	run := func(flags ...string) error {
		command := newTestRootCmd(t)
		command.SetOut(new(bytes.Buffer))
		command.SetErr(new(bytes.Buffer))
		command.SetArgs(append([]string{"maintenance", "setup-secret-storage", "--data-dir", root, "--json"}, flags...))
		return command.ExecuteContext(t.Context())
	}
	require.NoError(t, run("--dry-run"))
	require.Equal(t, before, treeBytes(t, root))
	require.NoError(t, run("--confirm"))
	key, err := os.ReadFile(filepath.Join(root, gatewaypaths.MasterKeyName))
	require.NoError(t, err)
	require.Len(t, key, 32)
	info, err := os.Stat(filepath.Join(root, gatewaypaths.MasterKeyName))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	credential, err := os.ReadFile(bearer)
	require.NoError(t, err)
	require.Equal(t, before[bearer], string(credential))
	after := treeBytes(t, root)
	require.NoError(t, run("--confirm"))
	require.Equal(t, after, treeBytes(t, root))
	still, err := os.ReadFile(bearer)
	require.NoError(t, err)
	require.Equal(t, credential, still)
	require.NoError(t, os.Remove(filepath.Join(root, gatewaypaths.MasterKeyName)))
	require.Error(t, run("--confirm"))
	_, err = os.Lstat(filepath.Join(root, gatewaypaths.MasterKeyName))
	require.ErrorIs(t, err, os.ErrNotExist)
}
