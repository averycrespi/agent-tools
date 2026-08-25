package runtimes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/remote"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servercredentials"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type driverWriteCloser struct{ bytes.Buffer }

func (*driverWriteCloser) Close() error { return nil }

type driverStdioRuntime struct {
	frames    chan []byte
	done      chan StdioExit
	input     *driverWriteCloser
	stop      bool
	stopCalls int
}

func newDriverStdioRuntime(stop bool) *driverStdioRuntime {
	return &driverStdioRuntime{frames: make(chan []byte), input: new(driverWriteCloser), stop: stop}
}

func newDriverStdioRuntimeWithFrames(stop bool, frames ...string) *driverStdioRuntime {
	runtime := &driverStdioRuntime{frames: make(chan []byte, len(frames)), input: new(driverWriteCloser), stop: stop}
	for _, frame := range frames {
		runtime.frames <- []byte(frame)
	}
	return runtime
}

func (runtime *driverStdioRuntime) Frames() <-chan []byte  { return runtime.frames }
func (runtime *driverStdioRuntime) Done() <-chan StdioExit { return runtime.done }
func (runtime *driverStdioRuntime) Input() io.WriteCloser  { return runtime.input }
func (runtime *driverStdioRuntime) Stop(context.Context) bool {
	runtime.stopCalls++
	return runtime.stop
}

type driverScriptedTransport struct {
	mu       sync.Mutex
	delegate downstream.Transport
	messages []downstream.Message
	closed   bool
}

func (transport *driverScriptedTransport) Kind() downstream.TransportKind {
	return transport.delegate.Kind()
}
func (transport *driverScriptedTransport) Exchange(_ context.Context, message downstream.Message) (downstream.WireResponse, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.messages = append(transport.messages, message)
	member := `{"result":{"ttlMs":0,"cacheScope":"public","supportedVersions":["2026-07-28"],"capabilities":{}}}`
	switch message.Method {
	case "initialize":
		member = `{"result":{"protocolVersion":"2025-11-25","capabilities":{},"serverInfo":{"name":"fixture","version":"1"}}}`
	case "tools/list":
		member = `{"result":{"tools":[]}}`
	}
	return driverJSONResponse(message, member), nil
}
func (*driverScriptedTransport) Notify(context.Context, downstream.Message) (downstream.WireResponse, error) {
	return downstream.WireResponse{StatusCode: 202, ContentType: "application/json"}, nil
}
func (transport *driverScriptedTransport) Close(ctx context.Context) error {
	transport.mu.Lock()
	transport.closed = true
	transport.mu.Unlock()
	return transport.delegate.Close(ctx)
}

