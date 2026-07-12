package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

const (
	todoItemTextBytes     = 32
	todoSnapshotBytes     = 32768
	todoItemAnalysisBytes = 65535
)

type TodoItem struct {
	ID               string `json:"id"`
	Text             string `json:"text"`
	Status           string `json:"status"`
	RawStatus        string `json:"raw_status,omitempty"`
	Notes            string `json:"notes,omitempty"`
	ContentTruncated bool   `json:"content_truncated,omitempty"`
}

type TodoCounts struct {
	Todo       int `json:"todo"`
	InProgress int `json:"in_progress"`
	Done       int `json:"done"`
	Blocked    int `json:"blocked"`
	Unknown    int `json:"unknown"`
}

type TodoSnapshot struct {
	ID               string     `json:"id"`
	SourceLine       int        `json:"source_line"`
	Valid            bool       `json:"valid"`
	Error            string     `json:"error,omitempty"`
	ContentTruncated bool       `json:"content_truncated,omitempty"`
	Counts           TodoCounts `json:"counts"`
}

type TodoDiagnostics struct {
	FinalState           string         `json:"final_state"`
	MalformedSnapshots   int            `json:"page_malformed_snapshots"`
	DataQualityTruncated bool           `json:"data_quality_truncated"`
	SnapshotTotal        int            `json:"snapshot_total"`
	SnapshotOffset       int            `json:"snapshot_offset"`
	SnapshotsTruncated   bool           `json:"snapshots_truncated"`
	Snapshots            []TodoSnapshot `json:"snapshots"`
	FinalItemTotal       int            `json:"final_item_total"`
	FinalListTruncated   bool           `json:"final_list_truncated"`
	FinalItemOffset      int            `json:"final_item_offset"`
	FinalItemsTruncated  bool           `json:"final_items_truncated"`
	FinalItems           []TodoItem     `json:"final_items"`
}

type TodoQuery struct {
	SnapshotOffset, SnapshotLimit int
	ItemOffset, ItemLimit         int
}

func (s *Reader) TodoDiagnostics(ctx context.Context, prefix string) (TodoDiagnostics, error) {
	return s.TodoDiagnosticsPage(ctx, prefix, TodoQuery{})
}

