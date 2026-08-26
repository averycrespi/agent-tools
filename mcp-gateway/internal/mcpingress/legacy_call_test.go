package mcpingress

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newLegacyCallHarness(t *testing.T, call ToolsCallService) (*Handler, *testAuthority, http.Handler) {
	t.Helper()
	authority := newTestAuthority(t)
	authority.add(t, "valid", contract.VisibilityRequestable)
	authority.add(t, "other", contract.VisibilityAll)
	handler := New(Options{
		Authenticator: authority,
		CallTools:     call,
		Now:           testutil.NewFakeClock(time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)).Now,
		Entropy:       testutil.NewFakeEntropy(makeDistinctEntropy(4)),
	})
	return handler, authority, newLegacyBoundary(t, handler)
}

func initializeLegacyCall(t *testing.T, boundary http.Handler) string {
	t.Helper()
	response := httptest.NewRecorder()
	boundary.ServeHTTP(response, legacyRequest(http.MethodPost, legacyInitialize, "valid", ""))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.JSONEq(t, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"mcp-gateway","version":"s1"}}}`, response.Body.String())
	assert.NotContains(t, response.Body.String(), "listChanged")
	sessionID := response.Header().Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID)
	return sessionID
}

func legacyCallRequest(id, bearer, sessionID string) *http.Request {
	body := `{"jsonrpc":"2.0","id":` + id + `,"method":"tools/call","params":{"name":"sample.echo","arguments":{"value":1e0}}}`
	return legacyRequest(http.MethodPost, body, bearer, sessionID)
}

func TestLegacyCallUsesReauthenticatedRequestLeaseAndSharedEnvelope(t *testing.T) {
	var gotLease *authorization.Lease
	var gotRequest ToolsCallRequest
	service := toolsCallServiceFunc(func(_ context.Context, lease *authorization.Lease, request ToolsCallRequest) ToolsCallResponse {
		gotLease = lease
		gotRequest = request
		return ToolsCallResponse{Result: &ToolsCallResult{Content: []json.RawMessage{json.RawMessage(`{"type":"text","text":"ok"}`)}}}
	})
	handler, authority, boundary := newLegacyCallHarness(t, service)
	sessionID := initializeLegacyCall(t, boundary)
	leases := authority.captured()
	require.Len(t, leases, 1)
	sessionLease := leases[0]

	response := httptest.NewRecorder()
	boundary.ServeHTTP(response, legacyCallRequest(`"legacy"`, "valid", sessionID))

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, `{"jsonrpc":"2.0","id":"legacy","result":{"content":[{"type":"text","text":"ok"}]}}`, response.Body.String())
	leases = authority.captured()
	require.Len(t, leases, 2)
	require.NotNil(t, gotLease)
	assert.Same(t, leases[1], gotLease)
	assert.NotSame(t, sessionLease, gotLease)
	assert.True(t, gotRequest.WireValid)
	assert.Equal(t, "1e0", gotRequest.Params.Object[1].Value.Object[0].Value.Number)
	assert.True(t, leaseDone(gotLease))
	assert.False(t, leaseDone(sessionLease))
	handler.Shutdown()
	assert.True(t, leaseDone(sessionLease))
}

func TestLegacyCallRejectsSessionMismatchDeleteAndRestartBeforeService(t *testing.T) {
	var calls atomic.Int32
	service := toolsCallServiceFunc(func(context.Context, *authorization.Lease, ToolsCallRequest) ToolsCallResponse {
		calls.Add(1)
		return ToolsCallResponse{ErrorCode: contract.CallRejected, InvocationID: "01J60000000000000000000001"}
	})
	handler, authority, boundary := newLegacyCallHarness(t, service)
	sessionID := initializeLegacyCall(t, boundary)

	response := httptest.NewRecorder()
	boundary.ServeHTTP(response, legacyCallRequest(`1`, "other", sessionID))
	assert.Equal(t, http.StatusNotFound, response.Code)
	assert.Zero(t, calls.Load())

	response = httptest.NewRecorder()
	boundary.ServeHTTP(response, legacyRequest(http.MethodDelete, "", "valid", sessionID))
	require.Equal(t, http.StatusNoContent, response.Code, response.Body.String())
	response = httptest.NewRecorder()
	boundary.ServeHTTP(response, legacyCallRequest(`2`, "valid", sessionID))
	assert.Equal(t, http.StatusNotFound, response.Code)
	assert.Zero(t, calls.Load())

	restartSession := initializeLegacyCall(t, boundary)
	handler.Shutdown()
	restarted := New(Options{Authenticator: authority, CallTools: service})
	defer restarted.Shutdown()
	response = httptest.NewRecorder()
	newLegacyBoundary(t, restarted).ServeHTTP(response, legacyCallRequest(`3`, "valid", restartSession))
	assert.Equal(t, http.StatusNotFound, response.Code)
	assert.Zero(t, calls.Load())
}

func TestLegacyCallCredentialMutationsCloseSessionBeforeService(t *testing.T) {
	for _, mutation := range []string{"replacement", "revocation", "disable"} {
		t.Run(mutation, func(t *testing.T) {
			var calls atomic.Int32
			service := toolsCallServiceFunc(func(context.Context, *authorization.Lease, ToolsCallRequest) ToolsCallResponse {
				calls.Add(1)
				return ToolsCallResponse{ErrorCode: contract.CallRejected, InvocationID: "01J60000000000000000000001"}
			})
			handler, authority, boundary := newLegacyCallHarness(t, service)
			defer handler.Shutdown()
			sessionID := initializeLegacyCall(t, boundary)
			handler.mu.Lock()
			done := handler.legacy[sessionID].done
			handler.mu.Unlock()
			principal, err := authority.repository.GetPrincipal(t.Context(), authority.principals["valid"])
			require.NoError(t, err)
			bearer := "valid"
			switch mutation {
			case "replacement":
				credential, issueErr := authority.repository.IssueCredential(t.Context(), principal.ID, principal.Revision)
				err = issueErr
				if issueErr == nil {
					bearer = "replacement"
					authority.mu.Lock()
					authority.bearers[contract.AgentBearerPrefix+bearer] = credential.Bearer
					authority.mu.Unlock()
				}
			case "revocation":
				_, err = authority.repository.RevokeCredential(t.Context(), principal.ID, principal.Revision)
			case "disable":
				disabled := contract.PrincipalDisabled
				_, err = authority.repository.PatchPrincipal(t.Context(), principal.ID, authorization.PatchPrincipalRequest{ExpectedRevision: principal.Revision, State: &disabled})
			}
			require.NoError(t, err)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("credential mutation did not close legacy call session")
			}
			response := httptest.NewRecorder()
			boundary.ServeHTTP(response, legacyCallRequest(`1`, bearer, sessionID))
			if mutation == "replacement" {
				assert.Equal(t, http.StatusNotFound, response.Code)
			} else {
				assert.Equal(t, http.StatusUnauthorized, response.Code)
			}
			assert.Zero(t, calls.Load())
		})
	}
}

func TestLegacyCallDisconnectCancelsRequestWithoutClosingOrReplayingSession(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int32
	service := toolsCallServiceFunc(func(ctx context.Context, _ *authorization.Lease, _ ToolsCallRequest) ToolsCallResponse {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			return ToolsCallResponse{ErrorCode: contract.OutcomeUnknown, InvocationID: "01J60000000000000000000004"}
		}
		return ToolsCallResponse{ErrorCode: contract.CallRejected, InvocationID: "01J60000000000000000000005"}
	})
	handler, authority, boundary := newLegacyCallHarness(t, service)
	defer handler.Shutdown()
	sessionID := initializeLegacyCall(t, boundary)
	sessionLease := authority.captured()[0]
	ctx, cancel := context.WithCancel(context.Background())
	first := legacyCallRequest(`1`, "valid", sessionID).WithContext(ctx)
	firstResponse := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		boundary.ServeHTTP(firstResponse, first)
		close(done)
	}()
	<-started
	cancel()
	<-done
	assert.Empty(t, firstResponse.Body.String())
	assert.Equal(t, int32(1), calls.Load())
	assert.False(t, leaseDone(sessionLease))

	secondResponse := httptest.NewRecorder()
	boundary.ServeHTTP(secondResponse, legacyCallRequest(`2`, "valid", sessionID))
	assert.Equal(t, int32(2), calls.Load())
	assert.Equal(t, `{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"Call rejected","data":{"code":"call_rejected","invocationId":"01J60000000000000000000005"}}}`, secondResponse.Body.String())
	assert.False(t, leaseDone(sessionLease))
}
