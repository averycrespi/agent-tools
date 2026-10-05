//go:build integration

package invocation

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/downstream"
	"github.com/stretchr/testify/require"
)

func TestIntegrationInFlightPruningPreservesSelfContainedTerminal(t *testing.T) {
	_, audits, authority, _, credential := newAdmissionCoordinator(t, nil)
	traffic, _ := trafficFixture(t, func(c *TrafficConfig) { c.RetainedRecords = 4; c.BatchRecords = 1 }, nil)
	audits.traffic = traffic
	lease, err := authority.Authenticate(t.Context(), credential.Bearer)
	require.NoError(t, err)
	defer lease.Release()
	started, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	calls := 0
	service, err := newService(audits, authority, func(string) (callTarget, bool) {
		return serviceCallTarget(nil, func(context.Context) (executionLease, error) {
			return &serviceExecutionLease{execute: func(json.RawMessage) downstream.CallResult {
				calls++
				close(started)
				<-release
				return downstream.CallResult{Response: downstream.Response{Result: json.RawMessage(`{"content":[]}`)}}
			}}, nil
		}), true
	})
	require.NoError(t, err)
	done := make(chan CallResponse, 1)
	go func() { done <- service.Call(t.Context(), lease, validCallParams()) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("execution did not start")
	}
	inFlight := onlyInvocationRecord(t, audits)
	require.Nil(t, inFlight.TerminalClass)
	for i := 0; i < 4; i++ {
		recordMCP(t, traffic, trafficPrepared(1000+i))
	}
	_, found, err := audits.Read(t.Context(), inFlight.InvocationID)
	require.NoError(t, err)
	require.False(t, found)
	unblock()
	response := <-done
	require.NotNil(t, response.Result)
	require.Equal(t, 1, calls)
	waitTraffic(t, traffic)
	completed, found, err := audits.Read(t.Context(), inFlight.InvocationID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, contract.TerminalSucceeded, *completed.TerminalClass)
	count, err := audits.Count(t.Context())
	require.NoError(t, err)
	require.EqualValues(t, 4, count)
	require.False(t, audits.store.Latched())
}
