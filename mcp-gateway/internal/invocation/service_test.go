package invocation

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceClassifiesAndAuditsEveryRecognizableBranch(t *testing.T) {
	tests := []struct {
		name            string
		params          strictjson.Value
		found           bool
		validationErr   error
		wantClass       contract.InvocationAdmissionClass
		wantName        *string
		wantArguments   *string
		wantResolutions int
	}{
		{name: "absent params", wantClass: contract.AdmissionInvalidParams},
		{name: "missing name retains object capture", params: callParams(`{"arguments":{"secret":"value"}}`), wantClass: contract.AdmissionInvalidParams, wantArguments: stringPointer(`{"secret":"[REDACTED]"}`)},
		{name: "malformed name", params: callParams(`{"name":7,"arguments":{}}`), wantClass: contract.AdmissionInvalidParams, wantArguments: stringPointer(`{}`)},
		{name: "malformed arguments", params: callParams(`{"name":"namespace.tool","arguments":[]}`), wantClass: contract.AdmissionInvalidParams, wantName: stringPointer("namespace.tool")},
		{name: "unknown member", params: callParams(`{"name":"namespace.tool","arguments":{},"extra":true}`), wantClass: contract.AdmissionInvalidParams, wantName: stringPointer("namespace.tool"), wantArguments: stringPointer(`{}`)},
		{name: "unknown tool defaults absent arguments", params: callParams(`{"name":"namespace.tool"}`), wantClass: contract.AdmissionUnknownTool, wantName: stringPointer("namespace.tool"), wantArguments: stringPointer(`{}`), wantResolutions: 1},
		{name: "schema rejection pins route", params: callParams(`{"name":"namespace.tool","arguments":{"value":1e0}}`), found: true, validationErr: errors.New("schema mismatch"), wantClass: contract.AdmissionInvalidArguments, wantName: stringPointer("namespace.tool"), wantArguments: stringPointer(`{"value":1e0}`), wantResolutions: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, audits, authority, _, credential := newAdmissionCoordinator(t, nil)
			lease, err := authority.Authenticate(context.Background(), credential.Bearer)
			require.NoError(t, err)
			defer lease.Release()
			resolutions, acquisitions := 0, 0
			service, err := newService(audits, authority, func(name string) (callTarget, bool) {
				resolutions++
				return serviceCallTarget(test.validationErr, func(context.Context) (executionLease, error) {
					acquisitions++
					return nil, errors.New("must not acquire")
				}), test.found
			})
			require.NoError(t, err)

			response := service.Call(context.Background(), lease, CallRequest{Params: test.params, WireValid: true})

			assert.Equal(t, contract.CallRejected, response.ErrorCode)
			assert.NotEmpty(t, response.InvocationID)
			assert.Nil(t, response.Result)
			assert.Equal(t, test.wantResolutions, resolutions)
			assert.Zero(t, acquisitions)
			record, found, readErr := audits.Read(context.Background(), response.InvocationID)
			require.NoError(t, readErr)
			require.True(t, found)
			assert.Equal(t, test.wantClass, record.AdmissionClass)
			assert.Equal(t, test.wantName, record.RequestedName)
			assert.Equal(t, test.wantArguments, record.RedactedArguments)
			assert.Nil(t, record.AuthorizationDecision)
			if test.wantClass == contract.AdmissionInvalidArguments {
				assert.Equal(t, contract.SyntheticServerID, *record.ServerID)
			} else {
				assert.Nil(t, record.ServerID)
			}
		})
	}
}

