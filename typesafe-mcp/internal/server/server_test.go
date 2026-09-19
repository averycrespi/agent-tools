package server

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/typesafe-mcp/internal/provider"
	"github.com/stretchr/testify/require"
)

func TestStrictCatalogAndInputValidation(t *testing.T) {
	client, err := provider.New("fixture")
	require.NoError(t, err)
	srv := New(client)
	listed := srv.HandleMessage(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	raw, err := json.Marshal(listed)
	require.NoError(t, err)
	var envelope struct {
		Result struct {
			Tools []struct {
				Name        string
				InputSchema map[string]any
				Annotations map[string]bool
			}
		}
	}
	require.NoError(t, json.Unmarshal(raw, &envelope))
	require.Len(t, envelope.Result.Tools, 2)
	for _, tool := range envelope.Result.Tools {
		require.Equal(t, false, tool.InputSchema["additionalProperties"])
		require.False(t, tool.Annotations["destructiveHint"])
		require.True(t, tool.Annotations["openWorldHint"])
		require.Equal(t, tool.Name == "list_models", tool.Annotations["readOnlyHint"])
		require.Equal(t, tool.Name == "list_models", tool.Annotations["idempotentHint"])
	}
	// A nil client proves invalid inputs are rejected by server validation, before the handler.
	srv = New(nil)
	for _, params := range []string{`{"name":"list_models","arguments":{"url":"https://evil.test"}}`, `{"name":"evaluate","arguments":{"state":true,"questions":{}}}`, `{"name":"evaluate","arguments":{"state":"x","questions":{"q":{"type":"noul","instructions":"x","extra":true}}}}`} {
		response := srv.HandleMessage(t.Context(), json.RawMessage(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":`+params+`}`))
		raw, err = json.Marshal(response)
		require.NoError(t, err)
		require.Contains(t, string(raw), `"isError":true`)
	}
}
func TestBoundedFrames(t *testing.T) {
	for _, s := range []string{strings.Repeat("x", MaxFrameBytes+1) + "\n", `{"x":1}`, "{\"x\":1,\"x\":2}\n"} {
		err := Serve(t.Context(), nil, strings.NewReader(s), io.Discard)
		require.Error(t, err)
		require.NotContains(t, err.Error(), s)
	}
}
