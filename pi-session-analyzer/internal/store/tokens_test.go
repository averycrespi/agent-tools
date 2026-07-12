package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/ingest"
	"github.com/stretchr/testify/require"
)

func TestMessageTokenSequencePaginatesMessagesAndCompactionsWithoutCombiningTokens(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{ID: "tokens", Messages: []ingest.Message{
		{ID: "m1", SourceLine: 2, Role: "assistant", Model: "model", InputTokens: 11, OutputTokens: 2, ReasoningTokens: 3, CacheReadTokens: 5, CacheWriteTokens: 7, Cost: 0.25},
		{ID: "m1b", SourceLine: 2, Role: "assistant", OutputTokens: 17},
		{ID: "m2", SourceLine: 3, Role: "user", InputTokens: 13},
	}, Events: []ingest.Event{{ID: "compact", SourceLine: 2, Type: "compaction", TokensBefore: 99}}}, SourceMeta{Path: "tokens", Size: 1}))
	first, err := s.MessageTokenSequence(context.Background(), "tokens", "", 1)
	require.NoError(t, err)
	require.Equal(t, "message", first.Entries[0].Kind)
	require.Equal(t, int64(11), first.Entries[0].InputTokens)
	require.Equal(t, int64(2), first.Entries[0].OutputTokens)
	require.Equal(t, int64(3), first.Entries[0].ReasoningTokens)
	require.Equal(t, int64(5), first.Entries[0].CacheReadTokens)
	require.Equal(t, int64(7), first.Entries[0].CacheWriteTokens)
	require.NotEmpty(t, first.NextCursor)
	second, err := s.MessageTokenSequence(context.Background(), "tokens", first.NextCursor, 1)
	require.NoError(t, err)
	require.Equal(t, "m1b", second.Entries[0].ID)
	third, err := s.MessageTokenSequence(context.Background(), "tokens", second.NextCursor, 1)
	require.NoError(t, err)
	require.Equal(t, "compaction", third.Entries[0].Kind)
	require.Equal(t, int64(99), third.Entries[0].TokensBefore)
	fourth, err := s.MessageTokenSequence(context.Background(), "tokens", third.NextCursor, 1)
	require.NoError(t, err)
	require.Equal(t, "m2", fourth.Entries[0].ID)
	require.Empty(t, fourth.NextCursor)

	require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{ID: "other"}, SourceMeta{Path: "other", Size: 1}))
	_, err = s.MessageTokenSequence(context.Background(), "other", first.NextCursor, 1)
	require.ErrorIs(t, err, ErrInvalidStreamCursor)
}
