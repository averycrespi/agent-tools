package invocation

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/downstream"
	"github.com/stretchr/testify/require"
)

type isolationDiagnosticSink struct {
	entered, release chan struct{}
	once             sync.Once
	output           bytes.Buffer
}

func (s *isolationDiagnosticSink) Write(p []byte) (int, error) {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return s.output.Write(p)
}

func TestExecutionHealthAndRevocationWithBothSinksBlocked(t *testing.T) {
	_, audits, authority, principal, credential := newAdmissionCoordinator(t, nil)
	historyEntered, historyRelease := make(chan struct{}), make(chan struct{})
	releaseHistory := sync.OnceFunc(func() { close(historyRelease) })
	defer releaseHistory()
	var once sync.Once
	traffic, owner := trafficFixture(t, func(c *TrafficConfig) { c.QueueRecords = 1; c.BatchRecords = 1 }, func(point string) error {
		if point == "before_begin" {
			once.Do(func() { close(historyEntered); <-historyRelease })
		}
		return nil
	})
	audits.traffic = traffic
	// Occupancy includes the active write: subsequent starts AND terminals drop.
	require.NotNil(t, traffic.ObserveMCP(trafficPrepared(77)))
	select {
	case <-historyEntered:
	case <-time.After(time.Second):
		t.Fatal("history writer did not enter")
	}
	sink := &isolationDiagnosticSink{entered: make(chan struct{}), release: make(chan struct{})}
	releaseStderr := sync.OnceFunc(func() { close(sink.release) })
	adapter := diagnostics.New(sink, diagnostics.Debug)
	defer func() { releaseStderr(); adapter.Finish(nil); <-adapter.Done() }()
	adapter.Observe(diagnostics.Facts{Event: diagnostics.Startup})
	select {
	case <-sink.entered:
	case <-time.After(time.Second):
		t.Fatal("stderr writer did not enter")
	}
	for range diagnostics.QueueRecords + 10 {
		adapter.Observe(diagnostics.Facts{Event: diagnostics.Readiness})
	}
	var calls atomic.Int32
	const canary = "private-simultaneous-sink-canary"
	service, err := newService(audits, authority, func(string) (callTarget, bool) {
		return serviceCallTarget(nil, func(context.Context) (executionLease, error) {
			return &serviceExecutionLease{execute: func(json.RawMessage) downstream.CallResult {
				calls.Add(1)
				return downstream.CallResult{Response: downstream.Response{Result: json.RawMessage(`{"content":[{"type":"text","text":"` + canary + `"}]}`)}}
			}}, nil
		}), true
	})
	require.NoError(t, err)
	service.SetDiagnostics(adapter)
	for range 2 {
		lease, authErr := authority.Authenticate(t.Context(), credential.Bearer)
		require.NoError(t, authErr)
		done := make(chan CallResponse, 1)
		go func() {
			defer lease.Release()
			done <- service.Call(t.Context(), lease, CallRequest{Params: callParams(`{"name":"namespace.tool","arguments":{"secret":"` + canary + `"}}`), WireValid: true})
		}()
		select {
		case response := <-done:
			require.NotNil(t, response.Result)
			require.Empty(t, response.ErrorCode)
		case <-time.After(time.Second):
			t.Fatal("execution waited on optional sinks")
		}
	}
	lease, err := authority.Authenticate(t.Context(), credential.Bearer)
	require.NoError(t, err)
	defer lease.Release()
	_, err = authority.RevokeCredential(t.Context(), principal.ID, credential.Principal.Revision)
	require.NoError(t, err)
	response := service.Call(t.Context(), lease, validCallParams())
	require.Equal(t, contract.CallRejected, response.ErrorCode)
	require.EqualValues(t, 2, calls.Load(), "capture loss cannot weaken revocation or replay executions")
	health := traffic.Status(t.Context())
	require.Equal(t, 1, health.Delivery.QueueRecords)
	require.LessOrEqual(t, health.Delivery.QueueBytes, health.Delivery.QueueByteLimit)
	require.GreaterOrEqual(t, health.Delivery.Discarded, uint64(4))
	diagnosticHealth := adapter.Status()
	require.True(t, diagnosticHealth.Writing)
	require.Positive(t, diagnosticHealth.Dropped)
	require.LessOrEqual(t, diagnosticHealth.QueueRecords, diagnostics.QueueRecords)
	observed := service.observations.Status().Protocols[diagnostics.MCP]
	require.EqualValues(t, 3, observed.Requests)
	require.EqualValues(t, 2, observed.Executions)
	require.EqualValues(t, 2, observed.Results[diagnostics.Succeeded])
	owned, waiting := audits.store.MutationOccupancy()
	require.False(t, owned)
	require.Zero(t, waiting)
	require.False(t, audits.store.Latched())
	releaseHistory()
	waitTraffic(t, traffic)
	releaseStderr()
	require.True(t, adapter.Finish(nil))
	<-adapter.Done()
	require.NotContains(t, sink.output.String(), canary)
	require.NotContains(t, sink.output.String(), credential.Bearer)
	require.NoError(t, traffic.Close())
	files, err := filepath.Glob(filepath.Join(owner.Layout().Root, "traffic-*"))
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, path := range files {
		content, readErr := os.ReadFile(path)
		require.NoError(t, readErr)
		require.NotContains(t, string(content), canary)
		require.NotContains(t, string(content), credential.Bearer)
	}
}
