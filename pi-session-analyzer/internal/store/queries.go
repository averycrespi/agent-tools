package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/ingest"
	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/scrub"
)

// FindingRow is a persisted finding plus detector freshness state.
type FindingRow struct {
	ID, SessionID, Detector, Classification, Severity string
	Summary, EvidenceID, Details, RunStatus, RunError string
	SourceLine, Generation                            int
	Stale                                             bool
}

// DetectorFinding is the persistence shape accepted from detector orchestration.
type DetectorFinding struct {
	Detector, Classification, Severity string
	Summary, EvidenceID, Details       string
	SourceLine                         int
}

// LoadSession reconstructs normalized detector input without requiring the source file.
func (s *Store) LoadSession(ctx context.Context, id string) (ingest.Session, error) {
	var out ingest.Session
	err := s.db.QueryRowContext(ctx, `SELECT id,schema_version,timestamp,cwd,total_records,malformed_records,unknown_records,schema_drift FROM sessions WHERE id=?`, id).Scan(&out.ID, &out.Version, &out.Timestamp, &out.CWD, &out.Stats.Total, &out.Stats.Malformed, &out.Stats.Unknown, &out.Stats.SchemaDrift)
	if err != nil {
		return out, fmt.Errorf("load session: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,parent_id,source_line,timestamp,role,model,stop_reason,text,input_tokens,output_tokens,reasoning_tokens,cache_read_tokens,cache_write_tokens,cost FROM messages WHERE session_id=? ORDER BY source_line,id`, id)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var v ingest.Message
		if err = rows.Scan(&v.ID, &v.ParentID, &v.SourceLine, &v.Timestamp, &v.Role, &v.Model, &v.StopReason, &v.Text, &v.InputTokens, &v.OutputTokens, &v.ReasoningTokens, &v.CacheReadTokens, &v.CacheWriteTokens, &v.Cost); err != nil {
			_ = rows.Close()
			return out, err
		}
		out.Messages = append(out.Messages, v)
	}
	if err = finishRows(rows); err != nil {
		return out, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT id,message_id,source_line,name,arguments FROM tool_calls WHERE session_id=? ORDER BY source_line,id`, id)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var v ingest.ToolCall
		if err = rows.Scan(&v.ID, &v.MessageID, &v.SourceLine, &v.Name, &v.Arguments); err != nil {
			_ = rows.Close()
			return out, err
		}
		out.ToolCalls = append(out.ToolCalls, v)
	}
	if err = finishRows(rows); err != nil {
		return out, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT id,message_id,call_id,source_line,name,content,is_error FROM tool_results WHERE session_id=? ORDER BY source_line,id`, id)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var v ingest.ToolResult
		var flag sql.NullBool
		if err = rows.Scan(&v.ID, &v.MessageID, &v.CallID, &v.SourceLine, &v.Name, &v.Content, &flag); err != nil {
			_ = rows.Close()
			return out, err
		}
		if flag.Valid {
			v.IsError = &flag.Bool
		}
		out.ToolResults = append(out.ToolResults, v)
	}
	if err = finishRows(rows); err != nil {
		return out, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT id,source_line,type,value,details,tokens_before FROM events WHERE session_id=? ORDER BY source_line,id`, id)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var v ingest.Event
		if err = rows.Scan(&v.ID, &v.SourceLine, &v.Type, &v.Value, &v.Details, &v.TokensBefore); err != nil {
			_ = rows.Close()
			return out, err
		}
		out.Events = append(out.Events, v)
	}
	if err = finishRows(rows); err != nil {
		return out, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT id,source_line,type,status,data FROM custom_state WHERE session_id=? ORDER BY source_line,id`, id)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var v ingest.CustomState
		if err = rows.Scan(&v.ID, &v.SourceLine, &v.Type, &v.Status, &v.Data); err != nil {
			_ = rows.Close()
			return out, err
		}
		out.CustomStates = append(out.CustomStates, v)
	}
	if err = finishRows(rows); err != nil {
		return out, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT id,source_line,type,kind,content,details FROM custom_messages WHERE session_id=? ORDER BY source_line,id`, id)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var v ingest.CustomMessage
		if err = rows.Scan(&v.ID, &v.SourceLine, &v.Type, &v.Kind, &v.Content, &v.Details); err != nil {
			_ = rows.Close()
			return out, err
		}
		out.CustomMessages = append(out.CustomMessages, v)
	}
	if err = finishRows(rows); err != nil {
		return out, err
	}
	return out, nil
}

func finishRows(rows *sql.Rows) error {
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	return rows.Close()
}

// SessionIDs returns all IDs in stable order.
func (s *Store) SessionIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM sessions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SaveDetectorSuccess atomically replaces only one detector's findings.
func (s *Store) SaveDetectorSuccess(ctx context.Context, sessionID, detector string, findings []DetectorFinding) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	generation, err := nextGeneration(ctx, tx, sessionID, detector)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM findings WHERE session_id=? AND detector=?`, sessionID, detector); err != nil {
		return err
	}
	for _, f := range findings {
		_, err = tx.ExecContext(ctx, `INSERT INTO findings(session_id,detector,classification,severity,summary,first_evidence_id,source_line,details,generation,stale) VALUES(?,?,?,?,?,?,?,?,?,0)`, sessionID, detector, f.Classification, f.Severity, scrub.Scrub(f.Summary), scrub.Scrub(f.EvidenceID), f.SourceLine, scrub.JSON(f.Details), generation)
		if err != nil {
			return fmt.Errorf("insert finding: %w", err)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO detector_runs VALUES(?,?,?,?,?,?,?) ON CONFLICT(session_id,detector) DO UPDATE SET generation=excluded.generation,status='success',error_summary='',started_at=excluded.started_at,completed_at=excluded.completed_at`, sessionID, detector, generation, "success", "", now, now)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return s.repairPermissions()
}

// SaveDetectorFailure retains prior findings and marks them stale.
func (s *Store) SaveDetectorFailure(ctx context.Context, sessionID, detector string, runErr error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	generation, err := nextGeneration(ctx, tx, sessionID, detector)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE findings SET stale=1 WHERE session_id=? AND detector=?`, sessionID, detector); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	summary := scrub.Scrub(runErr.Error())
	_, err = tx.ExecContext(ctx, `INSERT INTO detector_runs VALUES(?,?,?,?,?,?,?) ON CONFLICT(session_id,detector) DO UPDATE SET generation=excluded.generation,status='failed',error_summary=excluded.error_summary,started_at=excluded.started_at,completed_at=excluded.completed_at`, sessionID, detector, generation, "failed", summary, now, now)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return s.repairPermissions()
}

func nextGeneration(ctx context.Context, tx *sql.Tx, sessionID, detector string) (int, error) {
	var generation int
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(generation),0)+1 FROM detector_runs WHERE session_id=? AND detector=?`, sessionID, detector).Scan(&generation)
	return generation, err
}

func (s *Store) Findings(ctx context.Context, sessionID string) ([]FindingRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT CAST(f.id AS TEXT),f.session_id,f.detector,f.classification,f.severity,f.summary,f.first_evidence_id,f.details,f.source_line,f.generation,f.stale,COALESCE(r.status,''),COALESCE(r.error_summary,'') FROM findings f LEFT JOIN detector_runs r ON r.session_id=f.session_id AND r.detector=f.detector WHERE (?='' OR f.session_id=?) ORDER BY f.detector,f.session_id,f.first_evidence_id`, sessionID, sessionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []FindingRow{}
	for rows.Next() {
		var f FindingRow
		if err := rows.Scan(&f.ID, &f.SessionID, &f.Detector, &f.Classification, &f.Severity, &f.Summary, &f.EvidenceID, &f.Details, &f.SourceLine, &f.Generation, &f.Stale, &f.RunStatus, &f.RunError); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
