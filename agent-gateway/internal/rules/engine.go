package rules

import (
	"fmt"
	"sync/atomic"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/hostnorm"
)

// Engine is the top-level, hot-reloadable rules engine. It wraps an
// atomic.Pointer[ruleset] so that Evaluate reads are lock-free and Reload
// swaps the snapshot only on success, leaving the previous snapshot intact on
// any parse or compile error.
type Engine struct {
	dir      string
	snapshot atomic.Pointer[ruleset]
}

// NewEngine creates an Engine by loading all *.hcl files from dir. It returns
// an error if the initial load fails.
func NewEngine(dir string) (*Engine, error) {
	rs, err := load(dir)
	if err != nil {
		return nil, err
	}
	e := &Engine{dir: dir}
	e.snapshot.Store(rs)
	return e, nil
}

// Reload re-parses and re-compiles the rules directory. On success the
// internal snapshot is replaced atomically; on any error the previous snapshot
// is preserved and the error is returned.
func (e *Engine) Reload() error {
	rs, err := load(e.dir)
	if err != nil {
		return fmt.Errorf("rules: reload: %w", err)
	}
	e.snapshot.Store(rs)
	return nil
}

// Evaluate delegates to the current snapshot's Evaluate method. The read is
// lock-free: it loads the atomic pointer and evaluates against it.
func (e *Engine) Evaluate(req *Request) *MatchResult {
	return e.snapshot.Load().Evaluate(req)
}

// Rules returns the current rule list in evaluation order. The caller receives
// a copy of the slice from the current atomic snapshot; the underlying Rule
// values are immutable so shallow copies are safe to read concurrently.
func (e *Engine) Rules() []*Rule {
	rs := e.snapshot.Load()
	out := make([]*Rule, len(rs.rules))
	for i := range rs.rules {
		r := rs.rules[i] // copy
		out[i] = &r
	}
	return out
}

// HostsForAgent returns the set of host-glob patterns that have at least one
// rule applying to the given agent. Rules with no agent filter (nil Agents)
// are included for every agent.
//
// The returned map contains the raw glob strings from rule.Match.Host fields.
// Callers doing CONNECT-time host matching must perform their own glob check
// against concrete hostnames.
func (e *Engine) HostsForAgent(agent string) map[string]struct{} {
	return e.snapshot.Load().hostsForAgent(agent)
}

// NeedsBodyBuffer reports whether any rule that could match the given agent
// and host contains a body matcher. Callers use this to avoid the cost of
// buffering request bodies when no rule can ever examine them.
//
// The check is conservative: it returns true whenever any rule whose agent
// filter includes agent (or whose Agents list is nil, i.e. all-agents) AND
// whose host glob matches host has a non-nil body matcher.
func (e *Engine) NeedsBodyBuffer(agent, host string) bool {
	return e.snapshot.Load().needsBodyBuffer(agent, host)
}

// AllRuleHosts returns a deduplicated slice of every host-glob pattern
// mentioned in any rule across all agents. Patterns with an empty host
// constraint (i.e. rules that match any host) are not included because they
// represent wildcards rather than specific hosts.
//
// The primary use-case is the tunneled-hosts API endpoint: callers intersect
// this set with tunnel audit rows to surface hosts that have been tunneled but
// have no rule coverage.
func (e *Engine) AllRuleHosts() []string {
	rs := e.snapshot.Load()
	seen := make(map[string]struct{})
	for _, hosts := range rs.hostIndex {
		for h := range hosts {
			seen[h] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for h := range seen {
		out = append(out, h)
	}
	return out
}

// RulesOverlappingHost returns all rules whose match.host could plausibly
// overlap with the given pattern. The check is conservative: false positives
// are acceptable; false negatives (a shadow that is not reported) are the
// footgun this function exists to catch.
//
// Overlap is detected when either:
//   - the rule host exactly equals pattern, or
//   - MatchHostGlob(pattern, ruleHost) — pattern would match the rule's host literal, or
//   - MatchHostGlob(ruleHost, pattern) — rule's pattern would match the no-intercept host literal.
//
// Rules with no host constraint (empty match.host) are not included because
// they already match every host and the warning is about operator confusion
// when adding a no-intercept entry for a specific host.
func (e *Engine) RulesOverlappingHost(pattern string) []*Rule {
	var out []*Rule
	for _, r := range e.Rules() {
		rh := r.Match.Host
		if rh == "" {
			// Rule with no host constraint is intentionally catch-all; skip.
			continue
		}
		if hostsOverlap(pattern, rh) {
			out = append(out, r)
		}
	}
	return out
}

// hostsOverlap reports whether two host-glob patterns could match any common
// host. The check is conservative (false positives OK).
func hostsOverlap(a, b string) bool {
	if a == b {
		return true
	}
	// Does pattern a match b treated as a concrete host?
	if hostnorm.MatchHostGlob(a, b) {
		return true
	}
	// Does pattern b match a treated as a concrete host?
	if hostnorm.MatchHostGlob(b, a) {
		return true
	}
	return false
}

// load is the shared helper for NewEngine and Reload: parse + compile.
func load(dir string) (*ruleset, error) {
	rules, _, err := ParseDir(dir)
	if err != nil {
		return nil, fmt.Errorf("rules.Engine: load %q: %w", dir, err)
	}
	return Compile(rules), nil
}
