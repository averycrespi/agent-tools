package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

func TestCompleteRegistryIsClosedWorldReadOnly(t *testing.T) {
	path, db := testDatabase(t)
	handler := NewHandler(db, path)
	tools := handler.Tools()
	require.Len(t, tools, 6)
	names := []string{}
	for _, tool := range tools {
		names = append(names, tool.Name)
		require.NotNil(t, tool.Annotations.ReadOnlyHint)
		require.True(t, *tool.Annotations.ReadOnlyHint)
		require.NotNil(t, tool.Annotations.DestructiveHint)
		require.False(t, *tool.Annotations.DestructiveHint)
		require.NotNil(t, tool.Annotations.OpenWorldHint)
		require.False(t, *tool.Annotations.OpenWorldHint)
	}
	require.ElementsMatch(t, []string{"list_sessions", "session_summary", "top_failures", "get_conversation", "get_message", "run_select"}, names)
}

func TestAllHandlerResponsesUseValidJSONCap(t *testing.T) {
	path, db := testDatabase(t)
	handler := NewHandler(db, path)
	req := gomcp.CallToolRequest{}
	req.Params.Name = "run_select"
	req.Params.Arguments = map[string]any{"query": `SELECT printf('%.*c', 60000, 'x') AS huge`}
	result, err := handler.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)
	text := result.Content[0].(gomcp.TextContent).Text
	require.LessOrEqual(t, len(text), maxResponseBytes)
	var decoded any
	require.NoError(t, json.Unmarshal([]byte(text), &decoded))
	require.Contains(t, text, `"truncated":true`)
}

func TestGetMessageProjectionAndArgumentErrors(t *testing.T) {
	path, db := testDatabase(t)
	handler := NewHandler(db, path)
	req := gomcp.CallToolRequest{}
	req.Params.Name = "get_message"
	req.Params.Arguments = map[string]any{"session_id": "s", "message_id": "m000", "path": "message.Text"}
	result, err := handler.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)
	var projection map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(gomcp.TextContent).Text), &projection))
	require.Equal(t, "message", projection["value"])

	req.Params.Arguments = map[string]any{"session_id": "s", "message_id": "missing"}
	result, err = handler.Handle(context.Background(), req)
	require.NoError(t, err)
	require.True(t, result.IsError)
	req.Params.Name = "unknown"
	result, err = handler.Handle(context.Background(), req)
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.True(t, strings.Contains(result.Content[0].(gomcp.TextContent).Text, "unknown tool"))
}

func TestConversationCountIsBounded(t *testing.T) {
	path, db := testDatabase(t)
	handler := NewHandler(db, path)
	req := gomcp.CallToolRequest{}
	req.Params.Name = "get_conversation"
	req.Params.Arguments = map[string]any{"session_id": "s", "max_messages": 1000}
	result, err := handler.Handle(context.Background(), req)
	require.NoError(t, err)
	require.False(t, result.IsError)
	var entries []any
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(gomcp.TextContent).Text), &entries))
	require.Len(t, entries, 100)
}
