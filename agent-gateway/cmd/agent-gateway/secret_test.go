package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

// registryWithAgent creates a registry backed by db and registers the named
// agent so execSecretAdd's existence check passes in tests that use --agent.
func registryWithAgent(t *testing.T, db *sql.DB, name string) agents.Registry {
	t.Helper()
	r, err := agents.NewRegistry(context.Background(), db)
	require.NoError(t, err)
	_, err = r.Add(context.Background(), name, "")
	require.NoError(t, err)
	return r
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
	err := execSecretAdd(context.Background(), s, nil, "mytoken", "", "my token", []string{"**"}, strings.NewReader("s3cr3t\n"), &out, false, noSIGHUP, t.TempDir())
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
	r := registryWithAgent(t, db, "mybot")

	var out bytes.Buffer
	err := execSecretAdd(context.Background(), s, r, "mytoken", "mybot", "desc", []string{"**"}, strings.NewReader("s3cr3t\n"), &out, false, noSIGHUP, t.TempDir())
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
	r := registryWithAgent(t, db, "mybot")
	ctx := context.Background()

	// Set a global row first.
	require.NoError(t, s.Set(ctx, "mytoken", "", "global-val", "global", []string{"**"}))

	var out bytes.Buffer
	err := execSecretAdd(ctx, s, r, "mytoken", "mybot", "desc", []string{"**"}, strings.NewReader("agent-val\n"), &out, false, noSIGHUP, t.TempDir())
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
	err := execSecretAdd(ctx, s, nil, "tok", "", "", []string{"**"}, strings.NewReader("val\n"), &out, false, noSIGHUP, t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, "added: tok\n", out.String(),
		"success message must be printed, and no shadow warning when set globally")
}

// TestSecretAdd_AgentDoesNotExist verifies that adding an agent-scoped secret
// whose target agent isn't registered fails with a clear hint, without
// touching the secrets store.
func TestSecretAdd_AgentDoesNotExist(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	r, err := agents.NewRegistry(context.Background(), db)
	require.NoError(t, err)

	var out bytes.Buffer
	err = execSecretAdd(context.Background(), s, r, "tok", "ghost", "", []string{"**"}, strings.NewReader("v\n"), &out, false, noSIGHUP, t.TempDir())
	require.Error(t, err)

	msg := err.Error()
	assert.Contains(t, msg, `agent "ghost"`)
	assert.Contains(t, msg, "does not exist")
	assert.Contains(t, msg, "agent add ghost")

	// Secret must not have been written.
	_, _, _, getErr := s.Get(context.Background(), "tok", "ghost")
	assert.ErrorIs(t, getErr, secrets.ErrNotFound)
}

// TestSecretAdd_DuplicateMessage verifies that re-adding an existing secret
// returns a user-friendly error that names the secret and suggests
// "secret update", rather than leaking a SQLite constraint message.
func TestSecretAdd_DuplicateMessage(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	var out bytes.Buffer
	require.NoError(t, execSecretAdd(ctx, s, nil, "dupe", "", "", []string{"**"}, strings.NewReader("v1\n"), &out, false, noSIGHUP, t.TempDir()))

	out.Reset()
	err := execSecretAdd(ctx, s, nil, "dupe", "", "", []string{"**"}, strings.NewReader("v2\n"), &out, false, noSIGHUP, t.TempDir())
	require.Error(t, err)

	msg := err.Error()
	assert.Contains(t, msg, `"dupe"`)
	assert.Contains(t, msg, "already exists")
	assert.Contains(t, msg, "secret update dupe")
	assert.NotContains(t, msg, "sqlite", "raw sqlite error must not leak")
}

// TestSecretAdd_DuplicateMessage_Agent verifies the agent-scoped duplicate
// message includes the --agent flag in the suggested update command.
func TestSecretAdd_DuplicateMessage_Agent(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	r := registryWithAgent(t, db, "mybot")
	ctx := context.Background()

	var out bytes.Buffer
	require.NoError(t, execSecretAdd(ctx, s, r, "dupe", "mybot", "", []string{"**"}, strings.NewReader("v1\n"), &out, false, noSIGHUP, t.TempDir()))

	out.Reset()
	err := execSecretAdd(ctx, s, r, "dupe", "mybot", "", []string{"**"}, strings.NewReader("v2\n"), &out, false, noSIGHUP, t.TempDir())
	require.Error(t, err)

	msg := err.Error()
	assert.Contains(t, msg, `"dupe"`)
	assert.Contains(t, msg, `agent "mybot"`)
	assert.Contains(t, msg, "secret update dupe --agent mybot")
}

