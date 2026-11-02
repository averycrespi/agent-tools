package storage

import (
	"database/sql"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiagnosticMetadataNeverGrantsWriterOrCreatesSidecars(t *testing.T) {
	owner := newOwnership(t)
	store, err := Initialize(t.Context(), owner, testInstallationID)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	before, err := os.ReadFile(owner.Layout().Database)
	require.NoError(t, err)
	entries, err := os.ReadDir(owner.Layout().Root)
	require.NoError(t, err)
	identity, err := ReadDiagnosticMetadata(t.Context(), owner, func(tx *sql.Tx) error {
		_, writeErr := tx.ExecContext(t.Context(), `UPDATE gateway_meta SET revision=revision+1`)
		require.Error(t, writeErr, "passive metadata must never grant writable authority")
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, testInstallationID, identity.InstallationID)
	after, err := os.ReadFile(owner.Layout().Database)
	require.NoError(t, err)
	require.Equal(t, before, after)
	afterEntries, err := os.ReadDir(owner.Layout().Root)
	require.NoError(t, err)
	require.Equal(t, entries, afterEntries)
	require.NoError(t, os.WriteFile(owner.Layout().MutationMarker, []byte(`{"state":"unknown","secret":"DO-NOT-RENDER"}`), 0600))
	_, err = ReadDiagnosticMetadata(t.Context(), owner, nil)
	require.ErrorIs(t, err, ErrStorageLatched)
	require.NotContains(t, err.Error(), "DO-NOT-RENDER")
}

func TestDiagnosticMetadataRefusesActiveWAL(t *testing.T) {
	owner := newOwnership(t)
	store, err := Initialize(t.Context(), owner, testInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	before, err := os.ReadFile(owner.Layout().Database + "-wal")
	require.NoError(t, err)
	require.NotEmpty(t, before)
	called := false
	_, err = ReadDiagnosticMetadata(t.Context(), owner, func(*sql.Tx) error { called = true; return nil })
	require.ErrorIs(t, err, ErrInspectionUnavailable)
	require.False(t, called)
	after, err := os.ReadFile(owner.Layout().Database + "-wal")
	require.NoError(t, err)
	require.Equal(t, before, after)
}
