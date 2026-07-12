package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const MaxMatrixPageSize = 10

var ErrInvalidMatrixQuery = errors.New("invalid matrix query")

type MatrixQuery struct {
	FromUnix  int64
	ToUnix    int64
	Untimed   bool
	CWD       string
	Limit     int
	Cursor    string
	Direction string
}

type DetectorCoverage struct {
	Success int `json:"success"`
	Failed  int `json:"failed"`
	NotRun  int `json:"not_run"`
}

type MatrixRow struct {
	ID                    string            `json:"id"`
	StartedAtUnix         *int64            `json:"started_at_unix"`
	CWD                   string            `json:"cwd"`
	Records               int               `json:"records"`
	Turns                 int               `json:"turns"`
	Cost                  float64           `json:"cost_as_logged"`
	OutputTokens          int64             `json:"output_tokens"`
	ReasoningTokens       int64             `json:"reasoning_tokens"`
	CacheReadTokens       int64             `json:"cache_read_tokens"`
	CacheWriteTokens      int64             `json:"cache_write_tokens"`
	ToolOutcomes          ToolOutcomeTotals `json:"tool_outcomes"`
	ToolTotalCalls        int               `json:"tool_total_calls"`
	ToolErrorRate         *float64          `json:"tool_error_rate"`
	ToolAnalysisTruncated bool              `json:"tool_analysis_truncated"`
	Compactions           int               `json:"compactions"`
	BrokerGuards          int               `json:"broker_guards"`
	FreshSeverity         string            `json:"fresh_severity"`
	DetectorCoverage      DetectorCoverage  `json:"detector_coverage"`
	GoalOutcome           string            `json:"goal_outcome"`
	TodoOutcome           string            `json:"todo_outcome"`
	StopReason            string            `json:"stop_reason"`
	SchemaDrift           int               `json:"schema_drift"`
	MalformedRecords      int               `json:"malformed_records"`
	UnknownRecords        int               `json:"unknown_records"`
	ContentTruncated      bool              `json:"content_truncated"`
}

