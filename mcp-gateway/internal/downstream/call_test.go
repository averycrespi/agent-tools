package downstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type partialInput struct{}

func (partialInput) Write([]byte) (int, error) { return 1, io.ErrShortWrite }
func (partialInput) Close() error              { return nil }

type partialStdioRuntime struct{ frames chan []byte }

func (runtime *partialStdioRuntime) Frames() <-chan []byte { return runtime.frames }
func (*partialStdioRuntime) Input() io.WriteCloser         { return partialInput{} }
func (*partialStdioRuntime) Stop(context.Context) bool     { return true }

type callTransport struct {
	mu            sync.Mutex
	kind          TransportKind
	beforeMarker  chan struct{}
	releaseMarker chan struct{}
	handedOff     chan struct{}
	waitForCancel bool
	response      WireResponse
	exchangeErr   error
	messages      []Message
	notifications []Message
	closed        bool
}

func (transport *callTransport) Kind() TransportKind { return transport.kind }

func (transport *callTransport) Exchange(ctx context.Context, message Message) (WireResponse, error) {
	transport.mu.Lock()
	transport.messages = append(transport.messages, message)
	transport.mu.Unlock()
	if transport.beforeMarker != nil {
		close(transport.beforeMarker)
		select {
		case <-transport.releaseMarker:
		case <-ctx.Done():
			return WireResponse{}, ctx.Err()
		}
	}
	if message.MarkHandoff != nil {
		message.MarkHandoff()
	}
	if transport.handedOff != nil {
		close(transport.handedOff)
	}
	if transport.waitForCancel {
		<-ctx.Done()
		return WireResponse{}, ctx.Err()
	}
	return transport.response, transport.exchangeErr
}

func (transport *callTransport) Notify(_ context.Context, message Message) (WireResponse, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.notifications = append(transport.notifications, message)
	return WireResponse{StatusCode: 202}, nil
}

func (transport *callTransport) Close(context.Context) error {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.closed = true
	return nil
}

func TestCallUsesExactInjectedMaximumDeadline(t *testing.T) {
	transport := &callTransport{kind: TransportStdio, response: callSuccess(1)}
	runtime := runtimeForCall(t, EraModern, "", transport)
	var observed time.Duration
	runtime.callDeadline = func(ctx context.Context, duration time.Duration) (context.Context, context.CancelFunc) {
		observed = duration
		return context.WithCancel(ctx)
	}
	call, err := runtime.NewCall("tool", json.RawMessage(`{}`))
	require.NoError(t, err)
	result := call.Execute(context.Background())
	require.NoError(t, result.Err)
	assert.Equal(t, contract.MaximumDownstreamCallDeadline, observed)
}

func TestCallConstructsOneExactPinnedModernAttempt(t *testing.T) {
	transport := &callTransport{kind: TransportStdio, response: callSuccess(1)}
	runtime := runtimeForCall(t, EraModern, "", transport)
	call, err := runtime.NewCall("upstream.tool", json.RawMessage(`{"value":7}`))
	require.NoError(t, err)
	result := call.Execute(context.Background())
	require.NoError(t, result.Err)
	assert.Empty(t, result.Failure)
	require.Len(t, transport.messages, 1)
	assert.Equal(t, "upstream.tool", transport.messages[0].Name)
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"upstream.tool","arguments":{"value":7},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"mcp-gateway","version":"s2"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, string(transport.messages[0].Payload))
	second := call.Execute(context.Background())
	assert.ErrorIs(t, second.Err, ErrCallConsumed)
	assert.Equal(t, FailurePreStart, second.Failure)
	assert.Len(t, transport.messages, 1)
}

func TestCallRejectsInvalidConstructionBeforeTransport(t *testing.T) {
	transport := &callTransport{kind: TransportStdio}
	runtime := runtimeForCall(t, EraLegacy, "", transport)
	for _, test := range []struct {
		name string
		args string
	}{
		{name: "", args: `{}`},
		{name: "tool", args: `null`},
		{name: "tool", args: `[]`},
		{name: "tool", args: `{`},
	} {
		_, err := runtime.NewCall(test.name, json.RawMessage(test.args))
		assert.ErrorIs(t, err, ErrInvalidMessage)
	}
	assert.Empty(t, transport.messages)
}

