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
		require.NotNil(t, tool.Annotations.OpenWorldHint, tool.Name)
		require.False(t, *tool.Annotations.OpenWorldHint, tool.Name)
		switch tool.Name {
		case "list_messages", "get_message":
			require.NotNil(t, tool.Annotations.ReadOnlyHint, tool.Name)
			require.True(t, *tool.Annotations.ReadOnlyHint, tool.Name)
		case "send_message", "ack_message", "resolve_message":
			require.NotNil(t, tool.Annotations.ReadOnlyHint, tool.Name)
			require.False(t, *tool.Annotations.ReadOnlyHint, tool.Name)
		}
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

	for _, tc := range []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{name: "send missing required", tool: "send_message", args: map[string]any{"sender": "agent"}, want: "subject is required"},
		{name: "list fractional limit", tool: "list_messages", args: map[string]any{"limit": 1.5}, want: "limit must be an integer"},
		{name: "list string limit", tool: "list_messages", args: map[string]any{"limit": "500"}, want: "limit must be an integer"},
		{name: "list fractional negative offset", tool: "list_messages", args: map[string]any{"offset": -0.5}, want: "offset must be an integer"},
		{name: "get missing", tool: "get_message", args: map[string]any{"id": "missing"}, want: "message not found"},
		{name: "ack missing", tool: "ack_message", args: map[string]any{"id": "missing"}, want: "message not found"},
		{name: "resolve missing", tool: "resolve_message", args: map[string]any{"id": "missing"}, want: "message not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, handleErr := h.Handle(context.Background(), gomcp.CallToolRequest{Params: gomcp.CallToolParams{Name: tc.tool, Arguments: tc.args}})
			require.NoError(t, handleErr)
			require.True(t, result.IsError)
			require.Len(t, result.Content, 1)
			text, ok := result.Content[0].(gomcp.TextContent)
			require.True(t, ok)
			require.Contains(t, text.Text, tc.want)
		})
	}
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
