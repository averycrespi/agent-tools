package mcpingress

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/discovery"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/httpboundary"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newLegacyDiscoveryHarness(t *testing.T, list ToolsListService) (*Handler, *httpboundary.Boundary, *testAuthority) {
	t.Helper()
	authority := newTestAuthority(t)
	authority.add(t, "valid", contract.VisibilityRequestable)
	authority.add(t, "other", contract.VisibilityAll)
	handler := New(Options{
		Authenticator: authority, ListTools: list,
		Now:     testutil.NewFakeClock(time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC)).Now,
		Entropy: testutil.NewFakeEntropy(makeDistinctEntropy(4)),
	})
	return handler, newLegacyBoundary(t, handler), authority
}

func initializeLegacyDiscovery(t *testing.T, boundary *httpboundary.Boundary) string {
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

func TestLegacyDiscoveryUsesReauthenticatedRequestLeaseAndSharedCodec(t *testing.T) {
	var gotLease *authorization.Lease
	calls := 0
	list := listToolsFunc(func(ctx context.Context, lease *authorization.Lease, cursor string, encode ToolsListEncoder) ([]byte, error) {
		calls++
		gotLease = lease
		assert.Empty(t, cursor)
		return encode(ctx, []*discovery.Tool{{Name: "legacy.tool", InputSchema: json.RawMessage(`{"type":"object"}`)}}, "")
	})
	handler, boundary, authority := newLegacyDiscoveryHarness(t, list)
	sessionID := initializeLegacyDiscovery(t, boundary)
	leases := authority.captured()
	require.Len(t, leases, 1)
	sessionLease := leases[0]

	response := httptest.NewRecorder()
	boundary.ServeHTTP(response, legacyRequest(http.MethodPost, `{"jsonrpc":"2.0","id":"legacy-list","method":"tools/list"}`, "valid", sessionID))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, `{"jsonrpc":"2.0","id":"legacy-list","result":{"tools":[{"inputSchema":{"type":"object"},"name":"legacy.tool"}]}}`, response.Body.String())
	leases = authority.captured()
	require.Len(t, leases, 2)
	assert.Same(t, leases[1], gotLease)
	assert.True(t, leaseDone(leases[1]))
	assert.False(t, leaseDone(sessionLease))

	response = httptest.NewRecorder()
	boundary.ServeHTTP(response, legacyRequest(http.MethodPost, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{}}`, "valid", sessionID))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, `{"jsonrpc":"2.0","id":3,"error":{"code":-32601,"message":"Method not found."}}`, response.Body.String())
	assert.Equal(t, 1, calls)

	response = httptest.NewRecorder()
	boundary.ServeHTTP(response, legacyRequest(http.MethodPost, `{"jsonrpc":"2.0","id":4,"method":"tools/list","params":{}}`, "other", sessionID))
	assert.Equal(t, http.StatusNotFound, response.Code)
	assert.Equal(t, 1, calls)

	response = httptest.NewRecorder()
	boundary.ServeHTTP(response, legacyRequest(http.MethodDelete, "", "valid", sessionID))
	require.Equal(t, http.StatusNoContent, response.Code, response.Body.String())
	assert.True(t, leaseDone(sessionLease))
	response = httptest.NewRecorder()
	boundary.ServeHTTP(response, legacyRequest(http.MethodPost, `{"jsonrpc":"2.0","id":5,"method":"tools/list","params":{}}`, "valid", sessionID))
	assert.Equal(t, http.StatusNotFound, response.Code)
	handler.Shutdown()
}

func TestLegacyDiscoveryUsesRealPagerAcrossPagesAndMapsErrors(t *testing.T) {
	projected := make([]*discovery.Tool, 101)
	for index := range projected {
		projected[index] = &discovery.Tool{Name: "legacy-tool", InputSchema: json.RawMessage(`{"type":"object"}`)}
	}
	projector := fixedProjector{projection: discovery.Projection{
		Tools: projected,
		Snapshot: discovery.Snapshot{
			Generation:  catalog.CurrentGeneration{ProcessGeneration: "01ARZ3NDEKTSV4RRFFQ69G5FAY", ActiveGeneration: 2},
			PrincipalID: "01ARZ3NDEKTSV4RRFFQ69G5FAW", PrincipalRevision: "1", Visibility: contract.VisibilityRequestable,
			CredentialID: "01ARZ3NDEKTSV4RRFFQ69G5FAX", CredentialRevision: "1", AuthorizationRevision: "0",
			EvaluatedAt: time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC),
		},
	}}
	codec, err := discovery.NewCursorCodec(bytes.NewReader(bytes.Repeat([]byte{8}, 32)))
	require.NoError(t, err)
	pager, err := discovery.NewPager(projector, codec)
	require.NoError(t, err)
	handler, boundary, _ := newLegacyDiscoveryHarness(t, pagerAdapter{pager: pager})
	sessionID := initializeLegacyDiscovery(t, boundary)

	response := legacyListResponse(boundary, sessionID, 1, "")
	var first struct {
		Result struct {
			Tools      []discovery.Tool `json:"tools"`
			NextCursor string           `json:"nextCursor"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &first))
	assert.Len(t, first.Result.Tools, 100)
	require.NotEmpty(t, first.Result.NextCursor)

	response = legacyListResponse(boundary, sessionID, 2, first.Result.NextCursor)
	var second struct {
		Result struct {
			Tools      []discovery.Tool `json:"tools"`
			NextCursor string           `json:"nextCursor"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &second))
	assert.Len(t, second.Result.Tools, 1)
	assert.Empty(t, second.Result.NextCursor)

	response = legacyListResponse(boundary, sessionID, 3, "not-a-discovery-cursor")
	assert.Equal(t, `{"jsonrpc":"2.0","id":3,"error":{"code":-32602,"message":"The tools/list parameters are invalid.","data":{"code":"invalid_params"}}}`, response.Body.String())

	response = httptest.NewRecorder()
	boundary.ServeHTTP(response, legacyRequest(http.MethodPost, `{"jsonrpc":"2.0","id":4,"method":"tools/list","params":{"unknown":true}}`, "valid", sessionID))
	assert.Equal(t, `{"jsonrpc":"2.0","id":4,"error":{"code":-32602,"message":"The tools/list parameters are invalid.","data":{"code":"invalid_params"}}}`, response.Body.String())
	handler.Shutdown()
}

func TestLegacyDiscoveryCredentialMutationsCloseSession(t *testing.T) {
	for _, mutation := range []string{"replacement", "revocation", "disable"} {
		t.Run(mutation, func(t *testing.T) {
			calls := 0
			list := listToolsFunc(func(ctx context.Context, _ *authorization.Lease, _ string, encode ToolsListEncoder) ([]byte, error) {
				calls++
				return encode(ctx, make([]*discovery.Tool, 0), "")
			})
			handler, boundary, authority := newLegacyDiscoveryHarness(t, list)
			sessionID := initializeLegacyDiscovery(t, boundary)
			legacyListResponse(boundary, sessionID, 1, "")
			require.Equal(t, 1, calls)
			handler.mu.Lock()
			done := handler.legacy[sessionID].done
			handler.mu.Unlock()
			principal, err := authority.repository.GetPrincipal(t.Context(), authority.principals["valid"])
			require.NoError(t, err)
			bearer := "valid"
			switch mutation {
			case "replacement":
				creation, issueErr := authority.repository.IssueCredential(t.Context(), principal.ID, principal.Revision)
				err = issueErr
				if issueErr == nil {
					bearer = "replacement"
					authority.mu.Lock()
					authority.bearers[contract.AgentBearerPrefix+bearer] = creation.Bearer
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
				t.Fatal("credential mutation did not close legacy discovery session")
			}
			response := httptest.NewRecorder()
			boundary.ServeHTTP(response, legacyRequest(http.MethodPost, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`, bearer, sessionID))
			if mutation == "replacement" {
				assert.Equal(t, http.StatusNotFound, response.Code)
			} else {
				assert.Equal(t, http.StatusUnauthorized, response.Code)
			}
			assert.Equal(t, 1, calls)
			handler.Shutdown()
		})
	}
}

