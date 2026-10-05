package invocation

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/downstream"
	"github.com/stretchr/testify/require"
)

func TestConcurrentExecutionAndCompletionsIgnoreStalledRecorder(t *testing.T) {
	_, audits, authority, _, credential := newAdmissionCoordinator(t, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	var once sync.Once
	traffic, _ := trafficFixture(t, nil, func(point string) error {
		if point == "before_begin" {
			once.Do(func() { close(entered); <-release })
		}
		return nil
	})
	audits.traffic = traffic
	var calls atomic.Int32
	service, err := newService(audits, authority, func(string) (callTarget, bool) {
		return serviceCallTarget(nil, func(context.Context) (executionLease, error) {
			return &serviceExecutionLease{execute: func(json.RawMessage) downstream.CallResult {
				calls.Add(1)
				return downstream.CallResult{Response: downstream.Response{Result: json.RawMessage(`{"content":[]}`)}}
			}}, nil
		}), true
	})
	require.NoError(t, err)
	done := make(chan CallResponse, 2)
	for range 2 {
		lease, err := authority.Authenticate(t.Context(), credential.Bearer)
		require.NoError(t, err)
		defer lease.Release()
		go func() { done <- service.Call(t.Context(), lease, validCallParams()) }()
	}
	for range 2 {
		select {
		case response := <-done:
			require.NotNil(t, response.Result)
			require.Empty(t, response.ErrorCode)
		case <-time.After(time.Second):
			t.Fatal("response blocked on history")
		}
	}
	require.EqualValues(t, 2, calls.Load())
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("writer never entered")
	}
	count, err := audits.Count(t.Context())
	require.NoError(t, err)
	require.Zero(t, count, "executions already settled before SQL begins")
	owned, waiting := audits.store.MutationOccupancy()
	require.False(t, owned)
	require.Zero(t, waiting)
	unblock()
	waitTraffic(t, traffic)
	history, err := traffic.History(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, history.Records, 2)
	for _, record := range history.Records {
		require.NotNil(t, record.TerminalClass)
		require.Equal(t, contract.TerminalSucceeded, *record.TerminalClass)
	}
	require.False(t, audits.store.Latched())
}
