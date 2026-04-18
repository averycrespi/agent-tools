package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
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
	var outBuf, errBuf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	cmd.SetArgs([]string{"rules", "check"})
	err = cmd.Execute()
	return outBuf.String(), errBuf.String(), err
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
