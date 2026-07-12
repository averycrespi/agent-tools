package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCommandsRequireSpecificArguments(t *testing.T) {
	for _, name := range []string{"session-summary"} {
		cmd := newRootCommand()
		cmd.SetArgs([]string{name})
		err := cmd.Execute()
		require.Error(t, err)
		require.Contains(t, err.Error(), name+" requires SESSION_ID")
	}
	for _, name := range []string{"ingest", "list-sessions", "mcp"} {
		cmd := newRootCommand()
		cmd.SetArgs([]string{name, "unexpected"})
		err := cmd.Execute()
		require.Error(t, err)
		require.Contains(t, err.Error(), name+" accepts no arguments")
	}
	cmd := newRootCommand()
	cmd.SetArgs([]string{"detect", "a", "b"})
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "detect accepts at most one SESSION_ID")
}

func TestCommandWorkflowAndDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	sessions := filepath.Join(home, ".pi", "agent", "sessions")
	require.NoError(t, os.MkdirAll(sessions, 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(home, "data"), 0o755))
	fixture := `{"type":"session","version":99,"id":"session-command","cwd":"/repo"}` + "\n" +
		`{"type":"message","id":"a","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"done"},{"type":"toolCall","id":"call","name":"bash","arguments":{"command":"false"}}],"usage":{"output":2,"reasoning":3,"cacheRead":4,"cost":{"total":0.1}}}}` + "\n" +
		`{"type":"message","id":"result","message":{"role":"toolResult","toolCallId":"call","toolName":"bash","isError":true,"content":"failed"}}` + "\n" +
		`{"type":"compaction","id":"compact","tokensBefore":12000}` + "\n" +
		`{"type":"custom","id":"goal","customType":"goal-state","data":{"goal":{"status":"active"}}}` + "\n" +
		`{"type":"custom_message","id":"guard","customType":"broker-guard","details":{"kind":"credential"}}`
	require.NoError(t, os.WriteFile(filepath.Join(sessions, "s.jsonl"), []byte(fixture), 0o600))

	for _, args := range [][]string{{"ingest"}, {"list-sessions"}, {"detect", "session-c"}} {
		cmd := newRootCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs(args)
		require.NoError(t, cmd.Execute(), args)
		require.NotEmpty(t, out.String(), args)
	}
	cmd := newRootCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"session-summary", "session-c"})
	require.NoError(t, cmd.Execute())
	var summary map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &summary))
	require.Equal(t, float64(2), summary["OutputTokens"])
	require.Equal(t, float64(3), summary["ReasoningTokens"])
	require.Equal(t, float64(4), summary["CacheReadTokens"])
	require.Equal(t, float64(0), summary["CacheWriteTokens"])
	require.InDelta(t, 0.1, summary["Cost"], 0.0001)
	tools := summary["Tools"].(map[string]any)
	require.Equal(t, float64(1), tools["bash"].(map[string]any)["errors"])
	require.Equal(t, "stop", summary["FinalStopReason"])
	require.Equal(t, "active", summary["GoalState"])
	require.Equal(t, float64(1), summary["Compactions"])
	require.Equal(t, float64(1), summary["BrokerGuards"])
	require.Equal(t, float64(1), summary["SchemaDrift"])
	require.NotEmpty(t, summary["Tools"])
	require.NotEmpty(t, summary["Findings"])
	require.NotEmpty(t, summary["DetectorRuns"])
	dbPath := filepath.Join(home, "data", "pi-session-analyzer", "sessions.db")
	require.FileExists(t, dbPath)

	second := strings.Replace(fixture, "session-command", "session-companion", 1)
	require.NoError(t, os.WriteFile(filepath.Join(sessions, "second.jsonl"), []byte(second), 0o600))
	cmd = newRootCommand()
	cmd.SetArgs([]string{"ingest"})
	require.NoError(t, cmd.Execute())
	for _, tc := range []struct {
		id, message string
	}{{"session-com", "ambiguous"}, {"missing", "not found"}} {
		cmd = newRootCommand()
		cmd.SetArgs([]string{"session-summary", tc.id})
		execErr := cmd.Execute()
		require.ErrorContains(t, execErr, tc.message)
	}
}
