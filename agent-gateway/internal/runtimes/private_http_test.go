package runtimes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type privateServiceResolver struct{ calls atomic.Int32 }

func (r *privateServiceResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	r.calls.Add(1)
	if host != "notes-mcp" {
		return nil, fmt.Errorf("unexpected fixture hostname")
	}
	return []netip.Addr{netip.MustParseAddr("172.18.0.2")}, nil
}

func TestConcreteDriverPrivateServiceHTTPIsScopedAndUnauthenticated(t *testing.T) {
	var requests, dials atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		assert.Equal(t, "notes-mcp:8080", r.Host)
		assert.Empty(t, r.Header.Get("Authorization"))
		assert.Equal(t, "ordinary", r.Header.Get("X-Fixture"))
		var envelope struct {
			ID     uint64 `json:"id"`
			Method string `json:"method"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&envelope))
		w.Header().Set("Content-Type", "application/json")
		switch envelope.Method {
		case "server/discover":
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"ttlMs":0,"cacheScope":"public","supportedVersions":["2026-07-28"],"capabilities":{}}}`, envelope.ID)
		case "tools/list":
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[]}}`, envelope.ID)
		default:
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[]}}`, envelope.ID)
		}
	}))
	defer server.Close()
	resolver := new(privateServiceResolver)
	// This test-only dial maps the validated private service address onto the owned
	// loopback listener. It does not qualify container DNS/network reachability.
	factory := remote.New(remote.Options{Resolver: resolver, DialContext: func(ctx context.Context, network, target string) (net.Conn, error) {
		require.Equal(t, "172.18.0.2:8080", target)
		dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(server.URL, "http://"))
	}})
	driver, err := NewConcreteDriver(ConcreteDriverOptions{Owner: NewRuntimeOwner(), HTTPFactory: factory, StartStdio: func(context.Context, StdioDefinition) (downstream.StdioRuntime, error) {
		return nil, errors.New("unexpected stdio")
	}})
	require.NoError(t, err)
	transport := contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: "http://notes-mcp:8080/mcp", ProtocolMode: contract.ProtocolModern, Authentication: contract.NoAuthentication{Mode: contract.AuthenticationNone}, AllowPrivateHTTP: true, Headers: map[string]string{"X-Fixture": "ordinary"}}
	candidate := ownerCandidate(501, contract.TransportStreamableHTTP)
	candidate.Server.Transport = mustDriverTransport(t, transport)
	outcome := driver.Reconcile(t.Context(), candidate, nil)
	require.Equal(t, contract.RuntimeActive, outcome.State)
	runtime, ok := driver.Runtime(candidate)
	require.True(t, ok)
	_, err = runtime.Request(t.Context(), "tools/list", json.RawMessage(`{"cursor":""}`), "")
	require.NoError(t, err)
	call, err := runtime.NewCall("fixture", json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.Empty(t, call.Execute(t.Context()).Failure)
	require.True(t, driver.Stop(t.Context(), candidate))
	before := requests.Load()
	assert.Equal(t, before, dials.Load())
	assert.Equal(t, before, resolver.calls.Load(), "every exchange resolves afresh")
	// A second server with no opt-in must not inherit the first server's permission.
	transport.AllowPrivateHTTP = false
	other := ownerCandidate(502, contract.TransportStreamableHTTP)
	other.Server.Transport = mustDriverTransport(t, transport)
	outcome = driver.Reconcile(t.Context(), other, nil)
	assert.NotEqual(t, contract.RuntimeActive, outcome.State)
	assert.Equal(t, before, requests.Load())
}