func TestLegacyDiscoverySessionDoesNotSurviveRestart(t *testing.T) {
	list := listToolsFunc(func(ctx context.Context, _ *authorization.Lease, _ string, encode ToolsListEncoder) ([]byte, error) {
		return encode(ctx, make([]*discovery.Tool, 0), "")
	})
	handler, boundary, authority := newLegacyDiscoveryHarness(t, list)
	sessionID := initializeLegacyDiscovery(t, boundary)
	handler.Shutdown()
	restarted := New(Options{Authenticator: authority, ListTools: list})
	defer restarted.Shutdown()
	response := httptest.NewRecorder()
	newLegacyBoundary(t, restarted).ServeHTTP(response, legacyRequest(http.MethodPost, `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`, "valid", sessionID))
	assert.Equal(t, http.StatusNotFound, response.Code)
}

func legacyListResponse(boundary *httpboundary.Boundary, sessionID string, id int, cursor string) *httptest.ResponseRecorder {
	params := "{}"
	if cursor != "" {
		encoded, _ := json.Marshal(cursor)
		params = `{"cursor":` + string(encoded) + `}`
	}
	body := `{"jsonrpc":"2.0","id":` + jsonNumber(id) + `,"method":"tools/list","params":` + params + `}`
	response := httptest.NewRecorder()
	boundary.ServeHTTP(response, legacyRequest(http.MethodPost, body, "valid", sessionID))
	return response
}
