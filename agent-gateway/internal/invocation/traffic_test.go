package invocation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/activity"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func trafficFixture(t *testing.T, configure func(*TrafficConfig), fault func(string) error) (*TrafficStore, *gatewaypaths.Ownership) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "installation")
	owner, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Close()) })
	config := DefaultTrafficConfig()
	config.BudgetBytes = 8 << 20
	if configure != nil {
		configure(&config)
	}
	s, err := openTraffic(context.Background(), owner, invocationTestInstallationID, invocationID(90), config, true, fault)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s, owner
}

func trafficPrepared(id int) PreparedAdmission {
	return PreparedAdmission{Identity: activity.Identity{InvocationID: invocationID(id), AdmittedAt: canonicalInvocationTime(invocationTestTime)}, admission: testEvaluatedAdmission()}
}

func trafficCompletion() activity.Completion {
	return activity.Completion{CompletedAt: canonicalInvocationTime(invocationTestTime.Add(time.Second)), Class: contract.TerminalSucceeded}
}

func TestTrafficAcknowledgmentReceiptAndCompletion(t *testing.T) {
	s, _ := trafficFixture(t, nil, nil)
	prepared := trafficPrepared(1)
	receipt, err := s.Admit(context.Background(), prepared)
	require.NoError(t, err)
	require.NotNil(t, receipt)
	prepared.admission.MCP.RedactedArguments[0] = 'x'
	history, err := s.History(context.Background(), 0, 10)
	require.NoError(t, err)
	require.Len(t, history.Records, 1)
	assert.True(t, validStoredInvocation(history.Records[0]))
	require.True(t, s.Confirm(context.Background(), receipt))
	assert.False(t, s.Confirm(context.Background(), receipt))
	require.NoError(t, s.Complete(context.Background(), receipt, trafficCompletion()))
	assert.ErrorIs(t, s.Complete(context.Background(), receipt, trafficCompletion()), ErrInvalidInput)
	history, err = s.History(context.Background(), 0, 10)
	require.NoError(t, err)
	require.NotNil(t, history.Records[0].TerminalClass)
	assert.Equal(t, contract.TerminalSucceeded, *history.Records[0].TerminalClass)
	assert.Empty(t, s.pins)
	_, err = s.Admit(context.Background(), trafficPrepared(1))
	assert.ErrorIs(t, err, ErrIdentityUnavailable)
	assert.True(t, s.Healthy())
}

func TestTrafficFaultsNeverDispatchOrReplay(t *testing.T) {
	for _, point := range []string{"before_begin", "statement", "commit", "rollback", "acknowledgment"} {
		t.Run(point, func(t *testing.T) {
			s, owner := trafficFixture(t, nil, func(at string) error {
				if at == point || point == "rollback" && at == "statement" {
					return errors.New("injected I/O failure")
				}
				return nil
			})
			receipt, err := s.Admit(context.Background(), trafficPrepared(1))
			require.ErrorIs(t, err, ErrTrafficFault)
			require.Nil(t, receipt)
			dispatches := 0
			if s.Confirm(context.Background(), receipt) {
				dispatches++
			}
			assert.Zero(t, dispatches)
			assert.False(t, s.Healthy())
			_, err = s.Admit(context.Background(), trafficPrepared(2))
			assert.ErrorIs(t, err, ErrTrafficFault)
			history, err := s.History(context.Background(), 0, 10)
			require.NoError(t, err)
			expected := 0
			if point == "acknowledgment" {
				expected = 1
			}
			assert.Len(t, history.Records, expected)
			require.NoError(t, s.Close())
			reopened, err := OpenTraffic(context.Background(), owner, invocationTestInstallationID, invocationID(90), s.config)
			require.NoError(t, err)
			defer func() { require.NoError(t, reopened.Close()) }()
			assert.True(t, reopened.Healthy())
			assert.False(t, reopened.Confirm(context.Background(), receipt))
			history, err = reopened.History(context.Background(), 0, 10)
			require.NoError(t, err)
			assert.Len(t, history.Records, expected)
			for _, row := range history.Records {
				assert.Nil(t, row.CompletedAt)
			}
			if point == "acknowledgment" {
				receipt, err = reopened.Admit(context.Background(), trafficPrepared(1))
				assert.ErrorIs(t, err, ErrIdentityUnavailable)
				assert.Nil(t, receipt)
			}
			assert.Zero(t, dispatches)
		})
	}
}

