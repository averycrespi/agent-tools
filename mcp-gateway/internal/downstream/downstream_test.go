package downstream

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type captureTransport struct {
	message Message
	respond func(requestEnvelope) []byte
}

func (*captureTransport) Kind() TransportKind { return TransportStdio }
func (transport *captureTransport) Exchange(_ context.Context, message Message) (WireResponse, error) {
	transport.message = message
	var request requestEnvelope
	_ = json.Unmarshal(message.Payload, &request)
	return WireResponse{Body: transport.respond(request)}, nil
}
func (*captureTransport) Notify(context.Context, Message) (WireResponse, error) {
	return WireResponse{}, nil
}
func (*captureTransport) Close(context.Context) error { return nil }

func TestCoordinatorOwnsMonotonicIDsAndStrictMatchingEnvelopes(t *testing.T) {
	transport := &captureTransport{respond: func(request requestEnvelope) []byte {
		return []byte(`{"jsonrpc":"2.0","id":` + jsonNumber(request.ID) + `,"result":{"ok":true}}`)
	}}
	coordinator, err := NewCoordinator(transport)
	require.NoError(t, err)
	for expected := uint64(1); expected <= 2; expected++ {
		response, requestErr := coordinator.Request(context.Background(), "tools/list", json.RawMessage(`{"cursor":""}`), "2026-07-28", "")
		require.NoError(t, requestErr)
		assert.JSONEq(t, `{"ok":true}`, string(response.Result))
		var request requestEnvelope
		require.NoError(t, json.Unmarshal(transport.message.Payload, &request))
		assert.Equal(t, expected, request.ID)
		assert.Equal(t, "2.0", request.JSONRPC)
	}
	transport.respond = func(request requestEnvelope) []byte {
		return []byte(`{"jsonrpc":"2.0","id":` + jsonNumber(request.ID+1) + `,"result":null}`)
	}
	_, err = coordinator.Request(context.Background(), "tools/list", json.RawMessage(`{}`), "2026-07-28", "")
	assert.ErrorIs(t, err, ErrResponseMismatch)
	transport.respond = func(request requestEnvelope) []byte {
		return []byte(`{"jsonrpc":"2.0","id":` + jsonNumber(request.ID) + `,"result":null,"extra":true}`)
	}
	_, err = coordinator.Request(context.Background(), "tools/list", json.RawMessage(`{}`), "2026-07-28", "")
	assert.ErrorIs(t, err, ErrInvalidMessage)
}

