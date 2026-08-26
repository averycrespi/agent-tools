package invocation

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizeCallResultProjectsFiveClosedContentTypes(t *testing.T) {
	raw := `{
		"resultType":"complete",
		"_meta":{"private":"result-canary"},
		"content":[
			{"type":"text","text":"hello","annotations":{"audience":["user","assistant"],"priority":0.5,"lastModified":"2026-08-26T00:00:00Z"},"_meta":{"private":"text-canary"}},
			{"type":"image","data":"aW1hZ2U=","mimeType":"image/png","_meta":{"private":"image-canary"}},
			{"type":"audio","data":"YXVkaW8=","mimeType":"audio/wav"},
			{"type":"resource","resource":{"uri":"file:///safe.txt","mimeType":"text/plain","text":"safe","_meta":{"private":"resource-canary"}},"_meta":{"private":"wrapper-canary"}},
			{"type":"resource_link","uri":"https://example.test/item","name":"item","title":"Item","description":"safe","mimeType":"text/plain","size":4e0,"annotations":{"priority":1},"icons":[{"src":"data:image/png;base64,aQ==","mimeType":"image/png","sizes":["16x16","any"],"theme":"dark"}],"_meta":{"private":"link-canary"}}
		],
		"structuredContent":{"_meta":"tool-data","value":1e0},
		"isError":false
	}`

	outcome := SanitizeCallResult(downstream.CallResult{Response: downstream.Response{Result: json.RawMessage(raw)}})

	require.NotNil(t, outcome.Result)
	assert.Empty(t, outcome.ErrorCode)
	assert.Equal(t, contract.TerminalSucceeded, outcome.TerminalClass)
	projected := marshalProjectedResult(t, outcome.Result)
	assert.NotContains(t, projected, "result-canary")
	assert.NotContains(t, projected, "text-canary")
	assert.NotContains(t, projected, "image-canary")
	assert.NotContains(t, projected, "resource-canary")
	assert.NotContains(t, projected, "wrapper-canary")
	assert.NotContains(t, projected, "link-canary")
	assert.Contains(t, projected, `"structuredContent":{"_meta":"tool-data","value":1e0}`)
	assert.NotContains(t, projected, "resultType")
	assert.JSONEq(t, `{"content":[{"type":"text","text":"hello","annotations":{"audience":["user","assistant"],"priority":0.5,"lastModified":"2026-08-26T00:00:00Z"}},{"type":"image","data":"aW1hZ2U=","mimeType":"image/png"},{"type":"audio","data":"YXVkaW8=","mimeType":"audio/wav"},{"type":"resource","resource":{"uri":"file:///safe.txt","mimeType":"text/plain","text":"safe"}},{"type":"resource_link","uri":"https://example.test/item","name":"item","title":"Item","description":"safe","mimeType":"text/plain","size":4e0,"annotations":{"priority":1},"icons":[{"src":"data:image/png;base64,aQ==","mimeType":"image/png","sizes":["16x16","any"],"theme":"dark"}]}],"structuredContent":{"_meta":"tool-data","value":1e0},"isError":false}`, projected)
}

func TestSanitizeCallResultAcceptsLegacyShapeAndBinaryResource(t *testing.T) {
	raw := `{"content":[{"type":"resource","resource":{"uri":"urn:example:item","blob":"YmluYXJ5"}}]}`
	outcome := SanitizeCallResult(downstream.CallResult{Response: downstream.Response{Result: json.RawMessage(raw)}})
	require.NotNil(t, outcome.Result)
	assert.Equal(t, contract.TerminalSucceeded, outcome.TerminalClass)
	assert.JSONEq(t, raw, marshalProjectedResult(t, outcome.Result))
}

