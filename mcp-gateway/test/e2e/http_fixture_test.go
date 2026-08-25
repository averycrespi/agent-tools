//go:build e2e

package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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

type rawHTTPFixture struct {
	t       *testing.T
	mode    string
	session string
	server  *httptest.Server

	mu             sync.Mutex
	events         []httpFixtureEvent
	blockNextList  bool
	blocked        chan struct{}
	cancelled      chan struct{}
	release        chan struct{}
	loseSession    bool
	closeFixture   sync.Once
	closeRelease   sync.Once
	closeBlocked   sync.Once
	closeCancelled sync.Once
}

func newRawHTTPFixture(t *testing.T, mode string) *rawHTTPFixture {
	t.Helper()
	fixture := &rawHTTPFixture{t: t, mode: mode, session: "fixture-session", release: make(chan struct{})}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.serveHTTP))
	t.Cleanup(fixture.Close)
	return fixture
}

func (fixture *rawHTTPFixture) URL() string { return fixture.server.URL + "/mcp" }

func (fixture *rawHTTPFixture) Close() {
	fixture.closeFixture.Do(func() {
		fixture.closeRelease.Do(func() { close(fixture.release) })
		fixture.server.CloseClientConnections()
		fixture.server.Close()
	})
}

func (fixture *rawHTTPFixture) ArmBlockedList() (<-chan struct{}, <-chan struct{}) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.blockNextList = true
	fixture.blocked = make(chan struct{})
	fixture.cancelled = make(chan struct{})
	return fixture.blocked, fixture.cancelled
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
	block := envelope.Method == "tools/list" && fixture.blockNextList
	if block {
		fixture.blockNextList = false
		fixture.closeBlocked.Do(func() { close(fixture.blocked) })
	}
	loseSession := fixture.loseSession
	fixture.mu.Unlock()
	if block {
		select {
		case <-request.Context().Done():
			fixture.closeCancelled.Do(func() { close(fixture.cancelled) })
		case <-fixture.release:
		}
		return
	}

	writer.Header().Set("Content-Type", "application/json")
	switch envelope.Method {
	case "server/discover":
		writer.Header().Set("Set-Cookie", "ambient=forbidden")
		if fixture.mode == "auto" {
			writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(writer, "Bad Request: Unsupported protocol version\n")
			return
		}
		_, _ = io.WriteString(writer, rpcResult(envelope.ID, `{"ttlMs":0,"cacheScope":"public","supportedVersions":["2026-07-28"],"capabilities":{}}`))
	case "initialize":
		writer.Header().Set("Mcp-Session-Id", fixture.session)
		_, _ = io.WriteString(writer, rpcResult(envelope.ID, `{"protocolVersion":"2025-11-25","capabilities":{},"serverInfo":{"name":"http-fixture","version":"1"}}`))
	case "notifications/initialized":
		writer.Header().Set("Mcp-Session-Id", fixture.session)
		writer.WriteHeader(http.StatusAccepted)
	case "tools/list":
		if fixture.mode == "auto" {
			if loseSession {
				writer.Header().Set("Mcp-Session-Id", "replaced-session")
			} else {
				writer.Header().Set("Mcp-Session-Id", fixture.session)
			}
		}
		if envelope.Params.Cursor == "" {
			_, _ = io.WriteString(writer, rpcResult(envelope.ID, `{"tools":[{"name":"http-alpha","inputSchema":{"type":"object"}}],"nextCursor":"page-2"}`))
		} else {
			_, _ = io.WriteString(writer, rpcResult(envelope.ID, `{"tools":[{"name":"http-beta","inputSchema":{"type":"object"}}],"nextCursor":null}`))
		}
	default:
		writer.WriteHeader(http.StatusBadRequest)
	}
}

func rpcResult(id uint64, result string) string {
	return `{"jsonrpc":"2.0","id":` + strconv.FormatUint(id, 10) + `,"result":` + result + `}`
}
