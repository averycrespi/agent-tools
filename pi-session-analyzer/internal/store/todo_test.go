package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/ingest"
	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/robound"
	"github.com/stretchr/testify/require"
)

func TestTodoDiagnosticsTreatsOversizedMissingItemsAsMalformed(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	data := `{"padding":"` + strings.Repeat("x", todoSnapshotBytes+1) + `"}`
	require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{ID: "large-malformed", CustomStates: []ingest.CustomState{{ID: "todo", Type: "todo-state", Data: data}}}, SourceMeta{Path: "large.jsonl", Size: 1}))
	diagnostics, err := s.TodoDiagnostics(context.Background(), "large-malformed")
	require.NoError(t, err)
	require.Equal(t, "malformed", diagnostics.FinalState)
	require.True(t, diagnostics.FinalListTruncated)
}

func TestTodoDiagnosticsHandlesProgressionClearReopenRemoveAndMalformedTail(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	session := ingest.Session{ID: "todo-session", CustomStates: []ingest.CustomState{
		{ID: "t1", Type: "todo-state", SourceLine: 2, Data: `{"items":[{"id":1,"text":"first","status":"todo"},{"id":2,"text":"second","status":"done","notes":"kept"}]}`},
		{ID: "t2", Type: "todo-state", SourceLine: 3, Data: `{"items":[]}`},
		{ID: "t3", Type: "todo-state", SourceLine: 4, Data: `{"items":[{"id":1,"text":"first","status":"in_progress"},{"id":"future","text":"future","status":"paused"}]}`},
		{ID: "t4", Type: "todo-state", SourceLine: 5, Data: `{"items":[`},
	}}
	require.NoError(t, s.ReplaceSession(context.Background(), session, SourceMeta{Path: "todo.jsonl", Size: 1, ModTimeNS: 1}))

	diagnostics, err := s.TodoDiagnostics(context.Background(), "todo-sess")
	require.NoError(t, err)
	require.Equal(t, "malformed", diagnostics.FinalState)
	require.Equal(t, 1, diagnostics.MalformedSnapshots)
	require.Len(t, diagnostics.Snapshots, 4)
	require.Equal(t, TodoCounts{Todo: 1, Done: 1}, diagnostics.Snapshots[0].Counts)
	require.True(t, diagnostics.Snapshots[1].Valid)
	require.Equal(t, TodoCounts{}, diagnostics.Snapshots[1].Counts)
	require.Equal(t, TodoCounts{InProgress: 1, Unknown: 1}, diagnostics.Snapshots[2].Counts)
	require.False(t, diagnostics.Snapshots[3].Valid)
	require.Empty(t, diagnostics.FinalItems)

	paged, err := s.TodoDiagnosticsPage(context.Background(), "todo-session", TodoQuery{SnapshotOffset: 1, SnapshotLimit: 1, ItemOffset: 1, ItemLimit: 1})
	require.NoError(t, err)
	require.Equal(t, 4, paged.SnapshotTotal)
	require.Len(t, paged.Snapshots, 1)
	require.True(t, paged.SnapshotsTruncated)
	require.Zero(t, paged.FinalItemTotal)
	require.Empty(t, paged.FinalItems)
	require.False(t, paged.FinalItemsTruncated)
}

func TestTodoDiagnosticsKeepsOversizedValidFinalItemValid(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	data, err := json.Marshal(map[string]any{"items": []map[string]any{{"id": 1, "text": strings.Repeat("😀", 10000), "status": "todo"}}})
	require.NoError(t, err)
	require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{ID: "large-valid", CustomStates: []ingest.CustomState{{ID: "todo", Type: "todo-state", SourceLine: 2, Data: string(data)}}}, SourceMeta{Path: "large-valid", Size: 1}))
	diagnostics, err := s.TodoDiagnostics(context.Background(), "large-valid")
	require.NoError(t, err)
	require.Equal(t, "valid", diagnostics.FinalState)
	require.True(t, diagnostics.Snapshots[0].ContentTruncated)
	require.Len(t, diagnostics.FinalItems, 1)
	require.True(t, diagnostics.FinalItems[0].ContentTruncated)
}

func TestTodoDiagnosticsPagesOversizedFinalItemThroughSerializedReadBoundary(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sessions.db")
	s, err := Open(path)
	require.NoError(t, err)
	data, err := json.Marshal(map[string]any{"items": []map[string]any{{"id": 1, "text": strings.Repeat("x", todoSnapshotBytes+1), "status": "todo"}}})
	require.NoError(t, err)
	require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{ID: "serialized", CustomStates: []ingest.CustomState{{ID: "todo", Type: "todo-state", SourceLine: 2, Data: string(data)}}}, SourceMeta{Path: "serialized", Size: 1}))
	require.NoError(t, s.Close())
	boundary, err := robound.Open(context.Background(), path)
	require.NoError(t, err)
	reader := NewReader(
		func(ctx context.Context, query string, args ...any) (Rows, error) {
			return boundary.QueryContext(ctx, query, args...)
		},
		func(ctx context.Context, query string, args ...any) Row {
			return boundary.QueryRowContext(ctx, query, args...)
		},
	)
	done := make(chan error, 1)
	go func() {
		_, queryErr := reader.TodoDiagnostics(context.Background(), "serialized")
		done <- queryErr
	}()
	select {
	case err = <-done:
		require.NoError(t, err)
		require.NoError(t, boundary.Close())
	case <-time.After(time.Second):
		t.Fatal("todo diagnostics deadlocked on the serialized read boundary")
	}
}

func TestTodoDiagnosticsBoundsEscapedFinalItemsWithoutLosingPagination(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	items := make([]map[string]any, 50)
	for i := range items {
		items[i] = map[string]any{"id": i + 1, "text": strings.Repeat("\x01", 2000), "status": "todo", "notes": strings.Repeat("\x02", 2000)}
	}
	data, err := json.Marshal(map[string]any{"items": items})
	require.NoError(t, err)
	require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{ID: "bounded", CustomStates: []ingest.CustomState{{ID: "t", Type: "todo-state", SourceLine: 2, Data: string(data)}}}, SourceMeta{Path: "bounded.jsonl", Size: 1, ModTimeNS: 1}))
	diagnostics, err := s.TodoDiagnosticsPage(context.Background(), "bounded", TodoQuery{ItemLimit: 50})
	require.NoError(t, err)
	require.Len(t, diagnostics.FinalItems, 50)
	for i, item := range diagnostics.FinalItems {
		require.True(t, item.ContentTruncated, fmt.Sprintf("item %d", i))
	}
	encoded, err := json.Marshal(diagnostics)
	require.NoError(t, err)
	require.Less(t, len(encoded), 50_000)
}

func TestTodoDiagnosticsDistinguishesAbsentAndMalformedShape(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{ID: "absent"}, SourceMeta{Path: "absent.jsonl", Size: 1, ModTimeNS: 1}))
	absent, err := s.TodoDiagnostics(context.Background(), "absent")
	require.NoError(t, err)
	require.Equal(t, "absent", absent.FinalState)
	require.Empty(t, absent.FinalItems)

	require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{ID: "shape", CustomStates: []ingest.CustomState{{ID: "x", Type: "todo-state", SourceLine: 2, Data: `{"items":null}`}}}, SourceMeta{Path: "shape.jsonl", Size: 1, ModTimeNS: 1}))
	malformed, err := s.TodoDiagnostics(context.Background(), "shape")
	require.NoError(t, err)
	require.Equal(t, "malformed", malformed.FinalState)
	require.Equal(t, 1, malformed.MalformedSnapshots)
}
