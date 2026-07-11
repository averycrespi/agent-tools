package store

import (
	"context"
	"fmt"
	"sort"
)

type ConversationEntry struct {
	Kind, ID, Role, Name, StopReason, Text string
	SourceLine                             int
	IsError                                *bool
}

func (s *Store) Conversation(ctx context.Context, prefix, anchor string, limit int) ([]ConversationEntry, error) {
	id, err := s.ResolveSession(ctx, prefix)
	if err != nil {
		return nil, err
	}
	session, err := s.LoadSession(ctx, id)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	anchorLine := 0
	if anchor != "" {
		found := false
		for _, m := range session.Messages {
			if m.ID == anchor {
				anchorLine = m.SourceLine
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("message %q not found in session %s", anchor, id)
		}
	}
	entries := []ConversationEntry{}
	for _, m := range session.Messages {
		if m.SourceLine >= anchorLine {
			entries = append(entries, ConversationEntry{Kind: "message", ID: m.ID, Role: m.Role, StopReason: m.StopReason, Text: m.Text, SourceLine: m.SourceLine})
		}
	}
	for _, c := range session.ToolCalls {
		if c.SourceLine >= anchorLine {
			entries = append(entries, ConversationEntry{Kind: "tool_call", ID: c.ID, Name: c.Name, Text: c.Arguments, SourceLine: c.SourceLine})
		}
	}
	for _, r := range session.ToolResults {
		if r.SourceLine >= anchorLine {
			entries = append(entries, ConversationEntry{Kind: "tool_result", ID: r.ID, Name: r.Name, Text: r.Content, SourceLine: r.SourceLine, IsError: r.IsError})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].SourceLine == entries[j].SourceLine {
			return entries[i].Kind < entries[j].Kind
		}
		return entries[i].SourceLine < entries[j].SourceLine
	})
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

func (s *Store) Message(ctx context.Context, sessionPrefix, messageID string) (map[string]any, error) {
	id, err := s.ResolveSession(ctx, sessionPrefix)
	if err != nil {
		return nil, err
	}
	session, err := s.LoadSession(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, m := range session.Messages {
		if m.ID != messageID {
			continue
		}
		calls := []any{}
		for _, c := range session.ToolCalls {
			if c.MessageID == m.ID {
				calls = append(calls, c)
			}
		}
		results := []any{}
		for _, r := range session.ToolResults {
			if r.MessageID == m.ID {
				results = append(results, r)
			}
		}
		return map[string]any{"session_id": id, "message": m, "tool_calls": calls, "tool_results": results}, nil
	}
	return nil, fmt.Errorf("message %q not found in session %s", messageID, id)
}

type FailureQuery struct {
	Limit                                 int
	Detector, Classification, MinSeverity string
}

func (s *Store) TopFailures(ctx context.Context, q FailureQuery) ([]FindingRow, error) {
	if q.Limit <= 0 || q.Limit > 50 {
		q.Limit = 50
	}
	if q.MinSeverity == "" {
		q.MinSeverity = "warn"
	}
	rank := map[string]int{"info": 1, "warn": 2, "error": 3}[q.MinSeverity]
	rows, err := s.Findings(ctx, "")
	if err != nil {
		return nil, err
	}
	out := []FindingRow{}
	for _, f := range rows {
		if q.Detector != "" && f.Detector != q.Detector {
			continue
		}
		if q.Classification != "" && f.Classification != q.Classification {
			continue
		}
		if map[string]int{"info": 1, "warn": 2, "error": 3}[f.Severity] < rank {
			continue
		}
		out = append(out, f)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri := map[string]int{"info": 1, "warn": 2, "error": 3}[out[i].Severity]
		rj := map[string]int{"info": 1, "warn": 2, "error": 3}[out[j].Severity]
		if ri != rj {
			return ri > rj
		}
		if out[i].Detector != out[j].Detector {
			return out[i].Detector < out[j].Detector
		}
		if out[i].SessionID != out[j].SessionID {
			return out[i].SessionID < out[j].SessionID
		}
		return out[i].EvidenceID < out[j].EvidenceID
	})
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}
