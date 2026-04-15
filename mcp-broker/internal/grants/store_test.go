package grants

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

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

func TestStoreCreateAndLookup(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	store, err := NewStore(ctx, db)
	require.NoError(t, err)

	cred, err := NewCredential()
	require.NoError(t, err)

	now := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	g := Grant{
		ID:          cred.ID,
		Description: "push feat/foo",
		Entries: []Entry{{
			Tool:      "git.git_push",
			ArgSchema: json.RawMessage(`{"type":"object"}`),
		}},
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
	require.NoError(t, store.Create(ctx, g, cred.TokenHash))

	got, err := store.LookupByTokenHash(ctx, cred.TokenHash)
	require.NoError(t, err)
	require.Equal(t, cred.ID, got.ID)
	require.Equal(t, "push feat/foo", got.Description)
	require.Len(t, got.Entries, 1)
	require.Equal(t, "git.git_push", got.Entries[0].Tool)
	require.True(t, got.CreatedAt.Equal(now))
	require.True(t, got.ExpiresAt.Equal(now.Add(time.Hour)))
	require.Nil(t, got.RevokedAt)
}

func TestStoreLookupUnknown(t *testing.T) {
	db := openTestDB(t)
	store, err := NewStore(context.Background(), db)
	require.NoError(t, err)

	g, err := store.LookupByTokenHash(context.Background(), "deadbeef")
	require.NoError(t, err)
	require.Nil(t, g, "unknown token_hash must return (nil, nil)")
}

func TestStoreRevokeIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	store, err := NewStore(ctx, db)
	require.NoError(t, err)

	cred, _ := NewCredential()
	now := time.Now().UTC()
	g := Grant{
		ID:        cred.ID,
		Entries:   []Entry{{Tool: "x.y", ArgSchema: json.RawMessage(`{}`)}},
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	}
	require.NoError(t, store.Create(ctx, g, cred.TokenHash))

	require.NoError(t, store.Revoke(ctx, g.ID, now.Add(time.Minute)))
	require.NoError(t, store.Revoke(ctx, g.ID, now.Add(2*time.Minute)),
		"revoking an already-revoked grant must not error")

	got, err := store.LookupByTokenHash(ctx, cred.TokenHash)
	require.NoError(t, err)
	require.NotNil(t, got.RevokedAt, "RevokedAt must be set after revoke")
}

func TestStoreListActive(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	store, err := NewStore(ctx, db)
	require.NoError(t, err)
	now := time.Now().UTC()

	active, _ := NewCredential()
	expired, _ := NewCredential()
	revoked, _ := NewCredential()

	mk := func(id string, expiresIn time.Duration) Grant {
		return Grant{
			ID:        id,
			Entries:   []Entry{{Tool: "x.y", ArgSchema: json.RawMessage(`{}`)}},
			CreatedAt: now,
			ExpiresAt: now.Add(expiresIn),
		}
	}
	require.NoError(t, store.Create(ctx, mk(active.ID, time.Hour), active.TokenHash))
	require.NoError(t, store.Create(ctx, mk(expired.ID, -time.Hour), expired.TokenHash))
	require.NoError(t, store.Create(ctx, mk(revoked.ID, time.Hour), revoked.TokenHash))
	require.NoError(t, store.Revoke(ctx, revoked.ID, now))

	got, err := store.List(ctx, false)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, active.ID, got[0].ID)

	all, err := store.List(ctx, true)
	require.NoError(t, err)
	require.Len(t, all, 3)
}
