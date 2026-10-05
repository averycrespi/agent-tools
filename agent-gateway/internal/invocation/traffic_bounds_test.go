package invocation

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTrafficMixedBatchAtomicity(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(strconv.FormatBool(fail), func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			var begins atomic.Int32
			s, _ := trafficFixture(t, func(c *TrafficConfig) { c.Dwell = 10 * time.Millisecond; c.QueueRecords = 8; c.BatchRecords = 4 }, func(point string) error {
				if point == "before_begin" && begins.Add(1) == 1 {
					close(entered)
					<-release
				}
				if point == "statement" && fail && begins.Load() == 2 {
					return errors.New("batch failure")
				}
				return nil
			})
			require.NotNil(t, s.ObserveMCP(trafficPrepared(1)))
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("writer did not enter")
			}
			require.NotNil(t, s.ObserveMCP(trafficPrepared(2)))
			require.NotNil(t, s.ObserveHTTP(httpTrafficAdmission(3)))
			require.NotNil(t, s.ObserveGit(gitTrafficAdmission(4)))
			require.NotNil(t, s.ObserveMCP(trafficPrepared(5)))
			unblock()
			waitTraffic(t, s)
			m, err := s.History(t.Context(), 0, 10)
			require.NoError(t, err)
			h, err := s.HTTPHistory(t.Context(), 0, 10)
			require.NoError(t, err)
			g, err := s.GitHistory(t.Context(), 0, 10)
			require.NoError(t, err)
			require.EqualValues(t, 2, begins.Load())
			if fail {
				require.Len(t, m.Records, 1)
				require.Empty(t, h.Records)
				require.Empty(t, g.Records)
				require.False(t, s.Healthy())
			} else {
				require.Len(t, m.Records, 3)
				require.Len(t, h.Records, 1)
				require.Len(t, g.Records, 1)
			}
		})
	}
}

func TestTrafficSingleQueueBoundsInitialAndTerminal(t *testing.T) {
	for _, bound := range []string{"count", "bytes"} {
		t.Run(bound, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			var once sync.Once
			s, _ := trafficFixture(t, func(c *TrafficConfig) {
				c.BatchRecords = 1
				c.QueueLifetime = time.Second
				if bound == "count" {
					c.QueueRecords = 1
				} else {
					c.QueueRecords = 8
					c.QueueBytes = 16384
					c.BatchBytes = 16384
				}
			}, func(point string) error {
				if point == "before_begin" {
					once.Do(func() { close(entered); <-release })
				}
				return nil
			})
			p := trafficPrepared(1)
			if bound == "bytes" {
				p.admission.MCP.RedactedArguments = []byte(`{"value":"` + strings.Repeat("x", 7000) + `"}`)
			}
			o := s.ObserveMCP(p)
			require.NotNil(t, o)
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("writer did not enter")
			}
			require.ErrorIs(t, s.ObserveMCPCompletion(o, trafficCompletion(), nil), ErrTrafficCapacity)
			require.NotNil(t, s.ObserveMCP(p), "queue refusal loses capture, not the snapshot")
			s.mu.Lock()
			require.Equal(t, 1, s.queued)
			require.LessOrEqual(t, s.queuedBytes, s.config.QueueBytes)
			require.EqualValues(t, 2, s.quotaRefusals)
			s.mu.Unlock()
			health := s.Status(t.Context())
			require.EqualValues(t, 1, health.Delivery.Accepted)
			require.EqualValues(t, 2, health.Delivery.Discarded)
			require.Equal(t, 1, health.Delivery.QueueRecords)
			require.Positive(t, health.Delivery.QueueBytes)
			unblock()
			waitTraffic(t, s)
			history, err := s.History(t.Context(), 0, 10)
			require.NoError(t, err)
			require.Len(t, history.Records, 1)
			require.Nil(t, history.Records[0].TerminalClass)
			require.True(t, s.Healthy())
		})
	}
}

