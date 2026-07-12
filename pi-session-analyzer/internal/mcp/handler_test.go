package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/require"
)

func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	path, _ := testDatabase(t)
	boundary := testBoundary(t, path)
	return NewHandler(boundary)
}

func TestCompleteRegistryIsClosedWorldReadOnly(t *testing.T) {
	handler := newTestHandler(t)
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
	handler := newTestHandler(t)
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

func TestResponseCapBoundsBeforeSerialization(t *testing.T) {
	large := strings.Repeat("x", 2<<20)
	result := marshalCapped(map[string]any{"huge": large})
	require.LessOrEqual(t, len(result), maxResponseBytes)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(result), &decoded))
	require.Equal(t, true, decoded["truncated"])
	require.Contains(t, decoded, "value")
	require.NotContains(t, decoded, "preview")
}

func TestGetMessageProjectionAndArgumentErrors(t *testing.T) {
	handler := newTestHandler(t)
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

func TestConcurrentHandlerReadsShareBoundarySafely(t *testing.T) {
	handler := newTestHandler(t)
	start := make(chan struct{})
	errs := make(chan error, 20)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req := gomcp.CallToolRequest{}
			if i%2 == 0 {
				req.Params.Name = "list_sessions"
			} else {
				req.Params.Name = "run_select"
				req.Params.Arguments = map[string]any{"query": "SELECT id FROM sessions"}
			}
			result, err := handler.Handle(context.Background(), req)
			if err == nil && result.IsError {
				err = fmt.Errorf("tool returned error: %v", result.Content)
			}
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
}

func TestConversationCountIsBounded(t *testing.T) {
	handler := newTestHandler(t)
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
