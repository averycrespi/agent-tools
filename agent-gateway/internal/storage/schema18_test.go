package storage

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTrafficSelectionFollowsFailureDiagnosticsLineage(t *testing.T) {
	owner := newOwnership(t)
	writePopulatedSchemaNineFixture(t, t.Context(), owner)
	fixture, err := openConfigured(t.Context(), owner.Layout(), testOptions{})
	require.NoError(t, err)
	require.NoError(t, fixture.migrateThrough(t.Context(), 9, 17))
	raw := `{"gateway_observed":{"source":"tool","reason":"reported_error"}}`
	_, err = fixture.database.ExecContext(t.Context(), `UPDATE invocations SET completed_at='2026-08-26T00:00:01Z',terminal_class='downstream_failure',failure_diagnostics=? WHERE id='01J60000000000000000000030'`, raw)
	require.NoError(t, err)
	backup := filepath.Join(t.TempDir(), "legacy17.db")
	require.NoError(t, fixture.BackupTo(t.Context(), backup))
	require.NoError(t, fixture.Checkpoint(t.Context()))
	require.NoError(t, fixture.Close())
	identity, err := VerifyBackup(t.Context(), backup)
	require.NoError(t, err)
	require.Equal(t, 17, identity.SchemaVersion)
	require.Empty(t, identity.TrafficGeneration)
	current, err := Open(t.Context(), owner)
	require.NoError(t, err)
	defer func() { require.NoError(t, current.Close()) }()
	identity, err = current.Identity(t.Context())
	require.NoError(t, err)
	require.Equal(t, CurrentSchema, identity.SchemaVersion)
	selected, err := current.SelectedTraffic(t.Context())
	require.NoError(t, err)
	require.Empty(t, selected)
	var actual string
	require.NoError(t, current.database.QueryRowContext(t.Context(), `SELECT failure_diagnostics FROM invocations WHERE id='01J60000000000000000000030'`).Scan(&actual))
	require.Equal(t, raw, actual)
}
