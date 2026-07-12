package store

import (
	"context"
	"fmt"
)

type GoalSnapshot struct {
	ID               string `json:"id"`
	SourceLine       int    `json:"source_line"`
	State            string `json:"state"`
	ContentTruncated bool   `json:"content_truncated,omitempty"`
}

type GoalDiagnostics struct {
	FinalState string         `json:"final_state"`
	Total      int            `json:"total"`
	Offset     int            `json:"offset"`
	Truncated  bool           `json:"truncated"`
	Snapshots  []GoalSnapshot `json:"snapshots"`
}

func (s *Reader) GoalDiagnosticsPage(ctx context.Context, prefix string, offset, limit int) (GoalDiagnostics, error) {
	id, err := s.ResolveSession(ctx, prefix)
	if err != nil {
		return GoalDiagnostics{}, err
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	out := GoalDiagnostics{FinalState: "absent", Offset: offset, Snapshots: []GoalSnapshot{}}
	if err = s.query.QueryRowContext(ctx, `SELECT COUNT(*) FROM custom_state WHERE session_id=? AND type='goal-state'`, id).Scan(&out.Total); err != nil {
		return GoalDiagnostics{}, fmt.Errorf("count goal snapshots: %w", err)
	}
	rows, err := s.query.QueryContext(ctx, `
SELECT id,source_line,COALESCE(substr(CAST(status AS BLOB),1,64),X''),length(CAST(status AS BLOB))>64
FROM custom_state WHERE session_id=? AND type='goal-state' ORDER BY source_line,id LIMIT ? OFFSET ?`, id, limit, offset)
	if err != nil {
		return GoalDiagnostics{}, fmt.Errorf("query goal snapshots: %w", err)
	}
	for rows.Next() {
		var snapshot GoalSnapshot
		if err = rows.Scan(&snapshot.ID, &snapshot.SourceLine, &snapshot.State, &snapshot.ContentTruncated); err != nil {
			_ = rows.Close()
			return GoalDiagnostics{}, fmt.Errorf("scan goal snapshot: %w", err)
		}
		if snapshot.State == "" {
			snapshot.State = "cleared"
		}
		out.Snapshots = append(out.Snapshots, snapshot)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return GoalDiagnostics{}, fmt.Errorf("read goal snapshots: %w", err)
	}
	if err = rows.Close(); err != nil {
		return GoalDiagnostics{}, fmt.Errorf("close goal snapshots: %w", err)
	}
	out.Truncated = offset+len(out.Snapshots) < out.Total
	if out.Total > 0 {
		var truncated bool
		if err = s.query.QueryRowContext(ctx, `SELECT COALESCE(substr(CAST(status AS BLOB),1,64),X''),length(CAST(status AS BLOB))>64 FROM custom_state WHERE session_id=? AND type='goal-state' ORDER BY source_line DESC,id DESC LIMIT 1`, id).Scan(&out.FinalState, &truncated); err != nil {
			return GoalDiagnostics{}, fmt.Errorf("query final goal state: %w", err)
		}
		if out.FinalState == "" {
			out.FinalState = "cleared"
		}
		if truncated {
			out.FinalState = "truncated"
		}
	}
	return out, nil
}
