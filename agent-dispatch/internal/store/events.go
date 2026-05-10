package store

import (
	"context"
	"time"
)

func (s *Store) ListEvents(ctx context.Context, taskID string) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, task_id, run_id, timestamp, type, message, payload_json FROM events WHERE task_id = ? ORDER BY id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var events []Event
	for rows.Next() {
		var event Event
		var ts string
		if err := rows.Scan(&event.ID, &event.TaskID, &event.RunID, &ts, &event.Type, &event.Message, &event.PayloadJSON); err != nil {
			return nil, err
		}
		event.Timestamp, _ = time.Parse(time.RFC3339Nano, ts)
		events = append(events, event)
	}
	return events, rows.Err()
}
