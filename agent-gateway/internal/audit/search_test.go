package audit_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestAuditTargetSearchBeforePagingAndRecognitionChanges(t *testing.T) {
	store, _ := fixture(t)
	source := &recognitionFixture{}
	repository, err := audit.NewRepository(store, source)
	require.NoError(t, err)
	mutate := func(statement string, args ...any) {
		t.Helper()
		require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error { _, err := tx.ExecContext(t.Context(), statement, args...); return err }))
	}
	mutate("CREATE TABLE recognition_fixture (id TEXT PRIMARY KEY, name TEXT)")
	mutate("INSERT INTO recognition_fixture VALUES (?, ?)", credentialID, "Café Workshop")
	for i := 1; i <= 60; i++ {
		item := event(i)
		if i > 2 {
			item.Target.ID = event(999).ID
		}
		_, err := repository.Append(t.Context(), item)
		require.NoError(t, err)
	}
	query := audit.Query{Limit: 1, Filters: contract.AuditFilters{Target: "cafe workshpo"}}
	page, err := repository.List(t.Context(), query)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, event(2).ID, page.Items[0].ID)
	require.NotNil(t, page.NextCursor)
	require.Equal(t, 2, source.calls, "one search batch plus one selected-page recognition batch")
	query.Cursor = *page.NextCursor
	next, err := repository.List(t.Context(), query)
	require.NoError(t, err)
	require.Len(t, next.Items, 1)
	require.Equal(t, event(1).ID, next.Items[0].ID)
	query.Filters.Target = "Workshop"
	_, err = repository.List(t.Context(), query)
	require.ErrorIs(t, err, audit.ErrInvalidCursor)
	query.Filters.Target = "cafe workshpo"
	mutate("UPDATE recognition_fixture SET name = ?", "Renamed library")
	_, err = repository.List(t.Context(), query)
	require.ErrorIs(t, err, audit.ErrStaleCursor)
	query.Cursor = ""
	page, err = repository.List(t.Context(), query)
	require.NoError(t, err)
	require.Empty(t, page.Items)
	query.Filters.Target = "renmaed"
	page, err = repository.List(t.Context(), query)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, "Renamed library", *page.TargetRecognition[0].DisplayName)
	mutate("DELETE FROM recognition_fixture")
	page, err = repository.List(t.Context(), query)
	require.NoError(t, err)
	require.Empty(t, page.Items)
	for _, text := range []string{credentialID, "RRFFQ69"} {
		query.Filters.Target = text
		page, err = repository.List(t.Context(), query)
		require.NoError(t, err)
		require.Len(t, page.Items, 1)
		require.Nil(t, page.TargetRecognition[0].DisplayName)
	}
	for _, text := range []string{"rrffq69", "RRFFQ68", "%", "_"} {
		query.Filters.Target = text
		page, err = repository.List(t.Context(), query)
		require.NoError(t, err)
		require.Empty(t, page.Items, text)
	}
	query.Filters.Target = "\n"
	_, err = repository.List(t.Context(), query)
	require.ErrorIs(t, err, audit.ErrInvalidInput)
	query.Filters.Target = strings.Repeat("a", 257)
	_, err = repository.List(t.Context(), query)
	require.ErrorIs(t, err, audit.ErrInvalidInput)
}
