package downstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type scriptedTransport struct {
	mu            sync.Mutex
	kind          TransportKind
	exchanges     []func(Message) WireResponse
	messages      []Message
	notifications []Message
	closeErr      error
	closed        bool
}

func (transport *scriptedTransport) Kind() TransportKind {
	if transport.kind == "" {
		return TransportStdio
	}
	return transport.kind
}

func (transport *scriptedTransport) Exchange(_ context.Context, message Message) (WireResponse, error) {
	if message.MarkHandoff != nil {
		message.MarkHandoff()
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.messages = append(transport.messages, message)
	if len(transport.exchanges) == 0 {
		return WireResponse{}, errors.New("unexpected exchange")
	}
	respond := transport.exchanges[0]
	transport.exchanges = transport.exchanges[1:]
	return respond(message), nil
}

func (transport *scriptedTransport) Notify(_ context.Context, message Message) (WireResponse, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.notifications = append(transport.notifications, message)
	return WireResponse{StatusCode: 202, ContentType: "application/json"}, nil
}

func (transport *scriptedTransport) Close(context.Context) error {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.closed = true
	return transport.closeErr
}

func TestNegotiatorUsesExactInjectedInitializationDeadline(t *testing.T) {
	transport := &scriptedTransport{exchanges: []func(Message) WireResponse{modernSuccess}}
	var observed time.Duration
	negotiator, err := NewNegotiatorWithDeadline(func(context.Context) (*Coordinator, error) {
		return NewCoordinator(transport)
	}, func(ctx context.Context, duration time.Duration) (context.Context, context.CancelFunc) {
		observed = duration
		return context.WithCancel(ctx)
	})
	require.NoError(t, err)
	_, err = negotiator.Negotiate(context.Background(), ModeModern)
	require.NoError(t, err)
	assert.Equal(t, contract.DownstreamInitializationDeadline, observed)
}

func TestNegotiatorSendsExactModernDiscoveryAndBindsModern(t *testing.T) {
	transport := &scriptedTransport{exchanges: []func(Message) WireResponse{modernSuccess}}
	negotiator := newScriptedNegotiator(t, transport)
	runtime, err := negotiator.Negotiate(context.Background(), ModeModern)
	require.NoError(t, err)
	assert.Equal(t, EraModern, runtime.Era())
	require.Len(t, transport.messages, 1)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"mcp-gateway","version":"s2"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, string(transport.messages[0].Payload))
	assert.Equal(t, contract.ModernProtocolVersion, transport.messages[0].ProtocolVersion)
}

func TestNegotiatorRetriesModernVersionWithoutLegacyFallback(t *testing.T) {
	transport := &scriptedTransport{exchanges: []func(Message) WireResponse{
		func(message Message) WireResponse {
			return jsonResponse(message, `{"error":{"code":-32022,"message":"unsupported protocol version","data":{"supported":["2026-07-28"],"requested":"2026-07-28"}}}`)
		},
		modernSuccess,
	}}
	opened := 0
	negotiator, err := NewNegotiator(func(context.Context) (*Coordinator, error) {
		opened++
		return NewCoordinator(transport)
	})
	require.NoError(t, err)
	runtime, err := negotiator.Negotiate(context.Background(), ModeAuto)
	require.NoError(t, err)
	assert.Equal(t, EraModern, runtime.Era())
	assert.Equal(t, 1, opened)
	require.Len(t, transport.messages, 2)
	assert.Contains(t, string(transport.messages[1].Payload), `"id":2`)
}

func TestUnsupportedVersionEvidenceIsStrictAndNeverFallsBack(t *testing.T) {
	data := []string{
		`{"supported":[],"requested":"2026-07-28"}`,
		`{"supported":["2026-07-28"],"requested":"2025-11-25"}`,
		`{"supported":["2026-07-28"],"requested":"2026-07-28","extra":true}`,
		`null`,
	}
	for _, value := range data {
		t.Run(value, func(t *testing.T) {
			transport := &scriptedTransport{exchanges: []func(Message) WireResponse{func(message Message) WireResponse {
				return jsonResponse(message, `{"error":{"code":-32022,"message":"unsupported","data":`+value+`}}`)
			}}}
			opened := 0
			negotiator, err := NewNegotiator(func(context.Context) (*Coordinator, error) {
				opened++
				return NewCoordinator(transport)
			})
			require.NoError(t, err)
			_, err = negotiator.Negotiate(context.Background(), ModeAuto)
			assert.ErrorIs(t, err, ErrUnsupportedProtocol)
			assert.Equal(t, 1, opened)
		})
	}
}

