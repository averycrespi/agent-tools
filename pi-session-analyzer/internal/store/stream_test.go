package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/ingest"
	"github.com/stretchr/testify/require"
)

func TestSessionStreamPagesAllRecordKindsByStableSourceKey(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	flag := true
	session := ingest.Session{
		ID: "session-stream",
		Messages: []ingest.Message{
			{ID: "m", Role: "assistant", Text: "preview\ntext", SourceLine: 2},
			{ID: "r", Role: "toolResult", Text: "secret result detail", SourceLine: 3},
		},
		ToolCalls: []ingest.ToolCall{
			{ID: "c", MessageID: "m", Name: "bash", Arguments: `{"command":"secret detail"}`, SourceLine: 2},
			{ID: "d", MessageID: "m", Name: "read", Arguments: `{"path":"secret detail"}`, SourceLine: 2},
		},
		ToolResults:    []ingest.ToolResult{{ID: "r", MessageID: "r", CallID: "c", Name: "bash", Content: "secret result detail", IsError: &flag, SourceLine: 3}},
		Events:         []ingest.Event{{ID: "e", Type: "compaction", Value: "compact", SourceLine: 4, TokensBefore: 100}},
		CustomStates:   []ingest.CustomState{{ID: "s", Type: "todo-state", Status: "", Data: `{"items":[{"id":1,"text":"secret todo"}]}`, SourceLine: 5}},
		CustomMessages: []ingest.CustomMessage{{ID: "g", Type: "broker-guard", Kind: "credential", Content: "secret guard detail", SourceLine: 6}},
	}
	require.NoError(t, s.ReplaceSession(context.Background(), session, SourceMeta{Path: "s.jsonl", Size: 1, ModTimeNS: 1}))

	cursor := ""
	var entries []StreamEntry
	for {
		page, pageErr := s.SessionStream(context.Background(), "session-str", cursor, 1)
		require.NoError(t, pageErr)
		entries = append(entries, page.Entries...)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	require.Equal(t, []string{"message", "tool_call", "tool_call", "tool_result", "event", "custom_state", "custom_message"}, streamKinds(entries))
	require.Equal(t, []string{"c", "d"}, []string{entries[1].ID, entries[2].ID})
	require.Equal(t, "preview text", entries[0].Preview)
	anchored, err := s.SessionStreamFromLine(context.Background(), "session-str", "", 2, 4)
	require.NoError(t, err)
	require.Equal(t, "e", anchored.Entries[0].ID)
	for _, entry := range entries[1:] {
		if entry.Kind != "event" {
			require.Empty(t, entry.Preview)
		}
	}
}

func TestSessionStreamBoundsEscapedPreviewsWithoutLosingCursor(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	messages := make([]ingest.Message, 101)
	for i := range messages {
		messages[i] = ingest.Message{ID: fmt.Sprintf("m%03d", i), Role: "assistant", Text: strings.Repeat("\x01", 160), SourceLine: i + 1}
	}
	require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{ID: "s", Messages: messages}, SourceMeta{Path: "s.jsonl", Size: 1, ModTimeNS: 1}))
	page, err := s.SessionStream(context.Background(), "s", "", 100)
	require.NoError(t, err)
	require.Len(t, page.Entries, 100)
	require.NotEmpty(t, page.NextCursor)
	for _, entry := range page.Entries {
		require.LessOrEqual(t, len(entry.Preview), streamPreviewBytes)
		require.True(t, utf8.ValidString(entry.Preview))
	}
	encoded, err := json.Marshal(page)
	require.NoError(t, err)
	require.Less(t, len(encoded), 50_000)
}

func TestSessionStreamRejectsInvalidCursor(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{ID: "s", Messages: []ingest.Message{{ID: "a", Role: "user", SourceLine: 1}, {ID: "b", Role: "assistant", SourceLine: 2}}}, SourceMeta{Path: "s.jsonl", Size: 1, ModTimeNS: 1}))
	_, err = s.SessionStream(context.Background(), "s", "not-a-cursor", 10)
	require.ErrorContains(t, err, "cursor")

	first, err := s.SessionStream(context.Background(), "s", "", 1)
	require.NoError(t, err)
	require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{ID: "other", Messages: []ingest.Message{{ID: "m", Role: "user", SourceLine: 1}}}, SourceMeta{Path: "other.jsonl", Size: 1, ModTimeNS: 1}))
	_, err = s.SessionStream(context.Background(), "other", first.NextCursor, 10)
	require.ErrorIs(t, err, ErrInvalidStreamCursor)
}

func streamKinds(entries []StreamEntry) []string {
	kinds := make([]string, len(entries))
	for i, entry := range entries {
		kinds[i] = entry.Kind
	}
	return kinds
}