func (s *Reader) TodoDiagnosticsPage(ctx context.Context, prefix string, q TodoQuery) (TodoDiagnostics, error) {
	id, err := s.ResolveSession(ctx, prefix)
	if err != nil {
		return TodoDiagnostics{}, err
	}
	q = boundTodoQuery(q)
	out := TodoDiagnostics{FinalState: "absent", SnapshotOffset: q.SnapshotOffset, FinalItemOffset: q.ItemOffset, Snapshots: []TodoSnapshot{}, FinalItems: []TodoItem{}}
	if err = s.query.QueryRowContext(ctx, `SELECT COUNT(*) FROM custom_state WHERE session_id=? AND type='todo-state'`, id).Scan(&out.SnapshotTotal); err != nil {
		return TodoDiagnostics{}, fmt.Errorf("count todo snapshots: %w", err)
	}
	rows, err := s.query.QueryContext(ctx, `SELECT id,source_line,substr(CAST(data AS BLOB),1,?),length(CAST(data AS BLOB))>? FROM custom_state WHERE session_id=? AND type='todo-state' ORDER BY source_line,id LIMIT ? OFFSET ?`, todoSnapshotBytes, todoSnapshotBytes, id, q.SnapshotLimit, q.SnapshotOffset)
	if err != nil {
		return TodoDiagnostics{}, fmt.Errorf("query todo snapshots: %w", err)
	}
	for rows.Next() {
		var snapshot TodoSnapshot
		var data string
		if err = rows.Scan(&snapshot.ID, &snapshot.SourceLine, &data, &snapshot.ContentTruncated); err != nil {
			_ = rows.Close()
			return TodoDiagnostics{}, fmt.Errorf("scan todo snapshot: %w", err)
		}
		_, counts, parseErr := parseTodoItems(data)
		snapshot.Counts = counts
		switch {
		case snapshot.ContentTruncated:
			snapshot.Error = "todo snapshot exceeds analysis limit"
			out.MalformedSnapshots++
		case parseErr != nil:
			snapshot.Error = "malformed todo snapshot"
			out.MalformedSnapshots++
		default:
			snapshot.Valid = true
		}
		out.Snapshots = append(out.Snapshots, snapshot)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return TodoDiagnostics{}, fmt.Errorf("read todo snapshots: %w", err)
	}
	if err = rows.Close(); err != nil {
		return TodoDiagnostics{}, fmt.Errorf("close todo snapshots: %w", err)
	}
	out.SnapshotsTruncated = q.SnapshotOffset+len(out.Snapshots) < out.SnapshotTotal
	out.DataQualityTruncated = len(out.Snapshots) < out.SnapshotTotal
	var finalItems []TodoItem
	finalItemsPaged := false
	if out.SnapshotTotal > 0 {
		var snapshotID, finalData string
		var contentTruncated bool
		if err = s.query.QueryRowContext(ctx, `SELECT id,substr(CAST(data AS BLOB),1,?),length(CAST(data AS BLOB))>? FROM custom_state WHERE session_id=? AND type='todo-state' ORDER BY source_line DESC,id DESC LIMIT 1`, todoSnapshotBytes, todoSnapshotBytes, id).Scan(&snapshotID, &finalData, &contentTruncated); err != nil {
			return TodoDiagnostics{}, fmt.Errorf("query final todo snapshot: %w", err)
		}
		items, _, parseErr := parseTodoItems(finalData)
		if contentTruncated {
			var valid bool
			items, out.FinalItemTotal, valid, err = s.pagedTodoItems(ctx, id, snapshotID, q.ItemOffset, q.ItemLimit)
			if err != nil {
				return TodoDiagnostics{}, err
			}
			if valid {
				parseErr = nil
				finalItemsPaged = true
			} else {
				parseErr = fmt.Errorf("todo snapshot exceeds analysis limit")
				out.FinalListTruncated = true
			}
		}
		if parseErr != nil {
			out.FinalState = "malformed"
		} else {
			out.FinalState = "valid"
			finalItems = items
		}
	}
	if finalItemsPaged {
		out.FinalItems = append(out.FinalItems, finalItems...)
	} else {
		out.FinalItemTotal = len(finalItems)
		if q.ItemOffset < len(finalItems) {
			end := min(len(finalItems), q.ItemOffset+q.ItemLimit)
			out.FinalItems = append(out.FinalItems, finalItems[q.ItemOffset:end]...)
		}
	}
	for i := range out.FinalItems {
		text := truncateUTF8Bytes(out.FinalItems[i].Text, todoItemTextBytes)
		notes := truncateUTF8Bytes(out.FinalItems[i].Notes, todoItemTextBytes)
		out.FinalItems[i].ContentTruncated = text != out.FinalItems[i].Text || notes != out.FinalItems[i].Notes
		out.FinalItems[i].Text, out.FinalItems[i].Notes = text, notes
	}
	out.FinalItemsTruncated = q.ItemOffset+len(out.FinalItems) < out.FinalItemTotal
	return out, nil
}

