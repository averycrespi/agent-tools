package runtimes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/remote"
	"github.com/stretchr/testify/require"
)

func TestNegotiationPredicatesReachDefaultRuntimeSink(t *testing.T) {
	for _, test := range []struct{ name, body, field, rule string }{
		{"ttl", `{"ttlMs":-1,"cacheScope":"public","supportedVersions":["2026-07-28"],"capabilities":{}}`, "ttlMs", "minimum"},
		{"cache", `{"ttlMs":0,"cacheScope":"private-value-canary","supportedVersions":["2026-07-28"],"capabilities":{}}`, "cacheScope", "enum"},
		{"type", `{"ttlMs":"private-value-canary","cacheScope":"public","supportedVersions":["2026-07-28"],"capabilities":{}}`, "ttlMs", "kind"},
		{"unknown", `{"private-member-canary":"private-value-canary"}`, "unknown_member", "closed_object"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var envelope struct {
					ID uint64 `json:"id"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&envelope))
				w.Header().Set("Content-Type", contract.MediaTypeJSON)
				_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":%s}`, envelope.ID, test.body)
			}))
			defer upstream.Close()
			repository := newFakeRepository(1)
			var id string
			for key, server := range repository.servers {
				id = key
				server.Transport = mustDriverTransport(t, contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: upstream.URL + "/mcp", ProtocolMode: contract.ProtocolModern, Authentication: contract.NoAuthentication{Mode: contract.AuthenticationNone}})
				repository.servers[key] = server
			}
			driver, err := NewConcreteDriver(ConcreteDriverOptions{Owner: NewRuntimeOwner(), StartStdio: func(context.Context, StdioDefinition) (downstream.StdioRuntime, error) {
				return nil, errors.New("unexpected stdio")
			}, HTTPFactory: remote.New(remote.Options{})})
			require.NoError(t, err)
			var output bytes.Buffer
			adapter := diagnostics.New(&output, diagnostics.Warn)
			t.Cleanup(func() { adapter.Finish(nil); <-adapter.Done() })
			manager, err := New(Options{Repository: repository, Driver: driver, Catalog: diagnosticCatalog{}, Scheduler: newFakeScheduler(), Diagnostics: adapter, DiagnosticReference: func(string) uint64 { return 0 }})
			require.NoError(t, err)
			t.Cleanup(func() { <-manager.Drain(context.Background()) })
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			manager.Trigger(id, nil, true)
			require.True(t, manager.Wait(ctx))
			<-manager.Drain(ctx)
			require.True(t, adapter.Finish(nil))
			require.Contains(t, output.String(), "field="+test.field+" rule="+test.rule)
			require.Contains(t, output.String(), "method=server/discover")
			require.Contains(t, output.String(), "configured_mode=modern")
			require.Contains(t, output.String(), "HTTP_status=200")
			require.Contains(t, output.String(), upstream.URL)
			require.NotContains(t, output.String(), "private-value-canary")
			require.NotContains(t, output.String(), "private-member-canary")
			require.EqualValues(t, 1, calls.Load())
		})
	}
}
