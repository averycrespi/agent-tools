package invocation

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServiceAdmissionCommitExcludesCredentialAndPolicyMutationUntilDispatch(t *testing.T) {
	committed := make(chan struct{})
	releaseCommit := make(chan struct{})
	var blockCommit atomic.Bool
	fault := func(point storage.FaultPoint) error {
		if point == storage.FaultAfterCommit && blockCommit.CompareAndSwap(true, false) {
			close(committed)
			<-releaseCommit
		}
		return nil
	}
	_, audits, authority, principal, credential := newAdmissionCoordinator(t, fault)
	lease, err := authority.Authenticate(context.Background(), credential.Bearer)
	require.NoError(t, err)
	defer lease.Release()
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
	blockCommit.Store(true)
	response := make(chan CallResponse, 1)
	go func() { response <- service.Call(context.Background(), lease, validCallParams()) }()
	<-committed

	upstream := "tool"
	_, err = authority.CreateGrant(context.Background(), authorization.CreateGrantRequest{Description: stringPointer("Test grant"),
		PrincipalID: principal.ID, Effect: contract.GrantDeny, ServerID: contract.SyntheticServerID, UpstreamName: &upstream,
	}, func(context.Context, *sql.Tx, string) (bool, error) { return true, nil })
	assert.ErrorIs(t, err, authorization.ErrResourceLimit)
	_, err = authority.RevokeCredential(context.Background(), principal.ID, credential.Principal.Revision)
	assert.ErrorIs(t, err, authorization.ErrResourceLimit)
	close(releaseCommit)

	result := <-response
	require.NotNil(t, result.Result)
	assert.Equal(t, 1, executions)
	record := onlyInvocationRecord(t, audits)
	assert.Equal(t, contract.DecisionAllow, *record.AuthorizationDecision)
	assert.Equal(t, contract.TerminalSucceeded, *record.TerminalClass)

	_, err = authority.CreateGrant(context.Background(), authorization.CreateGrantRequest{Description: stringPointer("Test grant"),
		PrincipalID: principal.ID, Effect: contract.GrantDeny, ServerID: contract.SyntheticServerID, UpstreamName: &upstream,
	}, func(context.Context, *sql.Tx, string) (bool, error) { return true, nil })
	require.NoError(t, err, "policy mutation must proceed after invocation admission and dispatch release the gate")
	_, err = authority.RevokeCredential(context.Background(), principal.ID, credential.Principal.Revision)
	require.NoError(t, err, "credential mutation must proceed after invocation admission and dispatch release the gate")
}

func TestDrainAfterDetachmentDoesNotUndoCommittedAllow(t *testing.T) {
	_, audits, authority, _, credential := newAdmissionCoordinator(t, nil)
	lease, err := authority.Authenticate(context.Background(), credential.Bearer)
	require.NoError(t, err)
	defer lease.Release()
	executions := 0
	service, err := newService(audits, authority, func(string) (callTarget, bool) {
		return serviceCallTarget(nil, func(context.Context) (executionLease, error) {
			authority.BeginDrain()
			return &serviceExecutionLease{execute: func(json.RawMessage) downstream.CallResult {
				executions++
				return downstream.CallResult{Response: downstream.Response{Result: json.RawMessage(`{"content":[]}`)}}
			}}, nil
		}), true
	})
	require.NoError(t, err)

	result := service.Call(context.Background(), lease, validCallParams())

	require.NotNil(t, result.Result)
	assert.Equal(t, 1, executions)
	record := onlyInvocationRecord(t, audits)
	assert.Equal(t, contract.TerminalSucceeded, *record.TerminalClass)
	drainCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, authority.Drain(drainCtx))
}

func TestDrainBetweenCommitAndDetachmentNeverAcquiresCapability(t *testing.T) {
	committed := make(chan struct{})
	releaseCommit := make(chan struct{})
	var blockCommit atomic.Bool
	fault := func(point storage.FaultPoint) error {
		if point == storage.FaultAfterCommit && blockCommit.CompareAndSwap(true, false) {
			close(committed)
			<-releaseCommit
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
	blockCommit.Store(true)
	response := make(chan CallResponse, 1)
	go func() { response <- service.Call(context.Background(), lease, validCallParams()) }()
	<-committed

	authority.BeginDrain()
	close(releaseCommit)
	result := <-response

	assert.Equal(t, contract.CallRejected, result.ErrorCode)
	assert.NotEmpty(t, result.InvocationID)
	assert.Zero(t, acquisitions)
	record := onlyInvocationRecord(t, audits)
	assert.Equal(t, contract.DecisionAllow, *record.AuthorizationDecision)
	assert.Nil(t, record.TerminalClass)
	drainCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, authority.Drain(drainCtx))
}
