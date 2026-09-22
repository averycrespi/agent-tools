package invocation

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrafficTerminalBatchSettlement(t *testing.T) {
	for _, scenario := range []string{"success", "invalid-member", "cancelled-member", "close", "mixed-success", "mixed-invalid-member", "mixed-cancelled-member", "mixed-close"} {
		t.Run(scenario, func(t *testing.T) {
			mixed := strings.HasPrefix(scenario, "mixed-")
			mode := strings.TrimPrefix(scenario, "mixed-")
			entered, release := make(chan struct{}), make(chan struct{})
			var armed atomic.Bool
			var begins atomic.Int32
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			s, _ := trafficFixture(t, func(c *TrafficConfig) { c.BatchRecords = 2 }, func(point string) error {
				if point == "before_begin" && armed.Load() && begins.Add(1) == 1 {
					close(entered)
					<-release
				}
				return nil
			})
			defer unblock()
			completion := trafficCompletion()
			completion.Class = contract.TerminalDownstreamFailure
			expected := []contract.FailureObservation{
				{Source: "protocol", Reason: "rpc_error"},
				{Source: "protocol", Reason: "invalid_response"},
				{Source: "tool", Reason: "reported_error"},
				{Source: "result_validation", Reason: "result_shape"},
			}
			diagnostics := make([]*contract.FailureDiagnostics, 4)
			receipts := make([]*TrafficReceipt, 4)
			for i := range receipts {
				var receipt *TrafficReceipt
				var err error
				if mixed && i%2 == 1 {
					receipt, err = s.AdmitHTTP(t.Context(), httpTrafficAdmission(i+1))
				} else {
					receipt, err = s.Admit(t.Context(), trafficPrepared(i+1))
				}
				require.NoError(t, err)
				require.True(t, s.Confirm(t.Context(), receipt))
				receipts[i] = receipt
				diagnostics[i] = &contract.FailureDiagnostics{GatewayObserved: expected[i]}
			}
			if mode == "invalid-member" {
				receipts[2].evidence.InvocationID = invocationID(50)
			}
			armed.Store(true)
			results := make([]chan error, 4)
			for i := range results {
				results[i] = make(chan error, 1)
			}
			finish := func(ctx context.Context, i int) error {
				if mixed && i%2 == 1 {
					return s.CompleteHTTP(ctx, receipts[i], httpTrafficCompletion())
				}
				return s.complete(ctx, receipts[i], completion, diagnostics[i])
			}
			go func() { results[0] <- finish(t.Context(), 0) }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("terminal writer did not start")
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			// Queue in a known order behind the owned transaction, not via timing luck.
			for i := 1; i < 4; i++ {
				go func(i int) { results[i] <- finish(ctx, i) }(i)
				require.Eventually(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.terminalQueued == i+1 }, time.Second, time.Millisecond)
			}
			// Encoding has completed before enqueue; caller mutation cannot alter the batch.
			for _, diagnostic := range diagnostics {
				diagnostic.GatewayObserved.Reason = "caller-mutation"
			}
			if mode == "cancelled-member" {
				cancel()
			}
			closed := make(chan error, 1)
			if mode == "close" {
				go func() { closed <- s.Close() }()
				require.Eventually(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.closed }, time.Second, time.Millisecond)
			}
			for _, result := range results {
				select {
				case <-result:
					t.Fatal("abandoned unsettled writer")
				default:
				}
			}
			unblock()
			for i, result := range results {
				select {
				case err := <-result:
					switch {
					case i == 0 || mode == "success":
						require.NoError(t, err)
					case mode == "cancelled-member":
						require.ErrorIs(t, err, ErrTrafficDeadline)
					default:
						require.ErrorIs(t, err, ErrTrafficFault)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("completion did not settle")
				}
			}
			if mode == "close" {
				require.NoError(t, <-closed)
			} else {
				rows, err := s.History(t.Context(), 0, 10)
				require.NoError(t, err)
				expectedMCP := 4
				if mixed {
					expectedMCP = 2
				}
				require.Len(t, rows.Records, expectedMCP)
				completed := 0
				if mixed {
					httpRows, readErr := s.HTTPHistory(t.Context(), 0, 10)
					require.NoError(t, readErr)
					require.Len(t, httpRows.Records, 2)
					for _, row := range httpRows.Records {
						if row.Completion != nil {
							completed++
							assert.Equal(t, "succeeded", row.Completion.Outcome)
							assert.Equal(t, 200, row.Completion.Status)
						}
					}
				}
				for _, row := range rows.Records {
					if row.CompletedAt != nil {
						completed++
						require.NotNil(t, row.Diagnostics)
						for i := range expected {
							if row.InvocationID == invocationID(i+1) {
								assert.Equal(t, expected[i], row.Diagnostics.GatewayObserved)
							}
						}
					} else {
						assert.Nil(t, row.Diagnostics, "rollback/cancellation must not annotate diagnostics")
					}
				}
				want := 1
				if mode == "success" {
					want = 4
					assert.EqualValues(t, 3, begins.Load(), "one blocked + two bounded terminal transactions")
				}
				assert.Equal(t, want, completed, "invalid batch cannot partly annotate or replay")
			}
			s.mu.Lock()
			assert.Empty(t, s.pins)
			assert.Zero(t, s.terminalQueued)
			s.mu.Unlock()
		})
	}
}

func TestTrafficTerminalGatherDoesNotDwellOrExceedBounds(t *testing.T) {
	s := &TrafficStore{config: DefaultTrafficConfig(), terminals: make(chan *trafficRequest, 4)}
	first := &trafficRequest{bytes: maxTrafficCompletionBytes}
	require.Len(t, s.gatherTerminals(first), 1, "empty queue returns without a timer or wait")
	for range 4 {
		s.terminals <- &trafficRequest{bytes: maxTrafficCompletionBytes}
	}
	s.config.BatchRecords = 2
	require.Len(t, s.gatherTerminals(first), 2)
	require.Len(t, s.terminals, 3)
	s.config.BatchRecords = 4
	s.config.BatchBytes = 2*maxTrafficCompletionBytes + 128
	require.Len(t, s.gatherTerminals(first), 2)
	require.Len(t, s.terminals, 2)
}