func TestNegotiatorUsesOnlyClosedFallbackEvidenceAndFreshTransport(t *testing.T) {
	tests := []struct {
		name     string
		response func(Message) WireResponse
		allowed  bool
	}{
		{name: "stdio method absent", response: methodAbsentResponse(0, "", ``), allowed: true},
		{name: "stdio method null data", response: methodAbsentResponse(0, "", `,"data":null`), allowed: true},
		{name: "http JSON method absent", response: methodAbsentResponse(200, "application/json; charset=utf-8", ``), allowed: true},
		{name: "http JSON method null data", response: methodAbsentResponse(200, "application/json", `,"data":null`), allowed: true},
		{name: "HTTP exact unsupported method text", response: staticResponse(400, "text/plain; charset=utf-8", "JSON RPC not handled: \"server/discover\" unsupported\n"), allowed: true},
		{name: "HTTP exact unsupported version text", response: staticResponse(400, "text/plain", "Bad Request: Unsupported protocol version\n"), allowed: true},
		{name: "modern HTTP unknown method", response: methodAbsentResponse(404, "application/json", ``)},
		{name: "wrong JSON status", response: methodAbsentResponse(400, "application/json", ``)},
		{name: "wrong text status", response: staticResponse(200, "text/plain", "Bad Request: Unsupported protocol version\n")},
		{name: "nonnull method data", response: methodAbsentResponse(200, "application/json", `,"data":{}`)},
		{name: "SSE method absent", response: methodAbsentResponse(200, "text/event-stream", ``)},
		{name: "malformed media", response: methodAbsentResponse(200, "application/json; bad", ``)},
		{name: "authentication failure", response: staticResponse(401, "application/json", `{}`)},
		{name: "rate limit", response: staticResponse(429, "application/json", `{}`)},
		{name: "server failure", response: staticResponse(500, "application/json", `{}`)},
		{name: "adjacent text", response: staticResponse(400, "text/plain", "Bad Request: Unsupported protocol version")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			probe := &scriptedTransport{exchanges: []func(Message) WireResponse{test.response}}
			legacy := &scriptedTransport{exchanges: []func(Message) WireResponse{legacySuccess}}
			opened := 0
			negotiator, err := NewNegotiator(func(context.Context) (*Coordinator, error) {
				opened++
				if opened == 1 {
					return NewCoordinator(probe)
				}
				require.True(t, probe.closed, "legacy opened before probe close")
				return NewCoordinator(legacy)
			})
			require.NoError(t, err)
			runtime, negotiateErr := negotiator.Negotiate(context.Background(), ModeAuto)
			if !test.allowed {
				assert.Error(t, negotiateErr)
				assert.Equal(t, 1, opened)
				return
			}
			require.NoError(t, negotiateErr)
			assert.Equal(t, EraLegacy, runtime.Era())
			assert.Equal(t, 2, opened)
			require.Len(t, legacy.messages, 1)
			assert.JSONEq(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"mcp-gateway","version":"s2"}}}`, string(legacy.messages[0].Payload))
			require.Len(t, legacy.notifications, 1)
			assert.JSONEq(t, `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`, string(legacy.notifications[0].Payload))
		})
	}
}

func TestMalformedOrModernEvidenceNeverFallsBack(t *testing.T) {
	responses := map[string]string{
		"malformed":              `{`,
		"wrong version":          `{"jsonrpc":"1.0","id":1,"error":{"code":-32601,"message":"missing"}}`,
		"mismatched ID":          `{"jsonrpc":"2.0","id":2,"error":{"code":-32601,"message":"missing"}}`,
		"unknown envelope field": `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"missing"},"extra":true}`,
		"non-null data":          `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"missing","data":false}}`,
		"different error":        `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"invalid"}}`,
		"unsupported modern":     `{"jsonrpc":"2.0","id":1,"result":{"ttlMs":0,"cacheScope":"public","supportedVersions":["2027-01-01"],"capabilities":{}}}`,
	}
	for name, body := range responses {
		t.Run(name, func(t *testing.T) {
			transport := &scriptedTransport{exchanges: []func(Message) WireResponse{staticResponse(0, "", body)}}
			opened := 0
			negotiator, err := NewNegotiator(func(context.Context) (*Coordinator, error) {
				opened++
				return NewCoordinator(transport)
			})
			require.NoError(t, err)
			_, err = negotiator.Negotiate(context.Background(), ModeAuto)
			assert.Error(t, err)
			assert.Equal(t, 1, opened)
		})
	}
}

func TestNegotiatorRequiresVerifiedProbeStopBeforeLegacyConstruction(t *testing.T) {
	probe := &scriptedTransport{exchanges: []func(Message) WireResponse{methodAbsentResponse(0, "", ``)}, closeErr: ErrStopUnconfirmed}
	opened := 0
	negotiator, err := NewNegotiator(func(context.Context) (*Coordinator, error) {
		opened++
		return NewCoordinator(probe)
	})
	require.NoError(t, err)
	_, err = negotiator.Negotiate(context.Background(), ModeAuto)
	assert.ErrorIs(t, err, ErrStopUnconfirmed)
	assert.Equal(t, 1, opened)
}

func TestSelectedRuntimeReportsFirstFatalRequestFailureOnce(t *testing.T) {
	transport := &scriptedTransport{exchanges: []func(Message) WireResponse{
		modernSuccess,
		func(message Message) WireResponse { return jsonResponse(message, `{"result":`) },
		func(message Message) WireResponse { return jsonResponse(message, `{"result":`) },
	}}
	negotiator := newScriptedNegotiator(t, transport)
	runtime, err := negotiator.Negotiate(context.Background(), ModeModern)
	require.NoError(t, err)

	_, err = runtime.Request(context.Background(), "tools/list", json.RawMessage(`{"cursor":""}`), "")
	assert.Error(t, err)
	failure := <-runtime.Failures()
	assert.ErrorIs(t, failure, ErrInvalidMessage)
	_, _ = runtime.Request(context.Background(), "tools/list", json.RawMessage(`{"cursor":""}`), "")
	select {
	case duplicate := <-runtime.Failures():
		t.Fatalf("duplicate runtime failure: %v", duplicate)
	default:
	}
}

func TestSelectedRuntimeDoesNotReportHealthyApplicationFailure(t *testing.T) {
	transport := &scriptedTransport{exchanges: []func(Message) WireResponse{
		modernSuccess,
		func(message Message) WireResponse {
			return jsonResponse(message, `{"error":{"code":-32601,"message":"catalog unavailable"}}`)
		},
	}}
	negotiator := newScriptedNegotiator(t, transport)
	runtime, err := negotiator.Negotiate(context.Background(), ModeModern)
	require.NoError(t, err)

	response, err := runtime.Request(context.Background(), "tools/list", json.RawMessage(`{"cursor":""}`), "")
	require.NoError(t, err)
	require.NotNil(t, response.Error)
	select {
	case failure := <-runtime.Failures():
		t.Fatalf("healthy application failure reported runtime loss: %v", failure)
	default:
	}
}

func TestSelectedRuntimeClassifiesFatalHTTPStatus(t *testing.T) {
	tests := []struct {
		status int
		want   error
	}{
		{status: http.StatusUnauthorized, want: ErrAuthenticationRejected},
		{status: http.StatusForbidden, want: ErrAuthenticationRejected},
		{status: http.StatusTooManyRequests, want: ErrRemoteUnavailable},
		{status: http.StatusInternalServerError, want: ErrRemoteUnavailable},
	}
	for _, test := range tests {
		t.Run(fmt.Sprint(test.status), func(t *testing.T) {
			transport := &scriptedTransport{exchanges: []func(Message) WireResponse{modernSuccess, staticResponse(test.status, "application/json", `{}`)}}
			negotiator := newScriptedNegotiator(t, transport)
			runtime, err := negotiator.Negotiate(context.Background(), ModeModern)
			require.NoError(t, err)

			_, err = runtime.Request(context.Background(), "tools/list", json.RawMessage(`{"cursor":""}`), "")
			assert.ErrorIs(t, err, test.want)
			assert.ErrorIs(t, <-runtime.Failures(), test.want)
		})
	}
}

func TestSelectedRuntimeRetainsUnconfirmedCloseEvidence(t *testing.T) {
	transport := &scriptedTransport{exchanges: []func(Message) WireResponse{modernSuccess}, closeErr: ErrStopUnconfirmed}
	negotiator := newScriptedNegotiator(t, transport)
	runtime, err := negotiator.Negotiate(context.Background(), ModeModern)
	require.NoError(t, err)

	assert.ErrorIs(t, runtime.Close(context.Background()), ErrStopUnconfirmed)
	assert.ErrorIs(t, runtime.Close(context.Background()), ErrStopUnconfirmed)
}

func TestAutoHTTPFallbackUsesFreshTransportAndBindsLegacySession(t *testing.T) {
	type captured struct {
		body    string
		version string
		session string
	}
	requests := make(chan captured, 4)
	var sequence atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		requests <- captured{body: string(body), version: request.Header.Get("Mcp-Protocol-Version"), session: request.Header.Get("Mcp-Session-Id")}
		switch sequence.Add(1) {
		case 1:
			writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte("Bad Request: Unsupported protocol version\n"))
		case 2:
			writer.Header().Set("Content-Type", "application/json")
			writer.Header().Set("Mcp-Session-Id", "session-1")
			_, _ = writer.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","capabilities":{},"serverInfo":{"name":"fixture","version":"1"}}}`))
		case 3:
			writer.WriteHeader(http.StatusAccepted)
		case 4:
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusNotFound)
			_, _ = writer.Write([]byte(`{"jsonrpc":"2.0","id":2,"error":{"code":-32601,"message":"session lost"}}`))
		}
	}))
	defer server.Close()
	endpoint, err := remote.ParseEndpoint(server.URL+"/mcp", true)
	require.NoError(t, err)
	opened := 0
	negotiator, err := NewNegotiator(func(context.Context) (*Coordinator, error) {
		opened++
		transport, transportErr := NewHTTPTransport(remote.New(remote.Options{}), endpoint, "Bearer server-secret")
		if transportErr != nil {
			return nil, transportErr
		}
		return NewCoordinator(transport)
	})
	require.NoError(t, err)
	runtime, err := negotiator.Negotiate(context.Background(), ModeAuto)
	require.NoError(t, err)
	assert.Equal(t, 2, opened)
	assert.Equal(t, "session-1", runtime.SessionID())
	modern := <-requests
	assert.Equal(t, contract.ModernProtocolVersion, modern.version)
	assert.Empty(t, modern.session)
	legacy := <-requests
	assert.Equal(t, contract.LegacyProtocolVersion, legacy.version)
	assert.Empty(t, legacy.session)
	initialized := <-requests
	assert.Equal(t, contract.LegacyProtocolVersion, initialized.version)
	assert.Equal(t, "session-1", initialized.session)
	assert.JSONEq(t, `{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`, initialized.body)
	_, err = runtime.Request(context.Background(), "tools/list", json.RawMessage(`{"cursor":""}`), "")
	assert.ErrorIs(t, err, ErrSessionLost)
	call := <-requests
	assert.Equal(t, "session-1", call.session)
}

