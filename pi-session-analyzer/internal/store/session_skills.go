package store

import (
	"context"
	"fmt"
)

const maxSessionSkillInvocations = 100

// SessionSkillInvocation is one source-ordered SKILL.md read in a session.
type SessionSkillInvocation struct {
	CallID     string `json:"call_id"`
	Skill      string `json:"skill"`
	Target     string `json:"target"`
	SourceLine int    `json:"source_line"`
}

// SessionSkillReport lists a session's skill invocations in source order.
type SessionSkillReport struct {
	Invocations []SessionSkillInvocation `json:"invocations"`
	Truncated   bool                     `json:"truncated"`
}

func (s *Reader) SessionSkillInvocations(ctx context.Context, prefix string) (SessionSkillReport, error) {
	id, err := s.ResolveSession(ctx, prefix)
	if err != nil {
		return SessionSkillReport{}, err
	}
	out := SessionSkillReport{Invocations: []SessionSkillInvocation{}}
	rows, err := s.query.QueryContext(ctx, `
SELECT id,normalized_target,source_line FROM tool_calls
WHERE session_id=? AND name='read' AND normalized_target LIKE '%/' || '`+SkillFileBasename+`'
ORDER BY source_line,id LIMIT `+fmt.Sprint(maxSessionSkillInvocations+1), id)
	if err != nil {
		return SessionSkillReport{}, fmt.Errorf("query session skill invocations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var invocation SessionSkillInvocation
		if err = rows.Scan(&invocation.CallID, &invocation.Target, &invocation.SourceLine); err != nil {
			return SessionSkillReport{}, fmt.Errorf("scan session skill invocation: %w", err)
		}
		invocation.Skill = SkillNameFromTarget(invocation.Target)
		if invocation.Skill == "" {
			invocation.Skill = invocation.Target
		}
		out.Invocations = append(out.Invocations, invocation)
	}
	if err = rows.Err(); err != nil {
		return SessionSkillReport{}, fmt.Errorf("read session skill invocations: %w", err)
	}
	if len(out.Invocations) > maxSessionSkillInvocations {
		out.Invocations = out.Invocations[:maxSessionSkillInvocations]
		out.Truncated = true
	}
	return out, nil
}
