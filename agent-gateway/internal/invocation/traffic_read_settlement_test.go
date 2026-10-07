package invocation

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	"github.com/stretchr/testify/require"
)

func TestTrafficCallerCancellationDoesNotFaultRecording(t *testing.T) {
	s, _ := trafficFixture(t, nil, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, s.view(ctx, func(context.Context, *sql.Tx) error { t.Fatal("cancelled read ran"); return nil }), context.Canceled)
	_, err := s.History(ctx, 0, 10)
	require.ErrorIs(t, err, context.Canceled)
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	err = s.view(ctx, func(ctx context.Context, tx *sql.Tx) error {
		cancel()
		var value int
		return tx.QueryRowContext(ctx, "SELECT 1").Scan(&value)
	})
	require.ErrorIs(t, err, context.Canceled)
	require.True(t, s.Healthy())
	require.Nil(t, s.Status(t.Context()).Incident)
	recordMCP(t, s, trafficPrepared(1))
	h, err := s.History(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, h.Records, 1)
	require.EqualValues(t, 1, s.Status(t.Context()).Delivery.Acknowledged)
}

func TestTrafficReadSettlementFailureOverridesDomainResult(t *testing.T) {
	s, _ := trafficFixture(t, nil, nil)
	err := s.view(t.Context(), func(context.Context, *sql.Tx) error {
		return errors.Join(ErrNotFound, classifyTraffic(sqlite3.IOERR, "rollback", "uncertain"))
	})
	require.ErrorIs(t, err, ErrNotFound)
	require.False(t, s.Healthy())
	incident := s.Status(t.Context()).Incident
	require.Equal(t, "io", incident.Cause)
	require.Equal(t, "rollback", incident.Stage)
	require.Equal(t, "uncertain", incident.Settlement)
}

func TestTrafficReadLifetimeBoundsStatements(t *testing.T) {
	s, _ := trafficFixture(t, func(c *TrafficConfig) { c.ReadLifetime = 20 * time.Millisecond }, nil)
	err := s.view(t.Context(), func(ctx context.Context, tx *sql.Tx) error {
		var total int64
		return tx.QueryRowContext(ctx, `WITH RECURSIVE n(x) AS (VALUES(0) UNION ALL SELECT x+1 FROM n WHERE x<100000000) SELECT sum(x) FROM n`).Scan(&total)
	})
	require.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, sqlite3.INTERRUPT), "%v", err)
	require.Zero(t, s.readerDB.Stats().InUse)
	require.NotNil(t, s.Status(t.Context()).Incident)
	require.Equal(t, "deadline", s.Status(t.Context()).Incident.Cause)
}

// The driver barrier reproduces database/sql's tx.done-before-Rollback-return
// interleaving without simulating ErrTxDone. It also exercises explicit settlement.
func TestTrafficReadGateJoinsActualCancellationRollback(t *testing.T) {
	for _, rollbackErr := range []error{nil, sqlite3.IOERR} {
		for _, history := range []bool{false, true} {
			t.Run(fmt.Sprintf("%v/history=%v", rollbackErr, history), func(t *testing.T) {
				testTrafficReadCancellationRollback(t, rollbackErr, history)
			})
		}
	}
}

func testTrafficReadCancellationRollback(t *testing.T, rollbackErr error, history bool) {
	t.Helper()
	c := &blockedReadConnector{entered: make(chan struct{}), release: make(chan struct{}), rollbackErr: rollbackErr}
	db := sql.OpenDB(c)
	defer func() { require.NoError(t, db.Close()) }()
	var release sync.Once
	defer release.Do(func() { close(c.release) })
	s := &TrafficStore{readerDB: db, readSlots: make(chan struct{}, 1), config: TrafficConfig{ReadLifetime: time.Minute}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	c.query = func() {
		cancel()
		// Let any cancellation owner enter first. With an explicitly owned
		// transaction, rollback instead starts after the query returns.
		select {
		case <-c.entered:
		case <-time.After(20 * time.Millisecond):
		}
	}
	done := make(chan error, 1)
	go func() {
		if history {
			_, err := s.History(ctx, 0, 10)
			done <- err
			return
		}
		done <- s.view(ctx, func(ctx context.Context, tx *sql.Tx) error {
			var value int
			return tx.QueryRowContext(ctx, "SELECT 1").Scan(&value)
		})
	}()
	<-c.entered
	select {
	case err := <-done:
		t.Fatalf("read returned before actual rollback settled: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if s.readGate.TryLock() {
		s.readGate.Unlock()
		t.Error("close/checkpoint must remain fenced")
	}
	release.Do(func() { close(c.release) })
	err := <-done
	require.ErrorIs(t, err, context.Canceled)
	if rollbackErr != nil {
		require.ErrorIs(t, err, rollbackErr)
		require.False(t, s.Healthy())
		require.NotNil(t, s.incident)
		require.Equal(t, "rollback", s.incident.Stage)
		require.Equal(t, "uncertain", s.incident.Settlement)
		require.Equal(t, "io", s.incident.Cause)
	} else {
		require.True(t, s.Healthy())
		require.Nil(t, s.incident)
	}
	require.True(t, s.readGate.TryLock())
	s.readGate.Unlock()
	require.Zero(t, db.Stats().InUse)
}

type blockedReadConnector struct {
	entered, release chan struct{}
	rollbackErr      error
	query            func()
}

func (c *blockedReadConnector) Connect(context.Context) (driver.Conn, error) {
	return &blockedReadConn{c}, nil
}
func (c *blockedReadConnector) Driver() driver.Driver { return blockedReadDriver{} }

type blockedReadDriver struct{}

func (blockedReadDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("connector required")
}

type blockedReadConn struct{ c *blockedReadConnector }

func (*blockedReadConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (*blockedReadConn) Close() error                        { return nil }
func (c *blockedReadConn) Begin() (driver.Tx, error)         { return &blockedReadTx{c.c}, nil }
func (c *blockedReadConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return c.Begin()
}

func (c *blockedReadConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	c.c.query()
	return nil, context.Canceled
}

type blockedReadTx struct{ c *blockedReadConnector }

func (*blockedReadTx) Commit() error { return nil }
func (tx *blockedReadTx) Rollback() error {
	close(tx.c.entered)
	<-tx.c.release
	return tx.c.rollbackErr
}
