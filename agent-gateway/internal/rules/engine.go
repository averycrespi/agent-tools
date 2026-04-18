package rules

import (
	"fmt"
	"sync/atomic"
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
		return err
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

// load is the shared helper for NewEngine and Reload: parse + compile.
func load(dir string) (*ruleset, error) {
	rules, _, err := ParseDir(dir)
	if err != nil {
		return nil, fmt.Errorf("rules.Engine: load %q: %w", dir, err)
	}
	return Compile(rules), nil
}
