package invocation

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/accesstarget"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/downstream"
	"github.com/stretchr/testify/require"
)

func TestServiceAdmittedExecutionDoesNotHoldAuthority(t *testing.T) {
	admittedExecutionAuthority(t, false)
}

func TestServiceAdmittedExecutionSurvivesLostHistory(t *testing.T) {
	admittedExecutionAuthority(t, true)
}

func admittedExecutionAuthority(t *testing.T, loseHistory bool) {
	t.Helper()
	_, audits, authority, principal, credential := newAdmissionCoordinator(t, nil)
	if loseHistory {
		audits.traffic.BeginDrain()
	}
	lease, err := authority.Authenticate(t.Context(), credential.Bearer)
	require.NoError(t, err)
	defer lease.Release()
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	calls := 0
	service, err := newService(audits, authority, func(string) (callTarget, bool) {
		return serviceCallTarget(nil, func(context.Context) (executionLease, error) {
			return &serviceExecutionLease{execute: func(json.RawMessage) downstream.CallResult {
				calls++
				close(entered)
				<-release
				return downstream.CallResult{Response: downstream.Response{Result: json.RawMessage(`{"content":[]}`)}}
			}}, nil
		}), true
	})
	require.NoError(t, err)
	done := make(chan CallResponse, 1)
	go func() { done <- service.Call(t.Context(), lease, validCallParams()) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("execution did not enter")
	}
	_, err = authority.CreateGrant(t.Context(), authorization.CreateGrantRequest{PrincipalID: principal.ID, Effect: contract.GrantDeny, Target: accesstarget.MCP{ServerID: contract.SyntheticServerID}}, func(context.Context, *sql.Tx, string) (bool, error) { return true, nil })
	require.NoError(t, err)
	_, err = authority.RevokeCredential(t.Context(), principal.ID, credential.Principal.Revision)
	require.NoError(t, err)
	select {
	case <-lease.Done():
		t.Fatal("post-admission revocation cancelled execution")
	default:
	}
	unblock()
	response := <-done
	require.NotNil(t, response.Result)
	require.Equal(t, 1, calls)
	if loseHistory {
		require.GreaterOrEqual(t, audits.traffic.Status(t.Context()).Delivery.Discarded, uint64(2))
	} else {
		record := onlyInvocationRecord(t, audits)
		require.Equal(t, contract.TerminalSucceeded, *record.TerminalClass)
	}
}

func TestServiceControlWriterContentionCannotBlockTerminalObservation(t *testing.T) {
	_, audits, authority, _, credential := newAdmissionCoordinator(t, nil)
	lease, err := authority.Authenticate(t.Context(), credential.Bearer)
	require.NoError(t, err)
	defer lease.Release()
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	writerDone := make(chan error, 1)
	service, err := newService(audits, authority, func(string) (callTarget, bool) {
		return serviceCallTarget(nil, func(context.Context) (executionLease, error) {
			return &serviceExecutionLease{execute: func(json.RawMessage) downstream.CallResult {
				go func() {
					writerDone <- audits.store.Mutate(t.Context(), func(*sql.Tx) error { close(entered); <-release; return nil })
				}()
				<-entered
				return downstream.CallResult{Failure: downstream.FailureStartUncertain}
			}}, nil
		}), true
	})
	require.NoError(t, err)
	response := service.Call(t.Context(), lease, validCallParams())
	require.Equal(t, contract.OutcomeUnknown, response.ErrorCode)
	record := onlyInvocationRecord(t, audits)
	require.Equal(t, contract.TerminalOutcomeUnknown, *record.TerminalClass)
	require.False(t, audits.store.Latched())
	unblock()
	require.NoError(t, <-writerDone)
}

func TestDrainAfterConfirmationDoesNotUndoAllow(t *testing.T) {
	_, audits, authority, _, credential := newAdmissionCoordinator(t, nil)
	lease, err := authority.Authenticate(t.Context(), credential.Bearer)
	require.NoError(t, err)
	defer lease.Release()
	calls := 0
	service, err := newService(audits, authority, func(string) (callTarget, bool) {
		return serviceCallTarget(nil, func(context.Context) (executionLease, error) {
			authority.BeginDrain()
			return &serviceExecutionLease{execute: func(json.RawMessage) downstream.CallResult {
				calls++
				return downstream.CallResult{Response: downstream.Response{Result: json.RawMessage(`{"content":[]}`)}}
			}}, nil
		}), true
	})
	require.NoError(t, err)
	response := service.Call(t.Context(), lease, validCallParams())
	require.NotNil(t, response.Result)
	require.Equal(t, 1, calls)
	require.Equal(t, contract.TerminalSucceeded, *onlyInvocationRecord(t, audits).TerminalClass)
}

func TestDrainBeforeEvaluationNeverAcquiresCapability(t *testing.T) {
	_, audits, authority, _, credential := newAdmissionCoordinator(t, nil)
	lease, err := authority.Authenticate(t.Context(), credential.Bearer)
	require.NoError(t, err)
	defer lease.Release()
	calls := 0
	service, err := newService(audits, authority, func(string) (callTarget, bool) {
		authority.BeginDrain()
		return serviceCallTarget(nil, func(context.Context) (executionLease, error) { calls++; return nil, nil }), true
	})
	require.NoError(t, err)
	response := service.Call(t.Context(), lease, validCallParams())
	require.Equal(t, contract.CallRejected, response.ErrorCode)
	require.Equal(t, contract.RejectionAuthorizationUnavailable, response.RejectionReason)
	require.Zero(t, calls)
	waitTraffic(t, audits.traffic)
	count, err := audits.Count(t.Context())
	require.NoError(t, err)
	require.Zero(t, count)
}
