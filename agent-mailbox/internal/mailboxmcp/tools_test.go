package mailboxmcp

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-mailbox/internal/store"
	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

func TestToolsExposeMVPContract(t *testing.T) {
	h := NewHandler(nil)
	tools := h.Tools()
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
		require.Equal(t, "object", tool.InputSchema.Type)
		require.NotNil(t, tool.InputSchema.Properties)
	}
	require.Equal(t, []string{"send_message", "list_messages", "get_message", "ack_message", "resolve_message"}, names)
}

func TestSendListGetAckResolve(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "mailbox.db"))
	require.NoError(t, err)
	defer st.Close() //nolint:errcheck
	h := NewHandler(st)

	send := callTool(t, h, "send_message", map[string]any{"sender": "agent", "subject": "Hello", "body": "Body", "severity": "warning", "requires_response": true, "idempotency_key": "k1"})
	require.True(t, send["created"].(bool))
	message := send["message"].(map[string]any)
	id := message["id"].(string)
	require.Equal(t, "new", message["status"])

	sendAgain := callTool(t, h, "send_message", map[string]any{"sender": "agent", "subject": "Different", "body": "Body", "idempotency_key": "k1"})
	require.False(t, sendAgain["created"].(bool))
	require.Equal(t, id, sendAgain["message"].(map[string]any)["id"])

	listed := callTool(t, h, "list_messages", map[string]any{"status": "new", "limit": float64(10)})
	require.Equal(t, float64(1), listed["total"])
	messages := listed["messages"].([]any)
	require.Equal(t, id, messages[0].(map[string]any)["id"])
	require.NotContains(t, messages[0].(map[string]any), "body")

	detail := callTool(t, h, "get_message", map[string]any{"id": id})
	require.Equal(t, "Body", detail["message"].(map[string]any)["body"])
	require.Len(t, detail["events"].([]any), 1)

	acked := callTool(t, h, "ack_message", map[string]any{"id": id, "actor": "avery"})
	require.True(t, acked["changed"].(bool))
	require.Equal(t, "acknowledged", acked["message"].(map[string]any)["status"])

	resolved := callTool(t, h, "resolve_message", map[string]any{"id": id, "resolution": "fixed"})
	require.True(t, resolved["changed"].(bool))
	require.Equal(t, "resolved", resolved["message"].(map[string]any)["status"])
	detail = callTool(t, h, "get_message", map[string]any{"id": id})
	events := detail["events"].([]any)
	require.Equal(t, "fixed", events[len(events)-1].(map[string]any)["payload"].(map[string]any)["resolution"])
}

func TestValidationErrorsReturnToolErrors(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "mailbox.db"))
	require.NoError(t, err)
	defer st.Close() //nolint:errcheck
	h := NewHandler(st)

	result, err := h.Handle(context.Background(), gomcp.CallToolRequest{Params: gomcp.CallToolParams{Name: "send_message", Arguments: map[string]any{"sender": "agent"}}})
	require.NoError(t, err)
	require.True(t, result.IsError)
}

func callTool(t *testing.T, h *Handler, name string, args map[string]any) map[string]any {
	t.Helper()
	result, err := h.Handle(context.Background(), gomcp.CallToolRequest{Params: gomcp.CallToolParams{Name: name, Arguments: args}})
	require.NoError(t, err)
	require.False(t, result.IsError)
	require.Len(t, result.Content, 1)
	text, ok := result.Content[0].(gomcp.TextContent)
	require.True(t, ok)
	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(text.Text), &out))
	return out
}
