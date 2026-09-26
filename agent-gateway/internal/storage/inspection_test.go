package storage

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/stretchr/testify/require"
)

func TestMaintenanceInspectionIsImmutableAndRevalidatesMarkers(t *testing.T) {
	owner, err := gatewaypaths.Acquire(filepath.Join(t.TempDir(), "gateway"))
	require.NoError(t, err)
	defer func() { require.NoError(t, owner.Close()) }()
	const id = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	store, err := Initialize(t.Context(), owner, id)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	before, err := os.ReadFile(owner.Layout().Database)
	require.NoError(t, err)
	entries, err := os.ReadDir(owner.Layout().Root)
	require.NoError(t, err)
	inspection, err := InspectMaintenance(t.Context(), owner, func(tx *sql.Tx) error {
		_, writeErr := tx.ExecContext(t.Context(), `UPDATE gateway_meta SET revision=revision+1`)
		require.Error(t, writeErr, "inspection transaction must not grant a writer")
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, id, inspection.Identity.InstallationID)
	require.NoError(t, inspection.Revalidate(t.Context(), owner))
	after, err := os.ReadFile(owner.Layout().Database)
	require.NoError(t, err)
	require.Equal(t, before, after)
	afterEntries, err := os.ReadDir(owner.Layout().Root)
	require.NoError(t, err)
	require.Equal(t, entries, afterEntries)
	marker := []byte(`{"installation_id":"` + id + `","state":"armed"}`)
	require.NoError(t, os.WriteFile(owner.Layout().MutationMarker, marker, 0600))
	require.ErrorIs(t, inspection.Revalidate(t.Context(), owner), ErrPlanChanged)
	current, err := InspectMaintenance(t.Context(), owner, nil)
	require.NoError(t, err)
	require.True(t, current.Marked)
	require.NoError(t, os.WriteFile(owner.Layout().MutationMarker, []byte(`{"installation_id":"foreign","state":"armed"}`), 0600))
	_, err = InspectMaintenance(t.Context(), owner, nil)
	require.ErrorIs(t, err, ErrStorageLatched)
}

func TestMaintenanceInspectionRefusesUncheckpointedState(t *testing.T) {
	owner, err := gatewaypaths.Acquire(filepath.Join(t.TempDir(), "gateway"))
	require.NoError(t, err)
	defer func() { require.NoError(t, owner.Close()) }()
	store, err := Initialize(t.Context(), owner, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	require.NoError(t, os.Chmod(owner.Layout().Database+"-wal", 0600))
	_, err = InspectMaintenance(t.Context(), owner, nil)
	require.ErrorIs(t, err, ErrInspectionUnavailable)
}
