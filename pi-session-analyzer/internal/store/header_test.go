package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/ingest"
	"github.com/stretchr/testify/require"
)

func TestSessionHeaderReturnsBoundedHonestMetrics(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	start := int64(100)
	require.NoError(t, s.ReplaceSession(context.Background(), ingest.Session{
		ID: "header", Timestamp: "raw-time", StartedAtUnix: &start, CWD: "/work", Stats: ingest.ParseStats{Total: 7, Malformed: 1, Unknown: 2, SchemaDrift: 3},
		Messages: []ingest.Message{{ID: "m1", Role: "assistant", StopReason: "tool_use", InputTokens: 11, OutputTokens: 2, ReasoningTokens: 3, CacheReadTokens: 5, CacheWriteTokens: 7, Cost: 0.25}, {ID: "m2", Role: "assistant", StopReason: "end_turn", Cost: 0.5}},
		Events:   []ingest.Event{{ID: "e", Type: "compaction"}}, CustomMessages: []ingest.CustomMessage{{ID: "g", Type: "broker-guard"}},
		CustomStates: []ingest.CustomState{{ID: "goal", Type: "goal-state", Status: "active"}},
	}, SourceMeta{Path: "header.jsonl", Size: 1}))
	header, err := s.SessionHeader(context.Background(), "head")
	require.NoError(t, err)
	require.Equal(t, "header", header.ID)
	require.Equal(t, int64(100), *header.StartedAtUnix)
	require.Equal(t, 7, header.Records)
	require.Equal(t, 2, header.Turns)
	require.InDelta(t, 0.75, header.Cost, 0.0001)
	require.Equal(t, int64(11), header.InputTokens)
	require.Equal(t, int64(2), header.OutputTokens)
	require.Equal(t, int64(3), header.ReasoningTokens)
	require.Equal(t, "end_turn", header.StopReason)
	require.Equal(t, "active", header.GoalOutcome)
	require.Equal(t, 1, header.Compactions)
	require.Equal(t, 1, header.BrokerGuards)
}
