package mcpingress

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/discovery"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToolsListParamsAreClosedAndPreserveRequestID(t *testing.T) {
	t.Parallel()
	metadata := `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}`
	for _, test := range []struct {
		name, params, cursor string
		valid                bool
	}{
		{name: "absent", valid: true},
		{name: "empty", params: `{}`, valid: true},
		{name: "cursor", params: `{"cursor":"mgw_dc1_value"}`, cursor: "mgw_dc1_value", valid: true},
		{name: "modern metadata only", params: `{` + metadata + `}`, valid: true},
		{name: "modern metadata and cursor", params: `{"cursor":"mgw_dc1_value",` + metadata + `}`, cursor: "mgw_dc1_value", valid: true},
		{name: "empty cursor", params: `{"cursor":""}`},
		{name: "null cursor", params: `{"cursor":null}`},
		{name: "numeric cursor", params: `{"cursor":1}`},
		{name: "unknown member", params: `{"other":true}`},
		{name: "duplicate cursor", params: `{"cursor":"one","cursor":"two"}`},
		{name: "null params", params: `null`},
		{name: "array params", params: `[]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := `{"jsonrpc":"2.0","id":9007199254740993,"method":"tools/list"`
			if test.params != "" {
				body += `,"params":` + test.params
			}
			body += `}`
			var wire wireRequest
			require.NoError(t, json.Unmarshal([]byte(body), &wire))
			call, err := parseToolsList(wire)
			if !test.valid {
				assert.ErrorIs(t, err, errInvalidToolsListParams)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.cursor, call.Cursor)
			var encode discovery.ResultEncoder = func(ctx context.Context, result discovery.ListResult) ([]byte, error) {
				return call.EncodeResult(ctx, result.Tools, result.NextCursor)
			}
			encoded, err := encode(t.Context(), discovery.ListResult{Tools: make([]*discovery.Tool, 0)})
			require.NoError(t, err)
			assert.Equal(t, `{"jsonrpc":"2.0","id":9007199254740993,"result":{"tools":[]}}`, string(encoded))
		})
	}
}

func TestToolsListCodecUsesExactClosedEnvelopes(t *testing.T) {
	t.Parallel()
	id := json.RawMessage(`"request-\u0031"`)
	for _, test := range []struct {
		kind rpcErrorKind
		want string
	}{
		{rpcInvalidParams, `{"jsonrpc":"2.0","id":"request-\u0031","error":{"code":-32602,"message":"The tools/list parameters are invalid.","data":{"code":"invalid_params"}}}`},
		{rpcStaleCursor, `{"jsonrpc":"2.0","id":"request-\u0031","error":{"code":-32001,"message":"The tools/list cursor is stale.","data":{"code":"stale_cursor"}}}`},
		{rpcAuthorizationUnavailable, `{"jsonrpc":"2.0","id":"request-\u0031","error":{"code":-32002,"message":"Authorization is unavailable.","data":{"code":"authorization_unavailable"}}}`},
		{rpcResourceLimit, `{"jsonrpc":"2.0","id":"request-\u0031","error":{"code":-32003,"message":"The tool list exceeds a resource limit.","data":{"code":"resource_limit"}}}`},
		{rpcMethodNotFound, `{"jsonrpc":"2.0","id":"request-\u0031","error":{"code":-32601,"message":"Method not found."}}`},
	} {
		encoded, err := encodeRPCError(t.Context(), id, test.kind)
		require.NoError(t, err)
		assert.Equal(t, test.want, string(encoded))
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := encodeRPCError(canceled, id, rpcMethodNotFound)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestFeatureMethodsAreInterceptedBeforeModernSDK(t *testing.T) {
	t.Parallel()
	handler, boundary, _ := newModernBoundary(t)
	dispatched := 0
	handler.modern = http.HandlerFunc(func(http.ResponseWriter, *http.Request) { dispatched++ })

	for _, test := range []struct {
		name, body, want string
	}{
		{
			name: "valid list has no adapter yet",
			body: `{"jsonrpc":"2.0","id":7,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`,
			want: `{"jsonrpc":"2.0","id":7,"error":{"code":-32601,"message":"Method not found."}}`,
		},
		{
			name: "invalid list params",
			body: `{"jsonrpc":"2.0","id":"list","method":"tools/list","params":{"cursor":"","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`,
			want: `{"jsonrpc":"2.0","id":"list","error":{"code":-32602,"message":"The tools/list parameters are invalid.","data":{"code":"invalid_params"}}}`,
		},
		{
			name: "tools call",
			body: `{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"anything","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`,
			want: `{"jsonrpc":"2.0","id":8,"error":{"code":-32601,"message":"Method not found."}}`,
		},
		{
			name: "other feature",
			body: `{"jsonrpc":"2.0","id":9,"method":"resources/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`,
			want: `{"jsonrpc":"2.0","id":9,"error":{"code":-32601,"message":"Method not found."}}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := modernRequest(http.MethodPost, test.body)
			request.Header.Set("Mcp-Protocol-Version", contract.ModernProtocolVersion)
			response := httptest.NewRecorder()
			boundary.ServeHTTP(response, request)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			assert.Equal(t, contract.MediaTypeJSON, response.Header().Get("Content-Type"))
			assert.Equal(t, test.want, strings.TrimSpace(response.Body.String()))
		})
	}
	assert.Zero(t, dispatched)
}

func TestFeatureMethodsAreInterceptedInsideLegacySession(t *testing.T) {
	clock := testutil.NewFakeClock(time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC))
	handler, boundary, _ := newLegacyHarness(t, clock, testutil.NewFakeEntropy(makeDistinctEntropy(1)))
	sessionID := initializeLegacy(t, boundary)
	dispatched := 0
	handler.mu.Lock()
	handler.legacy[sessionID].handler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) { dispatched++ })
	handler.mu.Unlock()
	request := legacyRequest(http.MethodPost, `{"jsonrpc":"2.0","id":"legacy","method":"tools/list","params":{}}`, "valid", sessionID)
	response := httptest.NewRecorder()
	boundary.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, `{"jsonrpc":"2.0","id":"legacy","error":{"code":-32601,"message":"Method not found."}}`, response.Body.String())

	request = legacyRequest(http.MethodPost, `{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{}}`, "valid", sessionID)
	response = httptest.NewRecorder()
	boundary.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Equal(t, `{"jsonrpc":"2.0","id":11,"error":{"code":-32601,"message":"Method not found."}}`, response.Body.String())
	assert.Zero(t, dispatched)
	handler.Shutdown()
}

func TestLifecycleMethodsStillReachSDKDispatch(t *testing.T) {
	t.Parallel()
	handler, boundary, _ := newModernBoundary(t)
	dispatched := make(chan string, 2)
	handler.modern = http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		dispatched <- request.Header.Get("Mcp-Method")
		writer.WriteHeader(http.StatusNoContent)
	})
	for _, method := range []string{"server/discover", "ping"} {
		body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`
		request := modernRequest(http.MethodPost, body)
		request.Header.Set("Mcp-Protocol-Version", contract.ModernProtocolVersion)
		response := httptest.NewRecorder()
		boundary.ServeHTTP(response, request)
		require.Equal(t, http.StatusNoContent, response.Code, response.Body.String())
		assert.Equal(t, method, <-dispatched)
	}
}

func TestClassifyLegacyExistingAllowsAbsentParams(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	request.Header.Set("Mcp-Protocol-Version", contract.LegacyProtocolVersion)
	request.Header.Set("Mcp-Session-Id", "session")
	wire, era, code := classifyPOST(request, []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	assert.Empty(t, code)
	assert.Equal(t, eraLegacyExisting, era)
	assert.Equal(t, "tools/list", wire.Method)
}
