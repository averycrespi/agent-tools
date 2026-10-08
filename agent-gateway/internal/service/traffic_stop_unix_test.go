//go:build darwin || linux

package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServiceStopDoesNotCertifyTrafficCheckpoint(t *testing.T) {
	f := newFixture(t)
	f.install(t)
	d, _, _, err := f.m.read()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(d.DataDir, 0o700))
	// Utility fixtures qualify process observations only. These deliberately
	// unresolved artifacts must never be interpreted or cleaned by the wrapper.
	artifacts := map[string]string{"run.unclean": "unconfirmed storage settlement", "traffic-retained.db-wal": "retained traffic WAL"}
	for name, contents := range artifacts {
		require.NoError(t, os.WriteFile(filepath.Join(d.DataDir, name), []byte(contents), 0o600))
	}
	f.loaded, f.running = true, true
	result, err := f.m.execute(t.Context(), "stop", Changes{})
	require.NoError(t, err)
	require.Equal(t, "unloaded", result.Launchd)
	require.Equal(t, "not-probed", result.Readiness)
	require.Equal(t, []string{"bootout"}, f.mutations)
	for name, contents := range artifacts {
		actual, err := os.ReadFile(filepath.Join(d.DataDir, name))
		require.NoError(t, err)
		require.Equal(t, contents, string(actual))
	}
	require.NotContains(t, result.Message, "checkpoint")
	require.NotContains(t, result.Message, "clean shutdown")
}
