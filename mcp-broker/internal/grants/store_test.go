package grants

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/stretchr/testify/require"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "grants.db")
	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestNewStoreCreatesSchema(t *testing.T) {
	db := openTestDB(t)
	_, err := NewStore(context.Background(), db)
	require.NoError(t, err)

	row := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='grants'`)
	var name string
	require.NoError(t, row.Scan(&name))
	require.Equal(t, "grants", name)
}

func TestNewStoreIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	_, err := NewStore(context.Background(), db)
	require.NoError(t, err)
	_, err = NewStore(context.Background(), db)
	require.NoError(t, err, "running NewStore twice must not error")
}
