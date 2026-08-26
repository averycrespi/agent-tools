package mcpingress

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const modernCallMeta = `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"fixture","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}`

func newModernCallBoundary(t *testing.T, call ToolsCallService) (*Handler, *testAuthority, http.Handler) {
	t.Helper()
	authority := newTestAuthority(t)
	authority.add(t, "valid", contract.VisibilityRequestable)
	handler := New(Options{Authenticator: authority, CallTools: call})
	return handler, authority, newLegacyBoundary(t, handler)
}

func modernCallRequest(id string) *http.Request {
	body := `{"jsonrpc":"2.0","id":` + id + `,"method":"tools/call","params":{"name":"sample.echo","arguments":{"value":1e0},` + modernCallMeta + `}}`
	request := modernRequest(http.MethodPost, body)
	request.Header.Set("Mcp-Protocol-Version", contract.ModernProtocolVersion)
	return request
}

func TestModernCallUsesRegisteredRequestLeaseAndExactEnvelope(t *testing.T) {
	var gotLease *authorization.Lease
	var gotRequest ToolsCallRequest
	service := toolsCallServiceFunc(func(_ context.Context, lease *authorization.Lease, request ToolsCallRequest) ToolsCallResponse {
		gotLease = lease
		gotRequest = request
		return ToolsCallResponse{Result: &ToolsCallResult{Content: []json.RawMessage{json.RawMessage(`{"type":"text","text":"ok"}`)}}}
	})
	handler, authority, boundary := newModernCallBoundary(t, service)

	bootstrap := modernRequest(http.MethodPost, `{"jsonrpc":"2.0","id":"bootstrap","method":"server/discover","params":{`+modernCallMeta+`}}`)
	bootstrap.Header.Set("Mcp-Protocol-Version", contract.ModernProtocolVersion)
	bootstrapResponse := httptest.NewRecorder()
	boundary.ServeHTTP(bootstrapResponse, bootstrap)
	require.Equal(t, http.StatusOK, bootstrapResponse.Code, bootstrapResponse.Body.String())
	assert.Contains(t, bootstrapResponse.Body.String(), `"capabilities":{"tools":{}}`)
	assert.NotContains(t, bootstrapResponse.Body.String(), "listChanged")

	response := httptest.NewRecorder()
	boundary.ServeHTTP(response, modernCallRequest(`9007199254740993`))

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, contract.MediaTypeJSON, response.Header().Get("Content-Type"))
	assert.Empty(t, response.Header().Get("Mcp-Session-Id"))
	assert.Equal(t, `{"jsonrpc":"2.0","id":9007199254740993,"result":{"content":[{"type":"text","text":"ok"}]}}`, response.Body.String())
	require.NotNil(t, gotLease)
	assert.Equal(t, authority.principals["valid"], gotLease.Binding().PrincipalID)
	assert.True(t, gotRequest.WireValid)
	assert.Equal(t, "1e0", gotRequest.Params.Object[1].Value.Object[0].Value.Number)
	assert.True(t, leaseDone(gotLease))
	work, streams, legacy := handler.Status()
	assert.Zero(t, work.InUse)
	assert.Zero(t, streams.InUse)
	assert.Zero(t, legacy.InUse)
}

func TestModernCallMapsEverySafeServiceResponse(t *testing.T) {
	tests := []struct {
		name     string
		response ToolsCallResponse
		want     string
	}{
		{name: "success", response: ToolsCallResponse{Result: &ToolsCallResult{Content: []json.RawMessage{json.RawMessage(`{"type":"text","text":"ok"}`)}}}, want: `{"jsonrpc":"2.0","id":"modern","result":{"content":[{"type":"text","text":"ok"}]}}`},
		{name: "rejected", response: ToolsCallResponse{ErrorCode: contract.CallRejected, InvocationID: "01J60000000000000000000001"}, want: `{"jsonrpc":"2.0","id":"modern","error":{"code":-32000,"message":"Call rejected","data":{"code":"call_rejected","invocationId":"01J60000000000000000000001"}}}`},
		{name: "audit unavailable", response: ToolsCallResponse{ErrorCode: contract.AuditUnavailable}, want: `{"jsonrpc":"2.0","id":"modern","error":{"code":-32000,"message":"Call unavailable","data":{"code":"audit_unavailable"}}}`},
		{name: "tool unavailable", response: ToolsCallResponse{ErrorCode: contract.ToolUnavailable, InvocationID: "01J60000000000000000000002"}, want: `{"jsonrpc":"2.0","id":"modern","error":{"code":-32000,"message":"Tool unavailable","data":{"code":"tool_unavailable","invocationId":"01J60000000000000000000002"}}}`},
		{name: "downstream failure", response: ToolsCallResponse{ErrorCode: contract.DownstreamFailure, InvocationID: "01J60000000000000000000003"}, want: `{"jsonrpc":"2.0","id":"modern","error":{"code":-32000,"message":"Tool failed","data":{"code":"downstream_failure","invocationId":"01J60000000000000000000003"}}}`},
		{name: "outcome unknown", response: ToolsCallResponse{ErrorCode: contract.OutcomeUnknown, InvocationID: "01J60000000000000000000004"}, want: `{"jsonrpc":"2.0","id":"modern","error":{"code":-32000,"message":"Tool outcome unknown","data":{"code":"outcome_unknown","invocationId":"01J60000000000000000000004","outcomeUnknown":true}}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			service := toolsCallServiceFunc(func(context.Context, *authorization.Lease, ToolsCallRequest) ToolsCallResponse {
				calls.Add(1)
				return test.response
			})
			_, _, boundary := newModernCallBoundary(t, service)
			response := httptest.NewRecorder()

			boundary.ServeHTTP(response, modernCallRequest(`"modern"`))

			assert.Equal(t, test.want, response.Body.String())
			assert.Equal(t, int32(1), calls.Load())
		})
	}
}

func TestModernCallLeaseInvalidationCancelsBeforeAdmission(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	service := toolsCallServiceFunc(func(ctx context.Context, _ *authorization.Lease, _ ToolsCallRequest) ToolsCallResponse {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return ToolsCallResponse{ErrorCode: contract.AuditUnavailable}
	})
	_, authority, boundary := newModernCallBoundary(t, service)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		boundary.ServeHTTP(response, modernCallRequest(`1`))
		close(done)
	}()
	<-started
	principal, err := authority.repository.GetPrincipal(t.Context(), authority.principals["valid"])
	require.NoError(t, err)
	_, err = authority.repository.IssueCredential(t.Context(), principal.ID, principal.Revision)
	require.NoError(t, err)
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("credential invalidation did not cancel modern call")
	}
	<-done
	assert.Empty(t, response.Body.String())
}

func TestModernCallDisconnectCancelsOnceAndReconnectDoesNotReplay(t *testing.T) {
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
	_, _, boundary := newModernCallBoundary(t, service)
	ctx, cancel := context.WithCancel(context.Background())
	first := modernCallRequest(`1`).WithContext(ctx)
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

	secondResponse := httptest.NewRecorder()
	boundary.ServeHTTP(secondResponse, modernCallRequest(`2`))
	assert.Equal(t, int32(2), calls.Load())
	assert.Equal(t, `{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"Call rejected","data":{"code":"call_rejected","invocationId":"01J60000000000000000000005"}}}`, strings.TrimSpace(secondResponse.Body.String()))
}
