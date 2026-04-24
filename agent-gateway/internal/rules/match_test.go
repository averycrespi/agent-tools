package rules_test

import (
	"net/http"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/rules"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseInline writes hcl to a temp file in a temp dir and calls ParseDir.
func parseInline(t *testing.T, hcl string) []rules.Rule {
	t.Helper()
	dir := t.TempDir()
	writeHCL(t, dir, "inline.hcl", hcl)
	rs, _, err := rules.ParseDir(dir)
	require.NoError(t, err)
	return rs
}

// TestEvaluate_HostGlob covers exact match, single-label *, and multi-label **.
func TestEvaluate_HostGlob(t *testing.T) {
	rs := parseInline(t, `
rule "a" {
  match {
    host = "api.github.com"
    path = "/**"
  }
  verdict = "allow"
}
rule "b" {
  match {
    host = "*.github.com"
    path = "/**"
  }
  verdict = "deny"
}
rule "c" {
  match {
    host = "**.enterprise.local"
    path = "/**"
  }
  verdict = "allow"
}
`)
	engine := rules.Compile(rs)

	// Exact match for "a" beats the wildcard in "b".
	m := engine.Evaluate(&rules.Request{Agent: "x", Host: "api.github.com", Method: "GET", Path: "/foo"})
	require.NotNil(t, m)
	assert.Equal(t, "a", m.Rule.Name)
	assert.Equal(t, 0, m.Index)

	// Single-label wildcard "*.github.com" matches "git.github.com".
	m = engine.Evaluate(&rules.Request{Agent: "x", Host: "git.github.com", Method: "GET", Path: "/foo"})
	require.NotNil(t, m)
	assert.Equal(t, "b", m.Rule.Name)
	assert.Equal(t, 1, m.Index)

	// "*.github.com" must NOT match two labels "a.b.github.com".
	m = engine.Evaluate(&rules.Request{Agent: "x", Host: "a.b.github.com", Method: "GET", Path: "/foo"})
	assert.Nil(t, m)

	// "**.enterprise.local" matches one prefix label.
	m = engine.Evaluate(&rules.Request{Agent: "x", Host: "git.enterprise.local", Method: "GET", Path: "/foo"})
	require.NotNil(t, m)
	assert.Equal(t, "c", m.Rule.Name)
	assert.Equal(t, 2, m.Index)

	// "**.enterprise.local" matches multiple prefix labels.
	m = engine.Evaluate(&rules.Request{Agent: "x", Host: "a.b.enterprise.local", Method: "GET", Path: "/foo"})
	require.NotNil(t, m)
	assert.Equal(t, "c", m.Rule.Name)

	// No rule matches a completely different host.
	m = engine.Evaluate(&rules.Request{Agent: "x", Host: "example.com", Method: "GET", Path: "/foo"})
	assert.Nil(t, m)
}

// TestEvaluate_PathGlob covers exact match, single-segment *, and multi-segment **.
func TestEvaluate_PathGlob(t *testing.T) {
	rs := parseInline(t, `
rule "exact" {
  match {
    host = "api.example.com"
    path = "/repos/octocat/hello"
  }
  verdict = "allow"
}
rule "single" {
  match {
    host = "api.example.com"
    path = "/repos/*/hello"
  }
  verdict = "deny"
}
rule "multi" {
  match {
    host = "api.example.com"
    path = "/repos/**"
  }
  verdict = "allow"
}
`)
	engine := rules.Compile(rs)

	// Exact path beats wildcards.
	m := engine.Evaluate(&rules.Request{Agent: "x", Host: "api.example.com", Method: "GET", Path: "/repos/octocat/hello"})
	require.NotNil(t, m)
	assert.Equal(t, "exact", m.Rule.Name)

	// Single-segment * matches one segment.
	m = engine.Evaluate(&rules.Request{Agent: "x", Host: "api.example.com", Method: "GET", Path: "/repos/other-user/hello"})
	require.NotNil(t, m)
	assert.Equal(t, "single", m.Rule.Name)

	// Single-segment * must NOT cross a slash; falls through to "multi".
	m = engine.Evaluate(&rules.Request{Agent: "x", Host: "api.example.com", Method: "GET", Path: "/repos/org/repo/hello"})
	require.NotNil(t, m)
	assert.Equal(t, "multi", m.Rule.Name)

	// ** matches deep paths.
	m = engine.Evaluate(&rules.Request{Agent: "x", Host: "api.example.com", Method: "GET", Path: "/repos/org/repo/issues/42"})
	require.NotNil(t, m)
	assert.Equal(t, "multi", m.Rule.Name)

	// No rule for a different root.
	m = engine.Evaluate(&rules.Request{Agent: "x", Host: "api.example.com", Method: "GET", Path: "/users/octocat"})
	assert.Nil(t, m)
}

// TestEvaluate_Method covers exact uppercase matching and mismatch failure.
func TestEvaluate_Method(t *testing.T) {
	rs := parseInline(t, `
rule "post-only" {
  match {
    host   = "api.example.com"
    path   = "/issues"
    method = "POST"
  }
  verdict = "allow"
}
`)
	engine := rules.Compile(rs)

	m := engine.Evaluate(&rules.Request{Agent: "x", Host: "api.example.com", Method: "POST", Path: "/issues"})
	require.NotNil(t, m)
	assert.Equal(t, "post-only", m.Rule.Name)

	// GET does not match a POST rule.
	m = engine.Evaluate(&rules.Request{Agent: "x", Host: "api.example.com", Method: "GET", Path: "/issues"})
	assert.Nil(t, m)

	// Method matching is case-insensitive: lowercase "post" matches rule "POST".
	m = engine.Evaluate(&rules.Request{Agent: "x", Host: "api.example.com", Method: "post", Path: "/issues"})
	require.NotNil(t, m)
	assert.Equal(t, "post-only", m.Rule.Name)

	// Mixed case on the request side also matches.
	m = engine.Evaluate(&rules.Request{Agent: "x", Host: "api.example.com", Method: "Post", Path: "/issues"})
	require.NotNil(t, m)
	assert.Equal(t, "post-only", m.Rule.Name)
}

// TestEvaluate_MethodCaseInsensitive_RuleLowercase verifies that a rule
// written with a lowercase method is normalised at compile time and matches
// an uppercase request.
func TestEvaluate_MethodCaseInsensitive_RuleLowercase(t *testing.T) {
	rs := parseInline(t, `
rule "lowercase-method" {
  match {
    host   = "api.example.com"
    path   = "/x"
    method = "post"
  }
  verdict = "allow"
}
`)
	engine := rules.Compile(rs)

	m := engine.Evaluate(&rules.Request{Agent: "x", Host: "api.example.com", Method: "POST", Path: "/x"})
	require.NotNil(t, m)
	assert.Equal(t, "lowercase-method", m.Rule.Name)
}

// TestEvaluate_PathCaseInsensitive verifies that path globs match regardless
// of case on either side (rule pattern or request path). Upstream services
// commonly normalise path case, so a deny rule on "/admin/*" that silently
// misses "/ADMIN/foo" would be a security trap.
func TestEvaluate_PathCaseInsensitive(t *testing.T) {
	rs := parseInline(t, `
rule "admin-deny" {
  match {
    host = "api.example.com"
    path = "/admin/*"
  }
  verdict = "deny"
}
`)
	engine := rules.Compile(rs)

	// Uppercase request path matches lowercase rule.
	m := engine.Evaluate(&rules.Request{Agent: "x", Host: "api.example.com", Method: "GET", Path: "/ADMIN/foo"})
	require.NotNil(t, m, "uppercase request path /ADMIN/foo should match rule /admin/*")
	assert.Equal(t, "admin-deny", m.Rule.Name)

	// Mixed case request path matches.
	m = engine.Evaluate(&rules.Request{Agent: "x", Host: "api.example.com", Method: "GET", Path: "/Admin/Foo"})
	require.NotNil(t, m)
	assert.Equal(t, "admin-deny", m.Rule.Name)

	// Original lowercase still matches.
	m = engine.Evaluate(&rules.Request{Agent: "x", Host: "api.example.com", Method: "GET", Path: "/admin/foo"})
	require.NotNil(t, m)
	assert.Equal(t, "admin-deny", m.Rule.Name)
}

// TestEvaluate_PathCaseInsensitive_RuleUppercase verifies that an uppercase
// path pattern is lowercased at compile time and still matches a lowercase
// request path.
func TestEvaluate_PathCaseInsensitive_RuleUppercase(t *testing.T) {
	rs := parseInline(t, `
rule "uppercase-path" {
  match {
    host = "api.example.com"
    path = "/ADMIN/*"
  }
  verdict = "deny"
}
`)
	engine := rules.Compile(rs)

	m := engine.Evaluate(&rules.Request{Agent: "x", Host: "api.example.com", Method: "GET", Path: "/admin/foo"})
	require.NotNil(t, m, "lowercase request path should match uppercase rule pattern")
	assert.Equal(t, "uppercase-path", m.Rule.Name)
}

// TestEvaluate_HeaderRegexCaseSensitive verifies that header value regexes
// remain case-sensitive by default, and that users opt in to case-insensitive
// matching with the RE2 (?i) flag.
func TestEvaluate_HeaderRegexCaseSensitive(t *testing.T) {
	rs := parseInline(t, `
rule "case-sensitive" {
  match {
    host    = "api.example.com"
    path    = "/**"
    headers = {
      "X-Custom" = "^bar$"
    }
  }
  verdict = "allow"
}
rule "case-insensitive" {
  match {
    host    = "api2.example.com"
    path    = "/**"
    headers = {
      "X-Custom" = "(?i)^bar$"
    }
  }
  verdict = "allow"
}
`)
	engine := rules.Compile(rs)

	// Case-sensitive rule: exact value matches.
	h := make(http.Header)
	h.Set("X-Custom", "bar")
	m := engine.Evaluate(&rules.Request{Agent: "x", Host: "api.example.com", Method: "GET", Path: "/x", Header: h})
	require.NotNil(t, m)
	assert.Equal(t, "case-sensitive", m.Rule.Name)

	// Case-sensitive rule: different case does NOT match.
	h2 := make(http.Header)
	h2.Set("X-Custom", "BAR")
	m = engine.Evaluate(&rules.Request{Agent: "x", Host: "api.example.com", Method: "GET", Path: "/x", Header: h2})
	assert.Nil(t, m, "header regex without (?i) flag must be case-sensitive")

	// Case-insensitive rule with (?i): different case DOES match.
	h3 := make(http.Header)
	h3.Set("X-Custom", "BAR")
	m = engine.Evaluate(&rules.Request{Agent: "x", Host: "api2.example.com", Method: "GET", Path: "/x", Header: h3})
	require.NotNil(t, m, "header regex with (?i) flag should be case-insensitive")
	assert.Equal(t, "case-insensitive", m.Rule.Name)
}

// TestEvaluate_Headers covers header regex matching and case-insensitive lookup.
func TestEvaluate_Headers(t *testing.T) {
	rs := parseInline(t, `
rule "versioned" {
  match {
    host    = "api.github.com"
    path    = "/**"
    headers = {
      "X-GitHub-Api-Version" = "^2022-"
    }
  }
  verdict = "allow"
}
`)
	engine := rules.Compile(rs)

	// Matching header value.
	h := make(http.Header)
	h.Set("X-GitHub-Api-Version", "2022-11-28")
	m := engine.Evaluate(&rules.Request{Agent: "x", Host: "api.github.com", Method: "GET", Path: "/repos", Header: h})
	require.NotNil(t, m)
	assert.Equal(t, "versioned", m.Rule.Name)

	// Header value does not match the regex.
	h2 := make(http.Header)
	h2.Set("X-GitHub-Api-Version", "2021-06-01")
	m = engine.Evaluate(&rules.Request{Agent: "x", Host: "api.github.com", Method: "GET", Path: "/repos", Header: h2})
	assert.Nil(t, m)

	// Missing header → no match.
	m = engine.Evaluate(&rules.Request{Agent: "x", Host: "api.github.com", Method: "GET", Path: "/repos", Header: make(http.Header)})
	assert.Nil(t, m)

	// Case-insensitive header lookup: lowercase key should still be found via
	// http.Header.Get which canonicalises names.
	h3 := make(http.Header)
	h3.Set("x-github-api-version", "2022-11-28")
	m = engine.Evaluate(&rules.Request{Agent: "x", Host: "api.github.com", Method: "GET", Path: "/repos", Header: h3})
	require.NotNil(t, m)
	assert.Equal(t, "versioned", m.Rule.Name)
}

// TestEvaluate_Agents covers agent filter: nil=all, list membership.
func TestEvaluate_Agents(t *testing.T) {
	rs := parseInline(t, `
rule "claude-only" {
  agents  = ["claude"]
  match {
    host = "api.example.com"
    path = "/**"
  }
  verdict = "allow"
}
rule "open" {
  match {
    host = "api.example.com"
    path = "/**"
  }
  verdict = "deny"
}
`)
	engine := rules.Compile(rs)

	// claude matches the first rule.
	m := engine.Evaluate(&rules.Request{Agent: "claude", Host: "api.example.com", Method: "GET", Path: "/x"})
	require.NotNil(t, m)
	assert.Equal(t, "claude-only", m.Rule.Name)

	// codex skips the first rule, hits the open rule.
	m = engine.Evaluate(&rules.Request{Agent: "codex", Host: "api.example.com", Method: "GET", Path: "/x"})
	require.NotNil(t, m)
	assert.Equal(t, "open", m.Rule.Name)

	// multi-agent list: both listed agents match.
	rs2 := parseInline(t, `
rule "multi-agent" {
  agents  = ["claude", "gemini"]
  match {
    host = "api.example.com"
    path = "/**"
  }
  verdict = "allow"
}
`)
	eng2 := rules.Compile(rs2)
	for _, agent := range []string{"claude", "gemini"} {
		m = eng2.Evaluate(&rules.Request{Agent: agent, Host: "api.example.com", Method: "GET", Path: "/x"})
		require.NotNil(t, m, "agent %s should match", agent)
		assert.Equal(t, "multi-agent", m.Rule.Name)
	}
	m = eng2.Evaluate(&rules.Request{Agent: "codex", Host: "api.example.com", Method: "GET", Path: "/x"})
	assert.Nil(t, m, "codex should not match multi-agent rule")
}

// TestEvaluate_FirstMatchWins verifies ordering semantics.
func TestEvaluate_FirstMatchWins(t *testing.T) {
	rs := parseInline(t, `
rule "first" {
  match {
    host = "*.example.com"
    path = "/**"
  }
  verdict = "allow"
}
rule "second" {
  match {
    host = "*.example.com"
    path = "/**"
  }
  verdict = "deny"
}
`)
	engine := rules.Compile(rs)

	m := engine.Evaluate(&rules.Request{Agent: "x", Host: "api.example.com", Method: "GET", Path: "/foo"})
	require.NotNil(t, m)
	assert.Equal(t, "first", m.Rule.Name)
	assert.Equal(t, 0, m.Index)
}

// TestEvaluate_NoRules returns nil for an empty ruleset.
func TestEvaluate_NoRules(t *testing.T) {
	engine := rules.Compile(nil)
	m := engine.Evaluate(&rules.Request{Agent: "x", Host: "api.example.com", Method: "GET", Path: "/"})
	assert.Nil(t, m)
}

// TestEvaluate_OmittedCriteriaAreWildcards verifies that absent match fields
// match any value.
func TestEvaluate_OmittedCriteriaAreWildcards(t *testing.T) {
	rs := parseInline(t, `
rule "catch-all" {
  match { host = "example.com" }
  verdict = "allow"
}
`)
	engine := rules.Compile(rs)

	// Any method and path should match.
	for _, tc := range []struct{ method, path string }{
		{"GET", "/anything"},
		{"POST", "/other"},
		{"DELETE", "/deep/nested/path"},
	} {
		m := engine.Evaluate(&rules.Request{
			Agent:  "x",
			Host:   "example.com",
			Method: tc.method,
			Path:   tc.path,
			Header: make(http.Header),
		})
		require.NotNil(t, m, "expected match for %s %s", tc.method, tc.path)
		assert.Equal(t, "catch-all", m.Rule.Name)
	}
}

// TestEvaluate_ExactHostNoGlob verifies that an exact host pattern only
// matches the exact host, not subdomains.
func TestEvaluate_ExactHostNoGlob(t *testing.T) {
	rs := parseInline(t, `
rule "exact" {
  match {
    host = "api.github.com"
    path = "/**"
  }
  verdict = "allow"
}
`)
	engine := rules.Compile(rs)

	m := engine.Evaluate(&rules.Request{Agent: "x", Host: "api.github.com", Method: "GET", Path: "/foo"})
	require.NotNil(t, m)

	m = engine.Evaluate(&rules.Request{Agent: "x", Host: "sub.api.github.com", Method: "GET", Path: "/foo"})
	assert.Nil(t, m)

	m = engine.Evaluate(&rules.Request{Agent: "x", Host: "github.com", Method: "GET", Path: "/foo"})
	assert.Nil(t, m)
}

// TestEvaluate_MultipleHeaders verifies that all header patterns must match.
func TestEvaluate_MultipleHeaders(t *testing.T) {
	rs := parseInline(t, `
rule "multi-header" {
  match {
    host    = "api.example.com"
    path    = "/**"
    headers = {
      "X-Version"    = "^v2"
      "Content-Type" = "application/json"
    }
  }
  verdict = "allow"
}
`)
	engine := rules.Compile(rs)

	// Both headers match.
	h := make(http.Header)
	h.Set("X-Version", "v2.1")
	h.Set("Content-Type", "application/json")
	m := engine.Evaluate(&rules.Request{Agent: "x", Host: "api.example.com", Method: "POST", Path: "/data", Header: h})
	require.NotNil(t, m)

	// One header missing → no match.
	h2 := make(http.Header)
	h2.Set("X-Version", "v2.1")
	m = engine.Evaluate(&rules.Request{Agent: "x", Host: "api.example.com", Method: "POST", Path: "/data", Header: h2})
	assert.Nil(t, m)

	// One header has wrong value → no match.
	h3 := make(http.Header)
	h3.Set("X-Version", "v1.0")
	h3.Set("Content-Type", "application/json")
	m = engine.Evaluate(&rules.Request{Agent: "x", Host: "api.example.com", Method: "POST", Path: "/data", Header: h3})
	assert.Nil(t, m)
}

// TestEvaluate_DoubleStarMatchesRoot verifies that path "/**" matches the bare
// root path "/", i.e. ** matches zero characters after the slash.
func TestEvaluate_DoubleStarMatchesRoot(t *testing.T) {
	rs := parseInline(t, `
rule "catch-root" {
  match {
    host = "api.example.com"
    path = "/**"
  }
  verdict = "allow"
}
`)
	engine := rules.Compile(rs)

	m := engine.Evaluate(&rules.Request{Agent: "x", Host: "api.example.com", Method: "GET", Path: "/"})
	require.NotNil(t, m, "path /** should match bare root /")
	assert.Equal(t, "catch-root", m.Rule.Name)
}

// TestEvaluate_DoubleStarMatchesBareHost verifies that host "**.enterprise.local"
// matches the bare domain "enterprise.local", i.e. ** matches zero labels.
func TestEvaluate_DoubleStarMatchesBareHost(t *testing.T) {
	rs := parseInline(t, `
rule "catch-domain" {
  match {
    host = "**.enterprise.local"
    path = "/**"
  }
  verdict = "allow"
}
`)
	engine := rules.Compile(rs)

	m := engine.Evaluate(&rules.Request{Agent: "x", Host: "enterprise.local", Method: "GET", Path: "/"})
	require.NotNil(t, m, "host **.enterprise.local should match bare enterprise.local")
	assert.Equal(t, "catch-domain", m.Rule.Name)
}
