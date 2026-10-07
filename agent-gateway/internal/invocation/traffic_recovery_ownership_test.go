package invocation

import (
	"context"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	"github.com/stretchr/testify/require"
)

func TestTrafficRealBeginLockRecovers(t *testing.T) {
	s, _ := trafficFixture(t, nil, nil)
	other, err := trafficDatabase(t.Context(), s.path, s.config, false, false)
	require.NoError(t, err)
	defer func() { require.NoError(t, other.Close()) }()
	tx, err := other.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	s.ObserveMCP(trafficPrepared(1))
	waitTraffic(t, s)
	require.Equal(t, "locked", s.Status(t.Context()).Incident.Cause)
	require.NoError(t, tx.Rollback())
	s.recoverTraffic(time.Now().Add(time.Minute))
	recordMCP(t, s, trafficPrepared(2))
	h, err := s.History(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, h.Records, 1)
	require.Equal(t, invocationID(2), h.Records[0].InvocationID)
}

func TestTrafficUnresolvedConnectionCannotBeReplaced(t *testing.T) {
	s, _ := trafficFixture(t, nil, nil)
	conn, err := s.db.Conn(t.Context())
	require.NoError(t, err)
	_, err = conn.ExecContext(t.Context(), "BEGIN IMMEDIATE")
	require.NoError(t, err)
	s.writerGate.Lock()
	s.pendingConnection = conn
	s.failTraffic(classifyTraffic(sqlite3.IOERR, "rollback", "uncertain"), "rollback", "uncertain")
	s.writerGate.Unlock()
	s.recoverTraffic(time.Now().Add(time.Minute))
	require.False(t, s.Healthy())
	require.Same(t, conn, s.pendingConnection)
	require.Equal(t, 1, s.db.Stats().InUse)
	s.ObserveMCP(trafficPrepared(1))
	require.Zero(t, s.Status(t.Context()).Delivery.Acknowledged)
	// Only the original fixture owner settles its actual transaction.
	_, err = conn.ExecContext(context.Background(), "ROLLBACK")
	require.NoError(t, err)
	s.recoverTraffic(time.Now().Add(time.Minute))
	require.Nil(t, s.pendingConnection)
	recordMCP(t, s, trafficPrepared(2))
	h, err := s.History(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, h.Records, 1)
	require.Equal(t, invocationID(2), h.Records[0].InvocationID)
}
