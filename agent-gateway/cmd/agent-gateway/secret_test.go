package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/secrets"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openTestDB opens a temporary SQLite database with migrations applied.
func secretTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "state.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// newTestSecretStore creates a Store with a fixed key for testing.
func newTestSecretStore(t *testing.T, db *sql.DB) secrets.Store {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	s, err := secrets.NewStoreWithKey(db, slog.Default(), key)
	require.NoError(t, err)
	return s
}

// noSIGHUP is a no-op SIGHUP sender for tests.
func noSIGHUP(_ string) error { return nil }

// TestSecretSet_Global verifies that "secret set <name>" with non-TTY stdin stores a global row.
func TestSecretSet_Global(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)

	var out bytes.Buffer
	err := execSecretSet(context.Background(), s, "mytoken", "", "my token", strings.NewReader("s3cr3t\n"), &out, false, noSIGHUP)
	require.NoError(t, err)

	val, scope, getErr := s.Get(context.Background(), "mytoken", "")
	require.NoError(t, getErr)
	assert.Equal(t, "s3cr3t", val)
	assert.Equal(t, "global", scope)
}

// TestSecretSet_Agent verifies that "secret set <name> --agent <a>" creates an agent-scoped row.
func TestSecretSet_Agent(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)

	var out bytes.Buffer
	err := execSecretSet(context.Background(), s, "mytoken", "mybot", "desc", strings.NewReader("s3cr3t\n"), &out, false, noSIGHUP)
	require.NoError(t, err)

	val, scope, getErr := s.Get(context.Background(), "mytoken", "mybot")
	require.NoError(t, getErr)
	assert.Equal(t, "s3cr3t", val)
	assert.Equal(t, "agent:mybot", scope)
}

// TestSecretSet_ShadowWarning verifies that a shadow warning is printed when
// an agent-scoped set shadows an existing global row.
func TestSecretSet_ShadowWarning(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	// Set a global row first.
	require.NoError(t, s.Set(ctx, "mytoken", "", "global-val", "global"))

	var out bytes.Buffer
	err := execSecretSet(ctx, s, "mytoken", "mybot", "desc", strings.NewReader("agent-val\n"), &out, false, noSIGHUP)
	require.NoError(t, err)

	// Shadow warning must appear in output.
	assert.Contains(t, out.String(), `warning: secret "mytoken" is also set globally`)
	assert.Contains(t, out.String(), `"mybot"`)
}

// TestSecretSet_NoShadowWarning verifies that no shadow warning is printed
// when set globally (even if agent rows exist).
func TestSecretSet_NoShadowWarning(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	var out bytes.Buffer
	err := execSecretSet(ctx, s, "tok", "", "", strings.NewReader("val\n"), &out, false, noSIGHUP)
	require.NoError(t, err)
	assert.Empty(t, out.String())
}

// TestSecretSet_RefusesTTY verifies that set returns an error when stdin is a TTY.
func TestSecretSet_RefusesTTY(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)

	var out bytes.Buffer
	// isTTY=true simulates a TTY stdin.
	err := execSecretSet(context.Background(), s, "tok", "", "", strings.NewReader(""), &out, true, noSIGHUP)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pipe")
}

// TestSecretList verifies that "secret list" prints metadata rows without values.
func TestSecretList(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	require.NoError(t, s.Set(ctx, "alpha", "", "v1", "desc alpha"))
	require.NoError(t, s.Set(ctx, "beta", "mybot", "v2", "desc beta"))

	var out bytes.Buffer
	err := execSecretList(ctx, s, &out)
	require.NoError(t, err)

	output := out.String()
	assert.Contains(t, output, "alpha")
	assert.Contains(t, output, "global")
	assert.Contains(t, output, "beta")
	assert.Contains(t, output, "agent:mybot")
	assert.Contains(t, output, "desc alpha")
	assert.Contains(t, output, "desc beta")
	// Values must NOT appear.
	assert.NotContains(t, output, "v1")
	assert.NotContains(t, output, "v2")
}

// TestSecretRotate verifies that "secret rotate <name>" updates the value.
func TestSecretRotate(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	require.NoError(t, s.Set(ctx, "tok", "", "old-val", ""))

	var out bytes.Buffer
	err := execSecretRotate(ctx, s, "tok", "", strings.NewReader("new-val\n"), &out, false, noSIGHUP)
	require.NoError(t, err)

	val, _, getErr := s.Get(ctx, "tok", "")
	require.NoError(t, getErr)
	assert.Equal(t, "new-val", val)
}

// TestSecretRotate_Agent verifies agent-scoped rotate.
func TestSecretRotate_Agent(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	require.NoError(t, s.Set(ctx, "tok", "mybot", "old-val", ""))

	var out bytes.Buffer
	err := execSecretRotate(ctx, s, "tok", "mybot", strings.NewReader("new-val\n"), &out, false, noSIGHUP)
	require.NoError(t, err)

	val, _, getErr := s.Get(ctx, "tok", "mybot")
	require.NoError(t, getErr)
	assert.Equal(t, "new-val", val)
}

// TestSecretRM verifies that "secret rm <name>" removes the secret.
func TestSecretRM(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	require.NoError(t, s.Set(ctx, "tok", "", "val", ""))

	var out bytes.Buffer
	err := execSecretRM(ctx, s, "tok", "", &out, noSIGHUP)
	require.NoError(t, err)

	_, _, getErr := s.Get(ctx, "tok", "")
	assert.True(t, errors.Is(getErr, secrets.ErrNotFound))
}

// TestSecretRM_Agent verifies that "--agent" scopes the deletion.
func TestSecretRM_Agent(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	require.NoError(t, s.Set(ctx, "tok", "", "global-val", ""))
	require.NoError(t, s.Set(ctx, "tok", "mybot", "agent-val", ""))

	var out bytes.Buffer
	err := execSecretRM(ctx, s, "tok", "mybot", &out, noSIGHUP)
	require.NoError(t, err)

	// Agent-scoped secret gone; global still present and returned for mybot.
	val, scope, getErr := s.Get(ctx, "tok", "mybot")
	require.NoError(t, getErr)
	assert.Equal(t, "global-val", val)
	assert.Equal(t, "global", scope, "should fall back to global after agent row deleted")

	// Also reachable via other agents.
	val2, scope2, getErr2 := s.Get(ctx, "tok", "other")
	require.NoError(t, getErr2)
	assert.Equal(t, "global-val", val2)
	assert.Equal(t, "global", scope2)
}

// TestSecretMasterRotate verifies that "secret master rotate" calls MasterRotate.
func TestSecretMasterRotate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	require.NoError(t, s.Set(ctx, "tok", "", "val", ""))

	var out bytes.Buffer
	err := execSecretMasterRotate(ctx, s, &out, noSIGHUP)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "rotated master key")

	// Secret should still decrypt correctly.
	val, _, getErr := s.Get(ctx, "tok", "")
	require.NoError(t, getErr)
	assert.Equal(t, "val", val)
}