func driverJSONResponse(message downstream.Message, member string) downstream.WireResponse {
	var request struct {
		ID uint64 `json:"id"`
	}
	_ = json.Unmarshal(message.Payload, &request)
	return downstream.WireResponse{Body: []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,%s}`, request.ID, member[1:len(member)-1]))}
}

func driverCoordinatorFactory(transport downstream.Transport) (*downstream.Coordinator, error) {
	return downstream.NewCoordinator(&driverScriptedTransport{delegate: transport})
}

type driverChallengeTransport struct{ delegate downstream.Transport }

func (transport *driverChallengeTransport) Kind() downstream.TransportKind {
	return transport.delegate.Kind()
}
func (*driverChallengeTransport) Exchange(context.Context, downstream.Message) (downstream.WireResponse, error) {
	return downstream.WireResponse{StatusCode: http.StatusUnauthorized, OAuthChallenge: &downstream.OAuthChallengeDisposition{Kind: downstream.OAuthChallengeRefresh}}, nil
}
func (*driverChallengeTransport) Notify(context.Context, downstream.Message) (downstream.WireResponse, error) {
	return downstream.WireResponse{}, errors.New("unexpected notification")
}
func (transport *driverChallengeTransport) Close(ctx context.Context) error {
	return transport.delegate.Close(ctx)
}

func TestConcreteDriverRetainsChallengedHandleUntilVerifiedManagerStop(t *testing.T) {
	for _, mode := range []contract.ProtocolMode{contract.ProtocolModern, contract.ProtocolLegacy} {
		t.Run(string(mode), func(t *testing.T) {
			owner := NewRuntimeOwner()
			candidate := ownerCandidate(59, contract.TransportStreamableHTTP)
			candidate.Server.Transport = mustDriverTransport(t, contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: "http://127.0.0.1:9000/mcp", ProtocolMode: mode, Authentication: contract.NoAuthentication{Mode: contract.AuthenticationNone}})
			driver, err := NewConcreteDriver(ConcreteDriverOptions{Owner: owner, StartStdio: func(context.Context, StdioDefinition) (downstream.StdioRuntime, error) {
				return nil, errors.New("unexpected stdio")
			}, HTTPFactory: remote.New(remote.Options{}), NewCoordinator: func(transport downstream.Transport) (*downstream.Coordinator, error) {
				return downstream.NewCoordinator(&driverChallengeTransport{delegate: transport})
			}})
			require.NoError(t, err)

			outcome := driver.Reconcile(context.Background(), candidate, nil)

			require.NotNil(t, outcome.OAuthChallenge)
			assert.Equal(t, int64(1), owner.Status().InUse)
			assert.True(t, driver.Stop(context.Background(), candidate))
			assert.Equal(t, int64(0), owner.Status().InUse)
		})
	}
}

func TestReplayNegotiationModeSelectsExactChallengedStage(t *testing.T) {
	desired := contract.StreamableHTTPTransport{ProtocolMode: contract.ProtocolAuto}
	assert.Equal(t, downstream.ModeModern, replayNegotiationMode(desired, downstream.OAuthChallengeModernDiscovery))
	assert.Equal(t, downstream.ModeLegacy, replayNegotiationMode(desired, downstream.OAuthChallengeLegacyInitialize))
	assert.Equal(t, downstream.ModeAuto, replayNegotiationMode(desired, downstream.OAuthChallengeCatalogFirstPage))
	assert.Equal(t, downstream.ModeAuto, replayNegotiationMode(desired, ""))
}

func TestConstructionFailureCarriesOAuthChallengeDisposition(t *testing.T) {
	disposition := &downstream.OAuthChallengeDisposition{Kind: downstream.OAuthChallengeRefresh, Stage: downstream.OAuthChallengeModernDiscovery, Metadata: []string{"https://resource.example/metadata"}}

	outcome := constructionFailure(disposition)

	assert.Equal(t, contract.RuntimeAuthenticationRequired, outcome.State)
	assert.Equal(t, contract.ServerCredentialUnavailable, outcome.CredentialState)
	assert.Equal(t, contract.ReasonAuthenticationRejected, *outcome.Reason)
	assert.Same(t, disposition, outcome.OAuthChallenge)
	assert.False(t, outcome.Retryable)
}

func TestConcreteDriverOwnsStdioBeforeConstructionAndStopsExactly(t *testing.T) {
	owner := NewRuntimeOwner()
	candidate := ownerCandidate(60, contract.TransportStdio)
	candidate.Server.Transport = []byte(`{"kind":"stdio","executable":"/bin/server","arguments":["--flag"],"working_directory":"/tmp","environment":{"SAFE":"value"},"secret_environment":{"TOKEN":"api"}}`)
	generation, err := servercredentials.EncodeStaticGeneration(map[string]string{"api": "stdio-canary"})
	require.NoError(t, err)
	lease, err := NewMaterialLease(candidate.Key(), map[contract.ServerCredentialKind][]byte{contract.ServerCredentialStatic: generation})
	require.NoError(t, err)
	process := newDriverStdioRuntime(true)
	var definition StdioDefinition
	driver, err := NewConcreteDriver(ConcreteDriverOptions{
		Owner: owner,
		StartStdio: func(_ context.Context, received StdioDefinition) (downstream.StdioRuntime, error) {
			phase, ok := owner.Phase(candidate.Key())
			require.True(t, ok)
			assert.Equal(t, RuntimeConstructing, phase)
			definition = received
			definition.Secrets = cloneStrings(received.Secrets)
			return process, nil
		},
		HTTPFactory:    remote.New(remote.Options{}),
		NewCoordinator: driverCoordinatorFactory,
	})
	require.NoError(t, err)

	outcome := driver.Reconcile(context.Background(), candidate, lease)

	assert.Equal(t, contract.RuntimeActive, outcome.State)
	assert.Nil(t, outcome.Reason)
	assert.Equal(t, candidate.RuntimeID, definition.RuntimeID)
	assert.Equal(t, "/bin/server", definition.Executable)
	assert.Equal(t, []string{"--flag"}, definition.Arguments)
	assert.Equal(t, map[string]string{"SAFE": "value"}, definition.Environment)
	assert.Equal(t, map[string]string{"TOKEN": "api"}, definition.SecretEnvironment)
	assert.Equal(t, map[string]string{"api": "stdio-canary"}, definition.Secrets)
	phase, ok := owner.Phase(candidate.Key())
	require.True(t, ok)
	assert.Equal(t, RuntimeCataloging, phase)
	runtime, ok := driver.Runtime(candidate)
	require.True(t, ok)
	assert.Equal(t, downstream.EraModern, runtime.Era())
	coordinator, ok := driver.Coordinator(candidate)
	require.True(t, ok)
	assert.Equal(t, downstream.TransportStdio, coordinator.Kind())

	mismatch := candidate
	mismatch.Generation++
	assert.False(t, driver.Stop(context.Background(), mismatch))
	assert.True(t, driver.Stop(context.Background(), candidate))
	assert.Equal(t, 1, process.stopCalls)
	assert.Equal(t, int64(0), owner.Status().InUse)
	assert.False(t, driver.Stop(context.Background(), candidate))
}

func TestConcreteDriverConstructsHardenedHTTPAuthorization(t *testing.T) {
	tests := []struct {
		name           string
		authentication contract.HTTPAuthentication
		lease          func(Candidate) *MaterialLease
		wantAuth       string
	}{
		{name: "none", authentication: contract.NoAuthentication{Mode: contract.AuthenticationNone}},
		{name: "bearer", authentication: contract.BearerAuthentication{Mode: contract.AuthenticationBearer}, wantAuth: "Bearer bearer-canary", lease: func(candidate Candidate) *MaterialLease {
			generation, _ := servercredentials.EncodeStaticGeneration(map[string]string{"bearer": "bearer-canary"})
			lease, _ := NewMaterialLease(candidate.Key(), map[contract.ServerCredentialKind][]byte{contract.ServerCredentialStatic: generation})
			return lease
		}},
		{name: "oauth", authentication: contract.OAuthAuthentication{Mode: contract.AuthenticationOAuth, Registration: contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic}, TrustedOrigins: []string{}}, wantAuth: "Bearer oauth-canary", lease: func(candidate Candidate) *MaterialLease {
			lease, _ := NewOAuthMaterialLease(candidate.Key(), nil, []byte("oauth-canary"), OAuthMaterialMetadata{Scopes: []string{"read"}, ScopeSpecified: true})
			return lease
		}},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := ownerCandidate(70+index, contract.TransportStreamableHTTP)
			url := "https://resource.example/mcp"
			if test.name == "none" {
				url = "http://127.0.0.1:9000/mcp"
			}
			candidate.Server.Transport = mustDriverTransport(t, contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: url, ProtocolMode: contract.ProtocolModern, Authentication: test.authentication})
			var lease *MaterialLease
			if test.lease != nil {
				lease = test.lease(candidate)
			}
			driver, err := NewConcreteDriver(ConcreteDriverOptions{Owner: NewRuntimeOwner(), StartStdio: func(context.Context, StdioDefinition) (downstream.StdioRuntime, error) {
				return nil, errors.New("unexpected stdio")
			}, HTTPFactory: remote.New(remote.Options{}), NewCoordinator: driverCoordinatorFactory})
			require.NoError(t, err)
			outcome := driver.Reconcile(context.Background(), candidate, lease)
			assert.Equal(t, contract.RuntimeActive, outcome.State)
			coordinator, ok := driver.Coordinator(candidate)
			require.True(t, ok)
			assert.Equal(t, downstream.TransportHTTP, coordinator.Kind())
			driver.mu.Lock()
			handle := driver.handles[candidate.Key()]
			driver.mu.Unlock()
			require.NotNil(t, handle)
			handle.mu.Lock()
			assert.Equal(t, test.wantAuth, handle.authorization)
			handle.mu.Unlock()
			assert.True(t, driver.Stop(context.Background(), candidate))
		})
	}
}

func TestConcreteDriverNegotiatesEveryHTTPModeAndUsesSelectedRuntime(t *testing.T) {
	modes := []struct {
		mode contract.ProtocolMode
		era  downstream.Era
	}{
		{mode: contract.ProtocolModern, era: downstream.EraModern},
		{mode: contract.ProtocolLegacy, era: downstream.EraLegacy},
		{mode: contract.ProtocolAuto, era: downstream.EraLegacy},
	}
	for index, test := range modes {
		t.Run(string(test.mode), func(t *testing.T) {
			type requestRecord struct {
				method  string
				version string
				session string
			}
			var mu sync.Mutex
			requests := make([]requestRecord, 0, 4)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				var envelope struct {
					ID     uint64 `json:"id"`
					Method string `json:"method"`
				}
				_ = json.NewDecoder(request.Body).Decode(&envelope)
				mu.Lock()
				requests = append(requests, requestRecord{method: envelope.Method, version: request.Header.Get("Mcp-Protocol-Version"), session: request.Header.Get("Mcp-Session-Id")})
				mu.Unlock()
				writer.Header().Set("Content-Type", "application/json")
				switch envelope.Method {
				case "server/discover":
					if test.mode == contract.ProtocolAuto {
						_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%d,"error":{"code":-32601,"message":"Method not found"}}`, envelope.ID)
						return
					}
					_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%d,"result":{"ttlMs":0,"cacheScope":"public","supportedVersions":["2026-07-28"],"capabilities":{}}}`, envelope.ID)
				case "initialize":
					writer.Header().Set("Mcp-Session-Id", "session-"+string(test.mode))
					_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2025-11-25","capabilities":{},"serverInfo":{"name":"fixture","version":"1"}}}`, envelope.ID)
				case "notifications/initialized":
					writer.Header().Set("Mcp-Session-Id", "session-"+string(test.mode))
					writer.WriteHeader(http.StatusAccepted)
				case "tools/list":
					if test.era == downstream.EraLegacy {
						writer.Header().Set("Mcp-Session-Id", "session-"+string(test.mode))
					}
					_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[]}}`, envelope.ID)
				}
			}))
			defer server.Close()
			candidate := ownerCandidate(200+index, contract.TransportStreamableHTTP)
			candidate.Server.Transport = mustDriverTransport(t, contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: server.URL + "/mcp", ProtocolMode: test.mode, Authentication: contract.NoAuthentication{Mode: contract.AuthenticationNone}})
			driver, err := NewConcreteDriver(ConcreteDriverOptions{
				Owner: NewRuntimeOwner(),
				StartStdio: func(context.Context, StdioDefinition) (downstream.StdioRuntime, error) {
					return nil, errors.New("unexpected stdio")
				},
				HTTPFactory: remote.New(remote.Options{}),
			})
			require.NoError(t, err)

			outcome := driver.Reconcile(context.Background(), candidate, nil)

			assert.Equal(t, contract.RuntimeActive, outcome.State)
			runtime, ok := driver.Runtime(candidate)
			require.True(t, ok)
			assert.Equal(t, test.era, runtime.Era())
			response, err := runtime.Request(context.Background(), "tools/list", json.RawMessage(`{"cursor":""}`), "")
			require.NoError(t, err)
			assert.JSONEq(t, `{"tools":[]}`, string(response.Result))
			mu.Lock()
			captured := append([]requestRecord(nil), requests...)
			mu.Unlock()
			require.NotEmpty(t, captured)
			assert.Equal(t, "tools/list", captured[len(captured)-1].method)
			if test.era == downstream.EraLegacy {
				assert.Equal(t, contract.LegacyProtocolVersion, captured[len(captured)-1].version)
				assert.Equal(t, "session-"+string(test.mode), captured[len(captured)-1].session)
			} else {
				assert.Equal(t, contract.ModernProtocolVersion, captured[len(captured)-1].version)
				assert.Empty(t, captured[len(captured)-1].session)
			}
			assert.True(t, driver.Stop(context.Background(), candidate))
		})
	}
}

func TestConcreteDriverReportsHTTPSessionLossOnce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var envelope struct {
			ID     uint64 `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(request.Body).Decode(&envelope)
		writer.Header().Set("Content-Type", "application/json")
		switch envelope.Method {
		case "initialize":
			writer.Header().Set("Mcp-Session-Id", "session-1")
			_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2025-11-25","capabilities":{},"serverInfo":{"name":"fixture","version":"1"}}}`, envelope.ID)
		case "notifications/initialized":
			writer.Header().Set("Mcp-Session-Id", "session-1")
			writer.WriteHeader(http.StatusAccepted)
		case "tools/list":
			writer.Header().Set("Mcp-Session-Id", "session-2")
			_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%d,"result":{"tools":[]}}`, envelope.ID)
		}
	}))
	defer server.Close()
	candidate := ownerCandidate(399, contract.TransportStreamableHTTP)
	candidate.Server.Transport = mustDriverTransport(t, contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: server.URL + "/mcp", ProtocolMode: contract.ProtocolLegacy, Authentication: contract.NoAuthentication{Mode: contract.AuthenticationNone}})
	reports := make(chan FailureDisposition, 2)
	driver, err := NewConcreteDriver(ConcreteDriverOptions{
		Owner: NewRuntimeOwner(),
		StartStdio: func(context.Context, StdioDefinition) (downstream.StdioRuntime, error) {
			return nil, errors.New("unexpected stdio")
		},
		HTTPFactory: remote.New(remote.Options{}),
		ReportFailure: func(received Candidate, failure FailureDisposition) bool {
			assert.Equal(t, candidate.Key(), received.Key())
			reports <- failure
			return true
		},
	})
	require.NoError(t, err)
	require.Equal(t, contract.RuntimeActive, driver.Reconcile(context.Background(), candidate, nil).State)
	runtime, ok := driver.Runtime(candidate)
	require.True(t, ok)

	_, err = runtime.Request(context.Background(), "tools/list", json.RawMessage(`{"cursor":""}`), "")
	assert.ErrorIs(t, err, downstream.ErrSessionLost)
	failure := <-reports
	assert.Equal(t, contract.ReasonConnectivity, failure.Reason)
	assert.True(t, failure.Retryable)
	select {
	case duplicate := <-reports:
		t.Fatalf("duplicate failure report: %+v", duplicate)
	default:
	}
	assert.True(t, driver.Stop(context.Background(), candidate))
}

func TestConcreteDriverHTTPAutoFallbackEvidenceMatrix(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		allowed     bool
	}{
		{name: "JSON method absent", status: http.StatusOK, contentType: "application/json", body: `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`, allowed: true},
		{name: "JSON null data", status: http.StatusOK, contentType: "application/json; charset=utf-8", body: `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found","data":null}}`, allowed: true},
		{name: "exact unsupported method text", status: http.StatusBadRequest, contentType: "text/plain; charset=utf-8", body: "JSON RPC not handled: \"server/discover\" unsupported\n", allowed: true},
		{name: "exact unsupported version text", status: http.StatusBadRequest, contentType: "text/plain", body: "Bad Request: Unsupported protocol version\n", allowed: true},
		{name: "unknown method status", status: http.StatusNotFound, contentType: "application/json", body: `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`},
		{name: "wrong JSON status", status: http.StatusBadRequest, contentType: "application/json", body: `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`},
		{name: "wrong text status", status: http.StatusOK, contentType: "text/plain", body: "Bad Request: Unsupported protocol version\n"},
		{name: "non-null method data", status: http.StatusOK, contentType: "application/json", body: `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found","data":{}}}`},
		{name: "SSE method absent", status: http.StatusOK, contentType: "text/event-stream", body: `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`},
		{name: "malformed media", status: http.StatusOK, contentType: "application/json; bad", body: `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`},
		{name: "authentication failure", status: http.StatusUnauthorized, contentType: "application/json", body: `{}`},
		{name: "rate limit", status: http.StatusTooManyRequests, contentType: "application/json", body: `{}`},
		{name: "server failure", status: http.StatusInternalServerError, contentType: "application/json", body: `{}`},
		{name: "adjacent text", status: http.StatusBadRequest, contentType: "text/plain", body: "Bad Request: Unsupported protocol version"},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var mu sync.Mutex
			requestCount := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				mu.Lock()
				requestCount++
				sequence := requestCount
				mu.Unlock()
				if sequence == 1 {
					writer.Header().Set("Content-Type", test.contentType)
					writer.WriteHeader(test.status)
					_, _ = writer.Write([]byte(test.body))
					return
				}
				var envelope struct {
					ID     uint64 `json:"id"`
					Method string `json:"method"`
				}
				_ = json.NewDecoder(request.Body).Decode(&envelope)
				writer.Header().Set("Content-Type", "application/json")
				writer.Header().Set("Mcp-Session-Id", "fallback-session")
				if envelope.Method == "initialize" {
					_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":"2025-11-25","capabilities":{},"serverInfo":{"name":"fixture","version":"1"}}}`, envelope.ID)
					return
				}
				writer.WriteHeader(http.StatusAccepted)
			}))
			defer server.Close()
			candidate := ownerCandidate(400+index, contract.TransportStreamableHTTP)
			candidate.Server.Transport = mustDriverTransport(t, contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: server.URL + "/mcp", ProtocolMode: contract.ProtocolAuto, Authentication: contract.NoAuthentication{Mode: contract.AuthenticationNone}})
			driver, err := NewConcreteDriver(ConcreteDriverOptions{
				Owner: NewRuntimeOwner(),
				StartStdio: func(context.Context, StdioDefinition) (downstream.StdioRuntime, error) {
					return nil, errors.New("unexpected stdio")
				},
				HTTPFactory: remote.New(remote.Options{}),
			})
			require.NoError(t, err)

			outcome := driver.Reconcile(context.Background(), candidate, nil)

			if !test.allowed {
				assert.Equal(t, contract.RuntimeDegraded, outcome.State)
				_, ok := driver.Runtime(candidate)
				assert.False(t, ok)
				mu.Lock()
				assert.Equal(t, 1, requestCount)
				mu.Unlock()
				return
			}
			assert.Equal(t, contract.RuntimeActive, outcome.State)
			runtime, ok := driver.Runtime(candidate)
			require.True(t, ok)
			assert.Equal(t, downstream.EraLegacy, runtime.Era())
			mu.Lock()
			assert.Equal(t, 3, requestCount)
			mu.Unlock()
			assert.True(t, driver.Stop(context.Background(), candidate))
		})
	}
}