func TestTrafficCollisionPrecedesPruningAndUncertainRollbackFaults(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(strconv.FormatBool(uncertain), func(t *testing.T) {
			s, _ := trafficFixture(t, func(c *TrafficConfig) { c.RetainedRecords = 1; c.BatchRecords = 1 }, nil)
			recordMCP(t, s, trafficPrepared(1))
			if uncertain {
				s.fault = func(point string) error {
					if point == "rollback" {
						return errors.New("uncertain rollback")
					}
					return nil
				}
			}
			collision := trafficPrepared(1)
			collision.admission.MCP.RedactedArguments = []byte(`{"different":true}`)
			require.NotNil(t, s.ObserveMCP(collision))
			waitTraffic(t, s)
			require.Equal(t, !uncertain, s.Healthy())
			h, err := s.History(t.Context(), 0, 10)
			require.NoError(t, err)
			require.Len(t, h.Records, 1)
			require.Zero(t, h.Pruning)
			require.Equal(t, `{"value":1e0}`, *h.Records[0].RedactedArguments)
		})
	}
}

func TestTrafficPrunedInitialCanBeReplacedByTerminalSnapshot(t *testing.T) {
	s, _ := trafficFixture(t, func(c *TrafficConfig) { c.RetainedRecords = 1; c.BatchRecords = 1 }, nil)
	original := recordMCP(t, s, trafficPrepared(1))
	recordMCP(t, s, trafficPrepared(2))
	recordMCPCompletion(t, s, original, trafficCompletion())
	h, err := s.History(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, h.Records, 1)
	require.Equal(t, invocationID(1), h.Records[0].InvocationID)
	require.NotNil(t, h.Records[0].TerminalClass)
	require.EqualValues(t, 2, h.Pruning)
}

func TestTrafficPhysicalBudgetWALReaderPressure(t *testing.T) {
	s, _ := trafficFixture(t, func(c *TrafficConfig) { c.BudgetBytes = 1 << 20; c.BatchRecords = 1; c.RetainedRecords = 8 }, nil)
	recordMCP(t, s, trafficPrepared(1))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	tx, err := s.readerDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	var count int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM invocations`).Scan(&count))
	refused := false
	for id := 2; id < 100; id++ {
		require.NotNil(t, s.ObserveMCP(trafficPrepared(id)))
		waitTraffic(t, s)
		var combined int64
		for _, suffix := range []string{"", "-wal"} {
			info, err := os.Stat(s.path + suffix)
			require.NoError(t, err)
			combined += info.Size()
		}
		require.LessOrEqual(t, combined, s.config.BudgetBytes)
		s.mu.Lock()
		refused = s.quotaRefusals > 0
		s.mu.Unlock()
		if refused {
			break
		}
	}
	require.True(t, refused)
	require.True(t, s.Healthy())
	require.NoError(t, tx.Rollback())
	recordMCP(t, s, trafficPrepared(101))
	h, err := s.History(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, h.Records, 8)
	require.Positive(t, h.Pruning)
	for range s.config.Readers {
		s.readSlots <- struct{}{}
	}
	_, err = s.History(t.Context(), 0, 10)
	require.ErrorIs(t, err, ErrTrafficCapacity)
	for range s.config.Readers {
		<-s.readSlots
	}
}

func TestTrafficSQLiteFullFaultsOnlyOptionalHistory(t *testing.T) {
	s, _ := trafficFixture(t, func(c *TrafficConfig) { c.BatchRecords = 1 }, nil)
	var pages, maximum int64
	require.NoError(t, s.db.QueryRowContext(t.Context(), `PRAGMA page_count`).Scan(&pages))
	require.NoError(t, s.db.QueryRowContext(t.Context(), `PRAGMA max_page_count=`+strconv.FormatInt(pages, 10)).Scan(&maximum))
	require.Equal(t, pages, maximum)
	p := trafficPrepared(1)
	p.admission.MCP.RedactedArguments = []byte(`{"value":"` + strings.Repeat("x", 8100) + `"}`)
	require.NotNil(t, s.ObserveMCP(p))
	waitTraffic(t, s)
	require.False(t, s.Healthy())
	h, err := s.History(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Empty(t, h.Records)
}
