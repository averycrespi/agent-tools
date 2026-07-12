package store

import (
	"context"
	"fmt"
)

const MaxTokenSequencePageSize = 50

type TokenSequenceEntry struct {
	Kind             string  `json:"kind"`
	ID               string  `json:"id"`
	SourceLine       int     `json:"source_line"`
	Role             string  `json:"role,omitempty"`
	Model            string  `json:"model,omitempty"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	ReasoningTokens  int64   `json:"reasoning_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	Cost             float64 `json:"cost_as_logged,omitempty"`
	TokensBefore     int64   `json:"tokens_before,omitempty"`
}

type TokenSequencePage struct {
	Entries    []TokenSequenceEntry `json:"entries"`
	NextCursor string               `json:"next_cursor,omitempty"`
}

func (s *Reader) MessageTokenSequence(ctx context.Context, prefix, encodedCursor string, limit int) (TokenSequencePage, error) {
	id, err := s.ResolveSession(ctx, prefix)
	if err != nil {
		return TokenSequencePage{}, err
	}
	if limit <= 0 || limit > MaxTokenSequencePageSize {
		limit = MaxTokenSequencePageSize
	}
	cursor := streamCursor{Version: streamCursorVersion, SessionID: id, SourceLine: -1}
	if encodedCursor != "" {
		cursor, err = decodeStreamCursor(encodedCursor)
		if err != nil || cursor.SessionID != id || (cursor.KindRank != 10 && cursor.KindRank != 40) {
			return TokenSequencePage{}, ErrInvalidStreamCursor
		}
	}
	rows, err := s.query.QueryContext(ctx, `
WITH sequence AS (
 SELECT source_line,10 kind_rank,id,'message' kind,role,model,input_tokens,output_tokens,reasoning_tokens,
  cache_read_tokens,cache_write_tokens,cost,0 tokens_before FROM messages WHERE session_id=?
 UNION ALL
 SELECT source_line,40,id,'compaction','','',0,0,0,0,0,0,tokens_before FROM events WHERE session_id=? AND type='compaction'
)
SELECT source_line,kind_rank,id,kind,role,model,input_tokens,output_tokens,reasoning_tokens,
 cache_read_tokens,cache_write_tokens,cost,tokens_before
FROM sequence WHERE source_line>? OR (source_line=? AND kind_rank>?) OR (source_line=? AND kind_rank=? AND id>?)
ORDER BY source_line,kind_rank,id LIMIT ?`, id, id, cursor.SourceLine, cursor.SourceLine, cursor.KindRank, cursor.SourceLine, cursor.KindRank, cursor.ID, limit+1)
	if err != nil {
		return TokenSequencePage{}, fmt.Errorf("query token sequence: %w", err)
	}
	entries := make([]TokenSequenceEntry, 0, limit+1)
	kindRanks := make([]int, 0, limit+1)
	for rows.Next() {
		var entry TokenSequenceEntry
		var rank int
		if err = rows.Scan(&entry.SourceLine, &rank, &entry.ID, &entry.Kind, &entry.Role, &entry.Model, &entry.InputTokens,
			&entry.OutputTokens, &entry.ReasoningTokens, &entry.CacheReadTokens, &entry.CacheWriteTokens, &entry.Cost, &entry.TokensBefore); err != nil {
			_ = rows.Close()
			return TokenSequencePage{}, fmt.Errorf("scan token sequence: %w", err)
		}
		entries = append(entries, entry)
		kindRanks = append(kindRanks, rank)
	}
	if err = rows.Err(); err != nil {
		_ = rows.Close()
		return TokenSequencePage{}, fmt.Errorf("read token sequence: %w", err)
	}
	if err = rows.Close(); err != nil {
		return TokenSequencePage{}, fmt.Errorf("close token sequence: %w", err)
	}
	page := TokenSequencePage{Entries: entries}
	if len(entries) > limit {
		page.Entries = entries[:limit]
		lastIndex := limit - 1
		last := entries[lastIndex]
		page.NextCursor, err = encodeStreamCursor(streamCursor{Version: streamCursorVersion, SessionID: id, SourceLine: last.SourceLine, KindRank: kindRanks[lastIndex], ID: last.ID})
		if err != nil {
			return TokenSequencePage{}, err
		}
	}
	return page, nil
}
