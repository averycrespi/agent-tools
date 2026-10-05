package invocation

import (
	"context"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/stretchr/testify/require"
)

func TestHTTPRecordingExpiryIsNotAdmissionFailure(t *testing.T) {
	coordinator, audits, authority, principal, credential := newAdmissionCoordinator(t, nil)
	traffic, _ := trafficFixture(t, nil, nil)
	audits.traffic = traffic
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	current, err := authority.GetPrincipal(ctx, principal.ID)
	require.NoError(t, err)
	allow := contract.HTTPDefaultAllow
	_, err = authority.PatchPrincipal(ctx, principal.ID, authorization.PatchPrincipalRequest{ExpectedRevision: current.Revision, HTTPDefault: &allow})
	require.NoError(t, err)
	lease, err := authority.Authenticate(ctx, credential.Bearer)
	require.NoError(t, err)
	defer lease.Release()
	identity, err := audits.PrepareIdentity()
	require.NoError(t, err)
	traffic.writerGate.Lock()
	unlock := sync.OnceFunc(traffic.writerGate.Unlock)
	defer unlock()
	type outcome struct {
		result HTTPAdmissionResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		r, e := coordinator.AdmitHTTP(ctx, lease, identity, authorization.HTTPAccessInput{PrincipalID: principal.ID, URL: "https://example.com/private-canary", Method: "POST"}, httppolicy.AddressFacts{Complete: true, Addresses: []netip.Addr{netip.MustParseAddr("93.184.216.34")}}, nil)
		done <- outcome{r, e}
	}()
	require.Eventually(t, func() bool {
		traffic.mu.Lock()
		defer traffic.mu.Unlock()
		return traffic.queued == 1
	}, time.Second, time.Millisecond)
	select {
	case got := <-done:
		require.NoError(t, got.err)
		require.Equal(t, diagnostics.NoStage, got.result.FailureStage)
		require.Equal(t, diagnostics.None, got.result.FailureCause)
		require.True(t, got.result.DispatchAuthorized)
		got.result.Settle()
	case <-time.After(time.Second):
		t.Fatal("admission waited for the history writer")
	}
	// The writer's own queue deadline cannot retroactively revoke dispatch.
	<-time.After(traffic.config.QueueLifetime + 10*time.Millisecond)
	unlock()
	waitTraffic(t, traffic)
	history, err := traffic.HTTPHistory(ctx, 0, 10)
	require.NoError(t, err)
	require.Empty(t, history.Records)
	require.True(t, traffic.Healthy())
}

func TestHTTPAdmissionEvaluationDiagnostic(t *testing.T) {
	coordinator, audits, _, principal, _ := newAdmissionCoordinator(t, nil)
	traffic, _ := trafficFixture(t, nil, nil)
	audits.traffic = traffic
	identity, err := audits.PrepareIdentity()
	require.NoError(t, err)
	result, err := coordinator.AdmitHTTP(t.Context(), nil, identity, authorization.HTTPAccessInput{PrincipalID: principal.ID}, httppolicy.AddressFacts{}, nil)
	require.ErrorIs(t, err, authorization.ErrAdmissionUnavailable)
	require.Equal(t, diagnostics.ProxyEvaluation, result.FailureStage)
	require.Equal(t, diagnostics.Unavailable, result.FailureCause)
	require.False(t, result.DispatchAuthorized)
}
