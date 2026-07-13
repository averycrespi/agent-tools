package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/ingest"
	"github.com/stretchr/testify/require"
)

func TestSessionSkillInvocationsAreSourceOrderedAndFiltered(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	ctx := context.Background()
	require.NoError(t, s.ReplaceSession(ctx, ingest.Session{ID: "session-1", ToolCalls: []ingest.ToolCall{
		{ID: "c3", Name: "read", Arguments: `{"path":"/skills/review/SKILL.md"}`, SourceLine: 9},
		{ID: "c1", Name: "read", Arguments: `{"path":"/skills/tdd/SKILL.md"}`, SourceLine: 2},
		{ID: "c2", Name: "read", Arguments: `{"path":"/repo/main.go"}`, SourceLine: 5},
		{ID: "c4", Name: "edit", Arguments: `{"path":"/skills/tdd/SKILL.md"}`, SourceLine: 11},
	}}, SourceMeta{Path: "a.jsonl", Size: 1, ModTimeNS: 1}))

	report, err := s.SessionSkillInvocations(ctx, "session-1")
	require.NoError(t, err)
	require.False(t, report.Truncated)
	require.Equal(t, []SessionSkillInvocation{
		{CallID: "c1", Skill: "tdd", Target: "/skills/tdd/SKILL.md", SourceLine: 2},
		{CallID: "c3", Skill: "review", Target: "/skills/review/SKILL.md", SourceLine: 9},
	}, report.Invocations)

	_, err = s.SessionSkillInvocations(ctx, "missing")
	require.ErrorIs(t, err, ErrSessionNotFound)
}