func TestConcreteDriverSharesMixedRuntimeCapacity(t *testing.T) {
	owner := NewRuntimeOwner()
	driver, err := NewConcreteDriver(ConcreteDriverOptions{
		Owner: owner,
		StartStdio: func(context.Context, StdioDefinition) (downstream.StdioRuntime, error) {
			return newDriverStdioRuntime(true), nil
		},
		HTTPFactory:    remote.New(remote.Options{}),
		NewCoordinator: driverCoordinatorFactory,
	})
	require.NoError(t, err)
	candidates := make([]Candidate, 0, 32)
	for index := range 32 {
		candidate := ownerCandidate(100+index, contract.TransportStreamableHTTP)
		if index%2 == 0 {
			candidate.Server.Transport = []byte(`{"kind":"stdio","executable":"/bin/server","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{}}`)
		}
		outcome := driver.Reconcile(context.Background(), candidate, nil)
		assert.Equal(t, contract.RuntimeActive, outcome.State)
		candidates = append(candidates, candidate)
	}
	assert.True(t, owner.Status().Saturated)
	overflow := ownerCandidate(132, contract.TransportStreamableHTTP)
	outcome := driver.Reconcile(context.Background(), overflow, nil)
	require.NotNil(t, outcome.Reason)
	assert.Equal(t, contract.ReasonResourceLimit, *outcome.Reason)
	assert.True(t, outcome.Retryable)
	for _, candidate := range candidates {
		assert.True(t, driver.Stop(context.Background(), candidate))
	}
	assert.Equal(t, int64(0), owner.Status().InUse)
}

