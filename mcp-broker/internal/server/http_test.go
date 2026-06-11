package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

// TestIsUnauthorized guards the 401 detection that lets the plain-client-first
// connect upgrade to OAuth. mcp-go signals "authorization required" with
// different error types depending on whether an OAuth handler is configured;
// a regression here silently disables OAuth (servers fail with a bare
// "authorization required" instead of starting the browser flow).
func TestNewHTTPBackendReturnsWhenServerBlocks(t *testing.T) {
	unblock := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-unblock
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer close(unblock)

	start := time.Now()
	_, err := newHTTPBackend(context.Background(), "blocked", config.ServerConfig{URL: srv.URL + "/mcp", TimeoutSeconds: 1})

	require.Error(t, err)
	require.Less(t, time.Since(start), 2*time.Second)
	require.ErrorContains(t, err, "initialize server")
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
