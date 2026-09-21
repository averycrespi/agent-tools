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
		name    string
		args    []string
		message string
		exit    int
	}{
		{name: "confirmation", message: "--confirm", exit: 2},
		{name: "missing installation", args: []string{"--confirm"}, message: "migration", exit: 7},
		{name: "positional", args: []string{"unexpected"}, message: "Usage:", exit: 2},
		{name: "invalid flag", args: []string{"--confirm", "--traffic-budget-bytes", "invalid"}, message: "Usage:", exit: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := newRootCmd()
			stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
			command.SetOut(stdout)
			command.SetErr(stderr)
			args := []string{"storage", "migrate-traffic", "--installation-id", "01J60000000000000000000001", "--data-dir", filepath.Join(t.TempDir(), "absent")}
			command.SetArgs(append(args, test.args...))
			err := command.ExecuteContext(t.Context())
			require.Error(t, err)
			require.Equal(t, test.exit, commandExitCode(err))
			require.Empty(t, stdout.String())
			require.Contains(t, stderr.String(), test.message)
		})
	}
}

func TestTrafficMigrationProblemDoesNotExposeRawErrors(t *testing.T) {
	for _, cause := range []error{errors.New("private-database-value"), errors.Join(gatewaypaths.ErrInUse, errors.New("private-process-value"))} {
		problem := trafficMigrationProblem(cause)
		require.NotContains(t, problem.Title, "private-")
		if errors.Is(cause, gatewaypaths.ErrInUse) {
			require.Equal(t, 5, commandExitCode(problem))
			require.Contains(t, problem.Title, "Stop all Gateway launchers")
		} else {
			require.Equal(t, 7, commandExitCode(problem))
			require.Contains(t, problem.Title, "may already be selected")
		}
	}
}
