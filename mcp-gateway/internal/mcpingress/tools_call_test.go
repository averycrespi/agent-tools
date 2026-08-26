package mcpingress

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToolsCallClassificationKeepsS1AheadOfRecognition(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantEra  requestEra
		wantCode contract.ProblemCode
	}{
		{name: "valid request", body: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":7}`, wantEra: eraLegacyExisting},
		{name: "notification", body: `{"jsonrpc":"2.0","method":"tools/call","params":{}}`, wantEra: eraLegacyExisting},
		{name: "null id", body: `{"jsonrpc":"2.0","id":null,"method":"tools/call","params":{}}`, wantCode: contract.ProblemMalformedRequest},
		{name: "composite id", body: `{"jsonrpc":"2.0","id":{},"method":"tools/call","params":{}}`, wantCode: contract.ProblemMalformedRequest},
		{name: "batch", body: `[{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{}}]`, wantCode: contract.ProblemMalformedRequest},
		{name: "trailing", body: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{}} {}`, wantCode: contract.ProblemMalformedRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/mcp", nil)
			request.Header.Set("Mcp-Protocol-Version", contract.LegacyProtocolVersion)
			request.Header.Set("Mcp-Session-Id", "session")

			_, era, code := classifyPOST(request, []byte(test.body))

			assert.Equal(t, test.wantEra, era)
			assert.Equal(t, test.wantCode, code)
		})
	}
}

func TestToolsCallCodecPreservesIDAndStripsAllMetadata(t *testing.T) {
	wire := decodeCallWire(t, `{"jsonrpc":"2.0","id":"request-\u0031","method":"tools/call","params":{"name":"sample.echo","arguments":{"value":1e0},"_meta":{"progressToken":"private","task":{"id":"private"},"arbitrary":"private"}}}`)

	call, err := parseToolsCall(wire)

	require.NoError(t, err)
	assert.Equal(t, json.RawMessage(`"request-\u0031"`), call.ID)
	assert.False(t, call.Notification)
	assert.True(t, call.Request.WireValid)
	assert.Equal(t, strictjson.ValueObject, call.Request.Params.Type)
	require.Len(t, call.Request.Params.Object, 2)
	assert.Equal(t, "name", call.Request.Params.Object[0].Name)
	assert.Equal(t, "arguments", call.Request.Params.Object[1].Name)
	assert.Equal(t, "1e0", call.Request.Params.Object[1].Value.Object[0].Value.Number)
}

func TestToolsCallCodecMakesDisallowedOrMalformedMetadataAuditableInvalidParams(t *testing.T) {
	tests := []struct {
		name       string
		params     string
		wantFields int
	}{
		{name: "unknown member", params: `{"name":"sample.echo","arguments":{},"extra":"private"}`, wantFields: 2},
		{name: "null metadata", params: `{"name":"sample.echo","arguments":{},"_meta":null}`, wantFields: 2},
		{name: "scalar metadata", params: `{"name":"sample.echo","arguments":{},"_meta":"private"}`, wantFields: 2},
		{name: "duplicate member", params: `{"name":"sample.echo","name":"other","arguments":{}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wire := wireRequest{ID: json.RawMessage(`1`), Method: "tools/call", Params: json.RawMessage(test.params)}

			call, err := parseToolsCall(wire)

			require.NoError(t, err)
			assert.False(t, call.Request.WireValid)
			assert.Len(t, call.Request.Params.Object, test.wantFields)
			for _, member := range call.Request.Params.Object {
				assert.NotEqual(t, "_meta", member.Name)
				assert.NotEqual(t, "extra", member.Name)
			}
		})
	}
}

func TestToolsCallCodecTreatsAbsentIDAsNotificationWithoutInvokingService(t *testing.T) {
	called := 0
	service := toolsCallServiceFunc(func(context.Context, *authorization.Lease, ToolsCallRequest) ToolsCallResponse {
		called++
		return ToolsCallResponse{ErrorCode: contract.CallRejected}
	})
	wire := decodeCallWire(t, `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"sample.echo","arguments":{}}}`)
	response := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/mcp", nil)

	assert.True(t, interceptFeatureWithCall(response, request, wire, eraModern, nil, service, nil))

	assert.Zero(t, called)
	assert.Equal(t, 202, response.Code)
	assert.Empty(t, response.Body.String())
}

func TestToolsCallCodecRejectsNullAndCompositeIDsBeforeRecognition(t *testing.T) {
	for _, id := range []string{`null`, `true`, `false`, `{}`, `[]`} {
		t.Run(id, func(t *testing.T) {
			assert.False(t, validToolsCallID(json.RawMessage(id)))
		})
	}
	for _, id := range []string{`0`, `-1`, `1e0`, `""`, `"request"`} {
		t.Run(id, func(t *testing.T) {
			assert.True(t, validToolsCallID(json.RawMessage(id)))
		})
	}
}