func TestServiceDispatchesPinnedAllowOnceAfterGateRelease(t *testing.T) {
	_, audits, authority, principal, credential := newAdmissionCoordinator(t, nil)
	lease, err := authority.Authenticate(context.Background(), credential.Bearer)
	require.NoError(t, err)
	defer lease.Release()
	resolutions, validations, acquisitions, executions := 0, 0, 0, 0
	var executedArguments json.RawMessage
	service, err := newService(audits, authority, func(name string) (callTarget, bool) {
		resolutions++
		assert.Equal(t, "namespace.tool", name)
		target := serviceCallTarget(nil, func(context.Context) (executionLease, error) {
			acquisitions++
			upstream := "tool"
			_, grantErr := authority.CreateGrant(context.Background(), authorization.CreateGrantRequest{
				PrincipalID: principal.ID, Effect: contract.GrantDeny, ServerID: contract.SyntheticServerID, UpstreamName: &upstream,
			}, func(context.Context, *sql.Tx, string) (bool, error) { return true, nil })
			require.NoError(t, grantErr, "dispatch acquisition must run after the authority gate is released")
			return &serviceExecutionLease{execute: func(arguments json.RawMessage) downstream.CallResult {
				executions++
				executedArguments = append(json.RawMessage(nil), arguments...)
				return downstream.CallResult{Response: downstream.Response{Result: json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`)}}
			}}, nil
		})
		target.validate = func(arguments strictjson.Value) error {
			validations++
			assert.Equal(t, "1e0", arguments.Object[0].Value.Number)
			return nil
		}
		return target, true
	})
	require.NoError(t, err)

	response := service.Call(context.Background(), lease, CallRequest{Params: callParams(`{"name":"namespace.tool","arguments":{"value":1e0,"token":"raw"}}`), WireValid: true})

	require.NotNil(t, response.Result)
	assert.Empty(t, response.ErrorCode)
	assert.Empty(t, response.InvocationID, "successful results expose no Gateway metadata")
	assert.Equal(t, 1, resolutions)
	assert.Equal(t, 1, validations)
	assert.Equal(t, 1, acquisitions)
	assert.Equal(t, 1, executions)
	assert.Equal(t, `{"value":1e0,"token":"raw"}`, string(executedArguments))
	record := onlyInvocationRecord(t, audits)
	assert.Equal(t, contract.AdmissionEvaluated, record.AdmissionClass)
	assert.Equal(t, contract.DecisionAllow, *record.AuthorizationDecision)
	assert.Equal(t, `{"value":1e0,"token":"[REDACTED]"}`, *record.RedactedArguments)
	assert.Equal(t, contract.TerminalSucceeded, *record.TerminalClass)
}

func TestServiceMapsAdmissionFailuresWithoutDispatch(t *testing.T) {
	t.Run("committed deny is least disclosing", func(t *testing.T) {
		_, audits, authority, principal, credential := newAdmissionCoordinator(t, nil)
		upstream := "tool"
		_, err := authority.CreateGrant(context.Background(), authorization.CreateGrantRequest{
			PrincipalID: principal.ID, Effect: contract.GrantDeny, ServerID: contract.SyntheticServerID, UpstreamName: &upstream,
		}, func(context.Context, *sql.Tx, string) (bool, error) { return true, nil })
		require.NoError(t, err)
		lease, err := authority.Authenticate(context.Background(), credential.Bearer)
		require.NoError(t, err)
		defer lease.Release()
		acquisitions := 0
		service, err := newService(audits, authority, func(string) (callTarget, bool) {
			return serviceCallTarget(nil, func(context.Context) (executionLease, error) {
				acquisitions++
				return nil, nil
			}), true
		})
		require.NoError(t, err)

		response := service.Call(context.Background(), lease, validCallParams())

		assert.Equal(t, contract.CallRejected, response.ErrorCode)
		assert.NotEmpty(t, response.InvocationID)
		assert.Zero(t, acquisitions)
		record := onlyInvocationRecord(t, audits)
		assert.Equal(t, contract.DecisionDeny, *record.AuthorizationDecision)
		assert.Nil(t, record.TerminalClass)
	})

	t.Run("uncertain commit exposes no invocation identity", func(t *testing.T) {
		armed := false
		fault := func(point storage.FaultPoint) error {
			if armed && point == storage.FaultAfterCommit {
				return errors.New("commit acknowledgement lost")
			}
			return nil
		}
		_, audits, authority, _, credential := newAdmissionCoordinator(t, fault)
		lease, err := authority.Authenticate(context.Background(), credential.Bearer)
		require.NoError(t, err)
		defer lease.Release()
		acquisitions := 0
		service, err := newService(audits, authority, func(string) (callTarget, bool) {
			return serviceCallTarget(nil, func(context.Context) (executionLease, error) {
				acquisitions++
				return nil, nil
			}), true
		})
		require.NoError(t, err)
		armed = true

		response := service.Call(context.Background(), lease, validCallParams())

		assert.Equal(t, contract.AuditUnavailable, response.ErrorCode)
		assert.Empty(t, response.InvocationID)
		assert.Zero(t, acquisitions)
		assert.True(t, audits.store.Latched())
	})
}

func TestServiceWireInvalidParamsPreserveSafeFieldsWithoutResolution(t *testing.T) {
	_, audits, authority, _, credential := newAdmissionCoordinator(t, nil)
	lease, err := authority.Authenticate(context.Background(), credential.Bearer)
	require.NoError(t, err)
	defer lease.Release()
	resolutions := 0
	service, err := newService(audits, authority, func(string) (callTarget, bool) {
		resolutions++
		return callTarget{}, false
	})
	require.NoError(t, err)

	response := service.Call(context.Background(), lease, CallRequest{
		Params: callParams(`{"name":"namespace.tool","arguments":{"token":"private"}}`),
	})

	assert.Equal(t, contract.CallRejected, response.ErrorCode)
	assert.Zero(t, resolutions)
	record := onlyInvocationRecord(t, audits)
	assert.Equal(t, contract.AdmissionInvalidParams, record.AdmissionClass)
	assert.Equal(t, "namespace.tool", *record.RequestedName)
	assert.Equal(t, `{"token":"[REDACTED]"}`, *record.RedactedArguments)
}

func TestServiceIdentityFailuresNeverInsertOrDispatch(t *testing.T) {
	tests := []struct {
		name   string
		poison func(*Repository)
	}{
		{name: "entropy failure", poison: func(repository *Repository) { repository.entropy = strings.NewReader("") }},
		{name: "clock failure", poison: func(repository *Repository) { repository.clock = &repositoryClock{now: time.Time{}} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, audits, authority, _, credential := newAdmissionCoordinator(t, nil)
			test.poison(audits)
			lease, err := authority.Authenticate(context.Background(), credential.Bearer)
			require.NoError(t, err)
			defer lease.Release()
			acquisitions := 0
			service, err := newService(audits, authority, func(string) (callTarget, bool) {
				return serviceCallTarget(nil, func(context.Context) (executionLease, error) {
					acquisitions++
					return nil, nil
				}), true
			})
			require.NoError(t, err)

			response := service.Call(context.Background(), lease, validCallParams())

			assert.Equal(t, contract.AuditUnavailable, response.ErrorCode)
			assert.Empty(t, response.InvocationID)
			assert.Zero(t, acquisitions)
			count, countErr := audits.Count(context.Background())
			require.NoError(t, countErr)
			assert.Zero(t, count)
		})
	}

	t.Run("identity collision", func(t *testing.T) {
		_, audits, authority, _, credential := newAdmissionCoordinator(t, nil)
		audits.entropy = bytes.NewReader(bytes.Repeat([]byte{0xAB}, 20))
		executions := 0
		service, err := newService(audits, authority, func(string) (callTarget, bool) {
			return serviceCallTarget(nil, func(context.Context) (executionLease, error) {
				return &serviceExecutionLease{execute: func(json.RawMessage) downstream.CallResult {
					executions++
					return downstream.CallResult{Response: downstream.Response{Result: json.RawMessage(`{"content":[]}`)}}
				}}, nil
			}), true
		})
		require.NoError(t, err)
		firstLease, err := authority.Authenticate(context.Background(), credential.Bearer)
		require.NoError(t, err)
		first := service.Call(context.Background(), firstLease, validCallParams())
		firstLease.Release()
		require.NotNil(t, first.Result)
		secondLease, err := authority.Authenticate(context.Background(), credential.Bearer)
		require.NoError(t, err)
		defer secondLease.Release()

		second := service.Call(context.Background(), secondLease, validCallParams())

		assert.Equal(t, contract.AuditUnavailable, second.ErrorCode)
		assert.Empty(t, second.InvocationID)
		assert.Equal(t, 1, executions)
		count, countErr := audits.Count(context.Background())
		require.NoError(t, countErr)
		assert.Equal(t, int64(1), count)
	})
}

func TestServiceAcquisitionRejectionsAreKnownPrestartFailures(t *testing.T) {
	reasons := []downstream.RejectionReason{
		downstream.RejectionGlobalSaturated,
		downstream.RejectionServerSaturated,
		downstream.RejectionStale,
		downstream.RejectionWithdrawn,
		downstream.RejectionDraining,
		downstream.RejectionUnavailable,
		downstream.RejectionCancelled,
	}
	for _, reason := range reasons {
		t.Run(string(reason), func(t *testing.T) {
			_, audits, authority, _, credential := newAdmissionCoordinator(t, nil)
			lease, err := authority.Authenticate(context.Background(), credential.Bearer)
			require.NoError(t, err)
			defer lease.Release()
			acquisitions := 0
			service, err := newService(audits, authority, func(string) (callTarget, bool) {
				return serviceCallTarget(nil, func(context.Context) (executionLease, error) {
					acquisitions++
					return nil, &downstream.PreStartRejection{Reason: reason, Failure: downstream.FailurePreStart}
				}), true
			})
			require.NoError(t, err)

			response := service.Call(context.Background(), lease, validCallParams())

			assert.Equal(t, contract.ToolUnavailable, response.ErrorCode)
			assert.NotEmpty(t, response.InvocationID)
			assert.Equal(t, 1, acquisitions)
			record := onlyInvocationRecord(t, audits)
			assert.Equal(t, contract.TerminalPrestartFailure, *record.TerminalClass)
		})
	}
}

func TestServiceSanitizesEveryDispatchOutcomeAndAnnotatesOnce(t *testing.T) {
	toolError := &downstream.RPCError{Code: -32000, Message: "private", Data: json.RawMessage(`{"secret":"raw"}`)}
	tests := []struct {
		name         string
		result       downstream.CallResult
		wantCode     contract.AgentCallErrorCode
		wantTerminal contract.InvocationTerminalClass
		wantSuccess  bool
	}{
		{name: "prestart", result: downstream.CallResult{Failure: downstream.FailurePreStart, Err: errors.New("private")}, wantCode: contract.ToolUnavailable, wantTerminal: contract.TerminalPrestartFailure},
		{name: "invalid complete response", result: downstream.CallResult{Failure: downstream.FailureResponseInvalid, Err: errors.New("private")}, wantCode: contract.DownstreamFailure, wantTerminal: contract.TerminalDownstreamFailure},
		{name: "uncertain handoff", result: downstream.CallResult{Failure: downstream.FailureStartUncertain, Err: errors.New("private")}, wantCode: contract.OutcomeUnknown, wantTerminal: contract.TerminalOutcomeUnknown},
		{name: "rpc error", result: downstream.CallResult{Response: downstream.Response{Error: toolError}}, wantCode: contract.DownstreamFailure, wantTerminal: contract.TerminalDownstreamFailure},
		{name: "tool error", result: downstream.CallResult{Response: downstream.Response{Result: json.RawMessage(`{"content":[{"type":"text","text":"private"}],"isError":true}`)}}, wantCode: contract.DownstreamFailure, wantTerminal: contract.TerminalDownstreamFailure},
		{name: "malformed result", result: downstream.CallResult{Response: downstream.Response{Result: json.RawMessage(`{"content":"bad"}`)}}, wantCode: contract.DownstreamFailure, wantTerminal: contract.TerminalDownstreamFailure},
		{name: "success", result: downstream.CallResult{Response: downstream.Response{Result: json.RawMessage(`{"content":[]}`)}}, wantTerminal: contract.TerminalSucceeded, wantSuccess: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, audits, authority, _, credential := newAdmissionCoordinator(t, nil)
			lease, err := authority.Authenticate(context.Background(), credential.Bearer)
			require.NoError(t, err)
			defer lease.Release()
			executions := 0
			service, err := newService(audits, authority, func(string) (callTarget, bool) {
				return serviceCallTarget(nil, func(context.Context) (executionLease, error) {
					return &serviceExecutionLease{execute: func(json.RawMessage) downstream.CallResult {
						executions++
						return test.result
					}}, nil
				}), true
			})
			require.NoError(t, err)

			response := service.Call(context.Background(), lease, validCallParams())

			assert.Equal(t, 1, executions)
			assert.Equal(t, test.wantCode, response.ErrorCode)
			assert.Equal(t, test.wantSuccess, response.Result != nil)
			if test.wantSuccess {
				assert.Empty(t, response.InvocationID)
			} else {
				assert.NotEmpty(t, response.InvocationID)
			}
			record := onlyInvocationRecord(t, audits)
			assert.Equal(t, test.wantTerminal, *record.TerminalClass)
		})
	}
}

func TestServiceTerminalFailureNeverChangesLiveResultOrRetries(t *testing.T) {
	armed := false
	fault := func(point storage.FaultPoint) error {
		if armed && point == storage.FaultAfterCommit {
			return errors.New("terminal commit acknowledgement lost")
		}
		return nil
	}
	_, audits, authority, _, credential := newAdmissionCoordinator(t, fault)
	lease, err := authority.Authenticate(context.Background(), credential.Bearer)
	require.NoError(t, err)
	defer lease.Release()
	executions := 0
	service, err := newService(audits, authority, func(string) (callTarget, bool) {
		return serviceCallTarget(nil, func(context.Context) (executionLease, error) {
			return &serviceExecutionLease{execute: func(json.RawMessage) downstream.CallResult {
				executions++
				armed = true
				return downstream.CallResult{Response: downstream.Response{Result: json.RawMessage(`{"content":[]}`)}}
			}}, nil
		}), true
	})
	require.NoError(t, err)

	response := service.Call(context.Background(), lease, validCallParams())

	assert.NotNil(t, response.Result)
	assert.Empty(t, response.ErrorCode)
	assert.Empty(t, response.InvocationID)
	assert.Equal(t, 1, executions)
	assert.True(t, audits.store.Latched())
}

type serviceExecutionLease struct {
	execute func(json.RawMessage) downstream.CallResult
	cancel  func(context.Context) error
}

func (lease *serviceExecutionLease) Execute(_ context.Context, arguments json.RawMessage) downstream.CallResult {
	return lease.execute(arguments)
}

func (lease *serviceExecutionLease) Cancel(ctx context.Context) error {
	if lease.cancel == nil {
		return nil
	}
	return lease.cancel(ctx)
}

func serviceCallTarget(validationErr error, acquire func(context.Context) (executionLease, error)) callTarget {
	return callTarget{
		evidence: RouteEvidence{
			ServerID: contract.SyntheticServerID, ToolID: invocationID(11), UpstreamName: "tool",
			DescriptorRevision: "2", DescriptorFingerprint: strings.Repeat("a", 64),
		},
		validate: func(strictjson.Value) error { return validationErr },
		acquire:  acquire,
	}
}

func validCallParams() CallRequest {
	return CallRequest{Params: callParams(`{"name":"namespace.tool","arguments":{}}`), WireValid: true}
}

func callParams(raw string) strictjson.Value {
	value, err := strictjson.ParseValue([]byte(raw), strictjson.Options{MaxBytes: 1 << 20, MaxDepth: 32})
	if err != nil {
		panic(err)
	}
	return value
}

func onlyInvocationRecord(t *testing.T, repository *Repository) contract.InvocationAuditRecord {
	t.Helper()
	var invocationID string
	require.NoError(t, repository.store.View(context.Background(), func(transaction *sql.Tx) error {
		return transaction.QueryRowContext(context.Background(), `SELECT id FROM invocations`).Scan(&invocationID)
	}))
	record, found, err := repository.Read(context.Background(), invocationID)
	require.NoError(t, err)
	require.True(t, found)
	return record
}

func stringPointer(value string) *string { return &value }