func TestAutoStdioFallbackRequiresSupervisorStopProof(t *testing.T) {
	stdioRuntime := &fakeStdio{frames: make(chan []byte, 1), input: new(bytes.Buffer), stopResult: false}
	stdioRuntime.frames <- []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`)
	transport, err := NewStdioTransport(stdioRuntime)
	require.NoError(t, err)
	opened := 0
	negotiator, err := NewNegotiator(func(context.Context) (*Coordinator, error) {
		opened++
		if opened > 1 {
			t.Fatal("legacy coordinator opened after unconfirmed stdio stop")
		}
		return NewCoordinator(transport)
	})
	require.NoError(t, err)
	_, err = negotiator.Negotiate(context.Background(), ModeAuto)
	assert.ErrorIs(t, err, ErrStopUnconfirmed)
	assert.Equal(t, 1, opened)
	assert.True(t, stdioRuntime.stopped)
}

func TestLegacySessionIDBoundAndMultiplicityLimits(t *testing.T) {
	tests := []struct {
		name     string
		sessions []string
		wantErr  bool
	}{
		{name: "sessionless"},
		{name: "maximum", sessions: []string{strings.Repeat("s", 512)}},
		{name: "over maximum", sessions: []string{strings.Repeat("s", 513)}, wantErr: true},
		{name: "multiple", sessions: []string{"one", "two"}, wantErr: true},
		{name: "empty", sessions: []string{""}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := &scriptedTransport{exchanges: []func(Message) WireResponse{func(message Message) WireResponse {
				response := legacySuccess(message)
				response.SessionIDs = test.sessions
				return response
			}}}
			negotiator := newScriptedNegotiator(t, transport)
			runtime, err := negotiator.Negotiate(context.Background(), ModeLegacy)
			if test.wantErr {
				assert.ErrorIs(t, err, ErrSessionLost)
				assert.True(t, transport.closed)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, firstSession(test.sessions), runtime.SessionID())
		})
	}
}

func TestLegacyHTTPSessionIsBoundImmutableAndLossClosesRuntime(t *testing.T) {
	transport := &scriptedTransport{exchanges: []func(Message) WireResponse{
		func(message Message) WireResponse {
			response := legacySuccess(message)
			response.SessionIDs = []string{"session-1"}
			return response
		},
		func(message Message) WireResponse {
			assert.Equal(t, "session-1", message.SessionID)
			response := jsonResponse(message, `{"result":{}}`)
			response.SessionIDs = []string{"session-2"}
			return response
		},
	}}
	negotiator := newScriptedNegotiator(t, transport)
	runtime, err := negotiator.Negotiate(context.Background(), ModeLegacy)
	require.NoError(t, err)
	assert.Equal(t, "session-1", runtime.SessionID())
	_, err = runtime.Request(context.Background(), "tools/list", json.RawMessage(`{"cursor":""}`), "")
	assert.ErrorIs(t, err, ErrSessionLost)
	assert.True(t, transport.closed)
}

func TestModernRuntimeFreshlyBuildsMetadataAndRejectsSessionState(t *testing.T) {
	transport := &scriptedTransport{exchanges: []func(Message) WireResponse{
		modernSuccess,
		func(message Message) WireResponse {
			assert.JSONEq(t, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"cursor":"","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"mcp-gateway","version":"s2"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, string(message.Payload))
			response := jsonResponse(message, `{"result":{}}`)
			response.SessionIDs = []string{"forbidden"}
			return response
		},
	}}
	negotiator := newScriptedNegotiator(t, transport)
	runtime, err := negotiator.Negotiate(context.Background(), ModeModern)
	require.NoError(t, err)
	_, err = runtime.Request(context.Background(), "tools/list", json.RawMessage(`{"cursor":""}`), "")
	assert.ErrorIs(t, err, ErrSessionLost)
	assert.True(t, transport.closed)
}

func firstSession(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func newScriptedNegotiator(t *testing.T, transport Transport) *Negotiator {
	t.Helper()
	negotiator, err := NewNegotiator(func(context.Context) (*Coordinator, error) { return NewCoordinator(transport) })
	require.NoError(t, err)
	return negotiator
}

func modernSuccess(message Message) WireResponse {
	return jsonResponse(message, `{"result":{"ttlMs":0,"cacheScope":"public","supportedVersions":["2026-07-28"],"capabilities":{}}}`)
}

func legacySuccess(message Message) WireResponse {
	return jsonResponse(message, `{"result":{"protocolVersion":"2025-11-25","capabilities":{},"serverInfo":{"name":"fixture","version":"1"}}}`)
}

func methodAbsentResponse(status int, contentType, data string) func(Message) WireResponse {
	return func(message Message) WireResponse {
		return jsonResponseWithHTTP(message, status, contentType, fmt.Sprintf(`{"error":{"code":-32601,"message":"Method not found"%s}}`, data))
	}
}

func staticResponse(status int, contentType, body string) func(Message) WireResponse {
	return func(Message) WireResponse {
		return WireResponse{StatusCode: status, ContentType: contentType, Body: []byte(body)}
	}
}

func jsonResponse(message Message, member string) WireResponse {
	return jsonResponseWithHTTP(message, 0, "", member)
}

func jsonResponseWithHTTP(message Message, status int, contentType, member string) WireResponse {
	var request struct {
		ID uint64 `json:"id"`
	}
	_ = json.Unmarshal(message.Payload, &request)
	return WireResponse{StatusCode: status, ContentType: contentType, Body: []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,%s}`, request.ID, member[1:len(member)-1]))}
}
