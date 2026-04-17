package rules_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/rules"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParse_SimpleAllow(t *testing.T) {
	rs, warnings, err := rules.ParseDir("testdata/simple")
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Len(t, rs, 1)
	assert.Equal(t, "github-issue-create", rs[0].Name)
	assert.Equal(t, []string{"claude-review"}, rs[0].Agents)
	assert.Equal(t, "api.github.com", rs[0].Match.Host)
	assert.Equal(t, "POST", rs[0].Match.Method)
	assert.Equal(t, "/repos/*/*/issues", rs[0].Match.Path)
	assert.Equal(t, "^2022-", rs[0].Match.Headers["X-GitHub-Api-Version"])
	assert.Equal(t, "allow", rs[0].Verdict)
	require.NotNil(t, rs[0].Inject)
	assert.Equal(t, "Bearer ${secrets.gh_bot}", rs[0].Inject.SetHeaders["Authorization"])
}

func TestParse_JSONBodyMatcher(t *testing.T) {
	rs, warnings, err := rules.ParseDir("testdata/labelled-blocks")
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Len(t, rs, 1)
	assert.Equal(t, "github-issue-label-check", rs[0].Name)
	require.NotNil(t, rs[0].Match.JSONBody)
	require.Len(t, rs[0].Match.JSONBody.Paths, 2)
	assert.Equal(t, "$.title", rs[0].Match.JSONBody.Paths[0].Path)
	assert.Equal(t, `^\[bot\]`, rs[0].Match.JSONBody.Paths[0].Matches)
	assert.Equal(t, "$.labels[*]", rs[0].Match.JSONBody.Paths[1].Path)
	assert.Equal(t, "^automation$", rs[0].Match.JSONBody.Paths[1].Matches)
}

