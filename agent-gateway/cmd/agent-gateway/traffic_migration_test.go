package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/stretchr/testify/require"
)

func TestRemovedMaintenanceCommandsPreserveArtifacts(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"gateway.db.previous-old", "traffic-old.db", "master-key", "tombstone", "gateway.db.traffic-migration", "backups/retained"} {
		path := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte("retained "+name), 0o600))
	}
	before := treeBytes(t, root)
	for _, operation := range []string{"setup-secret-storage", "migrate-traffic-storage"} {
		command := newRootCmd()
		stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
		command.SetOut(stdout)
		command.SetErr(stderr)
		command.SetArgs([]string{"maintenance", operation, "--data-dir", root, "--confirm", "--json"})
		err := command.ExecuteContext(t.Context())
		require.Error(t, err)
		require.Empty(t, stdout.String())
		require.Contains(t, err.Error(), "maintenance --help")
		require.Equal(t, before, treeBytes(t, root))
	}
}

func TestMaintenanceProblemDoesNotExposeRawErrors(t *testing.T) {
	for _, cause := range []error{errors.New("private-database-value"), errors.Join(gatewaypaths.ErrInUse, errors.New("private-process-value"))} {
		problem := maintenanceProblem(cause, "/selected-root")
		require.NotContains(t, problem.Title, "private-")
		require.Contains(t, problem.Title, "doctor")
		if errors.Is(cause, gatewaypaths.ErrInUse) {
			require.Equal(t, 5, problem.Exit)
			require.NotContains(t, problem.Title, "uncertain")
		} else {
			require.Equal(t, 7, problem.Exit)
		}
	}
}
