package agents_test

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/agents"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/secrets"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newDB opens a temporary SQLite database with all migrations applied.
func newDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// newRegistry creates a Registry backed by a fresh test database.
func newRegistry(t *testing.T) agents.Registry {
	t.Helper()
	r, err := agents.NewRegistry(context.Background(), newDB(t))
	require.NoError(t, err)
	return r
}

func TestMintToken_PrefixAndFormat(t *testing.T) {
	tok := agents.MintToken()
	assert.True(t, strings.HasPrefix(tok, "agw_"), "token must start with agw_")
	assert.GreaterOrEqual(t, len(tok), 36, "token must be at least 36 chars")
}

func TestMintToken_Entropy(t *testing.T) {
	// Two tokens must not be equal.
	a, b := agents.MintToken(), agents.MintToken()
	assert.NotEqual(t, a, b)
}

func TestPrefix_Length(t *testing.T) {
	tok := agents.MintToken()
	p := agents.Prefix(tok)
	assert.Equal(t, 12, len(p), "prefix must be 12 chars")
	assert.True(t, strings.HasPrefix(p, "agw_"))
}

func TestRegistry_AddAndAuthenticate(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()

	tok, err := r.Add(ctx, "claude", "description")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(tok, "agw_"))

	a, err := r.Authenticate(ctx, tok)
	require.NoError(t, err)
	assert.Equal(t, "claude", a.Name)
}

func TestRegistry_WrongTokenRejected(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()

	_, err := r.Add(ctx, "bot", "")
	require.NoError(t, err)

	// Completely wrong token.
	_, err = r.Authenticate(ctx, "agw_wrongtoken")
	assert.ErrorIs(t, err, agents.ErrInvalidToken)

	// Valid prefix but wrong body — craft a token with the same prefix but different tail.
	tok, err := r.Add(ctx, "bot2", "")
	require.NoError(t, err)

	// Tamper: flip last byte.
	tampered := []byte(tok)
	tampered[len(tampered)-1] ^= 0xFF
	_, err = r.Authenticate(ctx, string(tampered))
	assert.ErrorIs(t, err, agents.ErrInvalidToken)
}

func TestRegistry_RotateInvalidatesOld(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()

	tok, err := r.Add(ctx, "rotate-me", "")
	require.NoError(t, err)

	newTok, err := r.Rotate(ctx, "rotate-me")
	require.NoError(t, err)
	assert.NotEqual(t, tok, newTok)

	// Old token must be rejected.
	_, err = r.Authenticate(ctx, tok)
	assert.ErrorIs(t, err, agents.ErrInvalidToken, "old token must be rejected after rotate")

	// New token must be accepted.
	a, err := r.Authenticate(ctx, newTok)
	require.NoError(t, err)
	assert.Equal(t, "rotate-me", a.Name)
}

func TestRegistry_RmCascadesSecrets(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()

	r, err := agents.NewRegistry(ctx, db)
	require.NoError(t, err)

	// Add agent and set a scoped secret.
	_, err = r.Add(ctx, "agent1", "")
	require.NoError(t, err)

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	ss, err := secrets.NewStoreWithKey(db, slog.Default(), key)
	require.NoError(t, err)

	require.NoError(t, ss.Set(ctx, "my-secret", "agent1", "super-secret", ""))

	// Verify the secret exists under agent scope.
	val, scope, err := ss.Get(ctx, "my-secret", "agent1")
	require.NoError(t, err)
	assert.Equal(t, "super-secret", val)
	assert.Equal(t, "agent:agent1", scope)

	// Remove the agent.
	require.NoError(t, r.Rm(ctx, "agent1"))

	// The scoped secret row must be gone.
	var count int
	err = db.QueryRowContext(ctx, `SELECT count(*) FROM secrets WHERE scope = 'agent:agent1'`).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "secrets for removed agent must be deleted")
}

func TestRegistry_AuthenticateUsesPrefixMap(t *testing.T) {
	// Add 100 agents. Only the matching one should be argon2-compared.
	// We verify correctness: exactly one succeeds; any one of the others fails.
	r := newRegistry(t)
	ctx := context.Background()

	const n = 100
	tokens := make([]string, n)
	for i := 0; i < n; i++ {
		name := strings.Repeat("x", i+1) // unique names
		tok, err := r.Add(ctx, name, "")
		require.NoError(t, err)
		tokens[i] = tok
	}

	// Every token must authenticate to its own agent name.
	for i, tok := range tokens {
		a, err := r.Authenticate(ctx, tok)
		require.NoError(t, err, "token %d must authenticate", i)
		expectedName := strings.Repeat("x", i+1)
		assert.Equal(t, expectedName, a.Name)
	}

	// A fabricated token with a prefix not in the map must be rejected without DB hit.
	_, err := r.Authenticate(ctx, "agw_00000000notaprefix")
	assert.ErrorIs(t, err, agents.ErrInvalidToken)
}

func TestRegistry_List(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()

	require.NoError(t, func() error { _, e := r.Add(ctx, "alpha", "first"); return e }())
	require.NoError(t, func() error { _, e := r.Add(ctx, "beta", "second"); return e }())

	list, err := r.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 2)

	names := map[string]bool{}
	for _, m := range list {
		names[m.Name] = true
		assert.NotEmpty(t, m.TokenPrefix)
	}
	assert.True(t, names["alpha"])
	assert.True(t, names["beta"])
}

func TestRegistry_ReloadFromDB(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()

	r1, err := agents.NewRegistry(ctx, db)
	require.NoError(t, err)

	tok, err := r1.Add(ctx, "persist-me", "")
	require.NoError(t, err)

	// Create a second registry on the same DB — it must find the agent via reload.
	r2, err := agents.NewRegistry(ctx, db)
	require.NoError(t, err)

	a, err := r2.Authenticate(ctx, tok)
	require.NoError(t, err)
	assert.Equal(t, "persist-me", a.Name)
}

func TestRegistry_LastSeenAtUpdated(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()

	r, err := agents.NewRegistry(ctx, db)
	require.NoError(t, err)

	tok, err := r.Add(ctx, "seen-agent", "")
	require.NoError(t, err)

	// last_seen_at is NULL before first auth.
	var lastSeen *int64
	require.NoError(t, db.QueryRowContext(ctx, `SELECT last_seen_at FROM agents WHERE name = 'seen-agent'`).Scan(&lastSeen))
	assert.Nil(t, lastSeen)

	a, err := r.Authenticate(ctx, tok)
	require.NoError(t, err)
	assert.False(t, a.LastSeenAt.IsZero(), "last_seen_at must be set after auth")

	// Check the DB was written.
	require.NoError(t, db.QueryRowContext(ctx, `SELECT last_seen_at FROM agents WHERE name = 'seen-agent'`).Scan(&lastSeen))
	assert.NotNil(t, lastSeen)
}

func TestRegistry_RmNotFound(t *testing.T) {
	r := newRegistry(t)
	err := r.Rm(context.Background(), "nonexistent")
	assert.ErrorIs(t, err, agents.ErrNotFound)
}

func TestRegistry_RotateNotFound(t *testing.T) {
	r := newRegistry(t)
	_, err := r.Rotate(context.Background(), "nonexistent")
	assert.ErrorIs(t, err, agents.ErrNotFound)
}