func TestSanitizeCallResultRejectsMalformedOrUnsupportedResults(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "not object", raw: `[]`},
		{name: "missing content", raw: `{}`},
		{name: "null content", raw: `{"content":null}`},
		{name: "unknown result member", raw: `{"content":[],"secret":"canary"}`},
		{name: "input required", raw: `{"resultType":"input_required","content":[],"inputRequests":{}}`},
		{name: "null block", raw: `{"content":[null]}`},
		{name: "unknown content type", raw: `{"content":[{"type":"tool_use","id":"x"}]}`},
		{name: "missing text", raw: `{"content":[{"type":"text"}]}`},
		{name: "cross union member", raw: `{"content":[{"type":"text","text":"x","data":"eA=="}]}`},
		{name: "bad base64", raw: `{"content":[{"type":"image","data":"%%%","mimeType":"image/png"}]}`},
		{name: "resource missing payload", raw: `{"content":[{"type":"resource","resource":{"uri":"urn:x"}}]}`},
		{name: "resource both payloads", raw: `{"content":[{"type":"resource","resource":{"uri":"urn:x","text":"x","blob":"eA=="}}]}`},
		{name: "bad annotation", raw: `{"content":[{"type":"text","text":"x","annotations":{"audience":["system"]}}]}`},
		{name: "bad annotation timestamp", raw: `{"content":[{"type":"text","text":"x","annotations":{"lastModified":"not-a-time"}}]}`},
		{name: "bad link size", raw: `{"content":[{"type":"resource_link","uri":"urn:x","name":"x","size":-1}]}`},
		{name: "oversized URI", raw: `{"content":[{"type":"resource_link","uri":"urn:` + strings.Repeat("x", 8190) + `","name":"x"}]}`},
		{name: "bad icon", raw: `{"content":[{"type":"resource_link","uri":"urn:x","name":"x","icons":[{"src":"urn:icon","theme":"system"}]}]}`},
		{name: "oversized", raw: `{"content":[{"type":"text","text":"` + strings.Repeat("x", 4*1024*1024) + `"}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outcome := SanitizeCallResult(downstream.CallResult{Response: downstream.Response{Result: json.RawMessage(test.raw)}})
			assert.Nil(t, outcome.Result)
			assert.Equal(t, contract.DownstreamFailure, outcome.ErrorCode)
			assert.Equal(t, contract.TerminalDownstreamFailure, outcome.TerminalClass)
		})
	}
}

func TestSanitizeCallResultHidesToolAndJSONRPCErrors(t *testing.T) {
	for _, test := range []downstream.CallResult{
		{Response: downstream.Response{Result: json.RawMessage(`{"content":[{"type":"text","text":"raw tool error canary"}],"isError":true}`)}},
		{Response: downstream.Response{Error: &downstream.RPCError{Code: -32001, Message: "raw RPC error canary", Data: json.RawMessage(`{"secret":"canary"}`)}}},
	} {
		outcome := SanitizeCallResult(test)
		assert.Nil(t, outcome.Result)
		assert.Equal(t, contract.DownstreamFailure, outcome.ErrorCode)
		assert.Equal(t, contract.TerminalDownstreamFailure, outcome.TerminalClass)
		assert.NotContains(t, outcome.SafeString(), "canary")
	}
}

func TestSanitizeCallResultUsesOnlyTypedCompletionEvidence(t *testing.T) {
	privateErr := errors.New("private transport canary")
	tests := []struct {
		failure  downstream.FailureClass
		wantCode contract.AgentCallErrorCode
		terminal contract.InvocationTerminalClass
	}{
		{failure: downstream.FailurePreStart, wantCode: contract.ToolUnavailable, terminal: contract.TerminalPrestartFailure},
		{failure: downstream.FailureResponseInvalid, wantCode: contract.DownstreamFailure, terminal: contract.TerminalDownstreamFailure},
		{failure: downstream.FailureStartUncertain, wantCode: contract.OutcomeUnknown, terminal: contract.TerminalOutcomeUnknown},
		{failure: "unexpected", wantCode: contract.OutcomeUnknown, terminal: contract.TerminalOutcomeUnknown},
	}
	for _, test := range tests {
		outcome := SanitizeCallResult(downstream.CallResult{Failure: test.failure, Err: privateErr})
		assert.Nil(t, outcome.Result)
		assert.Equal(t, test.wantCode, outcome.ErrorCode)
		assert.Equal(t, test.terminal, outcome.TerminalClass)
		assert.NotContains(t, outcome.SafeString(), "canary")
	}
}

func TestClassifyAdmissionIsLeastDisclosing(t *testing.T) {
	allow, deny, block := contract.DecisionAllow, contract.DecisionDeny, contract.DecisionBlock
	for _, test := range []struct {
		name      string
		committed bool
		class     contract.InvocationAdmissionClass
		decision  *contract.AuthorizationDecision
		wantCode  contract.AgentCallErrorCode
		mayRun    bool
	}{
		{name: "uncommitted", class: contract.AdmissionEvaluated, decision: &allow, wantCode: contract.AuditUnavailable},
		{name: "invalid params", committed: true, class: contract.AdmissionInvalidParams, wantCode: contract.CallRejected},
		{name: "unknown tool", committed: true, class: contract.AdmissionUnknownTool, wantCode: contract.CallRejected},
		{name: "invalid arguments", committed: true, class: contract.AdmissionInvalidArguments, wantCode: contract.CallRejected},
		{name: "authorization unavailable", committed: true, class: contract.AdmissionAuthorizationUnavailable, wantCode: contract.CallRejected},
		{name: "deny", committed: true, class: contract.AdmissionEvaluated, decision: &deny, wantCode: contract.CallRejected},
		{name: "block", committed: true, class: contract.AdmissionEvaluated, decision: &block, wantCode: contract.CallRejected},
		{name: "allow", committed: true, class: contract.AdmissionEvaluated, decision: &allow, mayRun: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			code, mayRun := ClassifyAdmission(test.committed, test.class, test.decision)
			assert.Equal(t, test.wantCode, code)
			assert.Equal(t, test.mayRun, mayRun)
		})
	}
}

func FuzzSanitizeCallResultNeverProjectsMetadataOrUnknownMembers(f *testing.F) {
	for _, seed := range []string{
		`{"content":[]}`,
		`{"content":[{"type":"text","text":"x","_meta":{"secret":"x"}}]}`,
		`{"content":[{"type":"image","data":"eA==","mimeType":"image/png"}]}`,
		`{"content":[{"type":"unknown"}]}`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 64*1024 {
			return
		}
		outcome := SanitizeCallResult(downstream.CallResult{Response: downstream.Response{Result: json.RawMessage(raw)}})
		if outcome.Result == nil {
			if outcome.ErrorCode != contract.DownstreamFailure {
				t.Fatal("invalid result did not fail closed")
			}
			return
		}
		encoded := marshalProjectedResult(t, outcome.Result)
		var projected map[string]any
		if err := json.Unmarshal([]byte(encoded), &projected); err != nil {
			t.Fatal("projected result is invalid JSON")
		}
		if _, present := projected["_meta"]; present {
			t.Fatal("projected result retained metadata")
		}
	})
}

func marshalProjectedResult(t *testing.T, result *ProjectedCallResult) string {
	t.Helper()
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	return string(encoded)
}
