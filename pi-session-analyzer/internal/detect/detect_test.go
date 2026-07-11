package detect

import (
	"testing"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/ingest"
	"github.com/stretchr/testify/require"
)

func boolPtr(v bool) *bool { return &v }

func detectorNames(findings []Finding) map[string]Finding {
	out := map[string]Finding{}
	for _, finding := range findings {
		out[finding.Detector] = finding
	}
	return out
}

func TestStructuralDetectors(t *testing.T) {
	t.Parallel()

	s := ingest.Session{ID: "s", CustomMessages: []ingest.CustomMessage{{ID: "g", Type: "broker-guard", Kind: "credential", SourceLine: 2}}, Events: []ingest.Event{{ID: "co", Type: "compaction", TokensBefore: 12000, Details: `{"autoRun":{"stopReason":"provider_error"}}`, SourceLine: 3}}, ToolCalls: []ingest.ToolCall{{ID: "mcp", Name: "mcp_call", SourceLine: 4}, {ID: "x1", Name: "bash"}, {ID: "x2", Name: "bash"}, {ID: "x3", Name: "bash"}}, ToolResults: []ingest.ToolResult{{ID: "mr", CallID: "mcp", Name: "mcp_call", IsError: boolPtr(true), SourceLine: 5}, {ID: "r1", CallID: "x1", Name: "bash", IsError: boolPtr(true)}, {ID: "r2", CallID: "x2", Name: "bash", IsError: boolPtr(true)}, {ID: "r3", CallID: "x3", Name: "bash", IsError: boolPtr(true)}}}

	got := detectorNames(Analyze(s))
	for _, name := range []string{"broker_guard", "compaction_pressure", "tool_error_burst", "mcp_failure"} {
		require.Contains(t, got, name)
		require.Equal(t, Structural, got[name].Classification)
	}
	require.Contains(t, got["mcp_failure"].Details, "structural_flag")
}

func TestStructuralDetectorsDoNotFireOnHealthySession(t *testing.T) {
	t.Parallel()

	findings := detectorNames(Analyze(ingest.Session{ID: "healthy", ToolCalls: []ingest.ToolCall{{ID: "c1", Name: "bash"}, {ID: "c2", Name: "bash"}}, ToolResults: []ingest.ToolResult{{ID: "r1", CallID: "c1", Name: "bash", IsError: boolPtr(true)}, {ID: "r2", CallID: "c2", Name: "bash", IsError: boolPtr(true)}}}))
	for _, detector := range []string{"broker_guard", "compaction_pressure", "tool_error_burst", "mcp_failure"} {
		require.NotContains(t, findings, detector)
	}
}

func TestMCPHistoricalFallbackIsNarrow(t *testing.T) {
	t.Parallel()

	failing := ingest.Session{ID: "s", ToolCalls: []ingest.ToolCall{{ID: "c", Name: "mcp_call"}}, ToolResults: []ingest.ToolResult{{ID: "r", CallID: "c", Name: "mcp_call", Content: "MCP error: unavailable"}}}
	require.Contains(t, detectorNames(Analyze(failing)), "mcp_failure")
	benign := ingest.Session{ID: "s", ToolCalls: []ingest.ToolCall{{ID: "c", Name: "mcp_call"}}, ToolResults: []ingest.ToolResult{{ID: "r", CallID: "c", Name: "mcp_call", Content: "We discussed ordinary errors"}}}
	require.NotContains(t, detectorNames(Analyze(benign)), "mcp_failure")
	explicitSuccess := ingest.Session{ID: "s", ToolCalls: []ingest.ToolCall{{ID: "c", Name: "mcp_call"}}, ToolResults: []ingest.ToolResult{{ID: "r", CallID: "c", Name: "mcp_call", IsError: boolPtr(false), Content: "MCP error is a detector marker"}}}
	require.NotContains(t, detectorNames(Analyze(explicitSuccess)), "mcp_failure")
}

func TestRetryLoopGuardsChangingOutputAndPass(t *testing.T) {
	t.Parallel()

	calls := []ingest.ToolCall{}
	failed := []ingest.ToolResult{}
	for i := 0; i < 4; i++ {
		id := string(rune('a' + i))
		calls = append(calls, ingest.ToolCall{ID: id, Name: "bash", Arguments: `{"command":"go test ./..."}`, SourceLine: i + 1})
		failed = append(failed, ingest.ToolResult{ID: "r" + id, CallID: id, IsError: boolPtr(true), Content: "same", SourceLine: i + 1})
	}
	require.Contains(t, detectorNames(Analyze(ingest.Session{ID: "s", ToolCalls: calls, ToolResults: failed})), "retry_loop")
	for i := range failed {
		failed[i].IsError = nil
		failed[i].Content = "password=secret" + string(rune('a'+i))
	}
	require.Contains(t, detectorNames(Analyze(ingest.Session{ID: "s", ToolCalls: calls, ToolResults: failed})), "retry_loop")
	for i := range failed {
		failed[i].IsError = boolPtr(true)
	}
	failed[1].Content = "different"
	failed[2].Content = "another"
	failed[3].IsError = boolPtr(false)
	require.NotContains(t, detectorNames(Analyze(ingest.Session{ID: "s", ToolCalls: calls, ToolResults: failed})), "retry_loop")
	for i := range failed {
		failed[i].IsError = boolPtr(false)
		failed[i].Content = "same success"
	}
	failed[3].IsError = boolPtr(true)
	require.NotContains(t, detectorNames(Analyze(ingest.Session{ID: "s", ToolCalls: calls, ToolResults: failed})), "retry_loop")
}

