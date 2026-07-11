// Package ingest parses Pi JSONL session logs into a normalized in-memory model.
package ingest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const currentSchemaVersion = 3

// ParseStats describes tolerated input drift and damage.
type ParseStats struct {
	Total       int `json:"total"`
	Malformed   int `json:"malformed"`
	Unknown     int `json:"unknown"`
	SchemaDrift int `json:"schema_drift"`
}

// Session is the normalized representation of one Pi JSONL file.
type Session struct {
	ID             string
	Version        int
	Timestamp      string
	CWD            string
	Stats          ParseStats
	Messages       []Message
	ToolCalls      []ToolCall
	ToolResults    []ToolResult
	Events         []Event
	CustomStates   []CustomState
	CustomMessages []CustomMessage
}

type Message struct {
	ID, ParentID, Timestamp, Role, Model, StopReason, Text string
	SourceLine                                             int
	InputTokens, OutputTokens, ReasoningTokens             int64
	CacheReadTokens, CacheWriteTokens                      int64
	Cost                                                   float64
}

type ToolCall struct {
	ID, MessageID, Name, Arguments string
	SourceLine                     int
}

type ToolResult struct {
	ID, MessageID, CallID, Name, Content string
	IsError                              *bool
	SourceLine                           int
}

type Event struct {
	ID, Type, Value, Details string
	SourceLine               int
	TokensBefore             int64
}

type CustomState struct {
	ID, Type, Status, Data string
	SourceLine             int
}

type CustomMessage struct {
	ID, Type, Kind, Content, Details string
	SourceLine                       int
}

type envelope struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Version int    `json:"version"`
}

type sessionRecord struct {
	Version   int    `json:"version"`
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	CWD       string `json:"cwd"`
}

type messageRecord struct {
	ID        string          `json:"id"`
	ParentID  string          `json:"parentId"`
	Timestamp string          `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
}

type messageBody struct {
	Role       string          `json:"role"`
	Model      string          `json:"model"`
	StopReason string          `json:"stopReason"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	IsError    *bool           `json:"isError"`
	Usage      usage           `json:"usage"`
}

type usage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	Reasoning  int64 `json:"reasoning"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
	Cost       struct {
		Total float64 `json:"total"`
	} `json:"cost"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// Parse tolerantly reads a Pi session. Individual malformed and unknown records are counted and skipped.
func Parse(r io.Reader) (Session, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 64*1024*1024)
	var result Session
	messageIndex := map[string]int{}
	callIndex := map[string]int{}
	resultIndex := map[string]int{}
	eventIndex := map[string]int{}
	stateIndex := map[string]int{}
	customMessageIndex := map[string]int{}
	line := 0
	for scanner.Scan() {
		line++
		result.Stats.Total++
		data := scanner.Bytes()
		var env envelope
		if err := json.Unmarshal(data, &env); err != nil {
			result.Stats.Malformed++
			continue
		}
		switch env.Type {
		case "session":
			var rec sessionRecord
			if json.Unmarshal(data, &rec) != nil || rec.ID == "" {
				result.Stats.Malformed++
				continue
			}
			result.ID, result.Version, result.Timestamp, result.CWD = rec.ID, rec.Version, rec.Timestamp, rec.CWD
			if rec.Version != currentSchemaVersion {
				result.Stats.SchemaDrift++
			}
		case "message":
			if err := parseMessage(data, line, &result, messageIndex, callIndex, resultIndex); err != nil {
				result.Stats.Malformed++
			}
		case "model_change", "thinking_level_change", "compaction":
			if err := parseEvent(data, env, line, &result, eventIndex); err != nil {
				result.Stats.Malformed++
			}
		case "custom":
			if err := parseCustom(data, env, line, &result, stateIndex); err != nil {
				result.Stats.Malformed++
			}
		case "custom_message":
			if err := parseCustomMessage(data, env, line, &result, customMessageIndex); err != nil {
				result.Stats.Malformed++
			}
		default:
			result.Stats.Unknown++
		}
	}
	if err := scanner.Err(); err != nil {
		return Session{}, fmt.Errorf("scan session: %w", err)
	}
	if result.ID == "" {
		return Session{}, fmt.Errorf("session record not found")
	}
	return result, nil
}

