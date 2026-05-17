package main

import (
	"testing"

	pdconfig "github.com/averycrespi/agent-tools/pi-dispatch/internal/config"
	"github.com/stretchr/testify/require"
)

func TestRunEnvMetadataPersistsKeysOnly(t *testing.T) {
	env, err := parseRunEnv([]string{"OPENAI_API_KEY=secret", "EMPTY=", "OPENAI_API_KEY=new secret"})
	require.NoError(t, err)

	namesJSON, err := runEnvNamesMetadata(env)

	require.NoError(t, err)
	require.JSONEq(t, `["OPENAI_API_KEY","EMPTY"]`, namesJSON)
	require.NotContains(t, namesJSON, "secret")
}

func TestParseRunEnvRejectsInvalidNames(t *testing.T) {
	_, err := parseRunEnv([]string{"BAD-NAME=value"})
	require.ErrorContains(t, err, "invalid env var name")
}

func TestRunLaunchMetadataRendersEffectiveOptions(t *testing.T) {
	agent := pdconfig.AgentOptions{Provider: "openai", Model: "gpt-5", Thinking: "high", Tools: []string{"bash"}, SystemPrompt: "secret system prompt"}

	agentOptionsJSON, piArgvJSON, err := runLaunchMetadata(agent)

	require.NoError(t, err)
	require.JSONEq(t, `{"provider":"openai","model":"gpt-5","thinking":"high","tools":["bash"],"system_prompt":"secret system prompt"}`, agentOptionsJSON)
	require.JSONEq(t, `["pi","--mode","rpc","--provider","openai","--model","gpt-5","--thinking","high","--tools","bash","--system-prompt","secret system prompt"]`, piArgvJSON)
}

func TestApplyRunOverrides(t *testing.T) {
	old := runAgentOverrides
	runAgentOverrides = pdconfig.AgentOptions{Model: "gpt-5", Thinking: "high", Tools: []string{"bash"}, Extensions: []string{"x.ts"}, DisableAllTools: true, SessionDir: "/tmp/pi"}
	defer func() { runAgentOverrides = old }()

	got := applyRunOverrides(pdconfig.AgentOptions{Provider: "anthropic", Model: "claude", Thinking: "medium"})

	require.Equal(t, "anthropic", got.Provider)
	require.Equal(t, "gpt-5", got.Model)
	require.Equal(t, "high", got.Thinking)
	require.Equal(t, []string{"bash"}, got.Tools)
	require.Equal(t, []string{"x.ts"}, got.Extensions)
	require.True(t, got.DisableAllTools)
	require.Equal(t, "/tmp/pi", got.SessionDir)
}
