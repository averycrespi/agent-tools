package main

import (
	"testing"

	adconfig "github.com/averycrespi/agent-tools/agent-dispatch/internal/config"
	"github.com/stretchr/testify/require"
)

func TestApplyRunOverrides(t *testing.T) {
	old := runAgentOverrides
	runAgentOverrides = adconfig.AgentTemplate{Model: "gpt-5", Thinking: "high", Tools: []string{"bash"}, Extensions: []string{"x.ts"}, DisableAllTools: true, SessionDir: "/tmp/pi"}
	defer func() { runAgentOverrides = old }()

	got := applyRunOverrides(adconfig.AgentTemplate{Provider: "anthropic", Model: "claude", Thinking: "medium"})

	require.Equal(t, "anthropic", got.Provider)
	require.Equal(t, "gpt-5", got.Model)
	require.Equal(t, "high", got.Thinking)
	require.Equal(t, []string{"bash"}, got.Tools)
	require.Equal(t, []string{"x.ts"}, got.Extensions)
	require.True(t, got.DisableAllTools)
	require.Equal(t, "/tmp/pi", got.SessionDir)
}