func TestConcreteDriverAutoFallbackUsesFreshRootOwnedRuntimeAndExactLookup(t *testing.T) {
	owner := NewRuntimeOwner()
	candidate := ownerCandidate(78, contract.TransportStdio)
	candidate.Server.Transport = []byte(`{"kind":"stdio","executable":"/bin/server","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{}}`)
	processes := []*driverStdioRuntime{
		newDriverStdioRuntimeWithFrames(true, `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`),
		newDriverStdioRuntimeWithFrames(true,
			`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","capabilities":{},"serverInfo":{"name":"fixture","version":"1"}}}`,
			`{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`,
		),
	}
	type contextKey string
	const rootKey contextKey = "runtime-root"
	starts := 0
	driver, err := NewConcreteDriver(ConcreteDriverOptions{
		Owner: owner,
		StartStdio: func(ctx context.Context, _ StdioDefinition) (downstream.StdioRuntime, error) {
			assert.Equal(t, "root", ctx.Value(rootKey))
			if starts == 1 {
				assert.Equal(t, 1, processes[0].stopCalls, "fallback started before verified probe reap")
			}
			process := processes[starts]
			starts++
			return process, nil
		},
		HTTPFactory: remote.New(remote.Options{}),
		NewNegotiator: func(open downstream.OpenCoordinator) (*downstream.Negotiator, error) {
			return downstream.NewNegotiatorWithDeadline(open, func(ctx context.Context, _ time.Duration) (context.Context, context.CancelFunc) {
				return context.WithCancel(context.WithValue(ctx, rootKey, "initialization"))
			})
		},
	})
	require.NoError(t, err)

	outcome := driver.Reconcile(context.WithValue(context.Background(), rootKey, "root"), candidate, nil)

	assert.Equal(t, contract.RuntimeActive, outcome.State)
	assert.Equal(t, 2, starts)
	runtime, ok := driver.Runtime(candidate)
	require.True(t, ok)
	assert.Equal(t, downstream.EraLegacy, runtime.Era())
	response, err := runtime.Request(context.Background(), "tools/list", json.RawMessage(`{"cursor":""}`), "")
	require.NoError(t, err)
	assert.JSONEq(t, `{"tools":[]}`, string(response.Result))
	mismatches := []Candidate{candidate, candidate, candidate, candidate}
	mismatches[0].RuntimeID += "-stale"
	mismatches[1].Generation++
	mismatches[2].DrainEpoch++
	mismatches[3].Server.DesiredRevision += "-stale"
	for _, mismatch := range mismatches {
		_, found := driver.Runtime(mismatch)
		assert.False(t, found)
	}
	assert.True(t, driver.Stop(context.Background(), candidate))
	assert.Equal(t, 1, processes[1].stopCalls)
}

