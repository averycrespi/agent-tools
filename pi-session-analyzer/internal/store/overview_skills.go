package store

import (
	"context"
	"fmt"
)

const maxSkillUsageRows = 20

// SkillUsageRow aggregates one SKILL.md target across sessions in range.
type SkillUsageRow struct {
	Skill        string `json:"skill"`
	Target       string `json:"target"`
	Invocations  int    `json:"invocations"`
	Sessions     int    `json:"sessions"`
	LastUsedUnix int64  `json:"last_used_unix"`
}

// SkillUsage summarizes skill invocations (read calls on SKILL.md targets)
// for sessions started in a half-open Unix range.
type SkillUsage struct {
	DistinctSkills     int             `json:"distinct_skills"`
	Invocations        int             `json:"invocations"`
	SessionsWithSkills int             `json:"sessions_with_skills"`
	Rows               []SkillUsageRow `json:"rows"`
	Truncated          bool            `json:"truncated"`
	ContentTruncated   bool            `json:"content_truncated"`
}

const skillCallsCTE = `
WITH candidates AS (SELECT id,started_at_unix FROM sessions WHERE started_at_unix>=? AND started_at_unix<?),
skill_calls AS (
 SELECT tc.session_id,tc.normalized_target target,c.started_at_unix
 FROM tool_calls tc JOIN candidates c ON c.id=tc.session_id
 WHERE tc.name='read' AND tc.normalized_target LIKE '%/' || '` + SkillFileBasename + `'
)`

func (s *Reader) SkillUsageSummary(ctx context.Context, fromUnix, toUnix int64) (SkillUsage, error) {
	if fromUnix >= toUnix {
		return SkillUsage{}, fmt.Errorf("range start must be before range end")
	}
	out := SkillUsage{Rows: []SkillUsageRow{}}
	err := s.query.QueryRowContext(ctx, skillCallsCTE+`
SELECT COUNT(DISTINCT target),COUNT(*),COUNT(DISTINCT session_id) FROM skill_calls`, fromUnix, toUnix).
		Scan(&out.DistinctSkills, &out.Invocations, &out.SessionsWithSkills)
	if err != nil {
		return SkillUsage{}, fmt.Errorf("query skill usage totals: %w", err)
	}
	rows, err := s.query.QueryContext(ctx, skillCallsCTE+`
SELECT target,COUNT(*),COUNT(DISTINCT session_id),MAX(started_at_unix)
FROM skill_calls GROUP BY target ORDER BY COUNT(*) DESC,target LIMIT `+fmt.Sprint(maxSkillUsageRows+1), fromUnix, toUnix)
	if err != nil {
		return SkillUsage{}, fmt.Errorf("query skill usage rows: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var row SkillUsageRow
		if err = rows.Scan(&row.Target, &row.Invocations, &row.Sessions, &row.LastUsedUnix); err != nil {
			return SkillUsage{}, fmt.Errorf("scan skill usage row: %w", err)
		}
		row.Skill = SkillNameFromTarget(row.Target)
		if row.Skill == "" {
			row.Skill = row.Target
		}
		if len(row.Skill) > 64 {
			row.Skill = row.Skill[:64]
			out.ContentTruncated = true
		}
		out.Rows = append(out.Rows, row)
	}
	if err = rows.Err(); err != nil {
		return SkillUsage{}, fmt.Errorf("read skill usage rows: %w", err)
	}
	if len(out.Rows) > maxSkillUsageRows {
		out.Rows = out.Rows[:maxSkillUsageRows]
		out.Truncated = true
	}
	return out, nil
}
