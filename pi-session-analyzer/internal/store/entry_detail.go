package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const MaxEntryDetailBytes = 32768

var (
	ErrInvalidEntryKind = errors.New("invalid entry kind")
	ErrEntryNotFound    = errors.New("entry not found")
)

type EntryDetail struct {
	Kind             string `json:"kind"`
	ID               string `json:"id"`
	Content          string `json:"content"`
	Details          string `json:"details,omitempty"`
	ContentTruncated bool   `json:"content_truncated"`
}

func (s *Reader) SessionEntryDetail(ctx context.Context, prefix, kind, entryID string) (EntryDetail, error) {
	sessionID, err := s.ResolveSession(ctx, prefix)
	if err != nil {
		return EntryDetail{}, err
	}
	var table, contentColumn, detailsColumn string
	switch kind {
	case "message":
		table, contentColumn = "messages", "text"
	case "tool_call":
		table, contentColumn = "tool_calls", "arguments"
	case "tool_result":
		table, contentColumn = "tool_results", "content"
	case "event":
		table, contentColumn, detailsColumn = "events", "value", "details"
	case "custom_state":
		table, contentColumn = "custom_state", "data"
	case "custom_message":
		table, contentColumn, detailsColumn = "custom_messages", "content", "details"
	default:
		return EntryDetail{}, ErrInvalidEntryKind
	}
	detailsExpression := "X''"
	detailsLength := "0"
	if detailsColumn != "" {
		detailsExpression = fmt.Sprintf("COALESCE(substr(CAST(%s AS BLOB),1,%d),X'')", detailsColumn, MaxEntryDetailBytes/2)
		detailsLength = fmt.Sprintf("length(CAST(%s AS BLOB))>%d", detailsColumn, MaxEntryDetailBytes/2)
	}
	query := fmt.Sprintf(`SELECT COALESCE(substr(CAST(%s AS BLOB),1,%d),X''),%s,
 length(CAST(%s AS BLOB))>%d OR %s FROM %s WHERE session_id=? AND id=?`,
		contentColumn, MaxEntryDetailBytes/2, detailsExpression, contentColumn, MaxEntryDetailBytes/2, detailsLength, table)
	out := EntryDetail{Kind: kind, ID: entryID}
	if err = s.query.QueryRowContext(ctx, query, sessionID, entryID).Scan(&out.Content, &out.Details, &out.ContentTruncated); err != nil { //nolint:gosec // Identifiers come only from the fixed switch above.
		if errors.Is(err, sql.ErrNoRows) {
			return EntryDetail{}, ErrEntryNotFound
		}
		return EntryDetail{}, fmt.Errorf("query entry detail: %w", err)
	}
	out.Content = truncateUTF8Bytes(out.Content, MaxEntryDetailBytes/2)
	out.Details = truncateUTF8Bytes(out.Details, MaxEntryDetailBytes/2)
	return out, nil
}
