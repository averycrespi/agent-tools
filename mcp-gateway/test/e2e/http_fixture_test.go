//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type httpFixtureEvent struct {
	Method         string
	MethodHeader   string
	Body           string
	ID             uint64
	Cursor         string
	Protocol       string
	Session        string
	Host           string
	Remote         string
	Authorization  string
	Cookie         string
	Accept         string
	ContentType    string
	AcceptEncoding string
	Forwarded      string
	Close          bool
}

type httpFixtureBarrier struct {
	method          string
	entered         chan struct{}
	cancelled       chan struct{}
	completed       chan struct{}
	release         chan struct{}
	holdAfterCancel bool
	once            sync.Once
}

func (barrier *httpFixtureBarrier) Release() { barrier.once.Do(func() { close(barrier.release) }) }

type fixtureTool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
}

type rawHTTPFixture struct {
	t                 *testing.T
	mode              string
	tools             []fixtureTool
	session           string
	sessionGeneration uint64
	server            *httptest.Server

	mu           sync.Mutex
	events       []httpFixtureEvent
	barrier      *httpFixtureBarrier
	barriers     []*httpFixtureBarrier
	loseSession  bool
	closeFixture sync.Once
}

func newRawHTTPFixture(t *testing.T, mode string) *rawHTTPFixture {
	t.Helper()
	return startRawHTTPFixture(t, mode, nil)
}

func newRawHTTPFixtureWithTools(t *testing.T, mode string, tools []fixtureTool) *rawHTTPFixture {
	t.Helper()
	cloned := make([]fixtureTool, len(tools))
	copy(cloned, tools)
	return startRawHTTPFixture(t, mode, cloned)
}

func startRawHTTPFixture(t *testing.T, mode string, tools []fixtureTool) *rawHTTPFixture {
	t.Helper()
	fixture := &rawHTTPFixture{t: t, mode: mode, tools: tools}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(fixture.Close)
	return fixture
}

func (fixture *rawHTTPFixture) URL() string { return fixture.server.URL + "/mcp" }

func (fixture *rawHTTPFixture) Close() {
	fixture.closeFixture.Do(func() {
		fixture.mu.Lock()
		barriers := append([]*httpFixtureBarrier(nil), fixture.barriers...)
		fixture.mu.Unlock()
		for _, barrier := range barriers {
			barrier.Release()
		}
		fixture.server.CloseClientConnections()
		fixture.server.Close()
	})
}

func (fixture *rawHTTPFixture) Arm(method string) *httpFixtureBarrier {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	barrier := &httpFixtureBarrier{method: method, entered: make(chan struct{}), cancelled: make(chan struct{}), completed: make(chan struct{}), release: make(chan struct{})}
	fixture.barrier = barrier
	fixture.barriers = append(fixture.barriers, barrier)
	return barrier
}

func (fixture *rawHTTPFixture) ArmBlockedList() (<-chan struct{}, <-chan struct{}) {
	barrier := fixture.Arm("tools/list")
	return barrier.entered, barrier.cancelled
}

func (fixture *rawHTTPFixture) ArmLateBlockedList() *httpFixtureBarrier {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	barrier := &httpFixtureBarrier{method: "tools/list", entered: make(chan struct{}), cancelled: make(chan struct{}), completed: make(chan struct{}), release: make(chan struct{}), holdAfterCancel: true}
	fixture.barrier = barrier
	fixture.barriers = append(fixture.barriers, barrier)
	return barrier
}

func (fixture *rawHTTPFixture) SetMode(mode string) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.mode = mode
}

func (fixture *rawHTTPFixture) LoseSession() {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.loseSession = true
}

func (fixture *rawHTTPFixture) Events() []httpFixtureEvent {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return append([]httpFixtureEvent(nil), fixture.events...)
}

func (fixture *rawHTTPFixture) Session() string {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return fixture.session
}

