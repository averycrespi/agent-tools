package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/stretchr/testify/require"
)

const cliImportDocument = `{"format":"agent-gateway-config","version":1,"agents":[],"mcp":{"servers":[],"grants":[]},"http":{"credentials":[],"grants":[]},"git":{"credentials":[],"repositories":[],"grants":[],"routing_origins":[]}}`

// Retained historical evidence is constructed as bytes, never through a live
// legacy parser/import path. No current command may replay or clean this input.
func historicalImportRoot(t *testing.T, complete bool) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "gateway")
	owner, err := gatewaypaths.AcquireInitialization(root)
	require.NoError(t, err)
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(cliImportDocument)))
	plan := gatewaypaths.InitializationPlan{Version: 1, Root: root, Bearer: owner.Layout().AdminBearer, Installation: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Traffic: "01ARZ3NDEKTSV4RRFFQ69G5FAW", ImportDigest: digest}
	require.NoError(t, gatewaypaths.BeginInitialization(owner, plan))
	require.NoError(t, os.WriteFile(filepath.Join(root, gatewaypaths.InitializationName, "input"), []byte(cliImportDocument), 0600))
	if complete {
		for _, step := range []struct{ name, evidence string }{{"control", plan.Installation}, {"custody-attempt", "attempt"}, {"custody", strings.Repeat("1", 64)}, {"traffic", plan.Traffic}, {"administrator", strings.Repeat("2", 64)}, {"ca", strings.Repeat("3", 64)}, {"certificate", strings.Repeat("3", 64)}, {"import", digest}, {"complete", "complete"}} {
			require.NoError(t, gatewaypaths.SealInitialization(owner, step.name, step.evidence))
		}
	}
	require.NoError(t, owner.Close())
	return root
}
func TestInitFromGrammarIsRetiredBeforeEffects(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	for _, args := range [][]string{{"init", "--from", "missing.json", "--confirm"}, {"initialize", "--from=missing.json", "--resume", "--confirm"}} {
		command := newTestRootCmd(t)
		stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
		command.SetOut(stdout)
		command.SetErr(stderr)
		command.SetArgs(append(args, "--data-dir", root))
		require.Error(t, command.ExecuteContext(t.Context()))
		require.Contains(t, stderr.String(), "flag is not recognized")
		_, err := os.Lstat(root)
		require.ErrorIs(t, err, os.ErrNotExist)
	}
	command := newTestRootCmd(t)
	init, _, err := command.Find([]string{"init"})
	require.NoError(t, err)
	require.Nil(t, init.Flags().Lookup("from"))
	require.NotContains(t, init.Long, "--from")
}
func TestHistoricalInitializationImportsAreNeverReplayedOrCleaned(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			root := historicalImportRoot(t, complete)
			before := treeBytes(t, root)
			for _, args := range [][]string{{"init"}, {"init", "--resume", "--confirm"}} {
				command := newTestRootCmd(t)
				stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
				command.SetOut(stdout)
				command.SetErr(stderr)
				command.SetArgs(append(args, "--data-dir", root))
				err := command.ExecuteContext(t.Context())
				if complete {
					require.NoError(t, err)
					require.Contains(t, stdout.String(), "already initialized")
				} else {
					require.Error(t, err)
					require.Contains(t, stderr.String(), "Historical initialization import")
					require.Contains(t, stderr.String(), "Preserve all artifacts")
				}
				require.Equal(t, before, treeBytes(t, root))
			}
		})
	}
}