func TestParse_EmptyAgentsIsError(t *testing.T) {
	dir := t.TempDir()
	writeHCL(t, dir, "empty-agents.hcl", `
rule "bad-rule" {
  agents  = []
  match   { host = "example.com" }
  verdict = "allow"
}
`)
	_, _, err := rules.ParseDir(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agents")
}

func TestParse_LexicalOrder(t *testing.T) {
	dir := t.TempDir()
	// 00-a.hcl defines rule A; 10-b.hcl defines rule B.
	// Lexical order means A comes first.
	writeHCL(t, dir, "00-a.hcl", `
rule "rule-a" {
  match   { host = "a.example.com" }
  verdict = "allow"
}
`)
	writeHCL(t, dir, "10-b.hcl", `
rule "rule-b" {
  match   { host = "b.example.com" }
  verdict = "deny"
}
`)
	rs, _, err := rules.ParseDir(dir)
	require.NoError(t, err)
	require.Len(t, rs, 2)
	assert.Equal(t, "rule-a", rs[0].Name)
	assert.Equal(t, "rule-b", rs[1].Name)
}

func TestParse_UnknownBodyBlockIsError(t *testing.T) {
	dir := t.TempDir()
	writeHCL(t, dir, "bad-body.hcl", `
rule "bad-body" {
  match {
    host = "example.com"
    xml_body {
      something = "^test$"
    }
  }
  verdict = "allow"
}
`)
	_, _, err := rules.ParseDir(dir)
	require.Error(t, err)
}

func TestParse_DuplicateRuleNameIsError(t *testing.T) {
	dir := t.TempDir()
	writeHCL(t, dir, "a.hcl", `
rule "dup" {
  match   { host = "a.example.com" }
  verdict = "allow"
}
`)
	writeHCL(t, dir, "b.hcl", `
rule "dup" {
  match   { host = "b.example.com" }
  verdict = "deny"
}
`)
	_, _, err := rules.ParseDir(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate")
}

func TestParse_OmittedAgentsIsNil(t *testing.T) {
	dir := t.TempDir()
	writeHCL(t, dir, "no-agents.hcl", `
rule "open-rule" {
  match   { host = "example.com" }
  verdict = "allow"
}
`)
	rs, _, err := rules.ParseDir(dir)
	require.NoError(t, err)
	require.Len(t, rs, 1)
	assert.Nil(t, rs[0].Agents)
}

func TestParse_InvalidTemplateIsWarning(t *testing.T) {
	dir := t.TempDir()
	writeHCL(t, dir, "bad-tmpl.hcl", `
rule "bad-template" {
  match { host = "example.com" }
  verdict = "allow"
  inject {
    set_header = {
      "Authorization" = "Bearer ${invalid.token}"
    }
  }
}
`)
	_, warnings, err := rules.ParseDir(dir)
	require.NoError(t, err)
	require.NotEmpty(t, warnings)
	assert.Contains(t, warnings[0], "${invalid.token}")
}

func TestParse_ValidTemplates(t *testing.T) {
	dir := t.TempDir()
	writeHCL(t, dir, "valid-tmpl.hcl", `
rule "valid-templates" {
  match { host = "example.com" }
  verdict = "allow"
  inject {
    set_header = {
      "Authorization" = "Bearer ${secrets.my_token}"
      "X-Agent"       = "${agent.name}"
      "X-Agent-ID"    = "${agent.id}"
    }
  }
}
`)
	_, warnings, err := rules.ParseDir(dir)
	require.NoError(t, err)
	require.Empty(t, warnings)
}

func TestParse_FormBodyMatcher(t *testing.T) {
	dir := t.TempDir()
	writeHCL(t, dir, "form.hcl", `
rule "form-rule" {
  match {
    host   = "auth.example.com"
    method = "POST"
    path   = "/oauth/token"
    form_body {
      field "grant_type" {
        matches = "^client_credentials$"
      }
    }
  }
  verdict = "allow"
}
`)
	rs, _, err := rules.ParseDir(dir)
	require.NoError(t, err)
	require.Len(t, rs, 1)
	require.NotNil(t, rs[0].Match.FormBody)
	require.Len(t, rs[0].Match.FormBody.Fields, 1)
	assert.Equal(t, "grant_type", rs[0].Match.FormBody.Fields[0].Field)
	assert.Equal(t, "^client_credentials$", rs[0].Match.FormBody.Fields[0].Matches)
}

func TestParse_TextBodyMatcher(t *testing.T) {
	dir := t.TempDir()
	writeHCL(t, dir, "text.hcl", `
rule "text-rule" {
  match {
    host = "example.com"
    text_body {
      matches = "deploy-token-v2"
    }
  }
  verdict = "allow"
}
`)
	rs, _, err := rules.ParseDir(dir)
	require.NoError(t, err)
	require.Len(t, rs, 1)
	require.NotNil(t, rs[0].Match.TextBody)
	assert.Equal(t, "deploy-token-v2", rs[0].Match.TextBody.Matches)
}

func TestParse_MultipleBodyBlocksIsError(t *testing.T) {
	dir := t.TempDir()
	writeHCL(t, dir, "multi-body.hcl", `
rule "multi-body" {
  match {
    host = "example.com"
    json_body {
      jsonpath "$.x" { matches = "." }
    }
    text_body {
      matches = "."
    }
  }
  verdict = "allow"
}
`)
	_, _, err := rules.ParseDir(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "body")
}

func TestParse_InvalidRegexpIsError(t *testing.T) {
	dir := t.TempDir()
	writeHCL(t, dir, "bad-re.hcl", `
rule "bad-re" {
  match {
    host    = "example.com"
    headers = {
      "X-Foo" = "[invalid"
    }
  }
  verdict = "allow"
}
`)
	_, _, err := rules.ParseDir(dir)
	require.Error(t, err)
}

func TestParse_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	rs, warnings, err := rules.ParseDir(dir)
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Empty(t, rs)
}

// writeHCL is a test helper that writes content to filename inside dir.
func writeHCL(t *testing.T, dir, filename, content string) {
	t.Helper()
	path := filepath.Join(dir, filename)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}
