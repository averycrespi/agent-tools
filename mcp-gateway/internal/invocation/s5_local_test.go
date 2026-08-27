package invocation

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/catalog"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS5LocalTargetPinsSyntheticCatalogEvidenceAndValidation(t *testing.T) {
	synthetic, found := catalog.ResolveSyntheticCall("mcp_gateway.get_identity")
	require.True(t, found)
	target, err := NewLocalTarget(synthetic, func(context.Context, authorization.AdmittedSubject, strictjson.Value) LocalCallResult {
		return LocalSuccess(json.RawMessage(`{"content":[]}`))
	})
	require.NoError(t, err)
	assert.Equal(t, synthetic.Descriptor.ID, target.evidence.ToolID)
	assert.Equal(t, synthetic.Descriptor.Fingerprint, target.evidence.DescriptorFingerprint)
	assert.NoError(t, target.validate(callParams(`{}`)))
	assert.Error(t, target.validate(callParams(`{"unexpected":true}`)))
}

func TestS5LocalInvocationRunsOnceAfterAuditWithMinimalAdmittedSubject(t *testing.T) {
	_, audits, authority, principal, credential := newAdmissionCoordinator(t, nil)
	lease, err := authority.Authenticate(context.Background(), credential.Bearer)
	require.NoError(t, err)
	defer lease.Release()
	resolutions, validations, executions := 0, 0, 0
	service, err := newService(audits, authority, func(name string) (callTarget, bool) {
		resolutions++
		assert.Equal(t, "mcp_gateway.get_identity", name)
		target := localServiceCallTarget(func(ctx context.Context, subject authorization.AdmittedSubject, arguments strictjson.Value) LocalCallResult {
			executions++
			record := onlyInvocationRecord(t, audits)
			assert.Equal(t, contract.AdmissionEvaluated, record.AdmissionClass)
			assert.Equal(t, contract.DecisionAllow, *record.AuthorizationDecision)
			assert.Nil(t, record.TerminalClass, "local execution must follow durable admission")
			assert.True(t, authority.OwnsAdmittedSubject(subject))
			assert.Equal(t, principal.ID, subject.PrincipalID())
			assert.Equal(t, credential.Principal.Revision, subject.PrincipalRevision())
			assert.Equal(t, credential.Principal.Credential.ID, subject.CredentialID())
			assert.Equal(t, credential.Principal.Credential.Revision, subject.CredentialRevision())
			assert.NotEmpty(t, subject.AuthorizationRevision())
			assert.Equal(t, []string{}, exportedSubjectFields(reflect.TypeOf(subject)))
			require.Len(t, arguments.Object, 1)
			assert.Equal(t, "1e0", arguments.Object[0].Value.Number, "the unchanged token tree reaches the local handler")
			upstream := "get_identity"
			_, grantErr := authority.CreateGrant(ctx, authorization.CreateGrantRequest{
				PrincipalID: principal.ID, Effect: contract.GrantDeny, ServerID: contract.SyntheticServerID, UpstreamName: &upstream,
			}, func(context.Context, *sql.Tx, string) (bool, error) { return true, nil })
			require.NoError(t, grantErr, "the authorization gate must be released before local execution")
			return LocalSuccess(json.RawMessage(`{"content":[{"type":"text","text":"Identity returned."}],"structuredContent":{"ok":true}}`))
		})
		target.validate = func(arguments strictjson.Value) error {
			validations++
			assert.Equal(t, "1e0", arguments.Object[0].Value.Number)
			return nil
		}
		return target, true
	})
	require.NoError(t, err)

	response := service.Call(context.Background(), lease, CallRequest{Params: callParams(`{"name":"mcp_gateway.get_identity","arguments":{"value":1e0}}`), WireValid: true})
	require.NotNil(t, response.Result)
	assert.Empty(t, response.ErrorCode)
	assert.Empty(t, response.InvocationID)
	assert.Equal(t, 1, resolutions)
	assert.Equal(t, 1, validations)
	assert.Equal(t, 1, executions)
	record := onlyInvocationRecord(t, audits)
	assert.Equal(t, contract.TerminalSucceeded, *record.TerminalClass)
}

