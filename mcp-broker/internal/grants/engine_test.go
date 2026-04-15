package grants

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func mkGrant(t *testing.T, store *Store, ttl time.Duration, entries []Entry) (grantID, token string) {
	t.Helper()
	cred, err := NewCredential()
	require.NoError(t, err)
	now := time.Now().UTC()
	g := Grant{
		ID:        cred.ID,
		Entries:   entries,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}
	require.NoError(t, store.Create(context.Background(), g, cred.TokenHash))
	return cred.ID, cred.Token
}

func TestEngineEvaluate(t *testing.T) {
	store, err := NewStore(context.Background(), openTestDB(t))
	require.NoError(t, err)
	eng := NewEngine(store)

	pushSchema := json.RawMessage(`{
		"type":"object",
		"properties":{"branch":{"const":"feat/foo"},"force":{"const":false}},
		"required":["branch","force"]
	}`)
	grantID, token := mkGrant(t, store, time.Hour, []Entry{
		{Tool: "git.git_push", ArgSchema: pushSchema},
		{Tool: "git.git_fetch", ArgSchema: json.RawMessage(`{"type":"object"}`)},
	})

	ctx := context.Background()

	t.Run("not presented", func(t *testing.T) {
		r, err := eng.Evaluate(ctx, "", "git.git_push", map[string]any{"branch": "feat/foo", "force": false})
		require.NoError(t, err)
		require.Equal(t, NotPresented, r.Outcome)
		require.Empty(t, r.GrantID)
	})

	t.Run("invalid token", func(t *testing.T) {
		r, err := eng.Evaluate(ctx, "gr_bogus", "git.git_push", nil)
		require.NoError(t, err)
		require.Equal(t, Invalid, r.Outcome)
		require.Empty(t, r.GrantID)
	})

	t.Run("matched", func(t *testing.T) {
		r, err := eng.Evaluate(ctx, token, "git.git_push", map[string]any{"branch": "feat/foo", "force": false})
		require.NoError(t, err)
		require.Equal(t, Matched, r.Outcome)
		require.Equal(t, grantID, r.GrantID)
	})

	t.Run("matched open-schema entry", func(t *testing.T) {
		r, err := eng.Evaluate(ctx, token, "git.git_fetch", map[string]any{"remote": "origin"})
		require.NoError(t, err)
		require.Equal(t, Matched, r.Outcome)
	})

	t.Run("fell through — wrong args", func(t *testing.T) {
		r, err := eng.Evaluate(ctx, token, "git.git_push", map[string]any{"branch": "main", "force": false})
		require.NoError(t, err)
		require.Equal(t, FellThrough, r.Outcome)
		require.Equal(t, grantID, r.GrantID)
	})

	t.Run("fell through — wrong tool", func(t *testing.T) {
		r, err := eng.Evaluate(ctx, token, "foo.bar", map[string]any{})
		require.NoError(t, err)
		require.Equal(t, FellThrough, r.Outcome)
		require.Equal(t, grantID, r.GrantID)
	})
}

func TestEngineExpiredIsInvalid(t *testing.T) {
	store, err := NewStore(context.Background(), openTestDB(t))
	require.NoError(t, err)
	eng := NewEngine(store)

	_, token := mkGrant(t, store, -time.Hour, []Entry{ // already expired
		{Tool: "x.y", ArgSchema: json.RawMessage(`{}`)},
	})

	r, err := eng.Evaluate(context.Background(), token, "x.y", nil)
	require.NoError(t, err)
	require.Equal(t, Invalid, r.Outcome)
}

func TestEngineRevokedIsInvalid(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(ctx, openTestDB(t))
	require.NoError(t, err)
	eng := NewEngine(store)

	id, token := mkGrant(t, store, time.Hour, []Entry{
		{Tool: "x.y", ArgSchema: json.RawMessage(`{}`)},
	})
	require.NoError(t, store.Revoke(ctx, id, time.Now().UTC()))

	r, err := eng.Evaluate(ctx, token, "x.y", nil)
	require.NoError(t, err)
	require.Equal(t, Invalid, r.Outcome)
}
