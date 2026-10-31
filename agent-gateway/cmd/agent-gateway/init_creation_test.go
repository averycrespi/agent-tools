package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/stretchr/testify/require"
)

func TestInitCompleteNeedsNeitherBearerNorOptionalHistory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	command := newTestRootCmd(t)
	command.SetOut(new(bytes.Buffer))
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"init", "--confirm", "--data-dir", root})
	require.NoError(t, command.ExecuteContext(t.Context()))
	require.NoError(t, os.Remove(filepath.Join(root, gatewaypaths.AdminBearerName)))
	traffic, err := filepath.Glob(filepath.Join(root, "traffic-*.db"))
	require.NoError(t, err)
	require.Len(t, traffic, 1)
	require.NoError(t, os.WriteFile(traffic[0], []byte("damaged optional history"), 0600))
	for _, running := range []bool{false, true} {
		var owner *gatewaypaths.Ownership
		if running {
			owner, err = gatewaypaths.AcquireExisting(root)
			require.NoError(t, err)
		}
		before := treeBytes(t, root)
		for _, flags := range [][]string{{}, {"--resume"}, {"--secret-output", filepath.Join(t.TempDir(), "absent")}} {
			command = newTestRootCmd(t)
			stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
			command.SetOut(stdout)
			command.SetErr(stderr)
			args := append([]string{"init", "--data-dir", root}, flags...)
			command.SetArgs(args)
			require.NoError(t, command.ExecuteContext(t.Context()))
			require.Contains(t, stdout.String(), "already initialized")
			require.Contains(t, stdout.String(), "agent-gateway serve")
			require.Empty(t, stderr.String())
			require.Equal(t, before, treeBytes(t, root))
		}
		if owner != nil {
			require.NoError(t, owner.MarkClean())
			require.NoError(t, owner.Close())
		}
	}
}

func TestInitRefusesNonemptyUnknownOrUnsafeRootsWithoutMutation(t *testing.T) {
	for _, scenario := range []string{"unknown", "unsafe-root", "partial-evidence", "no-resume-evidence"} {
		t.Run(scenario, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "gateway")
			require.NoError(t, os.Mkdir(root, 0700))
			args := []string{"init", "--confirm", "--data-dir", root}
			switch scenario {
			case "unknown":
				require.NoError(t, os.WriteFile(filepath.Join(root, "notes"), []byte("retain"), 0600))
			case "unsafe-root":
				require.NoError(t, os.Chmod(root, 0755))
			case "partial-evidence":
				require.NoError(t, os.Mkdir(filepath.Join(root, gatewaypaths.InitializationName), 0700))
			case "no-resume-evidence":
				args = append(args, "--resume")
			}
			before := treeBytes(t, root)
			command := newTestRootCmd(t)
			stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
			command.SetOut(stdout)
			command.SetErr(stderr)
			command.SetArgs(args)
			require.Error(t, command.ExecuteContext(t.Context()))
			require.Empty(t, stdout.String())
			require.Equal(t, before, treeBytes(t, root))
			require.NotContains(t, stderr.String(), "delete the directory")
		})
	}
}

func TestInitRecognizedInterruptionRequiresExplicitConsistentResume(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	owner, err := gatewaypaths.AcquireInitialization(root)
	require.NoError(t, err)
	plan := gatewaypaths.InitializationPlan{Version: 1, Root: root, Bearer: owner.Layout().AdminBearer, Installation: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Traffic: "01ARZ3NDEKTSV4RRFFQ69G5FAW"}
	require.NoError(t, gatewaypaths.BeginInitialization(owner, plan))
	require.NoError(t, owner.Close())
	before := treeBytes(t, root)
	for _, args := range [][]string{{"init", "--confirm"}, {"init", "--resume", "--confirm", "--secret-output", filepath.Join(t.TempDir(), "different")}, {"serve", "--clear-http-proxy-listen"}} {
		command := newTestRootCmd(t)
		stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
		command.SetOut(stdout)
		command.SetErr(stderr)
		command.SetArgs(append(args, "--data-dir", root))
		require.Error(t, command.ExecuteContext(t.Context()))
		require.Empty(t, stdout.String())
		require.Equal(t, before, treeBytes(t, root))
	}
	command := newTestRootCmd(t)
	output := new(bytes.Buffer)
	command.SetOut(output)
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"doctor", "--data-dir", root})
	_ = command.ExecuteContext(t.Context())
	require.Contains(t, output.String(), "initialization")
	require.Contains(t, output.String(), "init --resume")
	require.Equal(t, before, treeBytes(t, root))
	command = newTestRootCmd(t)
	command.SetOut(new(bytes.Buffer))
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"init", "--resume", "--confirm", "--data-dir", root})
	require.NoError(t, command.ExecuteContext(t.Context()))
}

func TestInitSafeAncestorAliasAndReservedBearerPaths(t *testing.T) {
	parent := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(parent, alias))
	root := filepath.Join(alias, "gateway")
	require.NoError(t, os.Mkdir(filepath.Join(parent, "gateway"), 0700))
	command := newTestRootCmd(t)
	command.SetOut(new(bytes.Buffer))
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"init", "--confirm", "--data-dir", root})
	require.NoError(t, command.ExecuteContext(t.Context()))
	command = newTestRootCmd(t)
	command.SetOut(new(bytes.Buffer))
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"init", "--data-dir", root})
	require.NoError(t, command.ExecuteContext(t.Context()))
	empty := filepath.Join(parent, "empty")
	require.NoError(t, os.Mkdir(empty, 0700))
	for _, reserved := range []string{gatewaypaths.DatabaseName, gatewaypaths.LockName, gatewaypaths.MasterKeyName, gatewaypaths.InitializationName, gatewaypaths.PublicCertificateName} {
		command = newTestRootCmd(t)
		command.SetOut(new(bytes.Buffer))
		command.SetErr(new(bytes.Buffer))
		command.SetArgs([]string{"init", "--confirm", "--data-dir", filepath.Join(alias, "empty"), "--secret-output", filepath.Join(empty, reserved)})
		require.Error(t, command.ExecuteContext(t.Context()))
		entries, err := os.ReadDir(empty)
		require.NoError(t, err)
		require.Empty(t, entries)
	}
}

func TestInitDefaultNoConsentCreatesNothing(t *testing.T) {
	for _, answer := range []string{"", "\n", "n\n"} {
		root := filepath.Join(t.TempDir(), "gateway")
		command := newTestRootCmd(t)
		command.SetIn(bytes.NewBufferString(answer))
		command.SetOut(new(bytes.Buffer))
		stderr := new(bytes.Buffer)
		command.SetErr(stderr)
		command.SetArgs([]string{"init", "--data-dir", root})
		require.Error(t, command.ExecuteContext(t.Context()))
		_, err := os.Stat(root)
		require.True(t, os.IsNotExist(err))
	}
}