func parseMessage(data []byte, line int, s *Session, messageIndex, callIndex, resultIndex map[string]int) error {
	var rec messageRecord
	if err := json.Unmarshal(data, &rec); err != nil || rec.ID == "" {
		return fmt.Errorf("invalid message")
	}
	var body messageBody
	if err := json.Unmarshal(rec.Message, &body); err != nil || body.Role == "" {
		return fmt.Errorf("invalid message body")
	}
	text, calls, err := parseContent(body.Content, rec.ID, line)
	if err != nil {
		return err
	}
	msg := Message{ID: rec.ID, ParentID: rec.ParentID, Timestamp: rec.Timestamp, Role: body.Role, Model: body.Model, StopReason: body.StopReason, Text: text, SourceLine: line, InputTokens: body.Usage.Input, OutputTokens: body.Usage.Output, ReasoningTokens: body.Usage.Reasoning, CacheReadTokens: body.Usage.CacheRead, CacheWriteTokens: body.Usage.CacheWrite, Cost: body.Usage.Cost.Total}
	upsert(&s.Messages, messageIndex, rec.ID, msg)
	for _, call := range calls {
		upsert(&s.ToolCalls, callIndex, call.ID, call)
	}
	if body.Role == "toolResult" {
		tr := ToolResult{ID: rec.ID, MessageID: rec.ID, CallID: body.ToolCallID, Name: body.ToolName, Content: text, IsError: body.IsError, SourceLine: line}
		upsert(&s.ToolResults, resultIndex, rec.ID, tr)
	}
	return nil
}

func parseContent(raw json.RawMessage, messageID string, line int) (string, []ToolCall, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil, nil
	}
	var plain string
	if json.Unmarshal(raw, &plain) == nil {
		return plain, nil, nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", nil, fmt.Errorf("parse content: %w", err)
	}
	var texts []string
	var calls []ToolCall
	for _, block := range blocks {
		switch block.Type {
		case "text":
			texts = append(texts, block.Text)
		case "toolCall":
			calls = append(calls, ToolCall{ID: block.ID, MessageID: messageID, Name: block.Name, Arguments: string(block.Arguments), SourceLine: line})
		}
	}
	return strings.Join(texts, "\n"), calls, nil
}

func parseEvent(data []byte, env envelope, line int, s *Session, indexes map[string]int) error {
	var rec struct {
		Provider      string          `json:"provider"`
		ModelID       string          `json:"modelId"`
		ThinkingLevel string          `json:"thinkingLevel"`
		Summary       string          `json:"summary"`
		TokensBefore  int64           `json:"tokensBefore"`
		Details       json.RawMessage `json:"details"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return err
	}
	value := rec.ModelID
	switch env.Type {
	case "thinking_level_change":
		value = rec.ThinkingLevel
	case "compaction":
		value = rec.Summary
	}
	id := recordID(env.ID, env.Type, line)
	upsert(&s.Events, indexes, id, Event{ID: id, Type: env.Type, Value: value, Details: string(rec.Details), SourceLine: line, TokensBefore: rec.TokensBefore})
	return nil
}

func parseCustom(data []byte, env envelope, line int, s *Session, indexes map[string]int) error {
	var rec struct {
		CustomType string          `json:"customType"`
		Data       json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return err
	}
	var state struct {
		Goal struct {
			Status string `json:"status"`
		} `json:"goal"`
	}
	_ = json.Unmarshal(rec.Data, &state)
	id := recordID(env.ID, rec.CustomType, line)
	upsert(&s.CustomStates, indexes, id, CustomState{ID: id, Type: rec.CustomType, Status: state.Goal.Status, Data: string(rec.Data), SourceLine: line})
	return nil
}

func parseCustomMessage(data []byte, env envelope, line int, s *Session, indexes map[string]int) error {
	var rec struct {
		CustomType string          `json:"customType"`
		Content    string          `json:"content"`
		Details    json.RawMessage `json:"details"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return err
	}
	var details struct {
		Kind string `json:"kind"`
	}
	_ = json.Unmarshal(rec.Details, &details)
	id := recordID(env.ID, rec.CustomType, line)
	upsert(&s.CustomMessages, indexes, id, CustomMessage{ID: id, Type: rec.CustomType, Kind: details.Kind, Content: rec.Content, Details: string(rec.Details), SourceLine: line})
	return nil
}

func recordID(id, recordType string, line int) string {
	if id != "" {
		return id
	}
	return fmt.Sprintf("%s:line:%d", recordType, line)
}

func upsert[T any](values *[]T, indexes map[string]int, id string, value T) {
	if i, ok := indexes[id]; ok {
		(*values)[i] = value
		return
	}
	indexes[id] = len(*values)
	*values = append(*values, value)
}