// TestSecretAdd_RefusesTTY verifies that set returns an error when stdin is a TTY.
func TestSecretAdd_RefusesTTY(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)

	var out bytes.Buffer
	// isTTY=true simulates a TTY stdin.
	err := execSecretAdd(context.Background(), s, nil, "tok", "", "", []string{"**"}, strings.NewReader(""), &out, true, noSIGHUP, t.TempDir())
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
	err := execSecretList(ctx, s, "text", &out)
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

// TestSecretList_JSONOutput verifies the JSON output contains exactly the 6
// expected fields and no sensitive fields.
func TestSecretList_JSONOutput(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	require.NoError(t, s.Set(ctx, "alpha", "", "cipher-val", "desc alpha", []string{"api.example.com"}))
	require.NoError(t, s.Set(ctx, "beta", "mybot", "nonce-val", "desc beta", []string{"*.example.com"}))

	var out bytes.Buffer
	err := execSecretList(ctx, s, "json", &out)
	require.NoError(t, err)

	raw := out.String()

	// Must NOT contain sensitive or excluded fields.
	assert.NotContains(t, raw, "cipher-val", "plaintext value must not appear in JSON output")
	assert.NotContains(t, raw, "nonce-val", "plaintext value must not appear in JSON output")
	assert.NotContains(t, raw, "ciphertext", "ciphertext field must not appear in JSON output")
	assert.NotContains(t, raw, "nonce", "nonce field must not appear in JSON output")
	assert.NotContains(t, raw, "description", "description field must not appear in JSON output")
	assert.NotContains(t, raw, "hash", "hash field must not appear in JSON output")
	assert.NotContains(t, raw, `"value"`, "value field must not appear in JSON output")

	// Decode and verify structure.
	var payload struct {
		Secrets []struct {
			Name         string   `json:"name"`
			Scope        string   `json:"scope"`
			AllowedHosts []string `json:"allowed_hosts"`
			CreatedAt    string   `json:"created_at"`
			RotatedAt    string   `json:"rotated_at"`
			LastUsedAt   *string  `json:"last_used_at"`
		} `json:"secrets"`
	}
	require.NoError(t, json.NewDecoder(&out).Decode(&payload), "JSON must be valid")
	require.Len(t, payload.Secrets, 2)

	names := []string{payload.Secrets[0].Name, payload.Secrets[1].Name}
	assert.Contains(t, names, "alpha")
	assert.Contains(t, names, "beta")

	for _, sec := range payload.Secrets {
		assert.NotEmpty(t, sec.Scope, "scope must be set")
		assert.NotEmpty(t, sec.AllowedHosts, "allowed_hosts must be set")
		assert.NotEmpty(t, sec.CreatedAt, "created_at must be set")
		assert.NotEmpty(t, sec.RotatedAt, "rotated_at must be set")
		// No secret has been used, so last_used_at must be null.
		assert.Nil(t, sec.LastUsedAt, "last_used_at must be null for unused secret")
	}

	// Verify scopes.
	for _, sec := range payload.Secrets {
		switch sec.Name {
		case "alpha":
			assert.Equal(t, "global", sec.Scope)
			assert.Equal(t, []string{"api.example.com"}, sec.AllowedHosts)
		case "beta":
			assert.Equal(t, "agent:mybot", sec.Scope)
			assert.Equal(t, []string{"*.example.com"}, sec.AllowedHosts)
		}
	}
}

// TestSecretList_TextOutput_Default verifies that output="" and output="text"
// both produce the tab-separated text table (unchanged behaviour).
func TestSecretList_TextOutput_Default(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	require.NoError(t, s.Set(ctx, "gamma", "", "val", "", []string{"**"}))

	for _, output := range []string{"", "text"} {
		var out bytes.Buffer
		require.NoError(t, execSecretList(ctx, s, output, &out))
		assert.Contains(t, out.String(), "NAME", "output=%q should produce text table", output)
		assert.Contains(t, out.String(), "gamma", "output=%q should list secret name", output)
	}
}

