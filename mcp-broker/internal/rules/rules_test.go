package rules

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

func TestEngine_Evaluate_AllowRule(t *testing.T) {
	engine, err := New([]config.RuleConfig{
		{Tool: "github.*", Verdict: "allow"},
	})
	require.NoError(t, err)
	require.Equal(t, Allow, engine.Evaluate("github.get_pr", nil))
}

func TestEngine_Evaluate_DenyRule(t *testing.T) {
	engine, err := New([]config.RuleConfig{
		{Tool: "*", Verdict: "deny"},
	})
	require.NoError(t, err)
	require.Equal(t, Deny, engine.Evaluate("anything", nil))
}

func TestEngine_Evaluate_RequireApprovalRule(t *testing.T) {
	engine, err := New([]config.RuleConfig{
		{Tool: "fs.write_file", Verdict: "require-approval"},
	})
	require.NoError(t, err)
	require.Equal(t, RequireApproval, engine.Evaluate("fs.write_file", nil))
}

func TestEngine_Evaluate_FirstMatchWins(t *testing.T) {
	engine, err := New([]config.RuleConfig{
		{Tool: "github.push", Verdict: "require-approval"},
		{Tool: "github.*", Verdict: "allow"},
		{Tool: "*", Verdict: "deny"},
	})
	require.NoError(t, err)
	require.Equal(t, RequireApproval, engine.Evaluate("github.push", nil))
	require.Equal(t, Allow, engine.Evaluate("github.get_pr", nil))
	require.Equal(t, Deny, engine.Evaluate("linear.search", nil))
}

func TestEngine_Evaluate_DefaultIsRequireApproval(t *testing.T) {
	engine, err := New(nil)
	require.NoError(t, err)
	require.Equal(t, RequireApproval, engine.Evaluate("anything", nil))
}

func TestVerdict_String(t *testing.T) {
	require.Equal(t, "allow", Allow.String())
	require.Equal(t, "deny", Deny.String())
	require.Equal(t, "require-approval", RequireApproval.String())
}

func TestEngine_Rules_ReturnsConfiguredRules(t *testing.T) {
	input := []config.RuleConfig{
		{Tool: "github.*", Verdict: "allow"},
		{Tool: "*", Verdict: "deny"},
	}
	engine, err := New(input)
	require.NoError(t, err)
	require.Equal(t, input, engine.Rules())
}

func TestEngine_Rules_EmptyWhenNil(t *testing.T) {
	engine, err := New(nil)
	require.NoError(t, err)
	require.Empty(t, engine.Rules())
}

func TestEngine_EvaluateWithRule_FirstMatchWins(t *testing.T) {
	engine, err := New([]config.RuleConfig{
		{Tool: "github.push", Verdict: "require-approval"}, // index 0
		{Tool: "github.*", Verdict: "allow"},               // index 1
		{Tool: "*", Verdict: "deny"},                       // index 2
	})
	require.NoError(t, err)

	v, idx := engine.EvaluateWithRule("github.push", nil)
	require.Equal(t, RequireApproval, v)
	require.Equal(t, 0, idx)

	v, idx = engine.EvaluateWithRule("github.get_pr", nil)
	require.Equal(t, Allow, v)
	require.Equal(t, 1, idx)

	v, idx = engine.EvaluateWithRule("linear.search", nil)
	require.Equal(t, Deny, v)
	require.Equal(t, 2, idx)
}

func TestEngine_EvaluateWithRule_NoMatchReturnsNegativeOne(t *testing.T) {
	engine, err := New([]config.RuleConfig{
		{Tool: "github.*", Verdict: "allow"},
	})
	require.NoError(t, err)
	v, idx := engine.EvaluateWithRule("linear.search", nil)
	require.Equal(t, RequireApproval, v) // default
	require.Equal(t, -1, idx)
}

func TestEngine_EvaluateWithRule_EmptyRules(t *testing.T) {
	engine, err := New(nil)
	require.NoError(t, err)
	v, idx := engine.EvaluateWithRule("anything", nil)
	require.Equal(t, RequireApproval, v)
	require.Equal(t, -1, idx)
}

// --- Arg-matching tests ---

// Rule with empty args behaves identically to a no-args rule.
func TestEngine_ArgMatch_EmptyArgsBehavesLikeLegacyRule(t *testing.T) {
	engine, err := New([]config.RuleConfig{
		{Tool: "github.*", Verdict: "allow"},
		{Tool: "*", Verdict: "deny"},
	})
	require.NoError(t, err)

	// Allow rule still fires
	require.Equal(t, Allow, engine.Evaluate("github.get_pr", map[string]any{"q": "hello"}))
	// Deny catchall still fires
	require.Equal(t, Deny, engine.Evaluate("linear.search", nil))
}

// Rule with all-passing arg patterns matches and returns its verdict.
func TestEngine_ArgMatch_AllPatternsMatch(t *testing.T) {
	engine, err := New([]config.RuleConfig{
		{
			Tool:    "git.*",
			Verdict: "allow",
			Args: []config.ArgPattern{
				{Path: "branch", Match: json.RawMessage(`"main"`)},
			},
		},
		{Tool: "*", Verdict: "deny"},
	})
	require.NoError(t, err)

	// Matches: branch == "main"
	require.Equal(t, Allow, engine.Evaluate("git.push", map[string]any{"branch": "main"}))
	// Does not match: branch != "main" → falls through to deny
	require.Equal(t, Deny, engine.Evaluate("git.push", map[string]any{"branch": "feature-x"}))
}

