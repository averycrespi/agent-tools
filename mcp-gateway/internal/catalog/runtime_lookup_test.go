package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/remote"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/runtimes"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTraverserUsesExactSelectedConcreteRuntime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var envelope struct {
			ID     uint64 `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(request.Body).Decode(&envelope)
		writer.Header().Set("Content-Type", "application/json")
		switch envelope.Method {
		case "server/discover":
			_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%d,"result":{"ttlMs":0,"cacheScope":"public","supportedVersions":["2026-07-28"],"capabilities":{}}}`, envelope.ID)
		case "tools/list":
			_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"one","description":"selected runtime","inputSchema":{"type":"object"}}]}}`, envelope.ID)
		}
	}))
	defer server.Close()
	transport, err := json.Marshal(contract.StreamableHTTPTransport{
		Kind: contract.TransportStreamableHTTP, URL: server.URL + "/mcp", ProtocolMode: contract.ProtocolModern,
		Authentication: contract.NoAuthentication{Mode: contract.AuthenticationNone},
	})
	require.NoError(t, err)
	candidate := runtimes.Candidate{
		Server:    servers.Server{ID: "selected-server", Namespace: "selected", DesiredRevision: "1", Transport: transport},
		RuntimeID: "selected-runtime", Generation: 1,
	}
	driver, err := runtimes.NewConcreteDriver(runtimes.ConcreteDriverOptions{
		Owner: runtimes.NewRuntimeOwner(),
		StartStdio: func(context.Context, runtimes.StdioDefinition) (downstream.StdioRuntime, error) {
			return nil, errors.New("unexpected stdio")
		},
		HTTPFactory: remote.New(remote.Options{}),
	})
	require.NoError(t, err)
	require.Equal(t, contract.RuntimeActive, driver.Reconcile(context.Background(), candidate, nil).State)
	selected, ok := driver.Runtime(candidate)
	require.True(t, ok)

	catalog, err := NewTraverser().Traverse(context.Background(), selected, candidate.Server.Namespace)

	require.NoError(t, err)
	require.Len(t, catalog.Tools, 1)
	assert.Equal(t, "one", catalog.Tools[0].UpstreamName)
	mismatch := candidate
	mismatch.Generation++
	_, ok = driver.Runtime(mismatch)
	assert.False(t, ok)
	assert.True(t, driver.Stop(context.Background(), candidate))
}
