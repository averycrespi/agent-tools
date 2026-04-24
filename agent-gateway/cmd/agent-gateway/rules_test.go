package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/store"
)

// writeRuleHCL is a helper that writes an HCL rule file into dir.
func writeRuleHCL(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

// setupRulesDir redirects XDG_CONFIG_HOME and XDG_DATA_HOME to tempdirs,
// creates the rules directory, and returns the rules directory path.
func setupRulesDir(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir := paths.RulesDir()
	require.NoError(t, os.MkdirAll(dir, 0o750))
	return dir
}

// runRulesCheck executes "rules check" against a command tree built from
// newRootCmd. Returns stdout, stderr, and the execution error.
func runRulesCheck(t *testing.T) (stdout, stderr string, err error) {
	t.Helper()
	return runRulesCheckArgs(t, []string{"rules", "check"})
}

// runRulesCheckStrict executes "rules check --strict".
func runRulesCheckStrict(t *testing.T) (stdout, stderr string, err error) {
	t.Helper()
	return runRulesCheckArgs(t, []string{"rules", "check", "--strict"})
}

// runRulesCheckArgs executes "rules check" with the given args.
func runRulesCheckArgs(t *testing.T, args []string) (stdout, stderr string, err error) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return outBuf.String(), errBuf.String(), err
}

// setupRulesDirWithStateDB sets up both the rules directory AND a state DB
// at paths.StateDB(), returning the rules dir and a db-backed secret setter.
// The db is opened read-write (via store.Open) so tests can populate secrets.
func setupRulesDirWithStateDB(t *testing.T) (rulesDir string, stateDBPath string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir := paths.RulesDir()
	require.NoError(t, os.MkdirAll(dir, 0o750))
	dbPath := paths.StateDB()
	require.NoError(t, os.MkdirAll(filepath.Dir(dbPath), 0o700))
	return dir, dbPath
}

func TestRulesCheck_ValidReturnsZero(t *testing.T) {
	dir := setupRulesDir(t)
	writeRuleHCL(t, dir, "00-allow.hcl", `
rule "github-issue-create" {
  agents  = ["claude-review"]
  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/repos/*/*/issues"
  }
  verdict = "allow"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.gh_bot}"
    }
  }
}
`)
	writeRuleHCL(t, dir, "10-deny.hcl", `
rule "deny-all" {
  match {
    host = "example.com"
  }
  verdict = "deny"
}
`)

	out, _, err := runRulesCheck(t)
	require.NoError(t, err, "valid rules should exit 0")
	assert.Contains(t, out, "ok:")
	assert.Contains(t, out, "2 rules")
	assert.Contains(t, out, "2 files")
}

func TestRulesCheck_InvalidReturnsNonZero(t *testing.T) {
	dir := setupRulesDir(t)
	writeRuleHCL(t, dir, "bad.hcl", `
rule "bad-rule" {
  # missing match block and verdict
}
`)

	_, stderr, err := runRulesCheck(t)
	require.Error(t, err, "invalid rules should exit non-zero")
	assert.NotEmpty(t, stderr, "error message should appear on stderr")
}

func TestRulesCheck_MissingSecretIsWarningNotError(t *testing.T) {
	// Rule references ${secrets.does_not_exist}; exits 0 but prints warning.
	dir := setupRulesDir(t)
	writeRuleHCL(t, dir, "secret-rule.hcl", `
rule "github-issue-create" {
  agents  = ["claude-review"]
  match {
    host   = "api.github.com"
    method = "POST"
    path   = "/repos/*/*/issues"
  }
  verdict = "allow"
  inject {
    replace_header = {
      "Authorization" = "Bearer ${secrets.does_not_exist}"
    }
  }
}
`)

	out, _, err := runRulesCheck(t)
	require.NoError(t, err, "missing secret should exit 0 (warning only)")
	assert.Contains(t, out, "warning:")
	assert.Contains(t, out, "does_not_exist")
}

