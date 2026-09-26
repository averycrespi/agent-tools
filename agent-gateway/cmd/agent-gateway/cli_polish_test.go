package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestInitActiveWALExplainsBlockerWithoutMutation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	owner, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	defer func() { require.NoError(t, owner.Close()) }()
	store, err := storage.Initialize(t.Context(), owner, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	for _, suffix := range []string{"-wal", "-shm"} {
		require.NoError(t, os.Chmod(owner.Layout().Database+suffix, 0644))
	}
	before := treeBytes(t, root)
	command := newTestRootCmd(t)
	stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
	command.SetOut(stdout)
	command.SetErr(stderr)
	command.SetArgs([]string{"init", "--data-dir", root})
	require.Error(t, command.ExecuteContext(t.Context()))
	require.Empty(t, stdout.String())
	require.Contains(t, stderr.String(), "Cannot verify setup while Gateway has active WAL or journal data")
	require.Contains(t, stderr.String(), "Nothing changed")
	require.Contains(t, stderr.String(), "agent-gateway init --data-dir '"+root+"'")
	require.NotContains(t, stderr.String(), "unsafe")
	require.Equal(t, before, treeBytes(t, root))
}

func TestPathFailurePreservesSafeCauseAndMutationEffect(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "gateway.db")
	require.NoError(t, os.WriteFile(path, nil, 0600))
	require.NoError(t, os.Chmod(path, 0666))
	err := gatewaypaths.ValidateOwnerOnlyFile(path)
	require.ErrorIs(t, err, gatewaypaths.ErrUnsafePath)
	problem := maintenanceProblem(errors.Join(err, errors.New("DO-NOT-PRINT-secret")), root)
	require.Contains(t, problem.Title, path)
	require.Contains(t, problem.Title, "permissions are 0666; expected 0600")
	require.NotContains(t, problem.Title, "DO-NOT-PRINT")
	changed := maintenanceProblem(&storage.OperationError{Effect: "changed", Cause: err}, root)
	require.Contains(t, changed.Title, "replacement was selected")
}

func TestDoctorCompactOutputAndVerboseDetails(t *testing.T) {
	result := doctorResult{DataDir: "/private/gateway", Selection: "account-home default", Checks: []doctorCheck{
		{Name: "installation", State: "ok", Path: "/private/gateway", Detail: "root details"},
		{Name: "storage", State: "present", Path: "/private/gateway/gateway.db", Detail: "presence is not integrity"},
		{Name: "public CA certificate", State: "absent", Path: "/private/gateway/http-ca.pem", Detail: "certificate absent", Next: "agent-gateway init"},
		{Name: "protected CA signing material", State: "not-checked", Detail: "no keyring probe"},
		{Name: "runtime readiness", State: "ok", Detail: "not process identity"},
		{Name: "administrator credential", State: "failed", Path: "/external/bearer", Detail: "permissions are 0666; expected 0600", Next: "Correct permissions"},
	}}
	compact := renderDoctorChecks(result, false)
	require.Contains(t, compact, "Database           Present      gateway.db\n")
	require.Contains(t, compact, "API readiness      Ready\n")
	require.Contains(t, compact, "  Next: agent-gateway init\n")
	require.Contains(t, compact, "/external/bearer")
	require.Contains(t, compact, "  permissions are 0666; expected 0600\n")
	require.NotContains(t, compact, "presence is not integrity")
	require.NotContains(t, compact, "root details")
	require.NotContains(t, compact, "[ok]")
	require.Equal(t, 1, strings.Count(compact, "/private/gateway"))
	verbose := renderDoctorChecks(result, true)
	require.Contains(t, verbose, "  presence is not integrity\n")
	require.Contains(t, verbose, "  no keyring probe\n")
}

func TestOrdinaryPathCommandsUseShellQuoting(t *testing.T) {
	for _, path := range []string{"/tmp/plain", "/tmp/space and 'quote", `/tmp/$(touch unsafe);back\slash`, "/tmp/café"} {
		got, err := renderPathFlagCommand("agent-gateway doctor", "--data-dir", "data_dir", path)
		require.NoError(t, err)
		require.Equal(t, "agent-gateway doctor --data-dir '"+strings.ReplaceAll(path, "'", `'"'"'`)+"'", got)
		require.NotContains(t, got, "printf")
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	defaults, err := gatewaypaths.Resolve("")
	require.NoError(t, err)
	got, err := renderInstallationCommand("agent-gateway init", defaults.Root)
	require.NoError(t, err)
	require.Equal(t, "agent-gateway init", got)
}
