package invocation

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrafficBatchAtomicityCollisionAndRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[fail], func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var mu sync.Mutex
			commits := 0
			s, _ := trafficFixture(t, func(c *TrafficConfig) { c.Dwell = 10 * time.Millisecond; c.QueueRecords = 8; c.BatchRecords = 4 }, func(point string) error {
				if point == "before_begin" {
					mu.Lock()
					commits++
					n := commits
					mu.Unlock()
					if n == 1 {
						close(entered)
						<-release
					}
				}
				if point == "statement" && fail {
					mu.Lock()
					n := commits
					mu.Unlock()
					if n == 2 {
						return errors.New("batch statement failed")
					}
				}
				return nil
			})
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
			})
			results := make(chan trafficResult, 5)
			submit := func(id int) {
				go func() { r, e := s.Admit(context.Background(), trafficPrepared(id)); results <- trafficResult{r, e} }()
			}
			submit(1)
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("writer not entered")
			}
			for _, id := range []int{2, 3, 4, 5} {
				submit(id)
			}
			require.Eventually(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.queued == 5 }, time.Second, time.Millisecond)
			close(release)
			successes := 0
			for range 5 {
				select {
				case r := <-results:
					if r.err == nil {
						successes++
						s.Release(r.receipt)
					} else {
						assert.ErrorIs(t, r.err, ErrTrafficFault)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("unsettled batch")
				}
			}
			if fail {
				assert.Equal(t, 1, successes)
			} else {
				assert.Equal(t, 5, successes)
			}
			mu.Lock()
			assert.Equal(t, 2, commits)
			mu.Unlock()
			history, err := s.History(context.Background(), 0, 10)
			require.NoError(t, err)
			assert.Len(t, history.Records, successes)
		})
	}
}

func TestTrafficRefusalRollbackUncertaintyFencesReceipts(t *testing.T) {
	for _, refusal := range []string{"collision", "capacity"} {
		t.Run(refusal, func(t *testing.T) {
			s, _ := trafficFixture(t, func(c *TrafficConfig) {
				c.RetainedRecords = 1
				c.BatchRecords = 1
			}, nil)
			receipt, err := s.Admit(context.Background(), trafficPrepared(1))
			require.NoError(t, err)
			s.fault = func(point string) error {
				if point == "rollback" {
					return errors.New("rollback settlement uncertain")
				}
				return nil
			}
			id := 1
			if refusal == "capacity" {
				id = 2
			}
			rejected, err := s.Admit(context.Background(), trafficPrepared(id))
			require.ErrorIs(t, err, ErrTrafficFault)
			assert.Nil(t, rejected)
			assert.False(t, s.Healthy())
			assert.False(t, s.Confirm(context.Background(), receipt))
			s.Release(receipt)
			rejected, err = s.Admit(context.Background(), trafficPrepared(3))
			assert.ErrorIs(t, err, ErrTrafficFault)
			assert.Nil(t, rejected)
		})
	}
}

func TestTrafficQueueBoundsAndProtectedCompletion(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var block bool
	var mu sync.Mutex
	s, _ := trafficFixture(t, func(c *TrafficConfig) {
		c.QueueRecords = 2
		c.BatchRecords = 1
		c.ActiveRecords = 8
		c.QueueLifetime = time.Second
	}, func(point string) error {
		mu.Lock()
		active := block
		mu.Unlock()
		if active && point == "before_begin" {
			once.Do(func() { close(entered); <-release })
		}
		return nil
	})
	r, err := s.Admit(context.Background(), trafficPrepared(1))
	require.NoError(t, err)
	require.True(t, s.Confirm(context.Background(), r))
	mu.Lock()
	block = true
	mu.Unlock()
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	results := make(chan trafficResult, 2)
	go func() { r, e := s.Admit(context.Background(), trafficPrepared(2)); results <- trafficResult{r, e} }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("writer not entered")
	}
	go func() { r, e := s.Admit(context.Background(), trafficPrepared(3)); results <- trafficResult{r, e} }()
	require.Eventually(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.queued == 2 }, time.Second, time.Millisecond)
	noReceipt, err := s.Admit(context.Background(), trafficPrepared(4))
	assert.ErrorIs(t, err, ErrTrafficCapacity)
	assert.Nil(t, noReceipt)
	completed := make(chan error, 1)
	go func() { completed <- s.Complete(context.Background(), r, trafficCompletion()) }()
	require.Eventually(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.terminalQueued == 1 }, time.Second, time.Millisecond)
	close(release)
	require.NoError(t, <-completed)
	for range 2 {
		got := <-results
		require.NoError(t, got.err)
		s.Release(got.receipt)
	}
	h, err := s.History(context.Background(), 0, 10)
	require.NoError(t, err)
	require.Len(t, h.Records, 3)
	require.NotNil(t, h.Records[0].CompletedAt)
	s.mu.Lock()
	assert.Zero(t, s.queued)
	assert.Zero(t, s.queuedBytes)
	assert.Zero(t, s.terminalQueued)
	assert.Empty(t, s.pins)
	s.mu.Unlock()
}