func TestHTTPTransportSendsOnlyClosedGatewayHeadersAndAcceptsBoundedJSONOrSSE(t *testing.T) {
	requests := make(chan *http.Request, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		copy := request.Clone(context.Background())
		copy.Body = io.NopCloser(bytes.NewReader(nil))
		requests <- copy
		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = writer.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer server.Close()
	endpoint, err := remote.ParseEndpoint(server.URL+"/mcp", true)
	require.NoError(t, err)
	transport, err := NewHTTPTransport(remote.New(remote.Options{}), endpoint, "Bearer server-secret")
	require.NoError(t, err)
	message := Message{Payload: []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`), Method: "tools/list", ProtocolVersion: "2026-07-28", Name: "server.tool", ParameterHeaders: map[string]string{"Region": "us"}}
	_, err = transport.Exchange(context.Background(), message)
	require.NoError(t, err)
	request := <-requests
	assert.Equal(t, "application/json", request.Header.Get("Content-Type"))
	assert.Equal(t, "application/json, text/event-stream", request.Header.Get("Accept"))
	assert.Equal(t, "2026-07-28", request.Header.Get("MCP-Protocol-Version"))
	assert.Equal(t, "tools/list", request.Header.Get("Mcp-Method"))
	assert.Equal(t, "server.tool", request.Header.Get("Mcp-Name"))
	assert.Equal(t, "us", request.Header.Get("Mcp-Param-Region"))
	assert.Equal(t, "Bearer server-secret", request.Header.Get("Authorization"))
	assert.Empty(t, request.Header.Get("Cookie"))
	assert.Empty(t, request.Header.Get("Mcp-Session-Id"))
	for name := range request.Header {
		assert.Contains(t, []string{"Accept", "Authorization", "Connection", "Content-Length", "Content-Type", "Mcp-Method", "Mcp-Name", "Mcp-Param-Region", "Mcp-Protocol-Version"}, name)
	}
}

type fakeStdio struct {
	frames     chan []byte
	input      *bytes.Buffer
	stopResult bool
	stopped    bool
	mu         sync.Mutex
}

func (runtime *fakeStdio) Frames() <-chan []byte { return runtime.frames }
func (runtime *fakeStdio) Input() io.WriteCloser { return nopWriteCloser{runtime.input} }
func (runtime *fakeStdio) Stop(context.Context) bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.stopped = true
	return runtime.stopResult
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func TestHTTPTransportReturnsFirstSSEEventAndClosesBlockedBody(t *testing.T) {
	probeClose := make(chan struct{})
	closed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n"))
		writer.(http.Flusher).Flush()
		<-probeClose
		chunk := bytes.Repeat([]byte("data: x\n\n"), 4096)
		for {
			if _, err := writer.Write(chunk); err != nil {
				close(closed)
				return
			}
			writer.(http.Flusher).Flush()
		}
	}))
	defer server.Close()
	endpoint, err := remote.ParseEndpoint(server.URL+"/mcp", true)
	require.NoError(t, err)
	transport, err := NewHTTPTransport(remote.New(remote.Options{}), endpoint, "")
	require.NoError(t, err)
	response, err := transport.Exchange(context.Background(), Message{Payload: []byte(`{}`), Method: "server/discover"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":1,"result":{}}`, string(response.Body))
	close(probeClose)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("SSE server could still write after the first event returned")
	}
}

func TestHTTPTransportCloseCancelsBlockedResponse(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		close(started)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		select {
		case <-request.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	endpoint, err := remote.ParseEndpoint(server.URL+"/mcp", true)
	require.NoError(t, err)
	transport, err := NewHTTPTransport(remote.New(remote.Options{}), endpoint, "")
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		_, exchangeErr := transport.Exchange(context.Background(), Message{Payload: []byte(`{}`), Method: "server/discover"})
		done <- exchangeErr
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("blocked HTTP exchange did not start")
	}
	require.NoError(t, transport.Close(context.Background()))
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("HTTP exchange survived transport close")
	}
	close(release)
}

func TestSSEParsingBoundsOneJSONEvent(t *testing.T) {
	value, err := parseSSE([]byte("event: message\ndata: {\"ok\":true}\n\n"))
	require.NoError(t, err)
	assert.JSONEq(t, `{"ok":true}`, string(value))
	_, err = parseSSE([]byte("unexpected\n\n"))
	assert.ErrorIs(t, err, ErrInvalidMessage)
}

func TestStdioTransportUsesBoundedNDJSONAndRequiresStopProof(t *testing.T) {
	runtime := &fakeStdio{frames: make(chan []byte, 1), input: new(bytes.Buffer), stopResult: true}
	transport, err := NewStdioTransport(runtime)
	require.NoError(t, err)
	runtime.frames <- []byte(`{"jsonrpc":"2.0","id":1,"result":{}}`)
	response, err := transport.Exchange(context.Background(), Message{Payload: []byte(`{"jsonrpc":"2.0","id":1,"method":"x","params":{}}`)})
	require.NoError(t, err)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":1,"result":{}}`, string(response.Body))
	assert.True(t, strings.HasSuffix(runtime.input.String(), "\n"))
	require.NoError(t, transport.Close(context.Background()))
	assert.True(t, runtime.stopped)
}

func jsonNumber(value uint64) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
