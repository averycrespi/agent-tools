package rules

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

func TestEngine_Evaluate_AllowRule(t *testing.T) {
	e := New([]config.RuleConfig{
		{Tool: "github.*", Verdict: "allow"},
	})
	require.Equal(t, Allow, e.Evaluate("github.get_pr"))
}

func TestEngine_Evaluate_DenyRule(t *testing.T) {
	e := New([]config.RuleConfig{
		{Tool: "*", Verdict: "deny"},
	})
	require.Equal(t, Deny, e.Evaluate("anything"))
}

func TestEngine_Evaluate_RequireApprovalRule(t *testing.T) {
	e := New([]config.RuleConfig{
		{Tool: "fs.write_file", Verdict: "require-approval"},
	})
	require.Equal(t, RequireApproval, e.Evaluate("fs.write_file"))
}

func TestEngine_Evaluate_FirstMatchWins(t *testing.T) {
	e := New([]config.RuleConfig{
		{Tool: "github.push", Verdict: "require-approval"},
		{Tool: "github.*", Verdict: "allow"},
		{Tool: "*", Verdict: "deny"},
	})
	require.Equal(t, RequireApproval, e.Evaluate("github.push"))
	require.Equal(t, Allow, e.Evaluate("github.get_pr"))
	require.Equal(t, Deny, e.Evaluate("linear.search"))
}

func TestEngine_Evaluate_DefaultIsRequireApproval(t *testing.T) {
	e := New(nil)
	require.Equal(t, RequireApproval, e.Evaluate("anything"))
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
	e := New(input)
	require.Equal(t, input, e.Rules())
}

func TestEngine_Rules_EmptyWhenNil(t *testing.T) {
	e := New(nil)
	require.Empty(t, e.Rules())
}

func TestEngine_EvaluateWithRule_FirstMatchWins(t *testing.T) {
	e := New([]config.RuleConfig{
		{Tool: "github.push", Verdict: "require-approval"}, // index 0
		{Tool: "github.*", Verdict: "allow"},               // index 1
		{Tool: "*", Verdict: "deny"},                       // index 2
	})

	v, idx := e.EvaluateWithRule("github.push")
	require.Equal(t, RequireApproval, v)
	require.Equal(t, 0, idx)

	v, idx = e.EvaluateWithRule("github.get_pr")
	require.Equal(t, Allow, v)
	require.Equal(t, 1, idx)

	v, idx = e.EvaluateWithRule("linear.search")
	require.Equal(t, Deny, v)
	require.Equal(t, 2, idx)
}

func TestEngine_EvaluateWithRule_NoMatchReturnsNegativeOne(t *testing.T) {
	e := New([]config.RuleConfig{
		{Tool: "github.*", Verdict: "allow"},
	})
	v, idx := e.EvaluateWithRule("linear.search")
	require.Equal(t, RequireApproval, v) // default
	require.Equal(t, -1, idx)
}

func TestEngine_EvaluateWithRule_EmptyRules(t *testing.T) {
	e := New(nil)
	v, idx := e.EvaluateWithRule("anything")
	require.Equal(t, RequireApproval, v)
	require.Equal(t, -1, idx)
}