func TestCallFailureClassComesOnlyFromMonotonicHandoffMarker(t *testing.T) {
	t.Run("cancelled before marker", func(t *testing.T) {
		transport := &callTransport{kind: TransportStdio, beforeMarker: make(chan struct{}), releaseMarker: make(chan struct{})}
		runtime := runtimeForCall(t, EraLegacy, "", transport)
		call, err := runtime.NewCall("tool", json.RawMessage(`{}`))
		require.NoError(t, err)
		done := make(chan CallResult, 1)
		go func() { done <- call.Execute(context.Background()) }()
		<-transport.beforeMarker
		require.NoError(t, call.Cancel(context.Background()))
		result := <-done
		assert.Equal(t, FailurePreStart, result.Failure)
		assert.ErrorIs(t, result.Err, context.Canceled)
		assert.Empty(t, transport.notifications)
	})
	for _, test := range []struct {
		name string
		kind TransportKind
		err  error
	}{
		{name: "pipe partial write", kind: TransportStdio, err: io.ErrShortWrite},
		{name: "HTTP RoundTripper entry", kind: TransportHTTP, err: errors.New("connect failed")},
		{name: "response read failure", kind: TransportHTTP, err: errors.New("read failed")},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &callTransport{kind: test.kind, exchangeErr: test.err}
			runtime := runtimeForCall(t, EraModern, "", transport)
			call, err := runtime.NewCall("tool", json.RawMessage(`{}`))
			require.NoError(t, err)
			result := call.Execute(context.Background())
			assert.Equal(t, FailureStartUncertain, result.Failure)
			assert.ErrorIs(t, result.Err, test.err)
		})
	}
}

func TestCallCompleteInvalidResponsesAreKnownFailures(t *testing.T) {
	for _, test := range []struct {
		name string
		body []byte
		err  error
	}{
		{name: "malformed", body: []byte(`{"jsonrpc":"2.0","id":1,"result":`)},
		{name: "oversized", body: bytes.Repeat([]byte("x"), int(limit("downstream_mcp_body_bytes"))+1)},
		{name: "mismatched ID", body: []byte(`{"jsonrpc":"2.0","id":2,"result":{}}`), err: ErrResponseMismatch},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &callTransport{kind: TransportStdio, response: WireResponse{Body: test.body}}
			runtime := runtimeForCall(t, EraModern, "", transport)
			call, err := runtime.NewCall("tool", json.RawMessage(`{}`))
			require.NoError(t, err)

			result := call.Execute(context.Background())

			assert.Equal(t, FailureResponseInvalid, result.Failure)
			if test.err != nil {
				assert.ErrorIs(t, result.Err, test.err)
			} else {
				assert.ErrorIs(t, result.Err, ErrInvalidMessage)
			}
			assert.Len(t, transport.messages, 1)
		})
	}
}

