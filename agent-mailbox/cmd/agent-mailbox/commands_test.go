package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCLICommandsUseDBPathAndWireFlags(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mailbox.db")

	send := executeCommand(t, "--db-path", dbPath, "send", "--sender", "agent", "--subject", "Hello", "--body", "Body", "--severity", "action_required", "--requires-response", "--thread-id", "thread-1", "--idempotency-key", "retry-1")
	message := send["message"].(map[string]any)
	id := message["id"].(string)
	require.True(t, send["created"].(bool))
	require.Equal(t, "thread-1", message["thread_id"])
	require.Equal(t, "action_required", message["severity"])
	require.True(t, message["requires_response"].(bool))

	listed := executeCommand(t, "--db-path", dbPath, "list", "--requires-response", "--limit", "10")
	require.Equal(t, float64(1), listed["total"])

	read := executeCommand(t, "--db-path", dbPath, "read", id)
	require.Equal(t, "Body", read["message"].(map[string]any)["body"])

	acked := executeCommand(t, "--db-path", dbPath, "ack", id, "--actor", "avery")
	require.True(t, acked["changed"].(bool))
	require.Equal(t, "acknowledged", acked["message"].(map[string]any)["status"])

	resolved := executeCommand(t, "--db-path", dbPath, "resolve", id, "--actor", "avery", "--resolution", "done")
	require.True(t, resolved["changed"].(bool))
	require.Equal(t, "resolved", resolved["message"].(map[string]any)["status"])

	read = executeCommand(t, "--db-path", dbPath, "read", id)
	events := read["events"].([]any)
	require.Equal(t, "done", events[len(events)-1].(map[string]any)["payload"].(map[string]any)["resolution"])
}

func executeCommand(t *testing.T, args ...string) map[string]any {
	t.Helper()
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs(args)
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
		dbPath = ""
	})
	require.NoError(t, Execute())
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &decoded), out.String())
	return decoded
}
