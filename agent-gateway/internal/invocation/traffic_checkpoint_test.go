package invocation

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTrafficUsesSQLiteDefaultAutocheckpoint(t *testing.T) {
	plain, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	defer func() { require.NoError(t, plain.Close()) }()
	var expected int
	require.NoError(t, plain.QueryRowContext(t.Context(), `PRAGMA wal_autocheckpoint`).Scan(&expected))
	require.Positive(t, expected)
	s, _ := trafficFixture(t, nil, nil)
	for i := 0; i < 3; i++ {
		conn, err := s.db.Conn(t.Context())
		require.NoError(t, err)
		var actual, busy, synchronous, spill, foreign, maximum int64
		require.NoError(t, conn.QueryRowContext(t.Context(), `PRAGMA wal_autocheckpoint`).Scan(&actual))
		require.EqualValues(t, expected, actual)
		require.NoError(t, conn.QueryRowContext(t.Context(), `SELECT (SELECT timeout FROM pragma_busy_timeout),(SELECT synchronous FROM pragma_synchronous),(SELECT cache_spill FROM pragma_cache_spill),(SELECT foreign_keys FROM pragma_foreign_keys),(SELECT max_page_count FROM pragma_max_page_count)`).Scan(&busy, &synchronous, &spill, &foreign, &maximum))
		require.Equal(t, []int64{50, 2, 0, 1, trafficPages(s.config)}, []int64{busy, synchronous, spill, foreign, maximum})
		require.NoError(t, conn.Close())
		require.Zero(t, s.db.Stats().OpenConnections, "settled writer handles are not retained")
	}
}

func TestTrafficMinimumBudgetContinuesAcknowledging(t *testing.T) {
	s, _ := trafficFixture(t, func(c *TrafficConfig) { c.BudgetBytes = 1 << 20; c.BatchRecords = 1; c.RetainedRecords = 8 }, nil)
	for id := 1; id <= 160; id++ {
		before := s.Status(t.Context()).Delivery.Acknowledged
		recordMCP(t, s, trafficPrepared(id))
		require.Equal(t, before+1, s.Status(t.Context()).Delivery.Acknowledged)
		assertTrafficPhysicalBudget(t, s)
	}
	history, err := s.History(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, history.Records, 8)
	require.EqualValues(t, 160, history.HighWater)
	require.EqualValues(t, 152, history.Pruning)
}

func TestTrafficReaderReleasePermitsFreshAcknowledgment(t *testing.T) {
	s, _ := trafficFixture(t, func(c *TrafficConfig) { c.BudgetBytes = 1 << 20; c.BatchRecords = 1; c.RetainedRecords = 8 }, nil)
	recordMCP(t, s, trafficPrepared(1))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	tx, err := s.readerDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	var count int
	require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM invocations`).Scan(&count))
	var discarded uint64
	for id := 2; id < 100; id++ {
		recordMCP(t, s, trafficPrepared(id))
		assertTrafficPhysicalBudget(t, s)
		discarded = s.Status(t.Context()).Delivery.Discarded
		if discarded > 0 {
			break
		}
	}
	require.Positive(t, discarded)
	require.True(t, s.Healthy())
	require.NoError(t, tx.Rollback())
	before := s.Status(t.Context()).Delivery.Acknowledged
	// A batch already refused at the physical boundary stays discarded. Closing
	// its ordinary scoped handle may settle SQLite recovery for the next batch;
	// only a new independently submitted observation can earn an acknowledgment.
	for id := 101; id <= 102; id++ {
		recordMCP(t, s, trafficPrepared(id))
	}
	status := s.Status(t.Context())
	require.Greater(t, status.Delivery.Acknowledged, before)
	require.GreaterOrEqual(t, status.Delivery.Discarded, discarded)
	history, err := s.History(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Equal(t, invocationID(102), history.Records[len(history.Records)-1].InvocationID)
	require.Zero(t, s.db.Stats().OpenConnections)
	require.Zero(t, s.readerDB.Stats().OpenConnections)
	assertTrafficPhysicalBudget(t, s)
}

func assertTrafficPhysicalBudget(t *testing.T, s *TrafficStore) {
	t.Helper()
	var total int64
	for _, suffix := range []string{"", "-wal"} {
		info, err := os.Stat(s.path + suffix)
		if suffix != "" && os.IsNotExist(err) {
			continue
		}
		require.NoError(t, err)
		total += info.Size()
	}
	require.LessOrEqual(t, total, s.config.BudgetBytes)
}
