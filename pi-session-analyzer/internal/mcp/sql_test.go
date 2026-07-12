package mcp

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/ingest"
	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/store"
	"github.com/stretchr/testify/require"
)

func testDatabase(t *testing.T) (string, *store.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data", "sessions.db")
	db, err := store.Open(path)
	require.NoError(t, err)
	messages := make([]ingest.Message, 150)
	for i := range messages {
		messages[i] = ingest.Message{ID: fmt.Sprintf("m%03d", i), Role: "user", Text: "message", SourceLine: i + 1}
	}
	require.NoError(t, db.ReplaceSession(context.Background(), ingest.Session{ID: "s", Messages: messages}, store.SourceMeta{Path: "x", Size: 1, ModTimeNS: 1}))
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return path, db
}

func TestRunSelectAcceptedAndRejectedQueries(t *testing.T) {
	path, _ := testDatabase(t)
	for _, query := range []string{"SELECT id FROM sessions", "SELECT 'delete' AS ordinary_text", "SELECT 1 /* delete is data here */", "SELECT/* adjacent comment */1", "SELECT id FROM sessions -- trailing comment", "WITH ids AS (SELECT id FROM sessions) SELECT * FROM ids"} {
		result, err := RunSelect(context.Background(), path, query)
		require.NoError(t, err, query)
		require.Len(t, result.Rows, 1)
	}
	for _, query := range []string{"DELETE FROM sessions", "PRAGMA table_info(sessions)", "ATTACH DATABASE 'x' AS x", "SELECT 1; SELECT 2", "WITH gone AS (DELETE FROM sessions RETURNING *) SELECT * FROM gone"} {
		_, err := RunSelect(context.Background(), path, query)
		require.Error(t, err, query)
	}
}

func TestReadOnlyBoundaryRejectsWriteWhenLexicalGuardBypassed(t *testing.T) {
	path, db := testDatabase(t)
	_, err := executeReadOnly(context.Background(), path, "DELETE FROM sessions")
	require.Error(t, err)
	sessions, listErr := db.ListSessions(context.Background(), 10, "")
	require.NoError(t, listErr)
	require.Len(t, sessions, 1)
}

func TestRunSelectRejectsOversizedCellBeforeResponseSerialization(t *testing.T) {
	path, _ := testDatabase(t)
	_, err := RunSelect(context.Background(), path, `SELECT printf('%.*c', 1000000, 'x')`)
	require.Error(t, err)
}

func TestRunSelectHonorsCanceledContext(t *testing.T) {
	path, _ := testDatabase(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := RunSelect(ctx, path, "SELECT id FROM sessions")
	require.Error(t, err)
}

func TestRunSelectEnforcesInternalTimeout(t *testing.T) {
	path, _ := testDatabase(t)
	started := time.Now()
	_, err := RunSelect(context.Background(), path, `WITH RECURSIVE infinite(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM infinite) SELECT sum(x) FROM infinite`)
	require.Error(t, err)
	require.GreaterOrEqual(t, time.Since(started), 4*time.Second)
	require.Less(t, time.Since(started), 8*time.Second)
}

func TestRunSelectRejectsRowsWiderThanSharedColumnLimit(t *testing.T) {
	path, _ := testDatabase(t)
	columns := make([]string, 33)
	for i := range columns {
		columns[i] = fmt.Sprintf("printf('%%.*c', 60000, 'x') AS c%d", i)
	}
	_, err := RunSelect(context.Background(), path, "SELECT "+strings.Join(columns, ","))
	require.Error(t, err)
}

func TestRunSelectBoundsCumulativeRowsBeforeSerialization(t *testing.T) {
	path, _ := testDatabase(t)
	result, err := RunSelect(context.Background(), path, `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1025) SELECT printf('%.*c', 60000, 'x') AS value FROM n`)
	require.NoError(t, err)
	require.Empty(t, result.Rows)
	require.True(t, result.Truncated)
}

func TestRunSelectAppliesIndependentRowCap(t *testing.T) {
	path, _ := testDatabase(t)
	result, err := RunSelect(context.Background(), path, `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<2000) SELECT x FROM n`)
	require.NoError(t, err)
	require.Len(t, result.Rows, 1024)
	require.True(t, result.Truncated)
}