func (fixture *rawHTTPFixture) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(io.LimitReader(request.Body, 256*1024))
	require.NoError(fixture.t, err)
	var envelope struct {
		ID     uint64 `json:"id"`
		Method string `json:"method"`
		Params struct {
			Cursor string `json:"cursor"`
		} `json:"params"`
	}
	require.NoError(fixture.t, json.Unmarshal(body, &envelope))
	event := httpFixtureEvent{
		Method: envelope.Method, MethodHeader: request.Header.Get("Mcp-Method"), Body: string(body), ID: envelope.ID, Cursor: envelope.Params.Cursor,
		Protocol: request.Header.Get("Mcp-Protocol-Version"), Session: request.Header.Get("Mcp-Session-Id"),
		Host: request.Host, Remote: request.RemoteAddr, Authorization: request.Header.Get("Authorization"),
		Cookie: request.Header.Get("Cookie"), Accept: request.Header.Get("Accept"), ContentType: request.Header.Get("Content-Type"),
		AcceptEncoding: request.Header.Get("Accept-Encoding"), Forwarded: request.Header.Get("Forwarded") + request.Header.Get("X-Forwarded-For") + request.Header.Get("X-Forwarded-Host"), Close: request.Close,
	}
	fixture.mu.Lock()
	fixture.events = append(fixture.events, event)
	barrier := fixture.barrier
	if barrier != nil && barrier.method == envelope.Method {
		fixture.barrier = nil
		close(barrier.entered)
	} else {
		barrier = nil
	}
	loseSession, mode, session := fixture.loseSession, fixture.mode, fixture.session
	tools := append([]fixtureTool(nil), fixture.tools...)
	customTools := fixture.tools != nil
	fixture.mu.Unlock()
	if barrier != nil {
		select {
		case <-request.Context().Done():
			close(barrier.cancelled)
			if barrier.holdAfterCancel {
				<-barrier.release
			}
		case <-barrier.release:
		}
		if request.Context().Err() != nil {
			close(barrier.completed)
			return
		}
		close(barrier.completed)
	}

	writer.Header().Set("Content-Type", "application/json")
	switch envelope.Method {
	case "server/discover":
		writer.Header().Set("Set-Cookie", "ambient=forbidden")
		if mode == "auto" {
			writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(writer, "Bad Request: Unsupported protocol version\n")
			return
		}
		_, _ = io.WriteString(writer, rpcResult(envelope.ID, `{"ttlMs":0,"cacheScope":"public","supportedVersions":["2026-07-28"],"capabilities":{}}`))
	case "initialize":
		fixture.mu.Lock()
		fixture.sessionGeneration++
		fixture.session = "fixture-session-" + strconv.FormatUint(fixture.sessionGeneration, 10)
		session = fixture.session
		fixture.mu.Unlock()
		writer.Header().Set("Mcp-Session-Id", session)
		_, _ = io.WriteString(writer, rpcResult(envelope.ID, `{"protocolVersion":"2025-11-25","capabilities":{},"serverInfo":{"name":"http-fixture","version":"1"}}`))
	case "notifications/initialized":
		writer.Header().Set("Mcp-Session-Id", session)
		writer.WriteHeader(http.StatusAccepted)
	case "tools/list":
		if mode == "auto" {
			if loseSession {
				writer.Header().Set("Mcp-Session-Id", "replaced-session")
			} else {
				writer.Header().Set("Mcp-Session-Id", session)
			}
		}
		if customTools {
			fixture.writeToolsPage(writer, envelope.ID, envelope.Params.Cursor, tools)
		} else if envelope.Params.Cursor == "" {
			_, _ = io.WriteString(writer, rpcResult(envelope.ID, `{"tools":[{"name":"http-alpha","inputSchema":{"type":"object"}}],"nextCursor":"page-2"}`))
		} else {
			_, _ = io.WriteString(writer, rpcResult(envelope.ID, `{"tools":[{"name":"http-beta","inputSchema":{"type":"object"}}],"nextCursor":null}`))
		}
	default:
		writer.WriteHeader(http.StatusBadRequest)
	}
}

func (fixture *rawHTTPFixture) writeToolsPage(writer http.ResponseWriter, id uint64, cursor string, tools []fixtureTool) {
	const pageSize = 100
	offset := 0
	if cursor != "" {
		var err error
		offset, err = strconv.Atoi(strings.TrimPrefix(cursor, "offset-"))
		if err != nil || !strings.HasPrefix(cursor, "offset-") || offset < 0 || offset > len(tools) {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
	}
	end := min(offset+pageSize, len(tools))
	var nextCursor *string
	if end < len(tools) {
		next := "offset-" + strconv.Itoa(end)
		nextCursor = &next
	}
	result, err := json.Marshal(struct {
		Tools      []fixtureTool `json:"tools"`
		NextCursor *string       `json:"nextCursor"`
	}{Tools: tools[offset:end], NextCursor: nextCursor})
	if err != nil {
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}
	_, _ = io.WriteString(writer, rpcResult(id, string(result)))
}

func rpcResult(id uint64, result string) string {
	return `{"jsonrpc":"2.0","id":` + strconv.FormatUint(id, 10) + `,"result":` + result + `}`
}
