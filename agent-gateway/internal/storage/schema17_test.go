package storage

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFailureDiagnosticsMigrationPreservesLegacyAndTerminalOnce(t *testing.T) {
	ownership := newOwnership(t)
	writePopulatedSchemaNineFixture(t, t.Context(), ownership)
	fixture, err := openConfigured(t.Context(), ownership.Layout(), testOptions{})
	require.NoError(t, err)
	require.NoError(t, fixture.migrateThrough(t.Context(), 9, 16))
	// The schema-nine fixture already contains the unterminated legacy row.
	require.NoError(t, fixture.Checkpoint(t.Context()))
	require.NoError(t, fixture.Close())
	store, err := Open(t.Context(), ownership)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	var diagnostic sql.NullString
	require.NoError(t, store.database.QueryRowContext(t.Context(), `SELECT failure_diagnostics FROM invocations WHERE id='01J60000000000000000000030'`).Scan(&diagnostic))
	require.False(t, diagnostic.Valid)
	for _, raw := range []string{`invalid`, `[]`, strings.Repeat(" ", 513) + `{}`} {
		_, err := store.database.ExecContext(t.Context(), `UPDATE invocations SET completed_at='2026-08-26T00:00:01Z', terminal_class='downstream_failure', failure_diagnostics=? WHERE id='01J60000000000000000000030'`, raw)
		require.Error(t, err)
	}
	_, err = store.database.ExecContext(t.Context(), `UPDATE invocations SET completed_at='2026-08-26T00:00:01Z', terminal_class='succeeded', failure_diagnostics='{}' WHERE id='01J60000000000000000000030'`)
	require.Error(t, err)
	raw := `{"gateway_observed":{"source":"tool","reason":"reported_error"}}`
	_, err = store.database.ExecContext(t.Context(), `UPDATE invocations SET completed_at='2026-08-26T00:00:01Z', terminal_class='downstream_failure', failure_diagnostics=? WHERE id='01J60000000000000000000030'`, raw)
	require.NoError(t, err)
	_, err = store.database.ExecContext(t.Context(), `UPDATE invocations SET failure_diagnostics=NULL WHERE id='01J60000000000000000000030'`)
	require.Error(t, err)
	require.NoError(t, store.database.QueryRowContext(t.Context(), `SELECT failure_diagnostics FROM invocations WHERE id='01J60000000000000000000030'`).Scan(&diagnostic))
	require.Equal(t, raw, diagnostic.String)
}
