package composition

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpstreamHeaderChangeFencesRuntimeAndRefreshesCatalog(t *testing.T) {
	for _, replacement := range []string{"next", ""} {
		t.Run("replacement="+replacement, func(t *testing.T) {
			testUpstreamHeaderChange(t, replacement)
		})
	}
}

func testUpstreamHeaderChange(t *testing.T, replacement string) {
	t.Helper()
	const initial = "default,actions,gists,issues,labels,pull_requests,users"
	replacementStarted := make(chan struct{})
	releaseReplacement := make(chan struct{})
	var releaseOnce, startedOnce, blockersOnce sync.Once
	blockersStarted := make(chan struct{}, 4)
	releaseBlockers := make(chan struct{})
	var staleCalls atomic.Int32
	defer releaseOnce.Do(func() { close(releaseReplacement) })
	defer blockersOnce.Do(func() { close(releaseBlockers) })
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
		if r.URL.Path == "/block" {
			if envelope.Method == "server/discover" {
				blockersStarted <- struct{}{}
				select {
				case <-releaseBlockers:
				case <-r.Context().Done():
					return
				}
				_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"ttlMs":0,"cacheScope":"public","supportedVersions":["2026-07-28"],"capabilities":{}}}`, envelope.ID)
			} else {
				_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[]}}`, envelope.ID)
			}
			return
		}
		tool := "old"
		if r.Header.Get("X-MCP-Toolsets") == replacement {
			tool = "new"
		} else {
			assert.Equal(t, initial, r.Header.Get("X-MCP-Toolsets"))
		}
		switch envelope.Method {
		case "server/discover":
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"ttlMs":0,"cacheScope":"public","supportedVersions":["2026-07-28"],"capabilities":{}}}`, envelope.ID)
		case "tools/list":
			if tool == "new" {
				startedOnce.Do(func() { close(replacementStarted) })
				select {
				case <-releaseReplacement:
				case <-r.Context().Done():
					return
				}
			}
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[{"name":%q,"inputSchema":{"type":"object"}}]}}`, envelope.ID, tool)
		default:
			if tool == "old" {
				staleCalls.Add(1)
			}
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[]}}`, envelope.ID)
		}
	}))
	defer server.Close()
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	built, err := New(options)
	require.NoError(t, err)
	defer built.shutdownConstructed()
	transport := contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: server.URL + "/mcp", ProtocolMode: contract.ProtocolModern, Authentication: contract.NoAuthentication{Mode: contract.AuthenticationNone}, Headers: map[string]string{"X-MCP-Toolsets": initial}}
	desired := enableCompositionServer(t, built.servers, createServerWithTransport(t, built.servers, "headers", transport))
	require.NoError(t, built.Start(t.Context()))
	startupCtx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	require.True(t, built.manager.Wait(startupCtx))
	routes := built.activeCatalog.Routes()
	require.Eventually(t, func() bool { _, ok := routes.ResolveCall("headers.old"); return ok }, 5*time.Second, time.Millisecond)
	old, ok := routes.ResolveCall("headers.old")
	require.True(t, ok)
	require.Eventually(t, func() bool { return built.RuntimeStatus(desired.ID).RuntimeID != nil }, 5*time.Second, time.Millisecond)
	oldRuntime := *built.RuntimeStatus(desired.ID).RuntimeID
	preacquired, err := old.Capability.Acquire(t.Context())
	require.NoError(t, err)
	defer func() { _ = preacquired.Cancel(t.Context()) }()
	for index := range 4 {
		blocker := createServerWithTransport(t, built.servers, fmt.Sprintf("blocker-%d", index), contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: server.URL + "/block", ProtocolMode: contract.ProtocolModern, Authentication: contract.NoAuthentication{Mode: contract.AuthenticationNone}})
		enabled := true
		activation, err := built.servers.Patch(t.Context(), blocker.ID, blocker.DesiredRevision, servers.Patch{Enabled: &enabled})
		require.NoError(t, err)
		require.NotNil(t, activation.Operation)
		built.TriggerServer(t.Context(), blocker.ID, &activation.Operation.ID, true)
	}
	for range 4 {
		select {
		case <-blockersStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("reconciliation blocker did not start")
		}
	}
	require.True(t, built.ReconciliationStatus().Saturated)
	transport.Headers = nil
	if replacement != "" {
		transport.Headers = map[string]string{"X-MCP-Toolsets": replacement}
	}
	patched, err := built.servers.Patch(t.Context(), desired.ID, desired.DesiredRevision, servers.Patch{Transport: transport})
	require.NoError(t, err)
	require.NotNil(t, patched.Operation)
	built.TriggerServer(t.Context(), desired.ID, &patched.Operation.ID, true)
	select {
	case <-replacementStarted:
		t.Fatal("replacement bypassed reconciliation admission")
	default:
	}
	_, ok = routes.ResolveCall("headers.old")
	assert.False(t, ok, "stale route survived the behavioral trigger")
	stale, err := old.Capability.Acquire(t.Context())
	assert.Error(t, err, "stale capability acquired while reconciliation was blocked")
	if stale != nil {
		_ = stale.Cancel(t.Context())
	}
	result := preacquired.Execute(t.Context(), json.RawMessage(`{}`))
	assert.Equal(t, downstream.FailurePreStart, result.Failure)
	assert.Zero(t, staleCalls.Load(), "pre-acquired lease sent old headers after the update")
	blockersOnce.Do(func() { close(releaseBlockers) })
	select {
	case <-replacementStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("replacement did not discover its catalog")
	}
	_, ok = routes.ResolveCall("headers.new")
	assert.False(t, ok, "catalog was published before discovery completed")
	releaseOnce.Do(func() { close(releaseReplacement) })
	require.Eventually(t, func() bool { _, ok := routes.ResolveCall("headers.new"); return ok }, 5*time.Second, time.Millisecond)
	require.Eventually(t, func() bool {
		current := built.RuntimeStatus(desired.ID).RuntimeID
		return current != nil && *current != oldRuntime
	}, 5*time.Second, time.Millisecond)
	_, ok = routes.ResolveCall("headers.old")
	assert.False(t, ok)
	current, ok := routes.ResolveCall("headers.new")
	require.True(t, ok)
	lease, err := current.Capability.Acquire(t.Context())
	require.NoError(t, err)
	assert.Empty(t, lease.Execute(t.Context(), json.RawMessage(`{}`)).Failure)
}