type SessionMatrixPage struct {
	Rows       []MatrixRow `json:"rows"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

type matrixCursor struct {
	FromUnix  int64  `json:"from"`
	ToUnix    int64  `json:"to"`
	Untimed   bool   `json:"untimed"`
	CWD       string `json:"cwd"`
	Start     *int64 `json:"start,omitempty"`
	ID        string `json:"id"`
	Direction string `json:"direction"`
}

func (s *Reader) SessionMatrix(ctx context.Context, q MatrixQuery, detectorNames []string) (SessionMatrixPage, error) {
	if q.Limit == 0 {
		q.Limit = MaxMatrixPageSize
	}
	if q.Direction == "" {
		q.Direction = "desc"
	}
	if q.Direction != "asc" && q.Direction != "desc" {
		return SessionMatrixPage{}, fmt.Errorf("%w: direction must be asc or desc", ErrInvalidMatrixQuery)
	}
	if q.Limit < 1 || q.Limit > MaxMatrixPageSize {
		return SessionMatrixPage{}, fmt.Errorf("%w: limit must be between 1 and %d", ErrInvalidMatrixQuery, MaxMatrixPageSize)
	}
	if !q.Untimed && q.FromUnix >= q.ToUnix {
		return SessionMatrixPage{}, fmt.Errorf("%w: range start must be before range end", ErrInvalidMatrixQuery)
	}
	if len(q.CWD) > 256 {
		return SessionMatrixPage{}, fmt.Errorf("%w: cwd filter is too long", ErrInvalidMatrixQuery)
	}
	var cursor matrixCursor
	if q.Cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		if err != nil || json.Unmarshal(decoded, &cursor) != nil || cursor.ID == "" {
			return SessionMatrixPage{}, fmt.Errorf("%w: invalid cursor", ErrInvalidMatrixQuery)
		}
		if cursor.FromUnix != q.FromUnix || cursor.ToUnix != q.ToUnix || cursor.Untimed != q.Untimed || cursor.CWD != q.CWD || cursor.Direction != q.Direction {
			return SessionMatrixPage{}, fmt.Errorf("%w: cursor does not match current filters", ErrInvalidMatrixQuery)
		}
	}
	args := make([]any, 0, len(detectorNames)*2+8)
	conditions := []string{}
	order := "s.started_at_unix DESC,s.id"
	comparison := "<"
	idComparison := ">"
	if q.Direction == "asc" {
		order = "s.started_at_unix ASC,s.id"
		comparison = ">"
	}
	if q.Untimed {
		conditions = append(conditions, "s.started_at_unix IS NULL")
		order = "s.id"
		if q.Direction == "desc" {
			order = "s.id DESC"
			idComparison = "<"
		}
		if q.Cursor != "" {
			conditions = append(conditions, "s.id"+idComparison+"?")
			args = append(args, cursor.ID)
		}
	} else {
		conditions = append(conditions, "s.started_at_unix>=?", "s.started_at_unix<?")
		args = append(args, q.FromUnix, q.ToUnix)
		if q.Cursor != "" {
			if cursor.Start == nil {
				return SessionMatrixPage{}, fmt.Errorf("%w: cursor does not match timed filter", ErrInvalidMatrixQuery)
			}
			conditions = append(conditions, "(s.started_at_unix"+comparison+"? OR (s.started_at_unix=? AND s.id>?))")
			args = append(args, *cursor.Start, *cursor.Start, cursor.ID)
		}
	}
	if q.CWD != "" {
		conditions = append(conditions, "s.cwd=?")
		args = append(args, q.CWD)
	}
	args = append(args, q.Limit+1)
	runFilter, freshFilter := "0", "0"
	if len(detectorNames) > 0 {
		placeholders := make([]string, len(detectorNames))
		for i := range detectorNames {
			placeholders[i] = "?"
		}
		freshFilter = "f.detector IN (" + strings.Join(placeholders, ",") + ")"
		runFilter = "r.detector IN (" + strings.Join(placeholders, ",") + ")"
		for _, name := range detectorNames {
			args = append(args, name)
		}
		for _, name := range detectorNames {
			args = append(args, name)
		}
	}
	query := `
WITH candidates AS (
 SELECT s.* FROM sessions s WHERE ` + strings.Join(conditions, " AND ") + ` ORDER BY ` + order + ` LIMIT ?
), message_facts AS (
 SELECT m.session_id,COUNT(*) turns,COALESCE(SUM(m.cost),0) cost,COALESCE(SUM(m.output_tokens),0) output_tokens,
  COALESCE(SUM(m.reasoning_tokens),0) reasoning_tokens,COALESCE(SUM(m.cache_read_tokens),0) cache_read_tokens,
  COALESCE(SUM(m.cache_write_tokens),0) cache_write_tokens
 FROM messages m JOIN candidates c ON c.id=m.session_id GROUP BY m.session_id
), event_facts AS (SELECT e.session_id,COUNT(*) compactions FROM events e JOIN candidates c ON c.id=e.session_id WHERE e.type='compaction' GROUP BY e.session_id),
 guard_facts AS (SELECT g.session_id,COUNT(*) guards FROM custom_messages g JOIN candidates c ON c.id=g.session_id WHERE g.type='broker-guard' GROUP BY g.session_id),
 fresh_facts AS (
  SELECT f.session_id,MAX(CASE f.severity WHEN 'error' THEN 3 WHEN 'warn' THEN 2 ELSE 1 END) severity
  FROM findings f JOIN candidates c ON c.id=f.session_id JOIN detector_runs r ON r.session_id=f.session_id AND r.detector=f.detector
  WHERE f.stale=0 AND r.status='success' AND f.generation=r.generation AND ` + freshFilter + ` GROUP BY f.session_id
 ), run_facts AS (
  SELECT r.session_id,SUM(r.status='success') successes,SUM(r.status='failed') failures
  FROM detector_runs r JOIN candidates c ON c.id=r.session_id WHERE ` + runFilter + ` GROUP BY r.session_id
 )
SELECT s.id,s.started_at_unix,s.cwd,s.total_records,s.schema_drift,s.malformed_records,s.unknown_records,
 COALESCE(m.turns,0),COALESCE(m.cost,0),COALESCE(m.output_tokens,0),COALESCE(m.reasoning_tokens,0),
 COALESCE(m.cache_read_tokens,0),COALESCE(m.cache_write_tokens,0),COALESCE(e.compactions,0),COALESCE(g.guards,0),
 COALESCE(f.severity,0),COALESCE(r.successes,0),COALESCE(r.failures,0),
 CASE WHEN EXISTS(SELECT 1 FROM custom_state x WHERE x.session_id=s.id AND x.type='goal-state')
  THEN (SELECT status FROM custom_state x WHERE x.session_id=s.id AND x.type='goal-state' ORDER BY source_line DESC,id DESC LIMIT 1)
  ELSE '__absent__' END,
 CASE WHEN EXISTS(SELECT 1 FROM custom_state x WHERE x.session_id=s.id AND x.type='todo-state') THEN 1 ELSE 0 END,
 COALESCE((SELECT substr(data,1,` + fmt.Sprint(todoSnapshotBytes) + `) FROM custom_state x WHERE x.session_id=s.id AND x.type='todo-state' ORDER BY source_line DESC,id DESC LIMIT 1),''),
 COALESCE((SELECT length(data)>` + fmt.Sprint(todoSnapshotBytes) + ` FROM custom_state x WHERE x.session_id=s.id AND x.type='todo-state' ORDER BY source_line DESC,id DESC LIMIT 1),0),
 COALESCE((SELECT stop_reason FROM messages x WHERE x.session_id=s.id AND x.role='assistant' AND x.stop_reason<>'' ORDER BY source_line DESC,id DESC LIMIT 1),'')
FROM candidates s
LEFT JOIN message_facts m ON m.session_id=s.id LEFT JOIN event_facts e ON e.session_id=s.id
LEFT JOIN guard_facts g ON g.session_id=s.id LEFT JOIN fresh_facts f ON f.session_id=s.id LEFT JOIN run_facts r ON r.session_id=s.id
ORDER BY ` + order
	rows, err := s.query.QueryContext(ctx, query, args...) //nolint:gosec // Dynamic fragments are fixed clauses and detector placeholders only.
	if err != nil {
		return SessionMatrixPage{}, fmt.Errorf("query session matrix: %w", err)
	}
	page := SessionMatrixPage{Rows: []MatrixRow{}}
	for rows.Next() {
		var row MatrixRow
		var started sql.NullInt64
		var severity, successes, failures, todoPresent, todoTruncated int
		var goal, todoData string
		if err = rows.Scan(&row.ID, &started, &row.CWD, &row.Records, &row.SchemaDrift, &row.MalformedRecords, &row.UnknownRecords,
			&row.Turns, &row.Cost, &row.OutputTokens, &row.ReasoningTokens, &row.CacheReadTokens, &row.CacheWriteTokens,
			&row.Compactions, &row.BrokerGuards, &severity, &successes, &failures, &goal, &todoPresent, &todoData, &todoTruncated, &row.StopReason); err != nil {
			_ = rows.Close()
			return SessionMatrixPage{}, fmt.Errorf("scan session matrix: %w", err)
		}
		if started.Valid {
			value := started.Int64
			row.StartedAtUnix = &value
		}
		row.FreshSeverity = []string{"none", "info", "warn", "error"}[severity]
		row.DetectorCoverage = DetectorCoverage{Success: successes, Failed: failures, NotRun: max(0, len(detectorNames)-successes-failures)}
		row.GoalOutcome = goalOutcome(goal)
		if todoTruncated != 0 {
			row.TodoOutcome = "truncated"
		} else {
			row.TodoOutcome = matrixTodoOutcome(todoPresent != 0, todoData)
		}
		boundedCWD := truncateUTF8Bytes(row.CWD, 64)
		row.ContentTruncated = boundedCWD != row.CWD
		row.CWD = boundedCWD
		page.Rows = append(page.Rows, row)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return SessionMatrixPage{}, fmt.Errorf("read session matrix: %w", err)
	}
	if err = rows.Close(); err != nil {
		return SessionMatrixPage{}, fmt.Errorf("close session matrix: %w", err)
	}
	hasMore := len(page.Rows) > q.Limit
	if hasMore {
		page.Rows = page.Rows[:q.Limit]
	}
	for i := range page.Rows {
		report, reportErr := s.ToolOutcomeReport(ctx, page.Rows[i].ID)
		if reportErr != nil {
			return SessionMatrixPage{}, fmt.Errorf("query tool outcomes for matrix: %w", reportErr)
		}
		page.Rows[i].ToolOutcomes = report.Totals
		page.Rows[i].ToolTotalCalls = report.TotalCalls
		page.Rows[i].ToolAnalysisTruncated = report.AnalysisTruncated
		if report.Totals.Classifiable > 0 {
			rate := float64(report.Totals.ConfirmedErrors+report.Totals.InferredErrors) / float64(report.Totals.Classifiable)
			page.Rows[i].ToolErrorRate = &rate
		}
	}
	if hasMore {
		last := page.Rows[len(page.Rows)-1]
		encoded, marshalErr := json.Marshal(matrixCursor{FromUnix: q.FromUnix, ToUnix: q.ToUnix, Untimed: q.Untimed, CWD: q.CWD, Start: last.StartedAtUnix, ID: last.ID, Direction: q.Direction})
		if marshalErr != nil {
			return SessionMatrixPage{}, fmt.Errorf("encode matrix cursor: %w", marshalErr)
		}
		page.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	return page, nil
}

func goalOutcome(status string) string {
	switch status {
	case "__absent__":
		return "absent"
	case "":
		return "cleared"
	default:
		return status
	}
}

func matrixTodoOutcome(present bool, data string) string {
	if !present {
		return "absent"
	}
	items, _, err := parseTodoItems(data)
	if err != nil {
		return "malformed"
	}
	if len(items) == 0 {
		return "cleared"
	}
	for _, item := range items {
		if item.Status != "done" {
			return "active"
		}
	}
	return "done"
}
