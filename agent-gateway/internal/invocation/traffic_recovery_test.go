package invocation

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"sync"

	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"

	"github.com/ncruces/go-sqlite3"
	"github.com/stretchr/testify/require"
)

func TestTrafficReadDomainOutcomesDoNotFaultRecording(t *testing.T) {
	s, _ := trafficFixture(t, nil, nil)
	for _, outcome := range []error{sql.ErrNoRows, ErrNotFound, ErrInvalidCursor, ErrStaleCursor, ErrInvalidInput} {
		err := s.view(t.Context(), func(context.Context, *sql.Tx) error { return outcome })
		require.ErrorIs(t, err, outcome)
		require.True(t, s.Healthy())
		require.Nil(t, s.Status(t.Context()).Incident)
	}
	recordMCP(t, s, trafficPrepared(1))
	require.EqualValues(t, 1, s.Status(t.Context()).Delivery.Acknowledged)
}

func TestTrafficReadStorageFailureUsesSameRecovery(t *testing.T) {
	s, _ := trafficFixture(t, nil, nil)
	err := s.view(t.Context(), func(context.Context, *sql.Tx) error { return sqlite3.BUSY })
	require.ErrorIs(t, err, sqlite3.BUSY)
	require.Equal(t, "read", s.Status(t.Context()).Incident.Stage)
	s.recoverTraffic(time.Now().Add(time.Minute))
	recordMCP(t, s, trafficPrepared(1))
	require.Equal(t, "recovered", s.Status(t.Context()).Health)
}

func TestTrafficDeadlineAfterPreflightDoesNotLatch(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s, _ := trafficFixture(t, nil, func(point string) error {
		if point == "before_begin" {
			cancel()
		}
		return nil
	})
	s.writerGate.Lock()
	_, err := s.writeTraffic(ctx, nil)
	require.ErrorIs(t, err, context.Canceled)
	s.failTraffic(err, "reservation", "not_started")
	s.writerGate.Unlock()
	s.recoverTraffic(time.Now().Add(time.Minute))
	recordMCP(t, s, trafficPrepared(2))
	h, err := s.History(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, h.Records, 1)
	require.EqualValues(t, 1, s.Status(t.Context()).Delivery.Acknowledged)
}

func TestTrafficUncertainSettlementRevalidatesWithoutReplay(t *testing.T) {
	for _, point := range []string{"commit", "rollback", "acknowledgment"} {
		t.Run(point, func(t *testing.T) {
			var failed atomic.Bool
			s, _ := trafficFixture(t, nil, func(at string) error {
				if point == "rollback" && at == "statement" && !failed.Load() {
					return sqlite3.BUSY
				}
				if at == point && failed.CompareAndSwap(false, true) {
					return errors.New("SECRET I/O uncertainty")
				}
				return nil
			})
			require.NotNil(t, s.ObserveMCP(trafficPrepared(1)))
			waitTraffic(t, s)
			first := s.Status(t.Context())
			require.Equal(t, "uncertain", first.Incident.Settlement)
			require.EqualValues(t, 0, first.Delivery.Acknowledged)
			s.recoverTraffic(time.Now().Add(time.Minute))
			require.Equal(t, "degraded", s.Status(t.Context()).Health)
			recordMCP(t, s, trafficPrepared(2))
			status := s.Status(t.Context())
			require.Equal(t, "recovered", status.Health)
			require.Equal(t, first.Incident.FirstFailure, status.Incident.FirstFailure)
			require.EqualValues(t, 1, status.Delivery.Acknowledged)
			require.EqualValues(t, 1, status.Incident.Discarded)
			require.NotEmpty(t, status.LastAcknowledged)
			h, err := s.History(t.Context(), 0, 10)
			require.NoError(t, err)
			expected := 1
			if point == "acknowledgment" {
				expected = 2
			}
			require.Len(t, h.Records, expected)
			require.Equal(t, invocationID(2), h.Records[expected-1].InvocationID)
		})
	}
}

func TestTrafficRecoveryRetainsActualOwner(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	var once sync.Once
	var validations atomic.Int32
	s, _ := trafficFixture(t, nil, func(point string) error {
		if point == "acknowledgment" {
			once.Do(func() { close(entered); <-release })
			return errors.New("uncertain")
		}
		if point == "revalidation" {
			validations.Add(1)
		}
		return nil
	})
	t.Cleanup(unblock)
	require.NotNil(t, s.ObserveMCP(trafficPrepared(1)))
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("writer not entered")
	}
	require.False(t, s.writerGate.TryLock(), "an unsettled writer cannot be replaced")
	require.Zero(t, validations.Load())
	h, err := s.History(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, h.Records, 1, "readable commit is not acknowledgment")
	require.Zero(t, s.Status(t.Context()).Delivery.Acknowledged)
	unblock()
	waitTraffic(t, s)
	s.recoverTraffic(time.Now().Add(time.Minute))
	require.EqualValues(t, 1, validations.Load())
}