func TestConcreteDriverAutoFallbackBlocksReplacementAfterUnconfirmedProbeStop(t *testing.T) {
	owner := NewRuntimeOwner()
	candidate := ownerCandidate(77, contract.TransportStdio)
	candidate.Server.Transport = []byte(`{"kind":"stdio","executable":"/bin/server","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{}}`)
	process := newDriverStdioRuntimeWithFrames(false, `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`)
	starts := 0
	driver, err := NewConcreteDriver(ConcreteDriverOptions{
		Owner: owner,
		StartStdio: func(context.Context, StdioDefinition) (downstream.StdioRuntime, error) {
			starts++
			return process, nil
		},
		HTTPFactory: remote.New(remote.Options{}),
	})
	require.NoError(t, err)

	outcome := driver.Reconcile(context.Background(), candidate, nil)

	assert.Equal(t, contract.RuntimeDegraded, outcome.State)
	assert.Equal(t, 1, starts)
	assert.Equal(t, 2, process.stopCalls)
	phase, ok := owner.Phase(candidate.Key())
	require.True(t, ok)
	assert.Equal(t, RuntimeBlockedStop, phase)
	_, ok = driver.Runtime(candidate)
	assert.False(t, ok)
}

func TestConcreteDriverReportsSelectedFailureOnceForExactCandidate(t *testing.T) {
	candidate := ownerCandidate(76, contract.TransportStdio)
	candidate.Server.Transport = []byte(`{"kind":"stdio","executable":"/bin/server","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{}}`)
	process := newDriverStdioRuntime(true)
	process.done = make(chan StdioExit, 2)
	reports := make(chan FailureDisposition, 2)
	driver, err := NewConcreteDriver(ConcreteDriverOptions{
		Owner: NewRuntimeOwner(),
		StartStdio: func(context.Context, StdioDefinition) (downstream.StdioRuntime, error) {
			return process, nil
		},
		HTTPFactory:    remote.New(remote.Options{}),
		NewCoordinator: driverCoordinatorFactory,
		ReportFailure: func(received Candidate, failure FailureDisposition) bool {
			assert.Equal(t, candidate.Key(), received.Key())
			reports <- failure
			return true
		},
	})
	require.NoError(t, err)
	require.Equal(t, contract.RuntimeActive, driver.Reconcile(context.Background(), candidate, nil).State)

	process.done <- StdioExit{Reason: contract.ReasonOutputLimit, Retryable: true}
	process.done <- StdioExit{Reason: contract.ReasonProcessExited, Retryable: true}

	failure := <-reports
	assert.Equal(t, contract.ReasonOutputLimit, failure.Reason)
	assert.True(t, failure.Retryable)
	select {
	case duplicate := <-reports:
		t.Fatalf("duplicate failure report: %+v", duplicate)
	default:
	}
	assert.True(t, driver.Stop(context.Background(), candidate))
}

