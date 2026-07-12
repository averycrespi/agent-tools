package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/ingest"
)

type ConversationEntry struct {
	Kind, ID, Role, Name, StopReason, Text string
	SourceLine                             int
	IsError                                *bool
}

func (s *Reader) Conversation(ctx context.Context, prefix, anchor string, limit int) ([]ConversationEntry, error) {
	id, err := s.ResolveSession(ctx, prefix)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	anchorLine := 0
	if anchor != "" {
		if err = s.query.QueryRowContext(ctx, `SELECT source_line FROM messages WHERE session_id=? AND id=?`, id, anchor).Scan(&anchorLine); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("message %q not found in session %s", anchor, id)
			}
			return nil, err
		}
	}
	rows, err := s.query.QueryContext(ctx, `
SELECT kind,id,role,name,stop_reason,text,source_line,is_error FROM (
 SELECT 'message' AS kind,id,role,'' AS name,stop_reason,text,source_line,NULL AS is_error FROM messages WHERE session_id=? AND source_line>=?
 UNION ALL
 SELECT 'tool_call',id,'',name,'',arguments,source_line,NULL FROM tool_calls WHERE session_id=? AND source_line>=?
 UNION ALL
 SELECT 'tool_result',id,'',name,'',content,source_line,is_error FROM tool_results WHERE session_id=? AND source_line>=?
) ORDER BY source_line,kind LIMIT ?`, id, anchorLine, id, anchorLine, id, anchorLine, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	entries := []ConversationEntry{}
	for rows.Next() {
		var entry ConversationEntry
		var isError sql.NullBool
		if err = rows.Scan(&entry.Kind, &entry.ID, &entry.Role, &entry.Name, &entry.StopReason, &entry.Text, &entry.SourceLine, &isError); err != nil {
			return nil, err
		}
		if isError.Valid {
			entry.IsError = &isError.Bool
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *Reader) Message(ctx context.Context, sessionPrefix, messageID string) (map[string]any, error) {
	id, err := s.ResolveSession(ctx, sessionPrefix)
	if err != nil {
		return nil, err
	}
	var message ingest.Message
	err = s.query.QueryRowContext(ctx, `SELECT id,parent_id,source_line,timestamp,role,model,stop_reason,text,input_tokens,output_tokens,reasoning_tokens,cache_read_tokens,cache_write_tokens,cost FROM messages WHERE session_id=? AND id=?`, id, messageID).Scan(
		&message.ID, &message.ParentID, &message.SourceLine, &message.Timestamp, &message.Role, &message.Model, &message.StopReason, &message.Text,
		&message.InputTokens, &message.OutputTokens, &message.ReasoningTokens, &message.CacheReadTokens, &message.CacheWriteTokens, &message.Cost,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("message %q not found in session %s", messageID, id)
		}
		return nil, err
	}
	calls, err := s.messageToolCalls(ctx, id, messageID)
	if err != nil {
		return nil, err
	}
	results, err := s.messageToolResults(ctx, id, messageID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"session_id": id, "message": message, "tool_calls": calls, "tool_results": results}, nil
}

func (s *Reader) messageToolCalls(ctx context.Context, sessionID, messageID string) ([]ingest.ToolCall, error) {
	rows, err := s.query.QueryContext(ctx, `SELECT id,message_id,source_line,name,arguments FROM tool_calls WHERE session_id=? AND message_id=? ORDER BY source_line,id`, sessionID, messageID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	calls := []ingest.ToolCall{}
	for rows.Next() {
		var call ingest.ToolCall
		if err = rows.Scan(&call.ID, &call.MessageID, &call.SourceLine, &call.Name, &call.Arguments); err != nil {
			return nil, err
		}
		calls = append(calls, call)
	}
	return calls, rows.Err()
}

func (s *Reader) messageToolResults(ctx context.Context, sessionID, messageID string) ([]ingest.ToolResult, error) {
	rows, err := s.query.QueryContext(ctx, `SELECT id,message_id,call_id,source_line,name,content,is_error FROM tool_results WHERE session_id=? AND message_id=? ORDER BY source_line,id`, sessionID, messageID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	results := []ingest.ToolResult{}
	for rows.Next() {
		var result ingest.ToolResult
		var isError sql.NullBool
		if err = rows.Scan(&result.ID, &result.MessageID, &result.CallID, &result.SourceLine, &result.Name, &result.Content, &isError); err != nil {
			return nil, err
		}
		if isError.Valid {
			result.IsError = &isError.Bool
		}
		results = append(results, result)
	}
	return results, rows.Err()
}

type FailureQuery struct {
	Limit                                 int
	Detector, Classification, MinSeverity string
}

func (s *Reader) TopFailures(ctx context.Context, q FailureQuery) ([]FindingRow, error) {
	if q.Limit <= 0 || q.Limit > 50 {
		q.Limit = 50
	}
	if q.MinSeverity == "" {
		q.MinSeverity = "warn"
	}
	minRank := severityRank(q.MinSeverity)
	rows, err := s.query.QueryContext(ctx, `
SELECT CAST(f.id AS TEXT),f.session_id,f.detector,f.classification,f.severity,f.summary,f.first_evidence_id,f.details,f.source_line,f.generation,f.stale,COALESCE(r.status,''),COALESCE(r.error_summary,'')
FROM findings f LEFT JOIN detector_runs r ON r.session_id=f.session_id AND r.detector=f.detector
WHERE (?='' OR f.detector=?) AND (?='' OR f.classification=?)
 AND CASE f.severity WHEN 'error' THEN 3 WHEN 'warn' THEN 2 ELSE 1 END >= ?
ORDER BY CASE f.severity WHEN 'error' THEN 3 WHEN 'warn' THEN 2 ELSE 1 END DESC,f.detector,f.session_id,f.first_evidence_id
LIMIT ?`, q.Detector, q.Detector, q.Classification, q.Classification, minRank, q.Limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []FindingRow{}
	for rows.Next() {
		var finding FindingRow
		if err = rows.Scan(&finding.ID, &finding.SessionID, &finding.Detector, &finding.Classification, &finding.Severity, &finding.Summary, &finding.EvidenceID, &finding.Details, &finding.SourceLine, &finding.Generation, &finding.Stale, &finding.RunStatus, &finding.RunError); err != nil {
			return nil, err
		}
		out = append(out, finding)
	}
	return out, rows.Err()
}

func severityRank(severity string) int {
	switch severity {
	case "error":
		return 3
	case "warn":
		return 2
	default:
		return 1
	}
}