// TestSecretList_InvalidOutput_Errors verifies that an unsupported output
// format returns a non-nil error with a clear message.
func TestSecretList_InvalidOutput_Errors(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	var out bytes.Buffer
	err := execSecretList(ctx, s, "yaml", &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "yaml")
	assert.Contains(t, err.Error(), "--output")
}

// TestSecretUpdate verifies that "secret update <name>" updates the value.
func TestSecretUpdate(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	require.NoError(t, s.Set(ctx, "tok", "", "old-val", "", []string{"**"}))

	var out bytes.Buffer
	err := execSecretUpdate(ctx, s, "tok", "", strings.NewReader("new-val\n"), &out, false, confirmYes, noSIGHUP, t.TempDir())
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
	err := execSecretUpdate(ctx, s, "tok", "mybot", strings.NewReader("new-val\n"), &out, false, confirmYes, noSIGHUP, t.TempDir())
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
	err := execSecretRM(ctx, s, "tok", "", &out, confirmYes, noSIGHUP, t.TempDir())
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
	err := execSecretRM(ctx, s, "tok", "", &out, confirmNo, noSIGHUP, t.TempDir())
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
	err := execSecretRM(ctx, s, "tok", "mybot", &out, confirmYes, noSIGHUP, t.TempDir())
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
	err := execSecretRM(context.Background(), s, "ghost", "", &out, confirm, noSIGHUP, t.TempDir())
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
	err := execSecretRM(ctx, s, "tok", "mybot", &out, confirm, noSIGHUP, t.TempDir())
	require.Error(t, err)
	assert.ErrorIs(t, err, secrets.ErrNotFound)
	assert.False(t, confirmCalled)
}

// --- Coverage warning tests ---

// coverageRulesDir writes a single rule referencing ${secrets.<name>} bound
// to host <ruleHost>, then returns the temp directory path.
func coverageRulesDir(t *testing.T, secretName, ruleHost string) string {
	t.Helper()
	dir := t.TempDir()
	writeRule(t, dir, "r.hcl", `
rule "r" {
  match  { host = "`+ruleHost+`" }
  verdict = "allow"
  inject {
    replace_header = { "Authorization" = "Bearer ${secrets.`+secretName+`}" }
  }
}
`)
	return dir
}

// TestSecretAdd_PrintsCoverageWarnings verifies that "secret add" prints a
// coverage warning when the new secret's allowed_hosts do not cover a rule
// that references it.
func TestSecretAdd_PrintsCoverageWarnings(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	rulesDir := coverageRulesDir(t, "gh", "api.github.com")

	var out bytes.Buffer
	// Secret bound to api.example.com — does NOT cover the rule host api.github.com.
	err := execSecretAdd(ctx, s, nil, "gh", "", "", []string{"api.example.com"}, strings.NewReader("val\n"), &out, false, noSIGHUP, rulesDir)
	require.NoError(t, err)

	output := out.String()
	assert.Contains(t, output, "warning:")
	assert.Contains(t, output, "gh")
}

// TestSecretAdd_NoCoverageWarningWhenCovered verifies no coverage warning is
// printed when the secret's allowed_hosts adequately cover the rule.
func TestSecretAdd_NoCoverageWarningWhenCovered(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	rulesDir := coverageRulesDir(t, "gh", "api.github.com")

	var out bytes.Buffer
	err := execSecretAdd(ctx, s, nil, "gh", "", "", []string{"api.github.com"}, strings.NewReader("val\n"), &out, false, noSIGHUP, rulesDir)
	require.NoError(t, err)

	output := out.String()
	assert.NotContains(t, output, "warning:")
}

// TestSecretUpdate_PrintsCoverageWarnings verifies that "secret update" prints
// coverage warnings after rotating a secret that is under-covered.
func TestSecretUpdate_PrintsCoverageWarnings(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	// Initial set with narrow host binding.
	require.NoError(t, s.Set(ctx, "gh", "", "old", "", []string{"api.example.com"}))

	rulesDir := coverageRulesDir(t, "gh", "api.github.com")

	var out bytes.Buffer
	err := execSecretUpdate(ctx, s, "gh", "", strings.NewReader("new\n"), &out, false, confirmYes, noSIGHUP, rulesDir)
	require.NoError(t, err)

	output := out.String()
	assert.Contains(t, output, "warning:")
	assert.Contains(t, output, "gh")
}