func TestConcreteDriverReleasesVerifiedConstructionFailure(t *testing.T) {
	owner := NewRuntimeOwner()
	candidate := ownerCandidate(79, contract.TransportStdio)
	generation, err := servercredentials.EncodeStaticGeneration(map[string]string{"api": "cleanup-canary"})
	require.NoError(t, err)
	lease, err := NewMaterialLease(candidate.Key(), map[contract.ServerCredentialKind][]byte{contract.ServerCredentialStatic: generation})
	require.NoError(t, err)
	process := newDriverStdioRuntime(true)
	var retained []byte
	driver, err := NewConcreteDriver(ConcreteDriverOptions{
		Owner:       owner,
		StartStdio:  func(context.Context, StdioDefinition) (downstream.StdioRuntime, error) { return process, nil },
		HTTPFactory: remote.New(remote.Options{}),
		NewCoordinator: func(downstream.Transport) (*downstream.Coordinator, error) {
			retained, _ = owner.Material(candidate.Key(), contract.ServerCredentialStatic)
			return nil, errors.New("post-start failure")
		},
	})
	require.NoError(t, err)

	outcome := driver.Reconcile(context.Background(), candidate, lease)

	assert.Equal(t, contract.RuntimeDegraded, outcome.State)
	assert.Equal(t, 1, process.stopCalls)
	assert.Equal(t, int64(0), owner.Status().InUse)
	assert.Equal(t, make([]byte, len(retained)), retained)
	assert.False(t, driver.Stop(context.Background(), candidate))
}

