package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRenderPiArgv(t *testing.T) {
	argv := RenderPiArgv(AgentOptions{Provider: "anthropic", Model: "claude", Thinking: "medium", Tools: []string{"bash"}, Extensions: []string{"x.ts"}, DisableContextFiles: true})
	assert.Equal(t, []string{"pi", "--mode", "rpc", "--provider", "anthropic", "--model", "claude", "--thinking", "medium", "--tools", "bash", "--extension", "x.ts", "--no-context-files"}, argv)
}

func TestApplyAgentOverrides(t *testing.T) {
	base := AgentOptions{Provider: "anthropic", Model: "claude", Thinking: "medium", Tools: []string{"bash"}, Extensions: []string{"old"}}
	overrides := AgentOptions{Model: "gpt-5", Tools: []string{"git"}, DisableAllTools: true, Extensions: []string{"new"}, SessionDir: "/tmp/sessions"}
	got := ApplyAgentOverrides(base, overrides)
	assert.Equal(t, "anthropic", got.Provider)
	assert.Equal(t, "gpt-5", got.Model)
	assert.Equal(t, []string{"git"}, got.Tools)
	assert.True(t, got.DisableAllTools)
	assert.Equal(t, []string{"new"}, got.Extensions)
	assert.Equal(t, "/tmp/sessions", got.SessionDir)
}

func TestRenderPiArgvSessionDir(t *testing.T) {
	argv := RenderPiArgv(AgentOptions{SessionDir: "/tmp/pi"})
	assert.Contains(t, argv, "--session-dir")
	assert.Contains(t, argv, "/tmp/pi")
}
