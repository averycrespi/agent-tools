package rules

import (
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

// Engine evaluates tool names against a static list of glob rules.
type Engine struct {
	rules []config.RuleConfig
}

// New creates a rules engine with the given rules.
func New(rules []config.RuleConfig) *Engine {
	return &Engine{rules: rules}
}

// Rules returns the configured rules in evaluation order.
func (e *Engine) Rules() []config.RuleConfig {
	return e.rules
}

// Evaluate returns the verdict for the given tool name.
// First matching rule wins. Default is require-approval.
func (e *Engine) Evaluate(tool string) Verdict {
	for _, rule := range e.rules {
		matched, err := filepath.Match(rule.Tool, tool)
		if err != nil {
			continue
		}
		if matched {
			return ParseVerdict(rule.Verdict)
		}
	}
	return RequireApproval
}
