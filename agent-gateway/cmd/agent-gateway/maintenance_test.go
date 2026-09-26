package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/composition"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func treeBytes(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[path] = string(data)
		return nil
	}))
	return result
}

func TestMaintenanceDryRunAndUnconfirmedExecutionDoNotWrite(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	init := newTestRootCmd(t)
	init.SetOut(new(bytes.Buffer))
	init.SetErr(new(bytes.Buffer))
	init.SetArgs([]string{"init", "--data-dir", root, "--confirm"})
	require.NoError(t, init.ExecuteContext(t.Context()))
	before := treeBytes(t, root)
	for _, operation := range []string{"verify-and-recover-storage", "reset-admin-credentials"} {
		for _, dry := range []bool{true, false} {
			command := newTestRootCmd(t)
			stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
			command.SetOut(stdout)
			command.SetErr(stderr)
			command.SetIn(bytes.NewBuffer(nil))
			args := []string{"maintenance", operation, "--data-dir", root, "--json"}
			secret := filepath.Join(t.TempDir(), "new-bearer")
			if operation == "reset-admin-credentials" {
				args = append(args, "--secret-output", secret)
			}
			if dry {
				args = append(args, "--dry-run")
			}
			command.SetArgs(args)
			err := command.ExecuteContext(t.Context())
			if dry {
				require.NoError(t, err, stderr.String())
				var plan maintenancePlan
				require.NoError(t, json.Unmarshal(stdout.Bytes(), &plan))
				require.True(t, plan.DryRun)
				require.NotEmpty(t, plan.InstallationID)
			} else {
				require.Error(t, err)
				require.Contains(t, stderr.String(), "--confirm")
				require.Empty(t, stdout.String())
			}
			require.Equal(t, before, treeBytes(t, root))
			_, err = os.Stat(secret)
			require.ErrorIs(t, err, os.ErrNotExist)
		}
	}
}

func TestMigrationDryRunRequiresIdentityAndPreservesGeneration(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	owner, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	const id = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	store, err := storage.Initialize(t.Context(), owner, id)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	require.NoError(t, owner.MarkClean())
	require.NoError(t, owner.Close())
	before := treeBytes(t, root)
	command := newTestRootCmd(t)
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	command.SetOut(stdout)
	command.SetErr(stderr)
	command.SetArgs([]string{"maintenance", "migrate-traffic-storage", "--data-dir", root, "--installation-id", id, "--dry-run", "--traffic-budget-bytes", "256MiB", "--json"})
	require.NoError(t, command.ExecuteContext(t.Context()), stderr.String())
	require.Contains(t, stdout.String(), id)
	require.Equal(t, before, treeBytes(t, root))
	command = newTestRootCmd(t)
	command.SetOut(new(bytes.Buffer))
	command.SetErr(stderr)
	command.SetArgs([]string{"maintenance", "migrate-traffic-storage", "--data-dir", root, "--dry-run"})
	require.Error(t, command.ExecuteContext(t.Context()))
	require.Contains(t, stderr.String(), "--installation-id")
	require.Equal(t, before, treeBytes(t, root))
}

func TestRetiredOfflineGrammarIsAbsent(t *testing.T) {
	for _, args := range [][]string{{"status"}, {"principal", "list"}, {"storage", "verify"}, {"admin", "reset"}, {"backup", "restore"}, {"http", "ca", "create"}} {
		command := newRootCmd()
		command.SetOut(new(bytes.Buffer))
		command.SetErr(new(bytes.Buffer))
		command.SetArgs(args)
		require.Error(t, command.ExecuteContext(t.Context()), args)
	}
	root := newRootCmd()
	alias, rest, err := root.Find([]string{"initialize"})
	require.NoError(t, err)
	require.Empty(t, rest)
	require.Equal(t, "init", alias.Name())
	command := newRootCmd()
	output := new(bytes.Buffer)
	command.SetOut(output)
	command.SetArgs([]string{"maintenance"})
	require.NoError(t, command.ExecuteContext(t.Context()))
	require.Contains(t, output.String(), "--dry-run")
}

func TestInitRunningNoopPreservesCredentialsAndCA(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	command := newTestRootCmd(t)
	command.SetOut(new(bytes.Buffer))
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"init", "--data-dir", root, "--confirm"})
	require.NoError(t, command.ExecuteContext(t.Context()))
	owner, err := gatewaypaths.AcquireStoppedExisting(root)
	require.NoError(t, err)
	defer func() { require.NoError(t, owner.Close()) }()
	before := treeBytes(t, root)
	output := new(bytes.Buffer)
	command = newTestRootCmd(t)
	command.SetOut(output)
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"initialize", "--data-dir", root, "--json"})
	require.NoError(t, command.ExecuteContext(t.Context()))
	require.Contains(t, output.String(), `"changed":false`)
	require.Equal(t, before, treeBytes(t, root))
	require.NoError(t, os.Remove(filepath.Join(root, gatewaypaths.PublicCertificateName)))
	command = newTestRootCmd(t)
	command.SetOut(new(bytes.Buffer))
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"init", "--data-dir", root, "--confirm"})
	require.Error(t, command.ExecuteContext(t.Context()))
	_, err = composition.HTTPCA(t.Context(), root, "", "export", systemClock{}, bytes.NewReader(nil))
	require.ErrorIs(t, err, gatewaypaths.ErrInUse)
}