func TestTrafficAtomicBatchAndCancellationSettlement(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	s, _ := trafficFixture(t, func(c *TrafficConfig) { c.Dwell = 10 * time.Millisecond }, func(point string) error {
		if point == "acknowledgment" {
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
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan trafficResult, 1)
	go func() { r, e := s.Admit(ctx, trafficPrepared(1)); result <- trafficResult{r, e} }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not reach acknowledgment")
	}
	cancel()
	select {
	case <-result:
		t.Fatal("cancellation abandoned commit settlement")
	default:
	}
	close(release)
	got := <-result
	assert.ErrorIs(t, got.err, ErrTrafficDeadline)
	assert.Nil(t, got.receipt)
	history, err := s.History(context.Background(), 0, 10)
	require.NoError(t, err)
	require.Len(t, history.Records, 1)
	assert.Nil(t, history.Records[0].CompletedAt)
	assert.Empty(t, s.pins)
}

func TestTrafficRetentionPinsHolesAndRestartRelease(t *testing.T) {
	s, owner := trafficFixture(t, func(c *TrafficConfig) { c.RetainedRecords = 3; c.BatchRecords = 1 }, nil)
	first, err := s.Admit(context.Background(), trafficPrepared(1))
	require.NoError(t, err)
	for _, id := range []int{2, 3, 4} {
		r, e := s.Admit(context.Background(), trafficPrepared(id))
		require.NoError(t, e)
		s.Release(r)
	}
	history, err := s.History(context.Background(), 0, 10)
	require.NoError(t, err)
	require.Len(t, history.Records, 3)
	assert.Equal(t, []int64{1, 3, 4}, []int64{history.Records[0].Sequence, history.Records[1].Sequence, history.Records[2].Sequence})
	assert.Equal(t, int64(1), history.Pruning)
	// Collision must not prune even when the history is at capacity.
	_, err = s.Admit(context.Background(), trafficPrepared(1))
	assert.ErrorIs(t, err, ErrIdentityUnavailable)
	same, err := s.History(context.Background(), 0, 10)
	require.NoError(t, err)
	assert.Equal(t, history, same)
	require.True(t, s.Confirm(context.Background(), first))
	require.NoError(t, s.Complete(context.Background(), first, trafficCompletion()))
	require.NoError(t, s.Close())
	reopened, err := OpenTraffic(context.Background(), owner, invocationTestInstallationID, invocationID(90), s.config)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	assert.Empty(t, reopened.pins)
	r, err := reopened.Admit(context.Background(), trafficPrepared(5))
	require.NoError(t, err)
	reopened.Release(r)
	history, err = reopened.History(context.Background(), 0, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(5), history.HighWater)
	assert.Equal(t, int64(2), history.Pruning)
	assert.Equal(t, int64(3), history.Records[0].Sequence)
}

func TestTrafficCompleteRefusalPreservesKnownLiveResult(t *testing.T) {
	s, _ := trafficFixture(t, nil, nil)
	r, err := s.Admit(context.Background(), trafficPrepared(1))
	require.NoError(t, err)
	require.True(t, s.Confirm(context.Background(), r))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	live := contract.TerminalSucceeded
	assert.ErrorIs(t, s.Complete(ctx, r, trafficCompletion()), ErrTrafficDeadline)
	assert.Equal(t, contract.TerminalSucceeded, live)
	assert.True(t, s.Healthy())
	assert.Empty(t, s.pins)
	h, err := s.History(context.Background(), 0, 10)
	require.NoError(t, err)
	assert.Nil(t, h.Records[0].CompletedAt)
}

func TestTrafficRestartRejectsInvalidGeneration(t *testing.T) {
	tests := []struct{ name, sql string }{
		{"semantic chronology", `PRAGMA ignore_check_constraints=ON; DROP TRIGGER invocations_terminal_once; UPDATE invocations SET evaluated_at='1970-01-01T00:00:00.000000000Z'`},
		{"accounting", `UPDATE traffic_meta SET bytes=bytes+1`},
		{"high water", `UPDATE traffic_meta SET high_water=high_water+1`},
		{"foreign binding", `UPDATE traffic_meta SET installation='01ARZ3NDEKTSV4RRFFQ69G5FAX'`},
		{"application", `PRAGMA application_id=0`},
		{"schema", `PRAGMA user_version=2`},
		{"unexpected object", `CREATE TABLE surprise(value TEXT)`},
		{"missing charge", `DELETE FROM traffic_sizes`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			s, owner := trafficFixture(t, nil, nil)
			r, err := s.Admit(context.Background(), trafficPrepared(1))
			require.NoError(t, err)
			s.Release(r)
			_, err = s.db.ExecContext(context.Background(), test.sql)
			require.NoError(t, err)
			require.NoError(t, s.Close())
			reopened, err := OpenTraffic(context.Background(), owner, invocationTestInstallationID, invocationID(90), s.config)
			assert.Error(t, err)
			assert.Nil(t, reopened)
		})
	}
	t.Run("missing", func(t *testing.T) {
		s, owner := trafficFixture(t, nil, nil)
		require.NoError(t, s.Close())
		require.NoError(t, os.Remove(s.path))
		r, e := OpenTraffic(context.Background(), owner, invocationTestInstallationID, invocationID(90), s.config)
		assert.Error(t, e)
		assert.Nil(t, r)
		_, e = os.Stat(s.path)
		assert.ErrorIs(t, e, os.ErrNotExist)
	})
	t.Run("corruption", func(t *testing.T) {
		s, owner := trafficFixture(t, nil, nil)
		require.NoError(t, s.Close())
		require.NoError(t, os.WriteFile(s.path, []byte("not SQLite"), 0600))
		r, e := OpenTraffic(context.Background(), owner, invocationTestInstallationID, invocationID(90), s.config)
		assert.Error(t, e)
		assert.Nil(t, r)
	})
}
