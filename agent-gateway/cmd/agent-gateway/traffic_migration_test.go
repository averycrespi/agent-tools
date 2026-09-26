package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/stretchr/testify/require"
)

func TestTrafficMigrationReportsErrors(t *testing.T) {
	for _, test := range []struct {
		args    []string
		message string
		exit    int
	}{
		{nil, "--installation-id", 2},
		{[]string{"--confirm"}, "--installation-id", 2},
		{[]string{"unexpected"}, "Usage:", 2},
		{[]string{"--traffic-budget-bytes", "invalid"}, "--traffic-budget-bytes", 2},
		{[]string{"--installation-id", "01J60000000000000000000001", "--confirm"}, "absent", 4},
	} {
		command := newRootCmd()
		stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
		command.SetOut(stdout)
		command.SetErr(stderr)
		args := []string{"maintenance", "migrate-traffic-storage", "--data-dir", filepath.Join(t.TempDir(), "absent")}
		command.SetArgs(append(args, test.args...))
		err := command.ExecuteContext(t.Context())
		require.Error(t, err)
		require.Equal(t, test.exit, commandExitCode(err))
		require.Empty(t, stdout.String())
		require.Contains(t, stderr.String(), test.message)
	}
}

func TestTrafficMigrationProblemDoesNotExposeRawErrors(t *testing.T) {
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