func TestCallCompleteJSONRPCErrorRemainsReceivedResponse(t *testing.T) {
	transport := &callTransport{kind: TransportStdio, response: WireResponse{Body: []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"private failure","data":{"secret":"canary"}}}`)}}
	runtime := runtimeForCall(t, EraModern, "", transport)
	call, err := runtime.NewCall("tool", json.RawMessage(`{}`))
	require.NoError(t, err)

	result := call.Execute(context.Background())

	require.NoError(t, result.Err)
	assert.Empty(t, result.Failure)
	require.NotNil(t, result.Response.Error)
	assert.Equal(t, int64(-32000), result.Response.Error.Code)
	assert.Len(t, transport.messages, 1)
}

func TestCallCancellationBeforeEntryIsPreStart(t *testing.T) {
	transport := &callTransport{kind: TransportStdio}
	runtime := runtimeForCall(t, EraModern, "", transport)
	call, err := runtime.NewCall("tool", json.RawMessage(`{}`))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := call.Execute(ctx)

	assert.Equal(t, FailurePreStart, result.Failure)
	assert.ErrorIs(t, result.Err, context.Canceled)
	assert.Empty(t, transport.messages)
}

func TestCallDeadlineAfterHandoffRemainsStartUncertain(t *testing.T) {
	transport := &callTransport{kind: TransportHTTP, handedOff: make(chan struct{}), waitForCancel: true}
	runtime := runtimeForCall(t, EraModern, "", transport)
	deadlineCtx, expire := context.WithCancel(context.Background())
	runtime.callDeadline = func(context.Context, time.Duration) (context.Context, context.CancelFunc) {
		return deadlineCtx, func() {}
	}
	call, err := runtime.NewCall("tool", json.RawMessage(`{}`))
	require.NoError(t, err)
	done := make(chan CallResult, 1)
	go func() { done <- call.Execute(context.Background()) }()
	<-transport.handedOff
	expire()

	result := <-done

	assert.Equal(t, FailureStartUncertain, result.Failure)
	assert.ErrorIs(t, result.Err, context.Canceled)
	assert.Len(t, transport.messages, 1)
}

func TestRealStdioResponseStreamLossAfterHandoffIsStartUncertain(t *testing.T) {
	runtimeProcess := &fakeStdio{frames: make(chan []byte), input: new(bytes.Buffer), stopResult: true}
	close(runtimeProcess.frames)
	transport, err := NewStdioTransport(runtimeProcess)
	require.NoError(t, err)
	runtime := runtimeForCall(t, EraLegacy, "", transport)
	call, err := runtime.NewCall("tool", json.RawMessage(`{}`))
	require.NoError(t, err)

	result := call.Execute(context.Background())

	assert.Equal(t, FailureStartUncertain, result.Failure)
	assert.ErrorIs(t, result.Err, ErrTransportClosed)
	assert.NotEmpty(t, runtimeProcess.input.String())
}

func TestRealStdioPartialWriteMarksAttemptStartUncertain(t *testing.T) {
	transport, err := NewStdioTransport(&partialStdioRuntime{frames: make(chan []byte)})
	require.NoError(t, err)
	runtime := runtimeForCall(t, EraLegacy, "", transport)
	call, err := runtime.NewCall("tool", json.RawMessage(`{}`))
	require.NoError(t, err)
	result := call.Execute(context.Background())
	assert.Equal(t, FailureStartUncertain, result.Failure)
	assert.ErrorIs(t, result.Err, ErrTransportClosed)
}

func TestCancellationBranchesSendAtMostOneExactNotification(t *testing.T) {
	tests := []struct {
		name        string
		era         Era
		kind        TransportKind
		session     string
		wantMessage bool
	}{
		{name: "modern stdio", era: EraModern, kind: TransportStdio, wantMessage: true},
		{name: "modern HTTP", era: EraModern, kind: TransportHTTP},
		{name: "legacy stdio", era: EraLegacy, kind: TransportStdio, wantMessage: true},
		{name: "legacy HTTP", era: EraLegacy, kind: TransportHTTP, session: "session-1", wantMessage: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := &callTransport{kind: test.kind, handedOff: make(chan struct{}), waitForCancel: true}
			runtime := runtimeForCall(t, test.era, test.session, transport)
			call, err := runtime.NewCall("tool", json.RawMessage(`{}`))
			require.NoError(t, err)
			done := make(chan CallResult, 1)
			go func() { done <- call.Execute(context.Background()) }()
			<-transport.handedOff
			require.NoError(t, call.Cancel(context.Background()))
			require.NoError(t, call.Cancel(context.Background()))
			result := <-done
			assert.Equal(t, FailureStartUncertain, result.Failure)
			assert.ErrorIs(t, result.Err, context.Canceled)
			if !test.wantMessage {
				assert.Empty(t, transport.notifications)
				return
			}
			require.Len(t, transport.notifications, 1)
			notification := transport.notifications[0]
			assert.Equal(t, "notifications/cancelled", notification.Method)
			assert.Equal(t, test.session, notification.SessionID)
			if test.era == EraModern {
				assert.JSONEq(t, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1,"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"mcp-gateway","version":"s2"},"io.modelcontextprotocol/clientCapabilities":{}}}}`, string(notification.Payload))
			} else {
				assert.JSONEq(t, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`, string(notification.Payload))
			}
		})
	}
}

func TestCallerContextCancellationUsesOwnedNotificationPath(t *testing.T) {
	transport := &callTransport{kind: TransportStdio, handedOff: make(chan struct{}), waitForCancel: true}
	runtime := runtimeForCall(t, EraModern, "", transport)
	call, err := runtime.NewCall("tool", json.RawMessage(`{}`))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan CallResult, 1)
	go func() { done <- call.Execute(ctx) }()
	<-transport.handedOff
	cancel()
	result := <-done
	assert.Equal(t, FailureStartUncertain, result.Failure)
	assert.ErrorIs(t, result.Err, context.Canceled)
	require.Eventually(t, func() bool {
		transport.mu.Lock()
		defer transport.mu.Unlock()
		return len(transport.notifications) == 1
	}, time.Second, time.Millisecond)
}

func TestRealHTTPModernCancellationSendsNoNotificationPOST(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var requests atomic.Int64
	methods := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		methods <- request.Header.Get("Mcp-Method")
		writer.Header().Set("Content-Type", contract.MediaTypeJSON)
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		close(started)
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
	runtime := runtimeForCall(t, EraModern, "", transport)
	call, err := runtime.NewCall("tool", json.RawMessage(`{}`))
	require.NoError(t, err)
	done := make(chan CallResult, 1)
	go func() { done <- call.Execute(context.Background()) }()
	<-started
	require.NoError(t, call.Cancel(context.Background()))
	result := <-done
	assert.Equal(t, FailureStartUncertain, result.Failure)
	assert.Equal(t, int64(1), requests.Load())
	assert.Equal(t, "tools/call", <-methods)
	close(release)
}

func TestLateCancellationDoesNotSendOrChangeCompletedCall(t *testing.T) {
	transport := &callTransport{kind: TransportStdio, response: callSuccess(1)}
	runtime := runtimeForCall(t, EraModern, "", transport)
	call, err := runtime.NewCall("tool", json.RawMessage(`{}`))
	require.NoError(t, err)
	result := call.Execute(context.Background())
	require.NoError(t, result.Err)
	require.NoError(t, call.Cancel(context.Background()))
	assert.Empty(t, transport.notifications)
}

func TestRuntimeCloseCancelsActiveCallWithoutReplay(t *testing.T) {
	transport := &callTransport{kind: TransportStdio, handedOff: make(chan struct{}), waitForCancel: true}
	runtime := runtimeForCall(t, EraModern, "", transport)
	call, err := runtime.NewCall("tool", json.RawMessage(`{}`))
	require.NoError(t, err)
	done := make(chan CallResult, 1)
	go func() { done <- call.Execute(context.Background()) }()
	<-transport.handedOff
	closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, runtime.Close(closeCtx))
	result := <-done
	assert.Equal(t, FailureStartUncertain, result.Failure)
	assert.Len(t, transport.messages, 1)
	assert.Len(t, transport.notifications, 1)
	assert.True(t, transport.closed)
}

func TestLegacySessionLossAfterHandoffIsStartUncertainAndClosesRuntime(t *testing.T) {
	transport := &callTransport{kind: TransportHTTP, response: WireResponse{StatusCode: 404, ContentType: contract.MediaTypeJSON, Body: []byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"lost"}}`)}}
	runtime := runtimeForCall(t, EraLegacy, "session-1", transport)
	call, err := runtime.NewCall("tool", json.RawMessage(`{}`))
	require.NoError(t, err)
	result := call.Execute(context.Background())
	assert.ErrorIs(t, result.Err, ErrSessionLost)
	assert.Equal(t, FailureStartUncertain, result.Failure)
	assert.True(t, transport.closed)
	assert.Len(t, transport.messages, 1)
	assert.Len(t, transport.notifications, 1)
}

func runtimeForCall(t *testing.T, era Era, sessionID string, transport Transport) *Runtime {
	t.Helper()
	coordinator, err := NewCoordinator(transport)
	require.NoError(t, err)
	return newRuntime(era, coordinator, sessionID)
}

func callSuccess(requestID uint64) WireResponse {
	body, _ := json.Marshal(struct {
		JSONRPC string         `json:"jsonrpc"`
		ID      uint64         `json:"id"`
		Result  map[string]any `json:"result"`
	}{JSONRPC: "2.0", ID: requestID, Result: map[string]any{}})
	return WireResponse{Body: body}
}
