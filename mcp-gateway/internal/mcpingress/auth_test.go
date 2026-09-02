package mcpingress

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/httpboundary"
	"github.com/stretchr/testify/require"
)

type authenticatorFunc func(context.Context, string) (*authorization.Lease, error)

func (authenticate authenticatorFunc) Authenticate(ctx context.Context, bearer string) (*authorization.Lease, error) {
	return authenticate(ctx, bearer)
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
	authority := newTestAuthority(t)
	authority.add(t, "valid", contract.VisibilityRequestable)
	var handled atomic.Int64
	ingress := New(Options{Authenticator: authority})
	boundary, err := httpboundary.New(httpboundary.Options{
		Authority: contract.DefaultAuthority,
		Authenticate: func(ctx context.Context, request *http.Request, credentialAuthority contract.CredentialAuthority) (context.Context, error) {
			return ingress.Authenticate(ctx, request, credentialAuthority)
		},
		Next: http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
			lease, ok := LeaseFromContext(request.Context())
			require.True(t, ok)
			require.True(t, lease.Current())
			require.Equal(t, authority.principals["valid"], lease.Binding().PrincipalID)
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
	leases := authority.captured()
	require.Len(t, leases, 1)
	require.True(t, leaseDone(leases[0]), "request lease survived handler completion")
}

func TestAgentAuthenticationRejectsInvalidLeaseAndProductionDeniesAll(t *testing.T) {
	authority := newTestAuthority(t)
	authority.add(t, "valid", contract.VisibilityRequestable)
	cancelled, err := authority.Authenticate(context.Background(), contract.AgentBearerPrefix+"valid")
	require.NoError(t, err)
	cancelled.Release()
	ingress := New(Options{Authenticator: authenticatorFunc(func(context.Context, string) (*authorization.Lease, error) {
		return cancelled, nil
	})})
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	request.Header.Set("Authorization", "Bearer "+contract.AgentBearerPrefix+"valid")
	_, err = ingress.Authenticate(context.Background(), request, contract.AuthorityAgent)
	var boundaryFailure httpboundary.Error
	require.ErrorAs(t, err, &boundaryFailure)
	require.Equal(t, contract.ProblemAuthenticationRequired, boundaryFailure.Code)

	production := New(Options{Authenticator: DenyAllAuthenticator{}})
	_, err = production.Authenticate(context.Background(), request, contract.AuthorityAgent)
	require.Error(t, err)
}

func TestBoundaryReleasesLeaseWhenDrainingAbortsAfterAuthentication(t *testing.T) {
	authority := newTestAuthority(t)
	authority.add(t, "valid", contract.VisibilityRequestable)
	ingress := New(Options{Authenticator: authority})
	boundary, err := httpboundary.New(httpboundary.Options{
		Authority: contract.DefaultAuthority, Draining: func() bool { return true },
		Authenticate: ingress.Authenticate,
		Next:         ingress,
	})
	require.NoError(t, err)
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
	request.Host = contract.DefaultAuthority
	request.Header.Set("Authorization", "Bearer "+contract.AgentBearerPrefix+"valid")
	response := httptest.NewRecorder()
	boundary.ServeHTTP(response, request)
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	leases := authority.captured()
	require.Len(t, leases, 1)
	require.True(t, leaseDone(leases[0]))
}
