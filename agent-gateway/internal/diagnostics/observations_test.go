package diagnostics

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestObservationsConcurrentBoundedAndIndependent(t *testing.T) {
	o := NewObservations()
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			for range 100 {
				o.Request(MCP)
				o.Execution(MCP)
				o.Result(MCP, Succeeded)
				o.Latency(MCP, ExecutionStage, 10*time.Millisecond)
			}
		})
	}
	workers.Wait()
	s := o.Status()
	require.EqualValues(t, 1600, s.Protocols[MCP].Requests)
	require.EqualValues(t, 1600, s.Protocols[MCP].Executions)
	require.EqualValues(t, 1600, s.Protocols[MCP].Results[Succeeded])
	require.EqualValues(t, 1600, s.Protocols[MCP].Latency[ExecutionStage][1])
	require.NotEqual(t, s.Epoch, NewObservations().Status().Epoch)
	o.status.Protocols[MCP].Requests = contract.RecordedActivityMaxCount
	o.Request(MCP)
	o.Request(MCP)
	require.Equal(t, contract.RecordedActivityMaxCount, o.Status().Protocols[MCP].Requests)
	require.True(t, o.Status().Overflow)
	o.Request(255)
	o.Result(MCP, 255)
	o.Latency(MCP, 255, time.Second)
	o.GitReport(strings.Repeat("secret-canary", 1000))
	encoded, err := json.Marshal(o.Status())
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "secret-canary")
	require.Less(t, len(encoded), 3000)
}

func TestObservationEpochFailureDoesNotGateCounting(t *testing.T) {
	o := newObservations(strings.NewReader(""))
	o.Request(MCP)
	require.Empty(t, o.Status().Epoch)
	require.EqualValues(t, 1, o.Status().Protocols[MCP].Requests)
}

func TestObservationsDisjointLatencyBucketsAndGitTrust(t *testing.T) {
	o := NewObservations()
	for _, d := range []time.Duration{0, time.Millisecond, 10 * time.Millisecond, 100 * time.Millisecond, time.Second, 10 * time.Second, 24 * time.Hour, -time.Second} {
		o.Latency(HTTP, RequestStage, d)
	}
	require.Equal(t, [6]uint64{2, 1, 1, 1, 1, 1}, o.Status().Protocols[HTTP].Latency[RequestStage])
	o.Result(Git, Unknown)
	o.GitReport("reported_success")
	s := o.Status().Protocols[Git]
	require.Zero(t, s.Results[Succeeded])
	require.EqualValues(t, 1, s.Results[Unknown])
	require.EqualValues(t, 1, s.GitReports[0])
}
