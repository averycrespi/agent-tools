package composition

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrivateHTTPRevocationAloneFencesPublishedRuntime(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			ID     uint64 `json:"id"`
			Method string `json:"method"`
		}
		if json.NewDecoder(r.Body).Decode(&envelope) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", contract.MediaTypeJSON)
		switch envelope.Method {
		case "server/discover":
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"ttlMs":0,"cacheScope":"public","supportedVersions":["2026-07-28"],"capabilities":{}}}`, envelope.ID)
		case "tools/list":
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":"echo","inputSchema":{"type":"object"}}]}}`, envelope.ID)
		default:
			calls.Add(1)
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[]}}`, envelope.ID)
		}
	}))
	defer server.Close()
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	built, err := New(options)
	require.NoError(t, err)
	defer built.shutdownConstructed()
	// Loopback remains valid after revocation, isolating the policy bit as the
	// sole behavioral change. Private-DNS dialing has its own driver fixture.
	transport := contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: server.URL + "/mcp", ProtocolMode: contract.ProtocolModern, Authentication: contract.NoAuthentication{Mode: contract.AuthenticationNone}, AllowPrivateHTTP: true}
	desired := enableCompositionServer(t, built.servers, createServerWithTransport(t, built.servers, "private", transport))
	require.NoError(t, built.Start(t.Context()))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	require.True(t, built.manager.Wait(ctx))
	routes := built.activeCatalog.Routes()
	old, ok := routes.ResolveCall("private.echo")
	require.True(t, ok)
	oldRuntime := built.RuntimeStatus(desired.ID).RuntimeID
	require.NotNil(t, oldRuntime)
	lease, err := old.Capability.Acquire(t.Context())
	require.NoError(t, err)
	defer func() { _ = lease.Cancel(t.Context()) }()
	transport.AllowPrivateHTTP = false
	patched, err := built.servers.Patch(t.Context(), desired.ID, desired.DesiredRevision, servers.Patch{Transport: transport})
	require.NoError(t, err)
	require.NotNil(t, patched.Operation)
	built.TriggerServer(t.Context(), desired.ID, &patched.Operation.ID, true)
	stale, err := old.Capability.Acquire(t.Context())
	require.Error(t, err)
	if stale != nil {
		_ = stale.Cancel(t.Context())
	}
	assert.Equal(t, downstream.FailurePreStart, lease.Execute(t.Context(), json.RawMessage(`{}`)).Failure)
	assert.Zero(t, calls.Load(), "old lease dispatched after permission revocation")
	require.True(t, built.manager.Wait(ctx))
	require.Eventually(t, func() bool {
		current := built.RuntimeStatus(desired.ID).RuntimeID
		return current != nil && *current != *oldRuntime
	}, 5*time.Second, time.Millisecond)
	current, ok := routes.ResolveCall("private.echo")
	require.True(t, ok)
	fresh, err := current.Capability.Acquire(t.Context())
	require.NoError(t, err)
	assert.Empty(t, fresh.Execute(t.Context(), json.RawMessage(`{}`)).Failure)
	assert.EqualValues(t, 1, calls.Load())
}