func TestRulesCheck_AgentRefsNotFlaggedAsMissingSecrets(t *testing.T) {
	// ${agent.name} and ${agent.id} must not produce a missing-secret warning.
	dir := setupRulesDir(t)
	writeRuleHCL(t, dir, "agent-ref.hcl", `
rule "agent-header" {
  match   { host = "api.example.com" }
  verdict = "allow"
  inject {
    replace_header = {
      "X-Agent-Name" = "${agent.name}"
      "X-Agent-ID"   = "${agent.id}"
    }
  }
}
`)

	out, _, err := runRulesCheck(t)
	require.NoError(t, err, "agent refs should not be flagged as missing secrets")
	assert.NotContains(t, out, "warning:")
	assert.Contains(t, out, "ok:")
}

// TestRulesCheck_WithCoverageWarnings_PrintsThem verifies that when a rule
// references a secret whose allowed_hosts don't cover the rule host,
// the coverage warning is printed and the command exits 0 (no --strict).
func TestRulesCheck_WithCoverageWarnings_PrintsThem(t *testing.T) {
	rulesDir, dbPath := setupRulesDirWithStateDB(t)

	// Rule matches "*.example.com"; secret only allows "api.github.com".
	writeRuleHCL(t, rulesDir, "rule.hcl", `
rule "test-rule" {
  match   { host = "*.example.com" }
  verdict = "allow"
  inject {
    replace_header = { "Authorization" = "Bearer ${secrets.my_token}" }
  }
}
`)

	// Populate the state DB with a secret bound to the wrong host.
	db, err := store.Open(dbPath)
	require.NoError(t, err)
	s := newTestSecretStore(t, db)
	require.NoError(t, s.Set(context.Background(), "my_token", "", "val", "", []string{"api.github.com"}))
	require.NoError(t, db.Close())

	out, _, err := runRulesCheck(t)
	require.NoError(t, err, "coverage warnings should exit 0 without --strict")
	assert.Contains(t, out, "warning:", "expected a coverage warning in stdout")
	assert.Contains(t, out, "my_token", "warning should name the secret")
}

// TestRulesCheck_Strict_FailsOnWarnings verifies that --strict causes a
// non-zero exit when coverage warnings are present.
func TestRulesCheck_Strict_FailsOnWarnings(t *testing.T) {
	rulesDir, dbPath := setupRulesDirWithStateDB(t)

	writeRuleHCL(t, rulesDir, "rule.hcl", `
rule "test-rule" {
  match   { host = "*.example.com" }
  verdict = "allow"
  inject {
    replace_header = { "Authorization" = "Bearer ${secrets.my_token}" }
  }
}
`)

	db, err := store.Open(dbPath)
	require.NoError(t, err)
	s := newTestSecretStore(t, db)
	require.NoError(t, s.Set(context.Background(), "my_token", "", "val", "", []string{"api.github.com"}))
	require.NoError(t, db.Close())

	_, _, err = runRulesCheckStrict(t)
	require.Error(t, err, "--strict should exit non-zero when coverage warnings exist")
}

// TestRulesCheck_NoStateDB_SkipsCoverage verifies that when no state.db
// exists, the command prints a note and exits 0 without panicking.
func TestRulesCheck_NoStateDB_SkipsCoverage(t *testing.T) {
	dir := setupRulesDir(t)
	// No state DB created — XDG_DATA_HOME points to an empty tempdir.
	writeRuleHCL(t, dir, "rule.hcl", `
rule "test-rule" {
  match   { host = "api.github.com" }
  verdict = "allow"
  inject {
    replace_header = { "Authorization" = "Bearer ${secrets.my_token}" }
  }
}
`)

	out, _, err := runRulesCheck(t)
	require.NoError(t, err, "missing state DB should exit 0")
	assert.Contains(t, out, "skipping secret coverage check", "should note that coverage check was skipped")
}
