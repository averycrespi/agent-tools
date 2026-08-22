package mcpingress

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/httpboundary"
	"github.com/stretchr/testify/require"
)

type fakeAuthenticator struct {
	binding Binding
	err     error
	calls   atomic.Int64
}

func (authenticator *fakeAuthenticator) Authenticate(_ context.Context, bearer string) (Binding, error) {
	authenticator.calls.Add(1)
	if bearer != contract.AgentBearerPrefix+"valid" {
		return Binding{}, ErrAuthenticationRequired
	}
	return authenticator.binding, authenticator.err
}

type readSpy struct {
	reads atomic.Int64
}

func (spy *readSpy) Read([]byte) (int, error) {
	spy.reads.Add(1)
	return 0, io.EOF
}

func (spy *readSpy) Close() error { return nil }

func TestAgentAuthenticationPrecedesMCPBodyAndHandlerWork(t *testing.T) {
	t.Parallel()
	authenticator := &fakeAuthenticator{binding: Binding{
		PrincipalID:  "01J00000000000000000000000",
		CredentialID: "01J00000000000000000000001",
		ExpiresAt:    time.Now().Add(time.Hour),
	}}
	var handled atomic.Int64
	ingress := New(Options{Authenticator: authenticator})
	boundary, err := httpboundary.New(httpboundary.Options{
		Authority: contract.DefaultAuthority,
		Authenticate: func(ctx context.Context, request *http.Request, authority contract.CredentialAuthority) (context.Context, error) {
			return ingress.Authenticate(ctx, request, authority)
		},
		Next: http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
			binding, ok := BindingFromContext(request.Context())
			require.True(t, ok)
			require.Equal(t, authenticator.binding, binding)
			handled.Add(1)
		}),
	})
	require.NoError(t, err)

	for _, test := range []struct {
		name, authorization string
		status              int
	}{
		{name: "missing", status: http.StatusUnauthorized},
		{name: "malformed", authorization: "Basic value", status: http.StatusUnauthorized},
		{name: "wrong domain", authorization: "Bearer " + contract.AdminBearerPrefix + "value", status: http.StatusForbidden},
		{name: "unknown agent", authorization: "Bearer " + contract.AgentBearerPrefix + "unknown", status: http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := &readSpy{}
			request := httptest.NewRequest(http.MethodPost, "/mcp", body)
			request.Host = contract.DefaultAuthority
			if test.authorization != "" {
				request.Header.Set("Authorization", test.authorization)
			}
			response := httptest.NewRecorder()
			boundary.ServeHTTP(response, request)
			require.Equal(t, test.status, response.Code)
			require.Zero(t, body.reads.Load(), "authentication failure read MCP body")
			require.Zero(t, handled.Load())
		})
	}

	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
	request.Host = contract.DefaultAuthority
	request.Header.Set("Authorization", "Bearer "+contract.AgentBearerPrefix+"valid")
	response := httptest.NewRecorder()
	boundary.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
	require.Equal(t, int64(1), handled.Load())
}

func TestAgentAuthenticationRejectsExpiredBindingAndProductionDeniesAll(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	authenticator := &fakeAuthenticator{binding: Binding{
		PrincipalID:  "01J00000000000000000000000",
		CredentialID: "01J00000000000000000000001",
		ExpiresAt:    now.Add(-time.Second),
	}}
	ingress := New(Options{Authenticator: authenticator, Now: func() time.Time { return now }})
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	request.Header.Set("Authorization", "Bearer "+contract.AgentBearerPrefix+"valid")
	_, err := ingress.Authenticate(context.Background(), request, contract.AuthorityAgent)
	var boundaryFailure httpboundary.Error
	require.ErrorAs(t, err, &boundaryFailure)
	require.Equal(t, contract.ProblemAuthenticationRequired, boundaryFailure.Code)

	production := New(Options{Authenticator: DenyAllAuthenticator{}})
	request.Header.Set("Authorization", "Bearer "+contract.AgentBearerPrefix+"valid")
	_, err = production.Authenticate(context.Background(), request, contract.AuthorityAgent)
	require.Error(t, err)
	require.True(t, errors.Is(ErrAuthenticationRequired, ErrAuthenticationRequired))
}
