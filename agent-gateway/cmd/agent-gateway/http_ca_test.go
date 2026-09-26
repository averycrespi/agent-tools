package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/stretchr/testify/require"
)

func TestHTTPCASafeGrammarAndFailures(t *testing.T) {
	for _, args := range [][]string{
		{"create"}, {"replace", "--installation-id", "invalid"},
		{"create", "--confirm", "--installation-id", "invalid"}, {"export", "unexpected"},
		{"export", "--confirm"}, {"create", "--unknown"},
		{"export", "--stdout", "--json"}, {"export", "--stdout", "--output", "file"},
	} {
		command := newRootCmd()
		stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
		command.SetOut(stdout)
		command.SetErr(stderr)
		command.SetArgs(append([]string{"--data-dir", filepath.Join(t.TempDir(), "absent"), "http", "ca"}, args...))
		err := command.ExecuteContext(t.Context())
		require.Error(t, err)
		require.Equal(t, 2, commandExitCode(err))
		require.Empty(t, stdout.String())
		require.Contains(t, stderr.String(), "Usage: agent-gateway http ca")
	}
	for _, err := range []error{errors.New("private-detail"), errors.Join(gatewaypaths.ErrInUse, errors.New("private-detail"))} {
		problem := httpCAProblem(err)
		require.NotContains(t, problem.Title, "private-detail")
		if errors.Is(err, gatewaypaths.ErrInUse) {
			require.Equal(t, 5, problem.Exit)
		} else {
			require.Equal(t, 7, problem.Exit)
		}
		require.Contains(t, problem.Title, "unchanged")
		require.NotContains(t, problem.Title, "uncertain")
	}
}
