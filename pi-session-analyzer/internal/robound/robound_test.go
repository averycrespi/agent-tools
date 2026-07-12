package robound

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
	"github.com/stretchr/testify/require"
)

func TestOpenUsesExistingDatabaseAndRejectsWrites(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sessions.db")
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE sample (value TEXT); INSERT INTO sample VALUES ('kept')`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	conn, err := Open(context.Background(), path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	var value string
	require.NoError(t, conn.QueryRowContext(context.Background(), `SELECT value FROM sample`).Scan(&value))
	require.Equal(t, "kept", value)
	_, err = conn.ExecContext(context.Background(), `DELETE FROM sample`)
	require.Error(t, err)
}

func TestOpenMissingDatabaseDoesNotCreateIt(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "missing.db")
	_, err := Open(context.Background(), path)
	require.ErrorContains(t, err, "does not exist")
	_, statErr := os.Stat(path)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestMarshalCappedAlwaysReturnsBoundedValidJSON(t *testing.T) {
	t.Parallel()

	result := MarshalCapped(map[string]any{"value": strings.Repeat("x", 2<<20)})
	require.LessOrEqual(t, len(result), MaxResponseBytes)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(result), &decoded))
	require.Equal(t, true, decoded["truncated"])
}