func TestTrafficRecoveryBackoffAndPersistentValidation(t *testing.T) {
	var validations atomic.Int32
	var invalid atomic.Bool
	s, _ := trafficFixture(t, nil, func(point string) error {
		if point == "before_begin" {
			return sqlite3.BUSY
		}
		if point == "revalidation" {
			validations.Add(1)
			if invalid.Load() {
				return ErrInvalidState
			}
			return sqlite3.BUSY
		}
		return nil
	})
	s.ObserveMCP(trafficPrepared(1))
	waitTraffic(t, s)
	first := s.Status(t.Context()).Incident
	for range 6 {
		s.recoverTraffic(time.Now().Add(time.Minute))
	}
	require.EqualValues(t, 6, validations.Load())
	require.Equal(t, 30*time.Second, s.recoveryDelay)
	s.recoverTraffic(time.Now())
	require.EqualValues(t, 6, validations.Load(), "backoff prevents busy validation")
	invalid.Store(true)
	s.recoverTraffic(time.Now().Add(time.Minute))
	require.Equal(t, "operator_action_required", s.Status(t.Context()).Health)
	require.Equal(t, first.Cause, s.Status(t.Context()).Incident.Cause, "preserve initiating cause")
	s.recoverTraffic(time.Now().Add(time.Hour))
	require.EqualValues(t, 7, validations.Load(), "time alone cannot clear integrity faults")
}

func TestTrafficRecoveryIgnoresDiagnosticSinkFailure(t *testing.T) {
	var sink bytes.Buffer
	adapter := diagnostics.New(&sink, diagnostics.Warn)
	var failed atomic.Bool
	s, _ := trafficFixture(t, nil, func(point string) error {
		if point == "statement" && failed.CompareAndSwap(false, true) {
			return sqlite3.BUSY
		}
		return nil
	})
	s.SetTrafficDiagnostics(adapter)
	s.ObserveMCP(trafficPrepared(1))
	waitTraffic(t, s)
	s.recoverTraffic(time.Now().Add(time.Minute))
	recordMCP(t, s, trafficPrepared(2))
	require.True(t, adapter.Finish(nil))
	<-adapter.Done()
	require.Contains(t, sink.String(), "traffic_failure")
	require.Contains(t, sink.String(), "traffic_recovered")
	require.Contains(t, sink.String(), `"sqlite_code":5`)
	require.NotContains(t, sink.String(), s.path)
	// A permanently failed adapter is not a recovery gate.
	broken := diagnostics.New(trafficBrokenSink{}, diagnostics.Warn)
	s.SetTrafficDiagnostics(broken)
	s.failTraffic(classifyTraffic(sqlite3.BUSY, "begin", "not_started"), "begin", "not_started")
	require.Eventually(t, func() bool { return broken.Status().State == "failed" }, time.Second, time.Millisecond)
	s.recoverTraffic(time.Now().Add(time.Minute))
	recordMCP(t, s, trafficPrepared(3))
	require.False(t, broken.Finish(nil))
	<-broken.Done()
	require.Equal(t, "recovered", s.Status(t.Context()).Health)
}

func TestTrafficRecoveryDoesNotWaitForBlockedFullDiagnostics(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	adapter := diagnostics.New(&trafficBlockedSink{entered: entered, release: release}, diagnostics.Warn)
	defer func() { unblock(); require.True(t, adapter.Finish(nil)); <-adapter.Done() }()
	var failed atomic.Bool
	s, _ := trafficFixture(t, nil, func(point string) error {
		if point == "statement" && failed.CompareAndSwap(false, true) {
			return sqlite3.BUSY
		}
		return nil
	})
	s.SetTrafficDiagnostics(adapter)
	s.ObserveMCP(trafficPrepared(1))
	waitTraffic(t, s)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("sink did not enter")
	}
	for range 300 {
		adapter.Observe(diagnostics.Facts{Event: diagnostics.LifecycleFailure, Cause: diagnostics.Unavailable})
	}
	require.Positive(t, adapter.Status().Dropped)
	require.NotNil(t, s.Status(t.Context()).Incident)
	s.recoverTraffic(time.Now().Add(time.Minute))
	recordMCP(t, s, trafficPrepared(2))
	require.Equal(t, "recovered", s.Status(t.Context()).Health)
	require.EqualValues(t, 1, s.Status(t.Context()).Delivery.Acknowledged)
}

type trafficBlockedSink struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *trafficBlockedSink) Write(p []byte) (int, error) {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return len(p), nil
}

type trafficBrokenSink struct{}

func (trafficBrokenSink) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestTrafficTransientFailureAllowsIndependentObservation(t *testing.T) {
	for _, point := range []string{"before_begin", "statement"} {
		for _, cause := range []error{context.DeadlineExceeded, sqlite3.BUSY} {
			t.Run(point+"/"+cause.Error(), func(t *testing.T) {
				var failed atomic.Bool
				s, _ := trafficFixture(t, nil, func(at string) error {
					if at == point && failed.CompareAndSwap(false, true) {
						return cause
					}
					return nil
				})
				require.NotNil(t, s.ObserveMCP(trafficPrepared(1)))
				waitTraffic(t, s)
				s.recoverTraffic(time.Now().Add(time.Minute))
				require.NotNil(t, s.ObserveMCP(trafficPrepared(2)))
				waitTraffic(t, s)
				h, err := s.History(t.Context(), 0, 10)
				require.NoError(t, err)
				require.Len(t, h.Records, 1)
				require.Equal(t, invocationID(2), h.Records[0].InvocationID)
				require.EqualValues(t, 1, s.Status(t.Context()).Delivery.Acknowledged)
			})
		}
	}
}
