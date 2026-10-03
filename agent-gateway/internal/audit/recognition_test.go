package audit_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

type recognitionFixture struct {
	calls   int
	targets []contract.AuditTarget
}

func (source *recognitionFixture) AuditTargetNamesTx(ctx context.Context, tx *sql.Tx, targets []contract.AuditTarget) (map[contract.AuditTarget]string, error) {
	source.calls++
	source.targets = targets
	rows, err := tx.QueryContext(ctx, "SELECT id, name FROM recognition_fixture")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := map[contract.AuditTarget]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		result[contract.AuditTarget{Type: "grant", ID: id}] = name
	}
	return result, rows.Err()
}
func TestAuditRecognitionIsCurrentSeparateAndBatched(t *testing.T) {
	store, _ := fixture(t)
	source := &recognitionFixture{}
	repository, err := audit.NewRepository(store, source)
	require.NoError(t, err)
	mutate := func(statement string, args ...any) {
		t.Helper()
		require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error { _, err := tx.ExecContext(t.Context(), statement, args...); return err }))
	}
	mutate("CREATE TABLE recognition_fixture (id TEXT PRIMARY KEY, name TEXT)")
	mutate("INSERT INTO recognition_fixture VALUES (?, ?)", credentialID, "Original name")
	var original contract.AuditEvent
	for i := 1; i <= 100; i++ {
		saved, err := repository.Append(t.Context(), event(i))
		require.NoError(t, err)
		original = saved
	}
	page, err := repository.List(t.Context(), audit.Query{Limit: 100})
	require.NoError(t, err)
	require.Len(t, page.Items, 100)
	require.Len(t, page.TargetRecognition, 1)
	require.Equal(t, "Original name", *page.TargetRecognition[0].DisplayName)
	require.Equal(t, 1, source.calls)
	require.Len(t, source.targets, 1)
	mutate("UPDATE recognition_fixture SET name = ?", "Renamed resource")
	item, err := repository.Read(t.Context(), original.ID, page.History.Generation)
	require.NoError(t, err)
	require.Equal(t, original, item.Event)
	require.Equal(t, "Renamed resource", *item.TargetRecognition[0].DisplayName)
	mutate("DELETE FROM recognition_fixture")
	item, err = repository.Read(t.Context(), original.ID, page.History.Generation)
	require.NoError(t, err)
	require.Equal(t, original, item.Event)
	require.Nil(t, item.TargetRecognition[0].DisplayName)
	mutate("DROP TABLE recognition_fixture")
	_, err = repository.Read(t.Context(), original.ID, page.History.Generation)
	require.Error(t, err)
}
