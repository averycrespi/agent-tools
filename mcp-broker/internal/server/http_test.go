package server

import (
	"errors"
	"fmt"
	"testing"

	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/stretchr/testify/require"
)

// TestIsUnauthorized guards the 401 detection that lets the plain-client-first
// connect upgrade to OAuth. mcp-go signals "authorization required" with
// different error types depending on whether an OAuth handler is configured;
// a regression here silently disables OAuth (servers fail with a bare
// "authorization required" instead of starting the browser flow).
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
