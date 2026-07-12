package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/ingest"
	"github.com/stretchr/testify/require"
)

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
	require.Len(t, diagnostics.FinalItems, 2)
	require.Equal(t, "1", diagnostics.FinalItems[0].ID)
	require.Equal(t, "future", diagnostics.FinalItems[1].ID)
	require.Equal(t, "unknown", diagnostics.FinalItems[1].Status)
	require.Equal(t, "paused", diagnostics.FinalItems[1].RawStatus)

	paged, err := s.TodoDiagnosticsPage(context.Background(), "todo-session", TodoQuery{SnapshotOffset: 1, SnapshotLimit: 1, ItemOffset: 1, ItemLimit: 1})
	require.NoError(t, err)
	require.Equal(t, 4, paged.SnapshotTotal)
	require.Len(t, paged.Snapshots, 1)
	require.True(t, paged.SnapshotsTruncated)
	require.Equal(t, 2, paged.FinalItemTotal)
	require.Len(t, paged.FinalItems, 1)
	require.False(t, paged.FinalItemsTruncated)
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
