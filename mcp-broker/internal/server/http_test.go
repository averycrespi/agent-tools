package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"

	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/stretchr/testify/require"
)

// TestIsUnauthorized guards the 401 detection that lets the plain-client-first
// connect upgrade to OAuth. mcp-go signals "authorization required" with
// different error types depending on whether an OAuth handler is configured;
// a regression here silently disables OAuth (servers fail with a bare
// "authorization required" instead of starting the browser flow).
func TestHTTPBackendTimeout_Default(t *testing.T) {
	require.Equal(t, 2*time.Minute, httpBackendTimeout(config.ServerConfig{}))
}

func TestHTTPBackendTimeout_Custom(t *testing.T) {
	srv := config.ServerConfig{HTTPTimeoutSeconds: 30}
	require.Equal(t, 30*time.Second, httpBackendTimeout(srv))
}

func TestHTTPBackendRecoversAfterServerLosesSessions(t *testing.T) {
	var mu sync.Mutex
	currentSession := "session-1"
	initializeCount := 0

	backendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))

		mu.Lock()
		defer mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if request.Method == "initialize" {
			initializeCount++
			w.Header().Set("Mcp-Session-Id", currentSession)
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-03-26","capabilities":{},"serverInfo":{"name":"test","version":"1"}}}`, request.ID)
			return
		}
		if request.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if r.Header.Get("Mcp-Session-Id") != currentSession {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32600,"message":"Bad Request: Missing session ID"}}`, request.ID)
			return
		}
		if request.Method == "tools/call" {
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"content":[]}}`, request.ID)
			return
		}
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[]}}`, request.ID)
	}))
	defer backendServer.Close()

	ctx := context.Background()
	backend, err := newHTTPBackend(ctx, ctx, "test", config.ServerConfig{
		Type: "streamable-http",
		URL:  backendServer.URL,
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, backend.Close()) }()

	mu.Lock()
	currentSession = "session-2"
	mu.Unlock()

	tools, err := backend.ListTools(ctx)
	require.NoError(t, err)
	require.Empty(t, tools)

	mu.Lock()
	currentSession = "session-3"
	mu.Unlock()

	_, err = backend.CallTool(ctx, "test", nil)
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 3, initializeCount)
}

func TestIsSessionInvalid(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "terminated session", err: transport.ErrSessionTerminated, want: true},
		{name: "missing session ID", err: errors.New("invalid request: Bad Request: Missing session ID"), want: true},
		{name: "unrelated error", err: errors.New("connection refused"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isSessionInvalid(tt.err))
		})
	}
}

func TestIsUnauthorized(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "plain client 401 (AuthorizationRequiredError)",
			err:  &transport.AuthorizationRequiredError{},
			want: true,
		},
		{
			name: "OAuth client 401 (OAuthAuthorizationRequiredError)",
			err:  &transport.OAuthAuthorizationRequiredError{},
			want: true,
		},
		{
			name: "bare ErrUnauthorized sentinel",
			err:  transport.ErrUnauthorized,
			want: true,
		},
		{
			name: "wrapped AuthorizationRequiredError",
			err:  fmt.Errorf("initialize: %w", &transport.AuthorizationRequiredError{}),
			want: true,
		},
		{
			name: "unrelated error",
			err:  errors.New("connection refused"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isUnauthorized(tt.err))
		})
	}
}
