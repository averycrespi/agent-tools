package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/stretchr/testify/require"
)

func TestMasterKeyCLIRequiresExplicitRetentionAndConfirmation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	initialize := newTestRootCmd(t)
	initialize.SetOut(new(bytes.Buffer))
	initialize.SetErr(new(bytes.Buffer))
	initialize.SetArgs([]string{"init", "--data-dir", root, "--confirm"})
	require.NoError(t, initialize.ExecuteContext(t.Context()))
	before := treeBytes(t, root)
	for _, flags := range [][]string{{"--confirm"}, {"--retain-recovery-keys"}, {"--retain-recovery-keys", "--dry-run"}} {
		command := newTestRootCmd(t)
		out, diagnostic := new(bytes.Buffer), new(bytes.Buffer)
		command.SetOut(out)
		command.SetErr(diagnostic)
		command.SetIn(new(bytes.Buffer))
		args := append([]string{"maintenance", "rotate-master-key", "--data-dir", root, "--json"}, flags...)
		command.SetArgs(args)
		err := command.ExecuteContext(t.Context())
		if flags[len(flags)-1] == "--dry-run" {
			require.NoError(t, err)
			require.Contains(t, out.String(), `"dry_run":true`)
		} else {
			require.Error(t, err)
		}
		require.Equal(t, before, treeBytes(t, root))
	}
	owner, err := gatewaypaths.AcquireStoppedExisting(root)
	require.NoError(t, err)
	command := newTestRootCmd(t)
	command.SetOut(new(bytes.Buffer))
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"maintenance", "rotate-master-key", "--data-dir", root, "--retain-recovery-keys", "--confirm"})
	require.Error(t, command.ExecuteContext(t.Context()))
	require.NoError(t, owner.Close())
	old, err := os.ReadFile(filepath.Join(root, gatewaypaths.MasterKeyName))
	require.NoError(t, err)
	defer clear(old)
	command = newTestRootCmd(t)
	out, diagnostic := new(bytes.Buffer), new(bytes.Buffer)
	command.SetOut(out)
	command.SetErr(diagnostic)
	command.SetArgs([]string{"maintenance", "rotate-master-key", "--data-dir", root, "--retain-recovery-keys", "--confirm", "--json"})
	require.NoError(t, command.ExecuteContext(t.Context()), diagnostic.String())
	require.Contains(t, out.String(), `"disposition":"rotated"`)
	require.NotContains(t, out.String(), string(old))
	current, err := os.ReadFile(filepath.Join(root, gatewaypaths.MasterKeyName))
	require.NoError(t, err)
	defer clear(current)
	require.NotEqual(t, old, current)
	require.NotContains(t, out.String(), string(current))
}