func TestToolsCallCodecEmitsExactSharedSuccessForBothEras(t *testing.T) {
	service := toolsCallServiceFunc(func(_ context.Context, _ *authorization.Lease, request ToolsCallRequest) ToolsCallResponse {
		assert.True(t, request.WireValid)
		return ToolsCallResponse{Result: &ToolsCallResult{
			Content:           []json.RawMessage{json.RawMessage(`{"type":"text","text":"ok"}`)},
			StructuredContent: json.RawMessage(`{"value":1e0}`),
		}}
	})
	wire := decodeCallWire(t, `{"jsonrpc":"2.0","id":9007199254740993,"method":"tools/call","params":{"name":"sample.echo","arguments":{}}}`)
	for _, era := range []requestEra{eraModern, eraLegacyExisting} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest("POST", "/mcp", nil)

		assert.True(t, interceptFeatureWithCall(response, request, wire, era, nil, service, nil))

		assert.Equal(t, 200, response.Code)
		assert.Equal(t, contract.MediaTypeJSON, response.Header().Get("Content-Type"))
		assert.Equal(t, `{"jsonrpc":"2.0","id":9007199254740993,"result":{"content":[{"type":"text","text":"ok"}],"structuredContent":{"value":1e0}}}`, response.Body.String())
	}
}

func TestToolsCallCodecEmitsOnlyClosedSafeErrors(t *testing.T) {
	tests := []struct {
		code       contract.AgentCallErrorCode
		invocation string
		want       string
	}{
		{code: contract.CallRejected, invocation: "01J60000000000000000000001", want: `{"jsonrpc":"2.0","id":"request","error":{"code":-32000,"message":"Call rejected","data":{"code":"call_rejected","invocationId":"01J60000000000000000000001"}}}`},
		{code: contract.AuditUnavailable, want: `{"jsonrpc":"2.0","id":"request","error":{"code":-32000,"message":"Call unavailable","data":{"code":"audit_unavailable"}}}`},
		{code: contract.ToolUnavailable, invocation: "01J60000000000000000000002", want: `{"jsonrpc":"2.0","id":"request","error":{"code":-32000,"message":"Tool unavailable","data":{"code":"tool_unavailable","invocationId":"01J60000000000000000000002"}}}`},
		{code: contract.DownstreamFailure, invocation: "01J60000000000000000000003", want: `{"jsonrpc":"2.0","id":"request","error":{"code":-32000,"message":"Tool failed","data":{"code":"downstream_failure","invocationId":"01J60000000000000000000003"}}}`},
		{code: contract.OutcomeUnknown, invocation: "01J60000000000000000000004", want: `{"jsonrpc":"2.0","id":"request","error":{"code":-32000,"message":"Tool outcome unknown","data":{"code":"outcome_unknown","invocationId":"01J60000000000000000000004","outcomeUnknown":true}}}`},
	}
	wire := decodeCallWire(t, `{"jsonrpc":"2.0","id":"request","method":"tools/call","params":{"name":"sample.echo","arguments":{}}}`)
	for _, test := range tests {
		t.Run(string(test.code), func(t *testing.T) {
			service := toolsCallServiceFunc(func(context.Context, *authorization.Lease, ToolsCallRequest) ToolsCallResponse {
				return ToolsCallResponse{ErrorCode: test.code, InvocationID: test.invocation}
			})
			response := httptest.NewRecorder()
			request := httptest.NewRequest("POST", "/mcp", nil)

			assert.True(t, interceptFeatureWithCall(response, request, wire, eraModern, nil, service, nil))

			assert.Equal(t, test.want, response.Body.String())
		})
	}
}

func TestToolsCallCodecFailsClosedOnInvalidServiceResult(t *testing.T) {
	service := toolsCallServiceFunc(func(context.Context, *authorization.Lease, ToolsCallRequest) ToolsCallResponse {
		return ToolsCallResponse{ErrorCode: contract.AgentCallErrorCode("private_failure"), InvocationID: "private"}
	})
	wire := decodeCallWire(t, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"sample.echo","arguments":{}}}`)
	response := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/mcp", nil)

	assert.True(t, interceptFeatureWithCall(response, request, wire, eraModern, nil, service, nil))

	assert.Equal(t, `{"jsonrpc":"2.0","id":7,"error":{"code":-32000,"message":"Call unavailable","data":{"code":"audit_unavailable"}}}`, response.Body.String())
	assert.NotContains(t, response.Body.String(), "private")
}

type toolsCallServiceFunc func(context.Context, *authorization.Lease, ToolsCallRequest) ToolsCallResponse

func (function toolsCallServiceFunc) Call(ctx context.Context, lease *authorization.Lease, request ToolsCallRequest) ToolsCallResponse {
	return function(ctx, lease, request)
}

func decodeCallWire(t *testing.T, raw string) wireRequest {
	t.Helper()
	var wire wireRequest
	require.NoError(t, json.Unmarshal([]byte(raw), &wire))
	return wire
}
