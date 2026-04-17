package rules_test

import (
	"sync"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/rules"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// write is a test helper that (over)writes filename inside dir with content.
func write(t *testing.T, dir, filename, content string) {
	t.Helper()
	writeHCL(t, dir, filename, content)
}

func TestEngine_ReloadSwapsAtomically(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "00.hcl", `
rule "a" {
  match {
    host = "a.com"
    path = "/**"
  }
  verdict = "allow"
}`)

	e, err := rules.NewEngine(dir)
	require.NoError(t, err)
	assert.Equal(t, "a", e.Evaluate(&rules.Request{Host: "a.com", Path: "/x"}).Rule.Name)

	write(t, dir, "00.hcl", `
rule "b" {
  match {
    host = "b.com"
    path = "/**"
  }
  verdict = "allow"
}`)
	require.NoError(t, e.Reload())

	assert.Equal(t, "b", e.Evaluate(&rules.Request{Host: "b.com", Path: "/x"}).Rule.Name)
	assert.Nil(t, e.Evaluate(&rules.Request{Host: "a.com", Path: "/x"}))
}

func TestEngine_InvalidReloadKeepsPreviousRuleset(t *testing.T) {
	dir := t.TempDir()
	// Write a valid ruleset and load it.
	write(t, dir, "00.hcl", `
rule "good" {
  match {
    host = "good.com"
    path = "/**"
  }
  verdict = "allow"
}`)

	e, err := rules.NewEngine(dir)
	require.NoError(t, err)
	require.NotNil(t, e.Evaluate(&rules.Request{Host: "good.com", Path: "/x"}))

	// Overwrite with invalid HCL; Reload must return an error.
	write(t, dir, "00.hcl", `this is not valid HCL !!!`)
	reloadErr := e.Reload()
	require.Error(t, reloadErr)

	// The previous ruleset must still be in effect.
	m := e.Evaluate(&rules.Request{Host: "good.com", Path: "/x"})
	require.NotNil(t, m, "previous ruleset should still be active after failed reload")
	assert.Equal(t, "good", m.Rule.Name)
}

func TestEngine_HostsForAgent(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "00.hcl", `
rule "claude-github" {
  agents  = ["claude"]
  match {
    host = "api.github.com"
    path = "/**"
  }
  verdict = "allow"
}
rule "claude-npm" {
  agents  = ["claude"]
  match {
    host = "registry.npmjs.org"
    path = "/**"
  }
  verdict = "allow"
}
rule "all-example" {
  match {
    host = "example.com"
    path = "/**"
  }
  verdict = "allow"
}
`)
	e, err := rules.NewEngine(dir)
	require.NoError(t, err)

	// claude has two specific host rules plus the agent-wildcard rule.
	claudeHosts := e.HostsForAgent("claude")
	assert.Contains(t, claudeHosts, "api.github.com")
	assert.Contains(t, claudeHosts, "registry.npmjs.org")
	assert.Contains(t, claudeHosts, "example.com") // nil-agents rule applies to all agents

	// codex only sees the agent-wildcard rule.
	codexHosts := e.HostsForAgent("codex")
	assert.NotContains(t, codexHosts, "api.github.com")
	assert.Contains(t, codexHosts, "example.com")
}

func TestEngine_HostsForAgent_RebuildsOnReload(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "00.hcl", `
rule "r1" {
  agents  = ["bot"]
  match {
    host = "old.example.com"
    path = "/**"
  }
  verdict = "allow"
}
`)
	e, err := rules.NewEngine(dir)
	require.NoError(t, err)
	assert.Contains(t, e.HostsForAgent("bot"), "old.example.com")

	write(t, dir, "00.hcl", `
rule "r2" {
  agents  = ["bot"]
  match {
    host = "new.example.com"
    path = "/**"
  }
  verdict = "allow"
}
`)
	require.NoError(t, e.Reload())
	hosts := e.HostsForAgent("bot")
	assert.Contains(t, hosts, "new.example.com")
	assert.NotContains(t, hosts, "old.example.com")
}

// TestEngine_ConcurrentEvaluateReload runs concurrent Evaluate calls during
// Reload to verify there is no data race (run with -race).
func TestEngine_ConcurrentEvaluateReload(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "00.hcl", `
rule "a" {
  match {
    host = "a.com"
    path = "/**"
  }
  verdict = "allow"
}`)

	e, err := rules.NewEngine(dir)
	require.NoError(t, err)

	const readers = 8
	const iterations = 200

	var wg sync.WaitGroup

	// Continuously reload in the background.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_ = e.Reload()
		}
	}()

	// Concurrently evaluate.
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_ = e.Evaluate(&rules.Request{Host: "a.com", Path: "/x"})
				_ = e.HostsForAgent("x")
			}
		}()
	}

	wg.Wait()
}