// TestSecretRM_PrintsCoverageWarnings verifies that "secret rm" prints
// coverage warnings after deleting a secret that other secrets still
// leave dangling.
func TestSecretRM_PrintsCoverageWarnings(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	// "other" secret with narrow coverage; the rule references it.
	require.NoError(t, s.Set(ctx, "other", "", "v", "", []string{"api.example.com"}))
	// "tok" is the one being deleted.
	require.NoError(t, s.Set(ctx, "tok", "", "v2", "", []string{"**"}))

	// Rule references "other" but the host doesn't match → warning after
	// tok is removed (other still exists with narrow binding).
	rulesDir := coverageRulesDir(t, "other", "api.github.com")

	var out bytes.Buffer
	err := execSecretRM(ctx, s, "tok", "", &out, confirmYes, noSIGHUP, rulesDir)
	require.NoError(t, err)

	output := out.String()
	assert.Contains(t, output, "warning:")
	assert.Contains(t, output, "other")
}

// TestSecretBind_PrintsCoverageWarnings verifies that "secret bind" prints
// coverage warnings when the resulting binding still doesn't cover a rule.
func TestSecretBind_PrintsCoverageWarnings(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	// Secret starts with a narrow binding.
	require.NoError(t, s.Set(ctx, "gh", "", "v", "", []string{"api.example.com"}))

	// Rule references "gh" bound to api.github.com — binding api.example.com
	// still won't cover it.
	rulesDir := coverageRulesDir(t, "gh", "api.github.com")

	var out bytes.Buffer
	// Adding another narrow host — doesn't cover the rule host.
	err := execSecretBind(ctx, s, "gh", "", []string{"other.example.com"}, &out, noSIGHUP, rulesDir)
	require.NoError(t, err)

	output := out.String()
	assert.Contains(t, output, "warning:")
	assert.Contains(t, output, "gh")
}

// TestSecretUnbind_PrintsCoverageWarnings verifies that "secret unbind"
// prints coverage warnings when removing a host glob leaves a rule dangling.
func TestSecretUnbind_PrintsCoverageWarnings(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	// Secret starts with two bindings: one covering the rule, one extra.
	require.NoError(t, s.Set(ctx, "gh", "", "v", "", []string{"api.github.com", "other.com"}))

	// Rule references "gh" for api.github.com.
	rulesDir := coverageRulesDir(t, "gh", "api.github.com")

	var out bytes.Buffer
	// Unbind api.github.com — should now warn.
	err := execSecretUnbind(ctx, s, "gh", "", []string{"api.github.com"}, &out, noSIGHUP, rulesDir)
	require.NoError(t, err)

	output := out.String()
	assert.Contains(t, output, "warning:")
	assert.Contains(t, output, "gh")
}

// TestSecretBind_NoCoverageWarning verifies no warning when the bind resolves coverage.
func TestSecretBind_NoCoverageWarning(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	// Secret starts with a different binding.
	require.NoError(t, s.Set(ctx, "gh", "", "v", "", []string{"other.example.com"}))

	// Rule for api.github.com.
	rulesDir := coverageRulesDir(t, "gh", "api.github.com")

	var out bytes.Buffer
	// Bind the correct host — coverage is now satisfied.
	err := execSecretBind(ctx, s, "gh", "", []string{"api.github.com"}, &out, noSIGHUP, rulesDir)
	require.NoError(t, err)

	output := out.String()
	assert.NotContains(t, output, "warning:")
}

func TestSecretRmCmd_HasLongHelp(t *testing.T) {
	cmd := newSecretRMCmd()
	require.NotEmpty(t, cmd.Long)
	require.Contains(t, cmd.Long, "Immediate consequences")
	require.Contains(t, cmd.Long, "Recovery")
	require.Contains(t, cmd.Long, "403")
}

// TestPrintCoverageAfterMutation_SilentOnBadRulesDir verifies that a bad
// rules dir does not cause the mutation command to fail.
func TestPrintCoverageAfterMutation_SilentOnBadRulesDir(t *testing.T) {
	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	require.NoError(t, s.Set(ctx, "tok", "", "v", "", []string{"**"}))

	var out bytes.Buffer
	// Non-existent rules dir — must not panic or return error.
	printCoverageAfterMutation(ctx, &out, s, "/nonexistent/rules.d")

	// No output expected (silent failure).
	assert.Empty(t, out.String())
}
