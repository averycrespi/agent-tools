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

// confirmYes is a confirmFn stub that always approves (skips interactive prompt).
func confirmYes() (bool, error) { return true, nil }

// confirmNo is a confirmFn stub that always cancels (simulates user typing "n").
func confirmNo() (bool, error) { return false, nil }

// TestSecretAdd_Global verifies that "secret add <name>" with non-TTY stdin stores a global row.
func TestSecretAdd_Global(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)

	var out bytes.Buffer
	err := execSecretAdd(context.Background(), s, "mytoken", "", "my token", []string{"**"}, strings.NewReader("s3cr3t\n"), &out, false, noSIGHUP)
	require.NoError(t, err)

	val, scope, _, getErr := s.Get(context.Background(), "mytoken", "")
	require.NoError(t, getErr)
	assert.Equal(t, "s3cr3t", val)
	assert.Equal(t, "global", scope)
}

// TestSecretAdd_Agent verifies that "secret add <name> --agent <a>" creates an agent-scoped row.
func TestSecretAdd_Agent(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)

	var out bytes.Buffer
	err := execSecretAdd(context.Background(), s, "mytoken", "mybot", "desc", []string{"**"}, strings.NewReader("s3cr3t\n"), &out, false, noSIGHUP)
	require.NoError(t, err)

	val, scope, _, getErr := s.Get(context.Background(), "mytoken", "mybot")
	require.NoError(t, getErr)
	assert.Equal(t, "s3cr3t", val)
	assert.Equal(t, "agent:mybot", scope)
}

// TestSecretAdd_ShadowWarning verifies that a shadow warning is printed when
// an agent-scoped set shadows an existing global row.
func TestSecretAdd_ShadowWarning(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	// Set a global row first.
	require.NoError(t, s.Set(ctx, "mytoken", "", "global-val", "global", []string{"**"}))

	var out bytes.Buffer
	err := execSecretAdd(ctx, s, "mytoken", "mybot", "desc", []string{"**"}, strings.NewReader("agent-val\n"), &out, false, noSIGHUP)
	require.NoError(t, err)

	// Shadow warning must appear in output.
	assert.Contains(t, out.String(), `warning: secret "mytoken" is also set globally`)
	assert.Contains(t, out.String(), `"mybot"`)
}

// TestSecretAdd_NoShadowWarning verifies that no shadow warning is printed
// when set globally (even if agent rows exist).
func TestSecretAdd_NoShadowWarning(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	var out bytes.Buffer
	err := execSecretAdd(ctx, s, "tok", "", "", []string{"**"}, strings.NewReader("val\n"), &out, false, noSIGHUP)
	require.NoError(t, err)
	assert.Empty(t, out.String())
}

// TestSecretAdd_RefusesTTY verifies that set returns an error when stdin is a TTY.
func TestSecretAdd_RefusesTTY(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)

	var out bytes.Buffer
	// isTTY=true simulates a TTY stdin.
	err := execSecretAdd(context.Background(), s, "tok", "", "", []string{"**"}, strings.NewReader(""), &out, true, noSIGHUP)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pipe")
}