func TestConcreteDriverRetainsBlockedConstructionAndMaterial(t *testing.T) {
	owner := NewRuntimeOwner()
	candidate := ownerCandidate(80, contract.TransportStdio)
	generation, err := servercredentials.EncodeStaticGeneration(map[string]string{"api": "blocked-canary"})
	require.NoError(t, err)
	lease, err := NewMaterialLease(candidate.Key(), map[contract.ServerCredentialKind][]byte{contract.ServerCredentialStatic: generation})
	require.NoError(t, err)
	process := newDriverStdioRuntime(false)
	driver, err := NewConcreteDriver(ConcreteDriverOptions{
		Owner:       owner,
		StartStdio:  func(context.Context, StdioDefinition) (downstream.StdioRuntime, error) { return process, nil },
		HTTPFactory: remote.New(remote.Options{}),
		NewCoordinator: func(downstream.Transport) (*downstream.Coordinator, error) {
			return nil, errors.New("post-start failure")
		},
	})
	require.NoError(t, err)

	outcome := driver.Reconcile(context.Background(), candidate, lease)

	assert.Equal(t, contract.RuntimeDegraded, outcome.State)
	assert.Equal(t, 1, process.stopCalls)
	phase, ok := owner.Phase(candidate.Key())
	require.True(t, ok)
	assert.Equal(t, RuntimeBlockedStop, phase)
	material, ok := owner.Material(candidate.Key(), contract.ServerCredentialStatic)
	require.True(t, ok)
	assert.Contains(t, string(material), "blocked-canary")
	assert.False(t, driver.Stop(context.Background(), candidate))
}

func mustDriverTransport(t *testing.T, transport contract.Transport) []byte {
	t.Helper()
	encoded, err := json.Marshal(transport)
	require.NoError(t, err)
	return encoded
}

var _ io.WriteCloser = (*driverWriteCloser)(nil)
