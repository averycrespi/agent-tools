package invocation

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrafficTerminalBatchSettlement(t *testing.T) {
	for _, mode := range []string{"success", "invalid-member", "cancelled-member", "close"} {
		t.Run(mode, func(t *testing.T) {
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
			receipts := make([]*TrafficReceipt, 4)
			for i := range receipts {
				receipt, err := s.Admit(t.Context(), trafficPrepared(i+1))
				require.NoError(t, err)
				require.True(t, s.Confirm(t.Context(), receipt))
				receipts[i] = receipt
			}
			if mode == "invalid-member" {
				receipts[2].evidence.InvocationID = invocationID(50)
			}
			armed.Store(true)
			results := make([]chan error, 4)
			for i := range results {
				results[i] = make(chan error, 1)
			}
			go func() { results[0] <- s.Complete(t.Context(), receipts[0], trafficCompletion()) }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("terminal writer did not start")
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			// Queue in a known order behind the owned transaction, not via timing luck.
			for i := 1; i < 4; i++ {
				go func(i int) { results[i] <- s.Complete(ctx, receipts[i], trafficCompletion()) }(i)
				require.Eventually(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.terminalQueued == i+1 }, time.Second, time.Millisecond)
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
				require.Len(t, rows.Records, 4)
				completed := 0
				for _, row := range rows.Records {
					if row.CompletedAt != nil {
						completed++
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
	first := &trafficRequest{bytes: 128}
	require.Len(t, s.gatherTerminals(first), 1, "empty queue returns without a timer or wait")
	for range 4 {
		s.terminals <- &trafficRequest{bytes: 128}
	}
	s.config.BatchRecords = 2
	require.Len(t, s.gatherTerminals(first), 2)
	require.Len(t, s.terminals, 3)
	s.config.BatchRecords = 4
	s.config.BatchBytes = 256
	require.Len(t, s.gatherTerminals(first), 2)
	require.Len(t, s.terminals, 2)
}