func (s *Reader) pagedTodoItems(ctx context.Context, sessionID, snapshotID string, offset, limit int) ([]TodoItem, int, bool, error) {
	var valid bool
	if err := s.query.QueryRowContext(ctx, `SELECT COALESCE(CASE WHEN json_valid(data) THEN json_type(data,'$.items')='array' ELSE 0 END,0) FROM custom_state WHERE session_id=? AND id=?`, sessionID, snapshotID).Scan(&valid); err != nil {
		return nil, 0, false, fmt.Errorf("validate large todo snapshot: %w", err)
	}
	if !valid {
		return nil, 0, false, nil
	}
	var total int
	if err := s.query.QueryRowContext(ctx, `SELECT json_array_length(data,'$.items') FROM custom_state WHERE session_id=? AND id=?`, sessionID, snapshotID).Scan(&total); err != nil {
		return nil, 0, false, fmt.Errorf("count large todo items: %w", err)
	}
	rows, err := s.query.QueryContext(ctx, `SELECT substr(CAST(j.value AS BLOB),1,?),length(CAST(j.value AS BLOB))>? FROM custom_state c,json_each(c.data,'$.items') j WHERE c.session_id=? AND c.id=? ORDER BY CAST(j.key AS INTEGER) LIMIT ? OFFSET ?`, todoItemAnalysisBytes, todoItemAnalysisBytes, sessionID, snapshotID, limit, offset)
	if err != nil {
		return nil, 0, false, fmt.Errorf("query large todo items: %w", err)
	}
	items := []TodoItem{}
	for rows.Next() {
		var value string
		var truncated bool
		if err = rows.Scan(&value, &truncated); err != nil {
			_ = rows.Close()
			return nil, 0, false, fmt.Errorf("scan large todo item: %w", err)
		}
		parsed, _, parseErr := parseTodoItems(`{"items":[` + value + `]}`)
		if parseErr != nil || truncated || len(parsed) != 1 {
			_ = rows.Close()
			return nil, 0, false, nil
		}
		items = append(items, parsed[0])
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return nil, 0, false, fmt.Errorf("read large todo items: %w", err)
	}
	if err = rows.Close(); err != nil {
		return nil, 0, false, fmt.Errorf("close large todo items: %w", err)
	}
	return items, total, true, nil
}

func boundTodoQuery(q TodoQuery) TodoQuery {
	if q.SnapshotOffset < 0 {
		q.SnapshotOffset = 0
	}
	if q.SnapshotLimit <= 0 || q.SnapshotLimit > 100 {
		q.SnapshotLimit = 50
	}
	if q.ItemOffset < 0 {
		q.ItemOffset = 0
	}
	if q.ItemLimit <= 0 || q.ItemLimit > 50 {
		q.ItemLimit = 20
	}
	return q
}

func parseTodoItems(data string) ([]TodoItem, TodoCounts, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &object); err != nil {
		return nil, TodoCounts{}, err
	}
	rawItems, exists := object["items"]
	if !exists {
		return nil, TodoCounts{}, fmt.Errorf("items missing")
	}
	if bytes.Equal(bytes.TrimSpace(rawItems), []byte("null")) {
		return nil, TodoCounts{}, fmt.Errorf("items must be an array")
	}
	var encodedItems []struct {
		ID     json.RawMessage `json:"id"`
		Text   *string         `json:"text"`
		Status *string         `json:"status"`
		Notes  string          `json:"notes"`
	}
	if err := json.Unmarshal(rawItems, &encodedItems); err != nil {
		return nil, TodoCounts{}, err
	}
	items := make([]TodoItem, 0, len(encodedItems))
	var counts TodoCounts
	for _, encoded := range encodedItems {
		id, err := todoItemID(encoded.ID)
		if err != nil || encoded.Text == nil || encoded.Status == nil {
			return nil, TodoCounts{}, fmt.Errorf("invalid todo item")
		}
		item := TodoItem{ID: id, Text: *encoded.Text, RawStatus: *encoded.Status, Notes: encoded.Notes}
		switch item.RawStatus {
		case "todo":
			item.Status = "todo"
			counts.Todo++
		case "in_progress":
			item.Status = "in_progress"
			counts.InProgress++
		case "done":
			item.Status = "done"
			counts.Done++
		case "blocked":
			item.Status = "blocked"
			counts.Blocked++
		default:
			item.Status = "unknown"
			counts.Unknown++
		}
		items = append(items, item)
	}
	return items, counts, nil
}

func todoItemID(raw json.RawMessage) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	switch typed := value.(type) {
	case string:
		if typed != "" {
			return typed, nil
		}
	case json.Number:
		return typed.String(), nil
	}
	return "", fmt.Errorf("invalid todo id")
}
