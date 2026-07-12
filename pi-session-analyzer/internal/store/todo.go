package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

const todoItemTextBytes = 32

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
	ID         string     `json:"id"`
	SourceLine int        `json:"source_line"`
	Valid      bool       `json:"valid"`
	Error      string     `json:"error,omitempty"`
	Counts     TodoCounts `json:"counts"`
}

type TodoDiagnostics struct {
	FinalState          string         `json:"final_state"`
	MalformedSnapshots  int            `json:"malformed_snapshots"`
	SnapshotTotal       int            `json:"snapshot_total"`
	SnapshotOffset      int            `json:"snapshot_offset"`
	SnapshotsTruncated  bool           `json:"snapshots_truncated"`
	Snapshots           []TodoSnapshot `json:"snapshots"`
	FinalItemTotal      int            `json:"final_item_total"`
	FinalItemOffset     int            `json:"final_item_offset"`
	FinalItemsTruncated bool           `json:"final_items_truncated"`
	FinalItems          []TodoItem     `json:"final_items"`
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
	rows, err := s.query.QueryContext(ctx, `SELECT id,source_line,data FROM custom_state WHERE session_id=? AND type='todo-state' ORDER BY source_line,id`, id)
	if err != nil {
		return TodoDiagnostics{}, fmt.Errorf("query todo snapshots: %w", err)
	}
	defer func() { _ = rows.Close() }()
	q = boundTodoQuery(q)
	out := TodoDiagnostics{FinalState: "absent", SnapshotOffset: q.SnapshotOffset, FinalItemOffset: q.ItemOffset, Snapshots: []TodoSnapshot{}, FinalItems: []TodoItem{}}
	var finalItems []TodoItem
	for rows.Next() {
		var snapshot TodoSnapshot
		var data string
		if err = rows.Scan(&snapshot.ID, &snapshot.SourceLine, &data); err != nil {
			return TodoDiagnostics{}, fmt.Errorf("scan todo snapshot: %w", err)
		}
		items, counts, parseErr := parseTodoItems(data)
		snapshot.Counts = counts
		if parseErr != nil {
			snapshot.Error = "malformed todo snapshot"
			out.MalformedSnapshots++
			out.FinalState = "malformed"
		} else {
			snapshot.Valid = true
			out.FinalState = "valid"
			finalItems = items
		}
		if out.SnapshotTotal >= q.SnapshotOffset && out.SnapshotTotal < q.SnapshotOffset+q.SnapshotLimit {
			out.Snapshots = append(out.Snapshots, snapshot)
		}
		out.SnapshotTotal++
	}
	if err = rows.Err(); err != nil {
		return TodoDiagnostics{}, fmt.Errorf("read todo snapshots: %w", err)
	}
	out.SnapshotsTruncated = q.SnapshotOffset+len(out.Snapshots) < out.SnapshotTotal
	out.FinalItemTotal = len(finalItems)
	if q.ItemOffset < len(finalItems) {
		end := min(len(finalItems), q.ItemOffset+q.ItemLimit)
		out.FinalItems = append(out.FinalItems, finalItems[q.ItemOffset:end]...)
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
