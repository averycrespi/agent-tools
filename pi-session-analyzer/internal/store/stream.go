package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

const streamCursorVersion = 1

type StreamEntry struct {
	SourceLine   int    `json:"source_line"`
	KindRank     int    `json:"kind_rank"`
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Role         string `json:"role,omitempty"`
	Name         string `json:"name,omitempty"`
	Type         string `json:"type,omitempty"`
	Status       string `json:"status,omitempty"`
	Preview      string `json:"preview,omitempty"`
	IsError      *bool  `json:"is_error,omitempty"`
	TokensBefore int64  `json:"tokens_before,omitempty"`
}

type SessionStreamPage struct {
	Entries    []StreamEntry `json:"entries"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

type streamCursor struct {
	Version    int    `json:"v"`
	SessionID  string `json:"s"`
	SourceLine int    `json:"l"`
	KindRank   int    `json:"k"`
	ID         string `json:"i"`
}

func (s *Reader) SessionStreamFromEvidence(ctx context.Context, prefix, encodedCursor string, limit, anchorLine int, evidenceID string) (SessionStreamPage, error) {
	id, err := s.ResolveSession(ctx, prefix)
	if err != nil {
		return SessionStreamPage{}, err
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	cursor := streamCursor{Version: streamCursorVersion, SessionID: id, SourceLine: -1}
	exactAnchorID := ""
	if anchorLine > 0 {
		cursor.SourceLine = anchorLine
		cursor.KindRank = 9
		if evidenceID != "" {
			var rank sql.NullInt64
			err = s.query.QueryRowContext(ctx, `SELECT MIN(kind_rank) FROM (
 SELECT 10 kind_rank FROM messages WHERE session_id=? AND source_line=? AND id=?
 UNION ALL SELECT 20 FROM tool_calls WHERE session_id=? AND source_line=? AND id=?
 UNION ALL SELECT 30 FROM tool_results WHERE session_id=? AND source_line=? AND id=?
 UNION ALL SELECT 40 FROM events WHERE session_id=? AND source_line=? AND id=?
 UNION ALL SELECT 50 FROM custom_state WHERE session_id=? AND source_line=? AND id=?
 UNION ALL SELECT 60 FROM custom_messages WHERE session_id=? AND source_line=? AND id=?)`, id, anchorLine, evidenceID, id, anchorLine, evidenceID, id, anchorLine, evidenceID, id, anchorLine, evidenceID, id, anchorLine, evidenceID, id, anchorLine, evidenceID).Scan(&rank)
			if err != nil {
				return SessionStreamPage{}, fmt.Errorf("locate stream evidence: %w", err)
			}
			if rank.Valid {
				cursor.KindRank, cursor.ID, exactAnchorID = int(rank.Int64), evidenceID, evidenceID
			}
		}
	}
	if encodedCursor != "" {
		cursor, err = decodeStreamCursor(encodedCursor)
		if err != nil || cursor.SessionID != id {
			return SessionStreamPage{}, ErrInvalidStreamCursor
		}
	}
	rows, err := s.query.QueryContext(ctx, `
WITH stream AS (
 SELECT source_line,10 kind_rank,id,'message' kind,role,'' name,'' type,'' status,
  '' preview,NULL is_error,0 tokens_before
 FROM messages WHERE session_id=? AND role<>'toolResult'
 UNION ALL
 SELECT source_line,20,id,'tool_call','',name,'','', '',NULL,0 FROM tool_calls WHERE session_id=?
 UNION ALL
 SELECT source_line,30,id,'tool_result','',name,'','', '',is_error,0 FROM tool_results WHERE session_id=?
 UNION ALL
 SELECT source_line,40,id,'event','','',type,'','',NULL,tokens_before FROM events WHERE session_id=?
 UNION ALL
 SELECT source_line,50,id,'custom_state','','',type,status,'',NULL,0 FROM custom_state WHERE session_id=?
 UNION ALL
 SELECT source_line,60,id,'custom_message','',kind,type,'','',NULL,0 FROM custom_messages WHERE session_id=?
)
SELECT source_line,kind_rank,id,kind,role,name,type,status,preview,is_error,tokens_before
FROM stream
WHERE source_line>? OR (source_line=? AND kind_rank>?) OR (source_line=? AND kind_rank=? AND id>?)
 OR (?<>'' AND source_line=? AND kind_rank=? AND id=?)
ORDER BY source_line,kind_rank,id
LIMIT ?`, id, id, id, id, id, id, cursor.SourceLine, cursor.SourceLine, cursor.KindRank, cursor.SourceLine, cursor.KindRank, cursor.ID, exactAnchorID, cursor.SourceLine, cursor.KindRank, exactAnchorID, limit+1)
	if err != nil {
		return SessionStreamPage{}, fmt.Errorf("query session stream: %w", err)
	}
	defer func() { _ = rows.Close() }()
	entries := make([]StreamEntry, 0, limit+1)
	for rows.Next() {
		var entry StreamEntry
		var isError sql.NullBool
		if err = rows.Scan(&entry.SourceLine, &entry.KindRank, &entry.ID, &entry.Kind, &entry.Role, &entry.Name, &entry.Type, &entry.Status, &entry.Preview, &isError, &entry.TokensBefore); err != nil {
			return SessionStreamPage{}, fmt.Errorf("scan session stream: %w", err)
		}
		if isError.Valid {
			entry.IsError = &isError.Bool
		}
		entries = append(entries, entry)
	}
	if err = rows.Err(); err != nil {
		return SessionStreamPage{}, fmt.Errorf("read session stream: %w", err)
	}
	page := SessionStreamPage{Entries: entries}
	if len(entries) > limit {
		page.Entries = entries[:limit]
		last := page.Entries[len(page.Entries)-1]
		page.NextCursor, err = encodeStreamCursor(streamCursor{Version: streamCursorVersion, SessionID: id, SourceLine: last.SourceLine, KindRank: last.KindRank, ID: last.ID})
		if err != nil {
			return SessionStreamPage{}, err
		}
	}
	return page, nil
}

func truncateUTF8Bytes(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func encodeStreamCursor(cursor streamCursor) (string, error) {
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode stream cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeStreamCursor(encoded string) (streamCursor, error) {
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return streamCursor{}, fmt.Errorf("%w: %w", ErrInvalidStreamCursor, err)
	}
	var cursor streamCursor
	if err = json.Unmarshal(data, &cursor); err != nil || cursor.Version != streamCursorVersion || cursor.SessionID == "" || cursor.SourceLine < 0 || cursor.KindRank < 10 || cursor.KindRank > 60 || cursor.ID == "" {
		return streamCursor{}, ErrInvalidStreamCursor
	}
	return cursor, nil
}
