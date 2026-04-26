package rules

import (
	"fmt"
	"path/filepath"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

// Verdict represents the outcome of a rule evaluation.
type Verdict int

const (
	Allow Verdict = iota
	Deny
	RequireApproval
)

func (v Verdict) String() string {
	switch v {
	case Allow:
		return "allow"
	case Deny:
		return "deny"
	case RequireApproval:
		return "require-approval"
	default:
		return "unknown"
	}
}

// ParseVerdict converts a string verdict to a Verdict value.
func ParseVerdict(s string) Verdict {
	switch s {
	case "allow":
		return Allow
	case "deny":
		return Deny
	case "require-approval":
		return RequireApproval
	default:
		return RequireApproval
	}
}

type compiledRule struct {
	raw     config.RuleConfig
	verdict Verdict
	args    []compiledPattern
}

// Engine evaluates tool names against a static list of glob rules.
type Engine struct {
	rules    []config.RuleConfig
	compiled []compiledRule
}

// New creates a rules engine, compiling each rule's argument patterns.
// Returns an error if any path is malformed or any regex fails to compile.
// Tool-name glob errors are still tolerated at evaluation time (preserves
// current behavior for malformed globs).
func New(rs []config.RuleConfig) (*Engine, error) {
	compiled := make([]compiledRule, len(rs))
	for i, r := range rs {
		cr := compiledRule{raw: r, verdict: ParseVerdict(r.Verdict)}
		for j, ap := range r.Args {
			segs, err := parsePath(ap.Path)
			if err != nil {
				return nil, fmt.Errorf("rule %d: args[%d]: path: %w", i, j, err)
			}
			m, err := decodeMatcher(ap.Match)
			if err != nil {
				return nil, fmt.Errorf("rule %d: args[%d]: %w", i, j, err)
			}
			cr.args = append(cr.args, compiledPattern{segments: segs, matcher: m})
		}
		compiled[i] = cr
	}
	return &Engine{rules: rs, compiled: compiled}, nil
}

// Rules returns the configured rules in evaluation order.
func (e *Engine) Rules() []config.RuleConfig {
	return e.rules
}

// Evaluate returns the verdict for the given tool name and arguments.
func (e *Engine) Evaluate(tool string, args map[string]any) Verdict {
	v, _ := e.EvaluateWithRule(tool, args)
	return v
}

// EvaluateWithRule returns the verdict and the zero-based index of the rule
// that matched. Returns (RequireApproval, -1) when no rule matches.
func (e *Engine) EvaluateWithRule(tool string, args map[string]any) (Verdict, int) {
	for i, cr := range e.compiled {
		nameMatched, err := filepath.Match(cr.raw.Tool, tool)
		if err != nil || !nameMatched {
			continue
		}
		if !argsMatch(cr.args, args) {
			continue
		}
		return cr.verdict, i
	}
	return RequireApproval, -1
}

// argsMatch returns true when every compiled pattern resolves and matches.
// Empty patterns slice → true (legacy rule behavior).
func argsMatch(patterns []compiledPattern, args map[string]any) bool {
	for _, p := range patterns {
		if !p.matchValue(args) {
			return false
		}
	}
	return true
}