// Rule with one failing arg pattern is skipped; evaluation falls through.
func TestEngine_ArgMatch_OneFailingPatternSkipsRule(t *testing.T) {
	engine, err := New([]config.RuleConfig{
		{
			Tool:    "fs.*",
			Verdict: "deny",
			Args: []config.ArgPattern{
				{Path: "path", Match: json.RawMessage(`"/etc/passwd"`)},
			},
		},
		{Tool: "fs.*", Verdict: "allow"},
	})
	require.NoError(t, err)

	// path == "/etc/passwd" → deny rule fires
	require.Equal(t, Deny, engine.Evaluate("fs.read", map[string]any{"path": "/etc/passwd"}))
	// path != "/etc/passwd" → deny rule skipped, allow rule fires
	require.Equal(t, Allow, engine.Evaluate("fs.read", map[string]any{"path": "/tmp/safe.txt"}))
}

// First-match-wins: when first name-matching rule has failing args, second rule (no args) wins.
func TestEngine_ArgMatch_FirstMatchWinsWithFallthrough(t *testing.T) {
	engine, err := New([]config.RuleConfig{
		{
			Tool:    "github.*",
			Verdict: "deny",
			Args: []config.ArgPattern{
				{Path: "repo", Match: json.RawMessage(`"dangerous"`)},
			},
		},
		{Tool: "github.*", Verdict: "allow"},
	})
	require.NoError(t, err)

	// repo == "dangerous" → first rule fires (deny)
	v, idx := engine.EvaluateWithRule("github.push", map[string]any{"repo": "dangerous"})
	require.Equal(t, Deny, v)
	require.Equal(t, 0, idx)

	// repo != "dangerous" → first rule skipped, second rule fires (allow)
	v, idx = engine.EvaluateWithRule("github.push", map[string]any{"repo": "safe"})
	require.Equal(t, Allow, v)
	require.Equal(t, 1, idx)
}

// Multiple AND patterns: rule fires only when both match.
func TestEngine_ArgMatch_MultipleAndPatterns(t *testing.T) {
	engine, err := New([]config.RuleConfig{
		{
			Tool:    "git.commit",
			Verdict: "allow",
			Args: []config.ArgPattern{
				{Path: "author", Match: json.RawMessage(`"alice"`)},
				{Path: "branch", Match: json.RawMessage(`"main"`)},
			},
		},
		{Tool: "*", Verdict: "deny"},
	})
	require.NoError(t, err)

	// Both match → allow
	require.Equal(t, Allow, engine.Evaluate("git.commit", map[string]any{
		"author": "alice",
		"branch": "main",
	}))
	// Only one matches → deny
	require.Equal(t, Deny, engine.Evaluate("git.commit", map[string]any{
		"author": "alice",
		"branch": "feature",
	}))
	// Neither matches → deny
	require.Equal(t, Deny, engine.Evaluate("git.commit", map[string]any{
		"author": "bob",
		"branch": "main",
	}))
}

// Path resolution failure (missing key) → rule does not match → fall-through.
func TestEngine_ArgMatch_MissingKeyFallsThrough(t *testing.T) {
	engine, err := New([]config.RuleConfig{
		{
			Tool:    "tool.*",
			Verdict: "deny",
			Args: []config.ArgPattern{
				{Path: "nonexistent", Match: json.RawMessage(`"value"`)},
			},
		},
		{Tool: "*", Verdict: "allow"},
	})
	require.NoError(t, err)

	// "nonexistent" key missing → deny rule skipped → allow fires
	require.Equal(t, Allow, engine.Evaluate("tool.call", map[string]any{"other": "value"}))
	// nil args → deny rule skipped → allow fires
	require.Equal(t, Allow, engine.Evaluate("tool.call", nil))
}

// Regex matcher: rule fires on matching string, not on non-matching string.
func TestEngine_ArgMatch_RegexMatcher(t *testing.T) {
	engine, err := New([]config.RuleConfig{
		{
			Tool:    "git.commit",
			Verdict: "allow",
			Args: []config.ArgPattern{
				{Path: "message", Match: json.RawMessage(`{"regex": "^feat:"}`)},
			},
		},
		{Tool: "*", Verdict: "deny"},
	})
	require.NoError(t, err)

	// Matches regex → allow
	require.Equal(t, Allow, engine.Evaluate("git.commit", map[string]any{"message": "feat: add login"}))
	// Does not match regex → deny
	require.Equal(t, Deny, engine.Evaluate("git.commit", map[string]any{"message": "fix: broken test"}))
}

// Constructor errors: bad path returns error mentioning rule and args index.
func TestEngine_New_BadPath_EmptyPath(t *testing.T) {
	_, err := New([]config.RuleConfig{
		{
			Tool:    "tool.*",
			Verdict: "allow",
			Args: []config.ArgPattern{
				{Path: "", Match: json.RawMessage(`"value"`)},
			},
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "rule 0")
	require.Contains(t, err.Error(), "args[0]")
}

// Constructor errors: double-dot path returns error mentioning rule and args index.
func TestEngine_New_BadPath_EmptySegment(t *testing.T) {
	_, err := New([]config.RuleConfig{
		{
			Tool:    "tool.*",
			Verdict: "allow",
			Args: []config.ArgPattern{
				{Path: "a..b", Match: json.RawMessage(`"value"`)},
			},
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "rule 0")
	require.Contains(t, err.Error(), "args[0]")
}

// Constructor errors: bad regex returns error mentioning rule and args index.
func TestEngine_New_BadRegex(t *testing.T) {
	_, err := New([]config.RuleConfig{
		{
			Tool:    "tool.*",
			Verdict: "allow",
			Args: []config.ArgPattern{
				{Path: "field", Match: json.RawMessage(`{"regex": "[invalid"}`)},
			},
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "rule 0")
	require.Contains(t, err.Error(), "args[0]")
}
