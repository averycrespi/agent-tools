package invocation

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrafficQueueExpiryDropsOnlyObservations(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	s, _ := trafficFixture(t, func(c *TrafficConfig) {
		c.BatchRecords = 1
		c.QueueRecords = 8
		c.QueueBytes = 16384
		c.BatchBytes = 16384
	}, func(point string) error {
		if point == "before_begin" {
			once.Do(func() { close(entered); <-release })
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
	capture := captureLossDiagnostics(t, s)
	big := trafficPrepared(1)
	big.admission.MCP.RedactedArguments = []byte(`{"value":"` + strings.Repeat("x", 8100) + `"}`)
	require.NotNil(t, s.ObserveMCP(big))
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("writer not entered")
	}
	another := big
	another.InvocationID = invocationID(2)
	require.NotNil(t, s.ObserveMCP(another))
	require.NotNil(t, s.ObserveMCP(trafficPrepared(3)))
	// Deterministically expire the queued observation, not an execution context.
	queued := <-s.observations
	queued.expires = time.Now().Add(-time.Second)
	s.observations <- queued
	close(release)
	waitTraffic(t, s)
	history, err := s.History(context.Background(), 0, 10)
	require.NoError(t, err)
	assert.Len(t, history.Records, 1)
	assert.True(t, s.Healthy())
	require.Contains(t, capture(), "reason=queue_expired initial_submissions=1 terminal_submissions=0")
}

func TestTrafficRestartValidatesEveryRowNotOnlyStructure(t *testing.T) {
	s, owner := trafficFixture(t, nil, nil)
	for id := 1; id <= 10; id++ {
		recordMCP(t, s, trafficPrepared(id))
	}
	var trigger string
	require.NoError(t, s.db.QueryRowContext(context.Background(), `SELECT sql FROM sqlite_schema WHERE name='invocations_terminal_once'`).Scan(&trigger))
	_, err := s.db.ExecContext(context.Background(), `DROP TRIGGER invocations_terminal_once; UPDATE invocations SET evaluated_at='1970-01-01T00:00:00.000000000Z' WHERE insertion_sequence=10`)
	require.NoError(t, err)
	_, err = s.db.ExecContext(context.Background(), trigger)
	require.NoError(t, err)
	var integrity string
	require.NoError(t, s.db.QueryRowContext(context.Background(), `PRAGMA integrity_check`).Scan(&integrity))
	require.Equal(t, "ok", integrity)
	require.NoError(t, s.Close())
	reopened, err := OpenTraffic(context.Background(), owner, invocationTestInstallationID, invocationID(90), s.config)
	assert.ErrorIs(t, err, ErrInvalidState)
	assert.Nil(t, reopened)
}

func TestTrafficFailureDiagnosticsReadAndRestartValidation(t *testing.T) {
	for _, test := range []struct {
		name, diagnostic string
		valid            bool
	}{
		{"valid", `{"gateway_observed":{"source":"protocol","reason":"rpc_error"}}`, true},
		{"terminal mismatch", `{"gateway_observed":{"source":"transport","reason":"prestart"}}`, false},
		{"unknown member", `{"gateway_observed":{"source":"protocol","reason":"rpc_error"},"raw":"untrusted"}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, owner := trafficFixture(t, nil, nil)
			recordMCP(t, s, trafficPrepared(1))
			// Inject retained evidence directly to test invalid combinations that
			// the production completion boundary refuses before queueing.
			_, err := s.db.ExecContext(t.Context(), `UPDATE invocations SET completed_at=?,terminal_class=?,failure_diagnostics=? WHERE id=?`, trafficCompletion().CompletedAt, string(contract.TerminalDownstreamFailure), test.diagnostic, invocationID(1))
			require.NoError(t, err)
			history, err := s.History(t.Context(), 0, 10)
			if test.valid {
				require.NoError(t, err)
				require.Len(t, history.Records, 1)
				require.NotNil(t, history.Records[0].Diagnostics)
				assert.Equal(t, contract.FailureObservation{Source: "protocol", Reason: "rpc_error"}, history.Records[0].Diagnostics.GatewayObserved)
			} else {
				require.ErrorIs(t, err, ErrInvalidState)
				require.Empty(t, history.Records)
			}
			require.NoError(t, s.Close())
			reopened, err := OpenTraffic(t.Context(), owner, invocationTestInstallationID, invocationID(90), s.config)
			if test.valid {
				require.NoError(t, err)
				defer func() { require.NoError(t, reopened.Close()) }()
				retained, readErr := reopened.History(t.Context(), 0, 10)
				require.NoError(t, readErr)
				assert.Equal(t, history, retained)
			} else {
				require.ErrorIs(t, err, ErrInvalidState)
				require.Nil(t, reopened)
			}
		})
	}
}

func TestTrafficAcceptsPrivateDirectorySidecarsAcrossReopen(t *testing.T) {
	s, owner := trafficFixture(t, nil, nil)
	// Sidecars belong to active SQLite handles, not an idle store facade.
	first, err := s.db.Conn(t.Context())
	require.NoError(t, err)
	defer func() { _ = first.Close() }()
	_, err = first.ExecContext(t.Context(), `UPDATE traffic_meta SET generation=generation`)
	require.NoError(t, err)
	for _, suffix := range []string{"-wal", "-shm"} {
		require.NoError(t, os.Chmod(s.path+suffix, 0644))
	}
	require.NoError(t, trafficFiles(s.path, s.config))
	require.NoError(t, first.Close())
	require.NoError(t, s.Close())
	// SQLite may remove sidecars on close. Retained empty files model restart
	// without changing the selected database or broadening database permissions.
	for _, suffix := range []string{"-wal", "-shm"} {
		require.NoError(t, os.WriteFile(s.path+suffix, nil, 0600))
		require.NoError(t, os.Chmod(s.path+suffix, 0644))
	}
	reopened, err := OpenTraffic(t.Context(), owner, invocationTestInstallationID, invocationID(90), s.config)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	second, err := reopened.db.Conn(t.Context())
	require.NoError(t, err)
	defer func() { _ = second.Close() }()
	_, err = second.ExecContext(t.Context(), `UPDATE traffic_meta SET generation=generation`)
	require.NoError(t, err)
	require.NoError(t, trafficFiles(s.path, s.config))
	require.NoError(t, os.Chmod(s.path+"-wal", 0666))
	require.Error(t, trafficFiles(s.path, s.config))
	require.NoError(t, os.Chmod(s.path+"-wal", 0644))
}

func TestTrafficGenerationOwnershipBoundsAndPrivacy(t *testing.T) {
	s, owner := trafficFixture(t, nil, nil)
	_, err := CreateTraffic(context.Background(), owner, invocationTestInstallationID, invocationID(91), s.config)
	assert.ErrorIs(t, err, ErrTrafficCapacity)
	_, err = OpenTraffic(context.Background(), owner, invocationTestInstallationID, invocationID(90), s.config)
	assert.ErrorIs(t, err, ErrTrafficCapacity)
	require.NoError(t, s.Close())
	_, err = CreateTraffic(context.Background(), owner, invocationTestInstallationID, invocationID(90), s.config)
	assert.ErrorIs(t, err, ErrInvalidState)
	before, err := os.ReadFile(s.path)
	require.NoError(t, err)
	bad := s.config
	bad.BudgetBytes = 1
	_, err = OpenTraffic(context.Background(), owner, invocationTestInstallationID, invocationID(90), bad)
	assert.ErrorIs(t, err, ErrInvalidInput)
	after, err := os.ReadFile(s.path)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	require.NoError(t, os.Chmod(s.path, 0644))
	_, err = OpenTraffic(context.Background(), owner, invocationTestInstallationID, invocationID(90), s.config)
	assert.Error(t, err)
	// No schema registry, execution queue or successful-result field is added.
	for _, forbidden := range []string{"bearer", "dispatch", "retry", "result", "http", "raw_error"} {
		assert.NotContains(t, strings.ToLower(storage.TrafficSchema()), forbidden)
	}
}

func TestTrafficRestartPreservesIncompleteHistoryWithoutOwners(t *testing.T) {
	s, owner := trafficFixture(t, func(c *TrafficConfig) { c.RetainedRecords = 1; c.BatchRecords = 1 }, nil)
	recordMCP(t, s, trafficPrepared(1))
	require.NoError(t, s.Close())
	reopened, err := OpenTraffic(context.Background(), owner, invocationTestInstallationID, invocationID(90), s.config)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	h, err := reopened.History(context.Background(), 0, 10)
	require.NoError(t, err)
	require.Len(t, h.Records, 1)
	assert.Nil(t, h.Records[0].CompletedAt)
	recordMCP(t, reopened, trafficPrepared(2))
	h, err = reopened.History(context.Background(), 0, 10)
	require.NoError(t, err)
	require.Len(t, h.Records, 1)
	assert.Equal(t, int64(2), h.HighWater)
}

func TestTrafficBatchCollisionPrecedesEviction(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	s, _ := trafficFixture(t, func(c *TrafficConfig) { c.RetainedRecords = 2; c.BatchRecords = 2; c.Dwell = 10 * time.Millisecond }, func(point string) error {
		if point == "before_begin" {
			once.Do(func() { close(entered); <-release })
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
	require.NotNil(t, s.ObserveMCP(trafficPrepared(1)))
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("writer not entered")
	}
	require.NotNil(t, s.ObserveMCP(trafficPrepared(2)))
	collision := trafficPrepared(2)
	collision.admission.MCP.RedactedArguments = []byte(`{"different":true}`)
	require.NotNil(t, s.ObserveMCP(collision))
	require.Eventually(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.queued == 3 }, time.Second, time.Millisecond)
	close(release)
	waitTraffic(t, s)
	s.mu.Lock()
	assert.EqualValues(t, 2, s.quotaRefusals)
	s.mu.Unlock()
	h, err := s.History(context.Background(), 0, 10)
	require.NoError(t, err)
	assert.Len(t, h.Records, 1)
	assert.Zero(t, h.Pruning)
	assert.True(t, s.Healthy())
}
