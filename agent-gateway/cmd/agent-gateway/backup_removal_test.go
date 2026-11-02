package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestInitResumePreservesRetiredBackupArtifacts(t *testing.T) {
	for _, mode := range []string{"incomplete", "completed-import-cleanup"} {
		t.Run(mode, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "gateway")
			if mode == "incomplete" {
				owner, err := gatewaypaths.AcquireInitialization(root)
				require.NoError(t, err)
				plan := gatewaypaths.InitializationPlan{Version: 1, Root: root, Bearer: owner.Layout().AdminBearer, Installation: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Traffic: "01ARZ3NDEKTSV4RRFFQ69G5FAW"}
				require.NoError(t, gatewaypaths.BeginInitialization(owner, plan))
				require.NoError(t, owner.Close())
			} else {
				source := filepath.Join(t.TempDir(), "config.json")
				require.NoError(t, os.WriteFile(source, []byte(cliImportDocument), 0o600))
				command := newTestRootCmd(t)
				command.SetOut(new(bytes.Buffer))
				command.SetErr(new(bytes.Buffer))
				command.SetArgs([]string{"init", "--from", source, "--confirm", "--data-dir", root})
				require.NoError(t, command.ExecuteContext(t.Context()))
				evidence := filepath.Join(root, gatewaypaths.InitializationName)
				require.NoError(t, os.Remove(filepath.Join(evidence, "input-removed")))
				require.NoError(t, os.WriteFile(filepath.Join(evidence, "input"), []byte(cliImportDocument), 0o600))
			}
			retained := testutil.RetiredBackupArtifacts(t, root)
			before := testutil.FingerprintArtifacts(t, retained)
			for _, args := range [][]string{{"init", "--resume", "--confirm"}, {"init"}, {"init", "--resume", "--confirm"}} {
				command := newTestRootCmd(t)
				stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
				command.SetOut(stdout)
				command.SetErr(stderr)
				command.SetArgs(append(args, "--data-dir", root))
				require.NoError(t, command.ExecuteContext(t.Context()), stderr.String())
				require.Equal(t, before, testutil.FingerprintArtifacts(t, retained))
			}
		})
	}
}

func TestRestoreOnlyFlagsAreAbsent(t *testing.T) {
	root := newRootCmd()
	maintenance, _, err := root.Find([]string{"maintenance"})
	require.NoError(t, err)
	for _, command := range maintenance.Commands() {
		require.NotEqual(t, "restore-backup", command.Name())
		require.Nil(t, command.Flags().Lookup("recovery-key"))
		require.Nil(t, command.Flags().Lookup("security-only"))
	}
}