// TestSecretList verifies that "secret list" prints metadata rows without values.
func TestSecretList(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	require.NoError(t, s.Set(ctx, "alpha", "", "v1", "desc alpha", []string{"**"}))
	require.NoError(t, s.Set(ctx, "beta", "mybot", "v2", "desc beta", []string{"**"}))

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

// TestSecretUpdate verifies that "secret update <name>" updates the value.
func TestSecretUpdate(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	require.NoError(t, s.Set(ctx, "tok", "", "old-val", "", []string{"**"}))

	var out bytes.Buffer
	err := execSecretUpdate(ctx, s, "tok", "", strings.NewReader("new-val\n"), &out, false, confirmYes, noSIGHUP)
	require.NoError(t, err)

	val, _, _, getErr := s.Get(ctx, "tok", "")
	require.NoError(t, getErr)
	assert.Equal(t, "new-val", val)
}

// TestSecretUpdate_Agent verifies agent-scoped update.
func TestSecretUpdate_Agent(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	require.NoError(t, s.Set(ctx, "tok", "mybot", "old-val", "", []string{"**"}))

	var out bytes.Buffer
	err := execSecretUpdate(ctx, s, "tok", "mybot", strings.NewReader("new-val\n"), &out, false, confirmYes, noSIGHUP)
	require.NoError(t, err)

	val, _, _, getErr := s.Get(ctx, "tok", "mybot")
	require.NoError(t, getErr)
	assert.Equal(t, "new-val", val)
}

// TestSecretRM verifies that "secret rm <name>" removes the secret.
func TestSecretRM(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	require.NoError(t, s.Set(ctx, "tok", "", "val", "", []string{"**"}))

	var out bytes.Buffer
	err := execSecretRM(ctx, s, "tok", "", &out, confirmYes, noSIGHUP)
	require.NoError(t, err)

	_, _, _, getErr := s.Get(ctx, "tok", "")
	assert.True(t, errors.Is(getErr, secrets.ErrNotFound))
}

// TestSecretRM_Cancelled verifies that a cancelled confirmation leaves the
// secret untouched.
func TestSecretRM_Cancelled(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	require.NoError(t, s.Set(ctx, "tok", "", "val", "", []string{"**"}))

	var out bytes.Buffer
	err := execSecretRM(ctx, s, "tok", "", &out, confirmNo, noSIGHUP)
	require.NoError(t, err, "cancelled confirmation should not be an error")

	val, _, _, getErr := s.Get(ctx, "tok", "")
	require.NoError(t, getErr)
	assert.Equal(t, "val", val, "secret should still exist after cancel")
}

// TestSecretRM_Agent verifies that "--agent" scopes the deletion.
func TestSecretRM_Agent(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	require.NoError(t, s.Set(ctx, "tok", "", "global-val", "", []string{"**"}))
	require.NoError(t, s.Set(ctx, "tok", "mybot", "agent-val", "", []string{"**"}))

	var out bytes.Buffer
	err := execSecretRM(ctx, s, "tok", "mybot", &out, confirmYes, noSIGHUP)
	require.NoError(t, err)

	// Agent-scoped secret gone; global still present and returned for mybot.
	val, scope, _, getErr := s.Get(ctx, "tok", "mybot")
	require.NoError(t, getErr)
	assert.Equal(t, "global-val", val)
	assert.Equal(t, "global", scope, "should fall back to global after agent row deleted")

	// Also reachable via other agents.
	val2, scope2, _, getErr2 := s.Get(ctx, "tok", "other")
	require.NoError(t, getErr2)
	assert.Equal(t, "global-val", val2)
	assert.Equal(t, "global", scope2)
}

// TestSecretRM_NotFound verifies that rm on a non-existent secret returns
// ErrNotFound without invoking the confirmation prompt.
func TestSecretRM_NotFound(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)

	var out bytes.Buffer
	confirmCalled := false
	confirm := func() (bool, error) {
		confirmCalled = true
		return true, nil
	}
	err := execSecretRM(context.Background(), s, "ghost", "", &out, confirm, noSIGHUP)
	require.Error(t, err)
	assert.ErrorIs(t, err, secrets.ErrNotFound)
	assert.False(t, confirmCalled, "confirm prompt must not be shown for a non-existent secret")
}

// TestSecretRM_AgentScopeMismatch verifies that an agent-scoped rm against a
// name that exists only globally reports NotFound (no fallback to the global
// row) and skips the confirmation prompt.
func TestSecretRM_AgentScopeMismatch(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	require.NoError(t, s.Set(ctx, "tok", "", "global-val", "", []string{"**"}))

	var out bytes.Buffer
	confirmCalled := false
	confirm := func() (bool, error) {
		confirmCalled = true
		return true, nil
	}
	err := execSecretRM(ctx, s, "tok", "mybot", &out, confirm, noSIGHUP)
	require.Error(t, err)
	assert.ErrorIs(t, err, secrets.ErrNotFound)
	assert.False(t, confirmCalled)
}
