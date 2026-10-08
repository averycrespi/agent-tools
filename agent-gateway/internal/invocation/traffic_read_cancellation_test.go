package invocation

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
	"github.com/stretchr/testify/require"
)

func TestTrafficActiveCallerCancellationKeepsRecording(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "cancel"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			s, _ := trafficFixture(t, func(c *TrafficConfig) { c.ReadLifetime = time.Second }, nil)
			// Retain this fixture-only SQL function on the connection under test.
			// Production operation-scoped handles are covered separately.
			s.readerDB.SetMaxIdleConns(1)
			defer s.readerDB.SetMaxIdleConns(0)
			conn, err := s.readerDB.Conn(t.Context())
			require.NoError(t, err)
			var caller context.Context
			var cancel context.CancelFunc
			entered := false
			require.NoError(t, conn.Raw(func(raw any) error {
				return raw.(sqlitedriver.Conn).Raw().CreateFunction("cancel_caller", 0, 0, func(ctx sqlite3.Context, _ ...sqlite3.Value) {
					entered = true
					if deadline {
						<-caller.Done()
					} else {
						cancel()
					}
					ctx.ResultInt(0)
				})
			}))
			require.NoError(t, conn.Close())
			if deadline {
				caller, cancel = context.WithTimeout(t.Context(), 50*time.Millisecond)
			} else {
				caller, cancel = context.WithCancel(t.Context())
			}
			defer cancel()
			err = s.view(caller, func(ctx context.Context, tx *sql.Tx) error {
				var total int64
				return tx.QueryRowContext(ctx, `WITH RECURSIVE n(x) AS (VALUES(cancel_caller()) UNION ALL SELECT x+1 FROM n WHERE x<100000000) SELECT sum(x) FROM n`).Scan(&total)
			})
			require.True(t, entered, "cancellation must happen inside executing SQLite statement")
			require.ErrorIs(t, err, sqlite3.INTERRUPT)
			require.Zero(t, s.readerDB.Stats().InUse)
			conn, err = s.readerDB.Conn(t.Context())
			require.NoError(t, err)
			require.True(t, trafficAutocommit(conn), "read transaction must actually settle")
			require.NoError(t, conn.Close())
			require.True(t, s.Healthy())
			require.Nil(t, s.Status(t.Context()).Incident)
			recordMCP(t, s, trafficPrepared(1))
			history, err := s.History(t.Context(), 0, 10)
			require.NoError(t, err)
			require.Len(t, history.Records, 1)
			require.EqualValues(t, 1, s.Status(t.Context()).Delivery.Acknowledged)
		})
	}
}

func TestTrafficInternalDeadlineBeforeCallerCancellationStillFaults(t *testing.T) {
	s, _ := trafficFixture(t, func(c *TrafficConfig) { c.ReadLifetime = 20 * time.Millisecond }, nil)
	caller, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := s.view(caller, func(ctx context.Context, _ *sql.Tx) error {
		<-ctx.Done()
		cancel()
		return errors.Join(ctx.Err(), caller.Err(), sqlite3.INTERRUPT)
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.False(t, s.Healthy())
	require.Equal(t, "deadline", s.Status(t.Context()).Incident.Cause)
}

func TestTrafficJoinedReadErrorKeepsIndependentFault(t *testing.T) {
	for _, storageErr := range []error{sqlite3.IOERR, sqlite3.CORRUPT, classifyTraffic(sqlite3.IOERR, "rollback", "uncertain")} {
		t.Run(storageErr.Error(), func(t *testing.T) {
			s, _ := trafficFixture(t, nil, nil)
			caller, cancel := context.WithCancel(t.Context())
			defer cancel()
			err := s.view(caller, func(context.Context, *sql.Tx) error {
				cancel()
				return errors.Join(ErrNotFound, errors.Join(caller.Err(), sqlite3.INTERRUPT, storageErr))
			})
			require.ErrorIs(t, err, storageErr)
			require.False(t, s.Healthy())
			incident := s.Status(t.Context()).Incident
			require.NotNil(t, incident)
			if errors.Is(storageErr, sqlite3.CORRUPT) {
				require.Equal(t, "integrity", incident.Cause)
			} else {
				require.Equal(t, "io", incident.Cause)
			}
			var failure *trafficFailure
			if errors.As(storageErr, &failure) {
				require.Equal(t, "rollback", incident.Stage)
				require.Equal(t, "uncertain", incident.Settlement)
			}
		})
	}
}

func TestTrafficCancelledMalformedStoredReadStillFaults(t *testing.T) {
	s, _ := trafficFixture(t, nil, nil)
	recordMCP(t, s, trafficPrepared(1))
	_, err := s.db.ExecContext(t.Context(), `DROP TRIGGER invocations_terminal_once; UPDATE invocations SET evaluated_at='1970-01-01T00:00:00.000000000Z'`)
	require.NoError(t, err)
	caller, cancel := context.WithCancel(t.Context())
	defer cancel()
	err = s.view(caller, func(ctx context.Context, tx *sql.Tx) error {
		record, scanErr := scanInvocation(tx.QueryRowContext(ctx, invocationSelect))
		if scanErr != nil {
			return scanErr
		}
		require.False(t, validStoredInvocation(record))
		cancel()
		return errors.Join(ErrInvalidState, caller.Err())
	})
	require.ErrorIs(t, err, ErrInvalidState)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, s.Healthy())
	incident := s.Status(t.Context()).Incident
	require.NotNil(t, incident)
	require.Equal(t, "integrity", incident.Cause)
	require.Equal(t, "operator_action_required", incident.Recovery)
	s.ObserveMCP(trafficPrepared(2))
	waitTraffic(t, s)
	require.EqualValues(t, 1, s.Status(t.Context()).Delivery.Acknowledged)
	var stored int
	require.NoError(t, s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM invocations`).Scan(&stored))
	require.Equal(t, 1, stored, "faulted recording must not persist another observation")
	require.Zero(t, s.readerDB.Stats().InUse)
}
