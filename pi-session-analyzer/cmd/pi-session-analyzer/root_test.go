package main

import (
	"bytes"
	"os"
	"path/filepath"
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
	fixture := `{"type":"session","version":3,"id":"session-command","cwd":"/repo"}` + "\n" + `{"type":"message","id":"a","message":{"role":"assistant","stopReason":"stop","content":"done","usage":{"output":2,"reasoning":3,"cacheRead":4,"cost":{"total":0.1}}}}`
	require.NoError(t, os.WriteFile(filepath.Join(sessions, "s.jsonl"), []byte(fixture), 0o600))

	for _, args := range [][]string{{"ingest"}, {"list-sessions"}, {"session-summary", "session-c"}, {"detect", "session-c"}} {
		cmd := newRootCommand()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs(args)
		require.NoError(t, cmd.Execute(), args)
		require.NotEmpty(t, out.String(), args)
	}
	require.FileExists(t, filepath.Join(home, "data", "pi-session-analyzer", "sessions.db"))
}