func TestSilentCloseRequiresStartedGoal(t *testing.T) {
	t.Parallel()

	messages := []ingest.Message{{ID: "a", Role: "assistant", StopReason: "stop", SourceLine: 5}}
	require.NotContains(t, detectorNames(Analyze(ingest.Session{ID: "s", Messages: messages})), "silent_close")
	started := ingest.Session{ID: "s", Messages: messages, CustomStates: []ingest.CustomState{{ID: "g", Type: "goal-state", Status: "active", SourceLine: 2}}}
	require.Contains(t, detectorNames(Analyze(started)), "silent_close")
	cleared := started
	cleared.CustomStates = append(cleared.CustomStates, ingest.CustomState{ID: "clear", Type: "goal-state", Status: "", SourceLine: 4})
	require.NotContains(t, detectorNames(Analyze(cleared)), "silent_close")
}

func TestUnverifiedCodeChangeGuards(t *testing.T) {
	t.Parallel()

	edit := ingest.ToolCall{ID: "e", Name: "edit", Arguments: `{"path":"main.go"}`, SourceLine: 2}
	require.Contains(t, detectorNames(Analyze(ingest.Session{ID: "s", ToolCalls: []ingest.ToolCall{edit}})), "unverified_code_change")
	verified := []ingest.ToolCall{edit, {ID: "v", Name: "bash", Arguments: `{"command":"go test ./..."}`, SourceLine: 3}}
	require.NotContains(t, detectorNames(Analyze(ingest.Session{ID: "s", ToolCalls: verified})), "unverified_code_change")
	compound := []ingest.ToolCall{edit, {ID: "v", Name: "bash", Arguments: `{"command":"cd src && go test ./..."}`, SourceLine: 3}}
	require.NotContains(t, detectorNames(Analyze(ingest.Session{ID: "s", ToolCalls: compound})), "unverified_code_change")
	for _, path := range []string{"docs/a.go", "config.yaml", "new.unknown"} {
		call := edit
		call.Arguments = `{"path":"` + path + `"}`
		require.NotContains(t, detectorNames(Analyze(ingest.Session{ID: "s", ToolCalls: []ingest.ToolCall{call}})), "unverified_code_change", path)
	}
}

func TestEditWithoutReadRecognizesReadsAndNewFiles(t *testing.T) {
	t.Parallel()

	edit := ingest.ToolCall{ID: "e", Name: "edit", Arguments: `{"path":"src/main.go"}`, SourceLine: 3}
	require.Contains(t, detectorNames(Analyze(ingest.Session{ID: "s", ToolCalls: []ingest.ToolCall{edit}})), "edit_without_read")
	cases := [][]ingest.ToolCall{
		{{ID: "r", Name: "read", Arguments: `{"path":"src/main.go"}`, SourceLine: 1}, edit},
		{{ID: "r", Name: "bash", Arguments: `{"command":"cat 'src/main.go'"}`, SourceLine: 1}, edit},
		{{ID: "r", Name: "bash", Arguments: `{"command":"cat main.go"}`, SourceLine: 1}, edit},
		{{ID: "w", Name: "write", Arguments: `{"path":"src/main.go"}`, SourceLine: 1}, edit},
	}
	for _, calls := range cases {
		require.NotContains(t, detectorNames(Analyze(ingest.Session{ID: "s", ToolCalls: calls})), "edit_without_read")
	}
	echo := []ingest.ToolCall{{ID: "r", Name: "bash", Arguments: `{"command":"echo src/main.go"}`, SourceLine: 1}, edit}
	require.Contains(t, detectorNames(Analyze(ingest.Session{ID: "s", ToolCalls: echo})), "edit_without_read")
	prefix := []ingest.ToolCall{{ID: "r", Name: "bash", Arguments: `{"command":"cat src/main.go.bak"}`, SourceLine: 1}, edit}
	require.Contains(t, detectorNames(Analyze(ingest.Session{ID: "s", ToolCalls: prefix})), "edit_without_read")
	spaceEdit := ingest.ToolCall{ID: "space", Name: "edit", Arguments: `{"path":"src/my file.go"}`, SourceLine: 3}
	quotedSpace := []ingest.ToolCall{{ID: "r", Name: "bash", Arguments: `{"command":"cat 'src/my file.go'"}`, SourceLine: 1}, spaceEdit}
	require.NotContains(t, detectorNames(Analyze(ingest.Session{ID: "s", ToolCalls: quotedSpace})), "edit_without_read")
	failed := ingest.Session{ID: "s", ToolCalls: []ingest.ToolCall{edit}, ToolResults: []ingest.ToolResult{{ID: "result", CallID: "e", IsError: boolPtr(true)}}}
	require.NotContains(t, detectorNames(Analyze(failed)), "edit_without_read")
}

func TestTerminationClassification(t *testing.T) {
	t.Parallel()

	provider := detectorNames(Analyze(ingest.Session{ID: "s", Messages: []ingest.Message{{ID: "a", Role: "assistant", StopReason: "error"}}}))
	require.Equal(t, Error, provider["termination"].Severity)
	user := detectorNames(Analyze(ingest.Session{ID: "s", Messages: []ingest.Message{{ID: "a", Role: "assistant", StopReason: "aborted"}}}))
	require.Equal(t, Info, user["termination"].Severity)
}
