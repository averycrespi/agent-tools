package store

import (
	"context"
	"database/sql"
	"fmt"
)

type SessionHeaderView struct {
	ID               string  `json:"id"`
	Timestamp        string  `json:"timestamp"`
	StartedAtUnix    *int64  `json:"started_at_unix"`
	CWD              string  `json:"cwd"`
	Records          int     `json:"records"`
	Turns            int     `json:"turns"`
	MalformedRecords int     `json:"malformed_records"`
	UnknownRecords   int     `json:"unknown_records"`
	SchemaDrift      int     `json:"schema_drift"`
	Cost             float64 `json:"cost_as_logged"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	ReasoningTokens  int64   `json:"reasoning_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	Compactions      int     `json:"compactions"`
	BrokerGuards     int     `json:"broker_guards"`
	StopReason       string  `json:"stop_reason"`
	GoalOutcome      string  `json:"goal_outcome"`
	ContentTruncated bool    `json:"content_truncated"`
}

func (s *Reader) SessionHeader(ctx context.Context, prefix string) (SessionHeaderView, error) {
	id, err := s.ResolveSession(ctx, prefix)
	if err != nil {
		return SessionHeaderView{}, err
	}
	var header SessionHeaderView
	var started sql.NullInt64
	var timestampTruncated, cwdTruncated, stopTruncated, goalTruncated bool
	var goal string
	err = s.query.QueryRowContext(ctx, `
WITH message_facts AS (
 SELECT COUNT(*) turns,COALESCE(SUM(cost),0) cost,COALESCE(SUM(input_tokens),0) input_tokens,
  COALESCE(SUM(output_tokens),0) output_tokens,COALESCE(SUM(reasoning_tokens),0) reasoning_tokens,
  COALESCE(SUM(cache_read_tokens),0) cache_read_tokens,COALESCE(SUM(cache_write_tokens),0) cache_write_tokens
 FROM messages WHERE session_id=?
)
SELECT s.id,COALESCE(substr(CAST(s.timestamp AS BLOB),1,128),X''),length(CAST(s.timestamp AS BLOB))>128,s.started_at_unix,
 COALESCE(substr(CAST(s.cwd AS BLOB),1,128),X''),length(CAST(s.cwd AS BLOB))>128,s.total_records,s.malformed_records,s.unknown_records,s.schema_drift,
 m.turns,m.cost,m.input_tokens,m.output_tokens,m.reasoning_tokens,m.cache_read_tokens,m.cache_write_tokens,
 (SELECT COUNT(*) FROM events e WHERE e.session_id=s.id AND e.type='compaction'),
 (SELECT COUNT(*) FROM custom_messages g WHERE g.session_id=s.id AND g.type='broker-guard'),
 COALESCE(substr(CAST((SELECT stop_reason FROM messages x WHERE x.session_id=s.id AND x.role='assistant' AND x.stop_reason<>'' ORDER BY source_line DESC,id DESC LIMIT 1) AS BLOB),1,64),X''),
 length(CAST(COALESCE((SELECT stop_reason FROM messages x WHERE x.session_id=s.id AND x.role='assistant' AND x.stop_reason<>'' ORDER BY source_line DESC,id DESC LIMIT 1),'') AS BLOB))>64,
 CASE WHEN EXISTS(SELECT 1 FROM custom_state x WHERE x.session_id=s.id AND x.type='goal-state')
  THEN COALESCE(substr(CAST((SELECT status FROM custom_state x WHERE x.session_id=s.id AND x.type='goal-state' ORDER BY source_line DESC,id DESC LIMIT 1) AS BLOB),1,64),X'') ELSE '__absent__' END,
 CASE WHEN EXISTS(SELECT 1 FROM custom_state x WHERE x.session_id=s.id AND x.type='goal-state')
  THEN length(CAST((SELECT status FROM custom_state x WHERE x.session_id=s.id AND x.type='goal-state' ORDER BY source_line DESC,id DESC LIMIT 1) AS BLOB))>64 ELSE 0 END
FROM sessions s CROSS JOIN message_facts m WHERE s.id=?`, id, id).Scan(
		&header.ID, &header.Timestamp, &timestampTruncated, &started, &header.CWD, &cwdTruncated, &header.Records,
		&header.MalformedRecords, &header.UnknownRecords, &header.SchemaDrift, &header.Turns, &header.Cost, &header.InputTokens,
		&header.OutputTokens, &header.ReasoningTokens, &header.CacheReadTokens, &header.CacheWriteTokens, &header.Compactions,
		&header.BrokerGuards, &header.StopReason, &stopTruncated, &goal, &goalTruncated,
	)
	if err != nil {
		return SessionHeaderView{}, fmt.Errorf("query session header: %w", err)
	}
	if started.Valid {
		header.StartedAtUnix = &started.Int64
	}
	header.GoalOutcome = goalOutcome(goal)
	if goalTruncated {
		header.GoalOutcome = "truncated"
	}
	header.ContentTruncated = timestampTruncated || cwdTruncated || stopTruncated || goalTruncated
	return header, nil
}
