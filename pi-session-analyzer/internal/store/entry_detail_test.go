package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/ingest"
	"github.com/stretchr/testify/require"
)

func TestSessionEntryDetailReturnsBoundedStoredTextByExactKindAndID(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	text := strings.Repeat("😀", 10000)
	require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{ID: "detail", Messages: []ingest.Message{{ID: "m", Text: text}}, ToolCalls: []ingest.ToolCall{{ID: "c", Arguments: `{"path":"safe"}`}}, ToolResults: []ingest.ToolResult{{ID: "r", Content: "result"}}}, SourceMeta{Path: "detail", Size: 1}))
	detail, err := s.SessionEntryDetail(context.Background(), "detail", "message", "m")
	require.NoError(t, err)
	require.True(t, detail.ContentTruncated)
	require.LessOrEqual(t, len(detail.Content), MaxEntryDetailBytes)
	call, err := s.SessionEntryDetail(context.Background(), "detail", "tool_call", "c")
	require.NoError(t, err)
	require.Contains(t, call.Content, "safe")
	_, err = s.SessionEntryDetail(context.Background(), "detail", "invalid", "m")
	require.ErrorIs(t, err, ErrInvalidEntryKind)
}