func TestTrafficCancellationAfterReceiptCannotConfirm(t *testing.T) {
	s, _ := trafficFixture(t, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, err := s.Admit(ctx, trafficPrepared(1))
	require.NoError(t, err)
	cancel()
	assert.False(t, s.Confirm(context.Background(), r))
	s.Release(r)
	assert.False(t, s.Confirm(context.Background(), &TrafficReceipt{}))
}

func TestTrafficPinnedCapacityAndTerminalFault(t *testing.T) {
	s, _ := trafficFixture(t, func(c *TrafficConfig) { c.RetainedRecords = 1; c.BatchRecords = 1 }, nil)
	r, err := s.Admit(context.Background(), trafficPrepared(1))
	require.NoError(t, err)
	_, err = s.Admit(context.Background(), trafficPrepared(2))
	assert.ErrorIs(t, err, ErrTrafficCapacity)
	assert.True(t, s.Healthy())
	require.True(t, s.Confirm(context.Background(), r))
	s.Release(r) // A live invocation cannot be prematurely unpinned.
	_, err = s.Admit(context.Background(), trafficPrepared(2))
	assert.ErrorIs(t, err, ErrTrafficCapacity)
	s.fault = func(point string) error {
		if point == "statement" {
			return errors.New("terminal I/O failure")
		}
		return nil
	}
	assert.ErrorIs(t, s.Complete(context.Background(), r, trafficCompletion()), ErrTrafficFault)
	assert.False(t, s.Healthy())
	assert.Empty(t, s.pins)
	h, err := s.History(context.Background(), 0, 10)
	require.NoError(t, err)
	assert.Nil(t, h.Records[0].CompletedAt)
}

func TestTrafficPhysicalBudgetWALReaderPressure(t *testing.T) {
	s, _ := trafficFixture(t, func(c *TrafficConfig) { c.BudgetBytes = 1 << 20; c.BatchRecords = 1; c.RetainedRecords = 8 }, nil)
	r, err := s.Admit(context.Background(), trafficPrepared(1))
	require.NoError(t, err)
	s.Release(r)
	// This independently held SQLite reader deliberately outlives the public
	// reader API. It proves checkpoint refusal, not permission to exceed budget.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.readerDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	var count int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM invocations`).Scan(&count))
	refused := false
	for id := 2; id < 100; id++ {
		r, e := s.Admit(context.Background(), trafficPrepared(id))
		var combined int64
		for _, suffix := range []string{"", "-wal"} {
			info, statErr := os.Stat(s.path + suffix)
			require.NoError(t, statErr)
			combined += info.Size()
		}
		assert.LessOrEqual(t, combined, s.config.BudgetBytes)
		if e != nil {
			require.ErrorIs(t, e, ErrTrafficCapacity)
			refused = true
			break
		}
		s.Release(r)
	}
	assert.True(t, refused)
	assert.True(t, s.Healthy())
	require.NoError(t, tx.Rollback())
	r, err = s.Admit(context.Background(), trafficPrepared(101))
	require.NoError(t, err)
	s.Release(r)
	h, err := s.History(context.Background(), 0, 10)
	require.NoError(t, err)
	assert.Len(t, h.Records, 8)
	assert.Positive(t, h.Pruning)
	for range s.config.Readers {
		s.readSlots <- struct{}{}
	}
	_, err = s.History(context.Background(), 0, 10)
	assert.ErrorIs(t, err, ErrTrafficCapacity)
	for range s.config.Readers {
		<-s.readSlots
	}
}

func TestTrafficSQLiteFullFaultsWithoutReceipt(t *testing.T) {
	s, _ := trafficFixture(t, func(c *TrafficConfig) { c.BatchRecords = 1 }, nil)
	var pages int64
	require.NoError(t, s.db.QueryRowContext(context.Background(), `PRAGMA page_count`).Scan(&pages))
	var maximum int64
	require.NoError(t, s.db.QueryRowContext(context.Background(), `PRAGMA max_page_count=`+strconv.FormatInt(pages, 10)).Scan(&maximum))
	require.Equal(t, pages, maximum)
	prepared := trafficPrepared(1)
	prepared.admission.MCP.RedactedArguments = []byte(`{"value":"` + strings.Repeat("x", 8100) + `"}`)
	r, err := s.Admit(context.Background(), prepared)
	require.ErrorIs(t, err, ErrTrafficFault)
	assert.Nil(t, r)
	assert.False(t, s.Healthy())
	h, err := s.History(context.Background(), 0, 10)
	require.NoError(t, err)
	assert.Empty(t, h.Records)
}
