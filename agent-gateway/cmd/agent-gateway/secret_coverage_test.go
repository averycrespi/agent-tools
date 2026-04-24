package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/rules"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeRule writes a rule HCL file to dir.
func writeRule(t *testing.T, dir, filename, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, filename), []byte(body), 0o600))
}

func TestWarnSecretCoverage_ConcreteHost_Covered(t *testing.T) {
	dir := t.TempDir()
	writeRule(t, dir, "r.hcl", `
rule "r" {
  match  { host = "api.github.com" }
  verdict = "allow"
  inject {
    replace_header = { "Authorization" = "Bearer ${secrets.gh}" }
  }
}
`)
	engine, err := rules.NewEngine(dir)
	require.NoError(t, err)

	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()
	require.NoError(t, s.Set(ctx, "gh", "", "v", "", []string{"*.github.com"}))

	warns := warnSecretCoverage(ctx, engine, s)
	assert.Empty(t, warns)
}

func TestWarnSecretCoverage_ConcreteHost_NotCovered(t *testing.T) {
	dir := t.TempDir()
	writeRule(t, dir, "r.hcl", `
rule "r" {
  match  { host = "evil.com" }
  verdict = "allow"
  inject {
    replace_header = { "Authorization" = "Bearer ${secrets.gh}" }
  }
}
`)
	engine, err := rules.NewEngine(dir)
	require.NoError(t, err)

	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()
	require.NoError(t, s.Set(ctx, "gh", "", "v", "", []string{"*.github.com"}))

	warns := warnSecretCoverage(ctx, engine, s)
	require.Len(t, warns, 1)
	assert.Contains(t, warns[0], `rule "r"`)
	assert.Contains(t, warns[0], `${secrets.gh}`)
	assert.Contains(t, warns[0], "evil.com")
}

func TestWarnSecretCoverage_DoubleStarBindingCoversEverything(t *testing.T) {
	dir := t.TempDir()
	writeRule(t, dir, "r.hcl", `
rule "r" {
  match  { host = "**" }
  verdict = "allow"
  inject {
    replace_header = { "Authorization" = "Bearer ${secrets.gh}" }
  }
}
`)
	engine, err := rules.NewEngine(dir)
	require.NoError(t, err)

	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()
	require.NoError(t, s.Set(ctx, "gh", "", "v", "", []string{"**"}))

	warns := warnSecretCoverage(ctx, engine, s)
	assert.Empty(t, warns)
}

func TestWarnSecretCoverage_WildcardRuleAndNarrowerBinding_Warns(t *testing.T) {
	// rule host is "*.github.com" (wildcard); secret allows only
	// "api.github.com". Rule host could match "other.github.com" which the
	// secret does NOT allow — we warn because pattern subset is not
	// obviously safe.
	dir := t.TempDir()
	writeRule(t, dir, "r.hcl", `
rule "r" {
  match  { host = "*.github.com" }
  verdict = "allow"
  inject {
    replace_header = { "Authorization" = "Bearer ${secrets.gh}" }
  }
}
`)
	engine, err := rules.NewEngine(dir)
	require.NoError(t, err)

	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()
	require.NoError(t, s.Set(ctx, "gh", "", "v", "", []string{"api.github.com"}))

	warns := warnSecretCoverage(ctx, engine, s)
	require.Len(t, warns, 1)
}

func TestWarnSecretCoverage_NoSecretReferenced(t *testing.T) {
	dir := t.TempDir()
	writeRule(t, dir, "r.hcl", `
rule "r" {
  match  { host = "example.com" }
  verdict = "allow"
  inject {
    remove_header = ["X-Debug"]
  }
}
`)
	engine, err := rules.NewEngine(dir)
	require.NoError(t, err)

	db := secretTestDB(t)
	s := newTestSecretStore(t, db)
	ctx := context.Background()

	warns := warnSecretCoverage(ctx, engine, s)
	assert.Empty(t, warns)
}

func TestWarnNoInterceptOverlap_GroupsByEntry(t *testing.T) {
	// Two no_intercept_hosts entries, three rules, overlap as:
	//   - "api.github.com" shadows rules A (exact) and B (*.github.com covers api.github.com)
	//   - "*.example.com" shadows rule C (foo.example.com matches *.example.com)
	// Expect exactly two warnings, each listing its shadowed rules.
	dir := t.TempDir()
	writeRule(t, dir, "10-gh.hcl", `
rule "A" {
  match   { host = "api.github.com" }
  verdict = "allow"
}
rule "B" {
  match   { host = "*.github.com" }
  verdict = "allow"
}
`)
	writeRule(t, dir, "20-ex.hcl", `
rule "C" {
  match   { host = "foo.example.com" }
  verdict = "allow"
}
`)
	engine, err := rules.NewEngine(dir)
	require.NoError(t, err)

	warns := warnNoInterceptOverlap(engine, []string{"api.github.com", "*.example.com"})
	require.Len(t, warns, 2)
	require.Contains(t, warns[0], "api.github.com")
	require.Contains(t, warns[0], `"A"`)
	require.Contains(t, warns[0], `"B"`)
	require.Contains(t, warns[1], "*.example.com")
	require.Contains(t, warns[1], `"C"`)
}

func TestWarnNoInterceptOverlap_NoOverlap(t *testing.T) {
	dir := t.TempDir()
	writeRule(t, dir, "r.hcl", `
rule "gh" {
  match   { host = "api.github.com" }
  verdict = "allow"
}
`)
	engine, err := rules.NewEngine(dir)
	require.NoError(t, err)

	warns := warnNoInterceptOverlap(engine, []string{"pinned.internal"})
	assert.Empty(t, warns)
}

func TestWarnNoInterceptOverlap_NilEngine(t *testing.T) {
	warns := warnNoInterceptOverlap(nil, []string{"api.github.com"})
	assert.Nil(t, warns)
}

func TestWarnNoInterceptOverlap_EmptyPatterns(t *testing.T) {
	dir := t.TempDir()
	writeRule(t, dir, "r.hcl", `
rule "gh" {
  match   { host = "api.github.com" }
  verdict = "allow"
}
`)
	engine, err := rules.NewEngine(dir)
	require.NoError(t, err)

	warns := warnNoInterceptOverlap(engine, nil)
	assert.Nil(t, warns)
}
