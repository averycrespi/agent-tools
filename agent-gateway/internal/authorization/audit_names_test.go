package authorization

import (
	"database/sql"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestAuditTargetNamesRecognizeOnlySelectedCurrentResources(t *testing.T) {
	repository, store := newRepository(t, nil)
	seedPrincipal(t, store, principalRow{id: id(1), displayName: "Original agent"})
	seedPrincipal(t, store, principalRow{id: id(2), displayName: "Not requested"})
	target := contract.AuditTarget{Type: "principal", ID: id(1)}
	read := func() map[contract.AuditTarget]string {
		t.Helper()
		var names map[contract.AuditTarget]string
		require.NoError(t, store.View(t.Context(), func(tx *sql.Tx) error {
			var err error
			names, err = repository.AuditTargetNamesTx(t.Context(), tx, []contract.AuditTarget{target, {Type: "grant", ID: id(99)}, {Type: "backup", ID: id(1)}})
			return err
		}))
		return names
	}
	require.Equal(t, map[contract.AuditTarget]string{target: "Original agent"}, read())
	require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(t.Context(), "UPDATE principals SET display_name = ? WHERE id = ?", "Renamed agent", id(1))
		return err
	}))
	require.Equal(t, "Renamed agent", read()[target])
	target.ID = id(99)
	require.Empty(t, read())
}
