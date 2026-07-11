package ingest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseDispatchesAllRecordTypesWithoutThinking(t *testing.T) {
	t.Parallel()

	jsonl := strings.Join([]string{
		`{"type":"session","version":3,"id":"s1","timestamp":"2026-01-01T00:00:00Z","cwd":"/repo"}`,
		`{"type":"message","id":"u1","timestamp":"2026-01-01T00:00:01Z","message":{"role":"user","content":"hello"}}`,
		`{"type":"message","id":"a1","timestamp":"2026-01-01T00:00:02Z","message":{"role":"assistant","model":"gpt","stopReason":"stop","content":[{"type":"thinking","thinking":"PRIVATE","thinkingSignature":"SECRET"},{"type":"text","text":"answer"},{"type":"toolCall","id":"c1","name":"read","arguments":{"path":"/repo/a.go"}}],"usage":{"output":7,"reasoning":5,"cacheRead":11,"cost":{"total":0.2}}}}`,
		`{"type":"message","id":"r1","timestamp":"2026-01-01T00:00:03Z","message":{"role":"toolResult","toolCallId":"c1","toolName":"read","isError":false,"content":[{"type":"text","text":"package main"}]}}`,
		`{"type":"model_change","id":"e1","provider":"openai","modelId":"gpt","parentId":"r1"}`,
		`{"type":"thinking_level_change","id":"e2","thinkingLevel":"high","parentId":"e1"}`,
		`{"type":"compaction","id":"e3","summary":"summary","tokensBefore":12000,"details":{"autoRun":{"stopReason":"provider_error"}}}`,
		`{"type":"custom","id":"e4","customType":"goal-state","data":{"goal":{"status":"active"}}}`,
		`{"type":"custom_message","id":"e5","customType":"broker-guard","content":"blocked","details":{"kind":"credential"}}`,
	}, "\n")

	s, err := Parse(strings.NewReader(jsonl))
	require.NoError(t, err)
	require.Equal(t, "s1", s.ID)
	require.Equal(t, 9, s.Stats.Total)
	require.Len(t, s.Messages, 3)
	require.Len(t, s.ToolCalls, 1)
	require.Len(t, s.ToolResults, 1)
	require.Len(t, s.Events, 3)
	require.Len(t, s.CustomStates, 1)
	require.Len(t, s.CustomMessages, 1)
	require.Equal(t, int64(7), s.Messages[1].OutputTokens)
	require.Equal(t, int64(5), s.Messages[1].ReasoningTokens)
	require.Equal(t, int64(11), s.Messages[1].CacheReadTokens)
	require.NotContains(t, s.Messages[1].Text, "PRIVATE")
	require.NotContains(t, s.Messages[1].Text, "SECRET")
	require.Equal(t, 3, s.Messages[1].SourceLine)
}

func TestParseToleratesDriftDamageAndTornFinalLine(t *testing.T) {
	t.Parallel()

	jsonl := "" +
		`{"type":"session","version":99,"id":"s1"}` + "\n" +
		`{"type":"future","id":"x"}` + "\n" +
		`not-json` + "\n" +
		`{"type":"message","id":"u1","message":{"role":"user","content":"valid"}}` + "\n" +
		`{"type":"message"`

	s, err := Parse(strings.NewReader(jsonl))
	require.NoError(t, err)
	require.Equal(t, ParseStats{Total: 5, Malformed: 2, Unknown: 1, SchemaDrift: 1}, s.Stats)
	require.Len(t, s.Messages, 1)
}

func TestParseDeduplicatesStreamedMessageIDs(t *testing.T) {
	t.Parallel()

	jsonl := `{"type":"session","version":3,"id":"s1"}` + "\n" +
		`{"type":"message","id":"a1","message":{"role":"assistant","content":[{"type":"text","text":"partial"}],"usage":{"output":2}}}` + "\n" +
		`{"type":"message","id":"a1","message":{"role":"assistant","content":[{"type":"text","text":"complete"}],"usage":{"output":3}}}`

	s, err := Parse(strings.NewReader(jsonl))
	require.NoError(t, err)
	require.Len(t, s.Messages, 1)
	require.Equal(t, int64(3), s.Messages[0].OutputTokens)
	require.Equal(t, "complete", s.Messages[0].Text)
}
