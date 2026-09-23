package composition

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/accesstarget"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/mcpingress"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The former single-store wait workload now proves count-one traffic evidence
// and diagnostic isolation through the same real HTTP ingress/downstream seam.
func TestIngressConcurrencyFourAuditWaitWorkload(t *testing.T) {
	for _, mode := range []string{"warn", "debug", "stalled"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			var output bytes.Buffer
			var sink io.Writer = &output
			var reader, writer *os.File
			if mode == "stalled" {
				var err error
				reader, writer, err = os.Pipe()
				require.NoError(t, err)
				sink = writer
			}
			level := diagnostics.Debug
			if mode == "warn" {
				level = diagnostics.Warn
			}
			diagnostic := diagnostics.New(sink, level)
			defer func() {
				diagnostic.Finish(nil)
				if reader != nil {
					require.NoError(t, reader.Close())
				}
				<-diagnostic.Done()
				if writer != nil {
					require.NoError(t, writer.Close())
				}
				for _, secret := range []string{"parallel.read", "argument-secret-canary", "downstream-secret-canary"} {
					assert.NotContains(t, output.String(), secret)
				}
			}()
			var calls, active, peak atomic.Int32
			var mu sync.Mutex
			arrived := 0
			barrier := make(chan struct{})
			downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var message struct {
					ID     uint64 `json:"id"`
					Method string `json:"method"`
				}
				if json.NewDecoder(r.Body).Decode(&message) != nil {
					w.WriteHeader(400)
					return
				}
				w.Header().Set("Content-Type", contract.MediaTypeJSON)
				result := `{"ttlMs":0,"cacheScope":"public","supportedVersions":["2026-07-28"],"capabilities":{}}`
				switch message.Method {
				case "tools/list":
					result = `{"tools":[{"name":"read","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":true}}]}`
				case "tools/call":
					calls.Add(1)
					current := active.Add(1)
					defer active.Add(-1)
					for prior := peak.Load(); current > prior && !peak.CompareAndSwap(prior, current); prior = peak.Load() {
					}
					mu.Lock()
					group := barrier
					arrived++
					if arrived == 4 {
						close(group)
						arrived = 0
						barrier = make(chan struct{})
					}
					mu.Unlock()
					select {
					case <-group:
					case <-ctx.Done():
						return
					}
					result = `{"content":[{"type":"text","text":"downstream-secret-canary"}]}`
				}
				_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":%s}`, message.ID, result)
			}))
			defer downstream.Close()
			options, cleanup := newCompositionOptions(t)
			defer cleanup()
			observed := &contentionDiagnostics{Observer: diagnostic}
			options.Diagnostics = observed
			options.Clock = testutil.NewFakeClock(compositionTime.Add(30 * time.Second))
			built, err := New(options)
			require.NoError(t, err)
			defer built.shutdownConstructed()
			server := enableCompositionServer(t, built.servers, createServerWithTransport(t, built.servers, "parallel", contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: downstream.URL + "/mcp", ProtocolMode: contract.ProtocolModern, Authentication: contract.NoAuthentication{Mode: contract.AuthenticationNone}}))
			principal, err := built.authorization.CreatePrincipal(ctx, authorization.CreatePrincipalRequest{DisplayName: "parallel fixture", Visibility: contract.VisibilityAll})
			require.NoError(t, err)
			credential, err := built.authorization.IssueCredential(ctx, principal.Principal.ID, principal.Principal.Revision)
			require.NoError(t, err)
			_, err = built.authorization.CreateGrant(ctx, authorization.CreateGrantRequest{PrincipalID: principal.Principal.ID, Effect: contract.GrantAllow, Target: accesstarget.MCP{ServerID: server.ID}}, func(context.Context, *sql.Tx, string) (bool, error) { return true, nil })
			require.NoError(t, err)
			require.NoError(t, built.Start(ctx))
			require.True(t, built.manager.Wait(ctx))
			bundle, ok := built.AgentIngress()
			require.True(t, ok)
			ingress := mcpingress.New(mcpingress.Options{Authenticator: bundle.Authenticator, ListTools: bundle.ListTools, CallTools: bundle.CallTools})
			defer ingress.Shutdown()
			request := func(method string) error {
				params := `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"fixture","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}`
				if method == "tools/call" {
					params += `,"name":"parallel.read","arguments":{"argument-secret-canary":"argument-secret-canary"}`
				}
				r := newAgentRequest(credential.Bearer, fmt.Sprintf(`{"jsonrpc":"2.0","id":"fixture","method":%q,"params":{%s}}`, method, params)).WithContext(ctx)
				authenticated, err := ingress.Authenticate(ctx, r, contract.AuthorityAgent)
				if err != nil {
					return err
				}
				response := httptest.NewRecorder()
				ingress.ServeHTTP(response, r.WithContext(authenticated))
				var result struct {
					Result json.RawMessage `json:"result"`
					Error  json.RawMessage `json:"error"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
					return err
				}
				if len(result.Result) == 0 || len(result.Error) != 0 {
					status := built.traffic.Status(ctx)
					return fmt.Errorf("%s did not succeed: %s (authority_expired=%d traffic_ready=%t traffic_faulted=%t traffic_pressure=%t quota_refusals=%d context_done=%t)", method, result.Error, observed.authorityExpired.Load(), status.Ready, status.Faulted, status.Pressure, status.QuotaRefusals, ctx.Err() != nil)
				}
				return nil
			}
			done := make(chan error, 4)
			for worker := range 4 {
				go func() {
					for iteration := range 8 {
						if err := request("tools/list"); err != nil {
							done <- fmt.Errorf("worker %d iteration %d: %w", worker, iteration, err)
							return
						}
						if err := request("tools/call"); err != nil {
							done <- fmt.Errorf("worker %d iteration %d: %w", worker, iteration, err)
							return
						}
					}
					done <- nil
				}()
			}
			for range 4 {
				require.NoError(t, <-done)
			}
			require.EqualValues(t, 32, calls.Load())
			require.EqualValues(t, 4, peak.Load())
			history, err := built.traffic.History(ctx, 0, 64)
			require.NoError(t, err)
			require.Len(t, history.Records, 32)
			for _, record := range history.Records {
				require.NotNil(t, record.TerminalClass)
				assert.Equal(t, contract.TerminalSucceeded, *record.TerminalClass)
			}
			var legacy int
			require.NoError(t, options.Store.View(ctx, func(tx *sql.Tx) error {
				return tx.QueryRowContext(ctx, `SELECT count(*) FROM invocations`).Scan(&legacy)
			}))
			assert.Zero(t, legacy, "no traffic dual write into control storage")
			assert.False(t, options.Store.Latched())
		})
	}
}

// Keep the real sink and backpressure behavior; retain only a typed rejection
// counter even when the intentionally stalled diagnostic pipe cannot be read.
type contentionDiagnostics struct {
	diagnostics.Observer
	authorityExpired atomic.Int32
}

func (observed *contentionDiagnostics) Authority(facts diagnostics.Facts) {
	observed.Observer.Authority(facts)
	if facts.Event == diagnostics.AuthorityReject && facts.Cause == diagnostics.Expired {
		observed.authorityExpired.Add(1)
	}
}

func TestInvocationOverloadBehindCatalogDelaysAuthorityButNotForeignRejection(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	built, err := New(options)
	require.NoError(t, err)
	defer built.shutdownConstructed()
	principal, err := built.authorization.CreatePrincipal(ctx, authorization.CreatePrincipalRequest{DisplayName: "independent traffic", Visibility: contract.VisibilityAll})
	require.NoError(t, err)
	credential, err := built.authorization.IssueCredential(ctx, principal.Principal.ID, principal.Principal.Revision)
	require.NoError(t, err)
	lease, err := built.authorization.Authenticate(ctx, credential.Bearer)
	require.NoError(t, err)
	defer lease.Release()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	done := make(chan error, 1)
	go func() {
		done <- options.Store.Mutate(ctx, func(*sql.Tx) error {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return nil
		})
	}()
	<-entered
	response := built.callTools.Call(ctx, lease, mcpingress.ToolsCallRequest{Params: contentionCallParams(), WireValid: true})
	require.NotNil(t, response.Result, "traffic commits independently while the unrelated control writer is held")
	owned, waiting := options.Store.MutationOccupancy()
	require.True(t, owned)
	require.Zero(t, waiting, "traffic never joins the control writer wait queue")
	err = options.Store.Mutate(ctx, func(*sql.Tx) error { return nil })
	require.ErrorIs(t, err, storage.ErrMutationBusy)
	fresh, err := built.authorization.Authenticate(ctx, credential.Bearer)
	require.NoError(t, err, "traffic has not retained the authority gate")
	fresh.Release()
	unblock()
	require.NoError(t, <-done)
}

func contentionCallParams() strictjson.Value {
	return strictjson.Value{Type: strictjson.ValueObject, Object: []strictjson.Member{
		{Name: "name", Value: strictjson.Value{Type: strictjson.ValueString, String: "mcp_gateway.get_identity"}},
		{Name: "arguments", Value: strictjson.Value{Type: strictjson.ValueObject}},
	}}
}

func TestCompositionDrainWakesInvocationStorageWaitBeforeForeignCleanup(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	built, err := New(options)
	require.NoError(t, err)
	built.authorization.BeginDrain()
	built.traffic.BeginDrain()
	response := built.callTools.Call(t.Context(), nil, mcpingress.ToolsCallRequest{Params: contentionCallParams(), WireValid: true})
	assert.Equal(t, contract.AuditUnavailable, response.ErrorCode)
	assert.Empty(t, response.InvocationID)
	<-built.Drain(t.Context())
	require.NoError(t, options.Store.Mutate(t.Context(), func(*sql.Tx) error { return nil }), "control cleanup remains independently available")
	history := trafficAfterDrain(t, options)
	assert.Empty(t, history.Records)
}
