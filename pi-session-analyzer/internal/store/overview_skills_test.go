package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/ingest"
	"github.com/stretchr/testify/require"
)

func TestSkillUsageSummaryCountsOnlyRangedSkillReads(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	ctx := context.Background()
	early := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC).Unix()
	late := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC).Unix()
	outside := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC).Unix()
	tdd := `{"path":"/home/u/.pi/agent/skills/tdd/SKILL.md"}`
	review := `{"path":"/home/u/.pi/agent/skills/review/SKILL.md"}`
	require.NoError(t, s.ReplaceSession(ctx, ingest.Session{ID: "a", StartedAtUnix: &early, ToolCalls: []ingest.ToolCall{
		{ID: "c1", Name: "read", Arguments: tdd, SourceLine: 1},
		{ID: "c2", Name: "read", Arguments: tdd, SourceLine: 2},
		{ID: "c3", Name: "read", Arguments: review, SourceLine: 3},
		{ID: "c4", Name: "edit", Arguments: tdd, SourceLine: 4},
		{ID: "c5", Name: "read", Arguments: `{"path":"/repo/README.md"}`, SourceLine: 5},
	}}, SourceMeta{Path: "a.jsonl", Size: 1, ModTimeNS: 1}))
	require.NoError(t, s.ReplaceSession(ctx, ingest.Session{ID: "b", StartedAtUnix: &late, ToolCalls: []ingest.ToolCall{
		{ID: "c1", Name: "read", Arguments: tdd, SourceLine: 1},
	}}, SourceMeta{Path: "b.jsonl", Size: 1, ModTimeNS: 1}))
	require.NoError(t, s.ReplaceSession(ctx, ingest.Session{ID: "c", StartedAtUnix: &late, ToolCalls: []ingest.ToolCall{
		{ID: "c1", Name: "bash", Arguments: `{"command":"ls"}`, SourceLine: 1},
	}}, SourceMeta{Path: "c.jsonl", Size: 1, ModTimeNS: 1}))
	require.NoError(t, s.ReplaceSession(ctx, ingest.Session{ID: "d", StartedAtUnix: &outside, ToolCalls: []ingest.ToolCall{
		{ID: "c1", Name: "read", Arguments: tdd, SourceLine: 1},
	}}, SourceMeta{Path: "d.jsonl", Size: 1, ModTimeNS: 1}))

	from := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC).Unix()
	to := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC).Unix()
	usage, err := s.SkillUsageSummary(ctx, from, to)
	require.NoError(t, err)
	require.Equal(t, 2, usage.DistinctSkills)
	require.Equal(t, 4, usage.Invocations)
	require.Equal(t, 2, usage.SessionsWithSkills)
	require.False(t, usage.Truncated)
	require.Len(t, usage.Rows, 2)
	require.Equal(t, "tdd", usage.Rows[0].Skill)
	require.Equal(t, "/home/u/.pi/agent/skills/tdd/SKILL.md", usage.Rows[0].Target)
	require.Equal(t, 3, usage.Rows[0].Invocations)
	require.Equal(t, 2, usage.Rows[0].Sessions)
	require.Equal(t, late, usage.Rows[0].LastUsedUnix)
	require.Equal(t, "review", usage.Rows[1].Skill)
	require.Equal(t, 1, usage.Rows[1].Invocations)
	require.Equal(t, early, usage.Rows[1].LastUsedUnix)
}

func TestSkillUsageSummaryBoundsRowCount(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	ctx := context.Background()
	started := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC).Unix()
	calls := make([]ingest.ToolCall, 0, maxSkillUsageRows+5)
	for i := range maxSkillUsageRows + 5 {
		calls = append(calls, ingest.ToolCall{
			ID: fmt.Sprintf("c%02d", i), Name: "read", SourceLine: i + 1,
			Arguments: fmt.Sprintf(`{"path":"/skills/skill-%02d/SKILL.md"}`, i),
		})
	}
	require.NoError(t, s.ReplaceSession(ctx, ingest.Session{ID: "a", StartedAtUnix: &started, ToolCalls: calls}, SourceMeta{Path: "a.jsonl", Size: 1, ModTimeNS: 1}))

	usage, err := s.SkillUsageSummary(ctx, started-1, started+1)
	require.NoError(t, err)
	require.Equal(t, maxSkillUsageRows+5, usage.DistinctSkills)
	require.True(t, usage.Truncated)
	require.Len(t, usage.Rows, maxSkillUsageRows)
}

func TestOverviewIncludesSkillUsageAndBucketSeries(t *testing.T) {
	t.Parallel()

	s, err := Open(filepath.Join(t.TempDir(), "sessions.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	ctx := context.Background()
	day1 := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC).Unix()
	day2 := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC).Unix()
	skill := `{"path":"/skills/tdd/SKILL.md"}`
	require.NoError(t, s.ReplaceSession(ctx, ingest.Session{ID: "a", StartedAtUnix: &day1, ToolCalls: []ingest.ToolCall{
		{ID: "c1", Name: "read", Arguments: skill, SourceLine: 1},
		{ID: "c2", Name: "read", Arguments: skill, SourceLine: 2},
	}}, SourceMeta{Path: "a.jsonl", Size: 1, ModTimeNS: 1}))
	require.NoError(t, s.ReplaceSession(ctx, ingest.Session{ID: "b", StartedAtUnix: &day2, ToolCalls: []ingest.ToolCall{
		{ID: "c1", Name: "read", Arguments: `{"path":"/repo/main.go"}`, SourceLine: 1},
	}}, SourceMeta{Path: "b.jsonl", Size: 1, ModTimeNS: 1}))

	location := time.UTC
	from := time.Date(2026, 7, 1, 0, 0, 0, 0, location)
	buckets, err := CalendarBuckets(from, from.AddDate(0, 0, 2), from, location, BucketDay)
	require.NoError(t, err)
	overview, err := s.Overview(ctx, OverviewQuery{Timezone: "UTC", Unit: BucketDay, Buckets: buckets})
	require.NoError(t, err)
	require.Equal(t, []int{2, 0}, overview.BucketSignals.SkillInvocations)
	require.Equal(t, 2, overview.Buckets[0].SkillInvocations)
	require.Equal(t, 1, overview.Skills.DistinctSkills)
	require.Equal(t, 2, overview.Skills.Invocations)
	require.Equal(t, 1, overview.Skills.SessionsWithSkills)
}