func TestS5LocalInvocationNeverExecutesWithoutAllow(t *testing.T) {
	_, audits, authority, principal, credential := newAdmissionCoordinator(t, nil)
	upstream := "get_identity"
	_, err := authority.CreateGrant(context.Background(), authorization.CreateGrantRequest{
		PrincipalID: principal.ID, Effect: contract.GrantDeny, ServerID: contract.SyntheticServerID, UpstreamName: &upstream,
	}, func(context.Context, *sql.Tx, string) (bool, error) { return true, nil })
	require.NoError(t, err)
	lease, err := authority.Authenticate(context.Background(), credential.Bearer)
	require.NoError(t, err)
	defer lease.Release()
	executions := 0
	service, err := newService(audits, authority, func(string) (callTarget, bool) {
		return localServiceCallTarget(func(context.Context, authorization.AdmittedSubject, strictjson.Value) LocalCallResult {
			executions++
			return LocalSuccess(json.RawMessage(`{"content":[]}`))
		}), true
	})
	require.NoError(t, err)
	response := service.Call(context.Background(), lease, CallRequest{Params: callParams(`{"name":"mcp_gateway.get_identity","arguments":{}}`), WireValid: true})
	assert.Equal(t, contract.CallRejected, response.ErrorCode)
	assert.Zero(t, executions)
	record := onlyInvocationRecord(t, audits)
	assert.Equal(t, contract.DecisionDeny, *record.AuthorizationDecision)
	assert.Nil(t, record.TerminalClass)
}

func TestS5LocalStorageFailuresUseToolUnavailableWithoutOutcomeUnknown(t *testing.T) {
	tests := []struct {
		name       string
		result     LocalCallResult
		wantRecord *contract.InvocationTerminalClass
	}{
		{name: "known precommit", result: LocalStorageFailure(false), wantRecord: terminalPointer(contract.TerminalPrestartFailure)},
		{name: "postcommit uncertainty", result: LocalStorageFailure(true)},
		{name: "invalid handler result", result: LocalSuccess(json.RawMessage(`{"unexpected":true}`)), wantRecord: terminalPointer(contract.TerminalPrestartFailure)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, audits, authority, _, credential := newAdmissionCoordinator(t, nil)
			lease, err := authority.Authenticate(context.Background(), credential.Bearer)
			require.NoError(t, err)
			defer lease.Release()
			executions := 0
			service, err := newService(audits, authority, func(string) (callTarget, bool) {
				return localServiceCallTarget(func(context.Context, authorization.AdmittedSubject, strictjson.Value) LocalCallResult {
					executions++
					return test.result
				}), true
			})
			require.NoError(t, err)
			response := service.Call(context.Background(), lease, CallRequest{Params: callParams(`{"name":"mcp_gateway.get_identity","arguments":{}}`), WireValid: true})
			assert.Equal(t, contract.ToolUnavailable, response.ErrorCode)
			assert.NotEqual(t, contract.OutcomeUnknown, response.ErrorCode)
			assert.NotEmpty(t, response.InvocationID)
			assert.Equal(t, 1, executions)
			record := onlyInvocationRecord(t, audits)
			assert.Equal(t, test.wantRecord, record.TerminalClass)
		})
	}
}

func TestS5LocalExtensionPreservesOneDownstreamAcquireAndExecute(t *testing.T) {
	_, audits, authority, _, credential := newAdmissionCoordinator(t, nil)
	lease, err := authority.Authenticate(context.Background(), credential.Bearer)
	require.NoError(t, err)
	defer lease.Release()
	acquisitions, executions := 0, 0
	service, err := newService(audits, authority, func(string) (callTarget, bool) {
		return serviceCallTarget(nil, func(context.Context) (executionLease, error) {
			acquisitions++
			return &serviceExecutionLease{execute: func(json.RawMessage) downstream.CallResult {
				executions++
				return downstream.CallResult{Response: downstream.Response{Result: json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`)}}
			}}, nil
		}), true
	})
	require.NoError(t, err)
	response := service.Call(context.Background(), lease, validCallParams())
	require.NotNil(t, response.Result)
	assert.Equal(t, 1, acquisitions)
	assert.Equal(t, 1, executions)
}

func localServiceCallTarget(handler LocalHandler) callTarget {
	return callTarget{
		evidence: RouteEvidence{
			ServerID: contract.SyntheticServerID, ToolID: "00000000000000000000000001", UpstreamName: "get_identity",
			DescriptorRevision: contract.SyntheticCatalogRevision, DescriptorFingerprint: "cc982af50fbc4873c57e89b5052a3c725f5e3898b2142dab096b99b0a4e656b9",
		},
		validate: func(strictjson.Value) error { return nil },
		local:    handler,
	}
}

func terminalPointer(value contract.InvocationTerminalClass) *contract.InvocationTerminalClass {
	return &value
}

func exportedSubjectFields(value reflect.Type) []string {
	result := make([]string, 0, value.NumField())
	for index := range value.NumField() {
		if value.Field(index).IsExported() {
			result = append(result, value.Field(index).Name)
		}
	}
	return result
}
