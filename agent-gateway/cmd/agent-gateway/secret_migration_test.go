package main

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSecretMaintenanceDryRunsAreNonmutatingAndCleanupRequiresAttestation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	initializeLegacyBackupFixture(t, root, filepath.Join(root, "admin-bearer"))
	before := treeBytes(t, root)
	for _, operation := range []string{"migrate-secrets", "verify-secrets", "cleanup-native-secrets"} {
		t.Run(operation, func(t *testing.T) {
			command := newTestRootCmd(t)
			out, stderr := new(bytes.Buffer), new(bytes.Buffer)
			command.SetOut(out)
			command.SetErr(stderr)
			args := []string{"maintenance", operation, "--data-dir", root, "--dry-run", "--json"}
			if operation == "cleanup-native-secrets" {
				args = append(args, "--operator-verified")
			}
			command.SetArgs(args)
			require.NoError(t, command.ExecuteContext(t.Context()))
			require.Contains(t, out.String(), `"dry_run":true`)
			require.Equal(t, before, treeBytes(t, root))
		})
	}
	command := newTestRootCmd(t)
	out := new(bytes.Buffer)
	command.SetOut(out)
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"maintenance", "cleanup-native-secrets", "--data-dir", root, "--confirm", "--json"})
	require.Error(t, command.ExecuteContext(t.Context()))
	require.Contains(t, out.String()+command.ErrOrStderr().(*bytes.Buffer).String(), "operator-verified")
	require.Equal(t, before, treeBytes(t, root))
}
