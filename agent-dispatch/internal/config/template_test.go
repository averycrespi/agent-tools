package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscoverTemplates(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.json"), []byte(`{"agent":{"model":"gpt-5","thinking":"high"}}`), 0o600))
	templates, err := DiscoverTemplates([]string{dir})
	require.NoError(t, err)
	require.Len(t, templates, 1)
	assert.Equal(t, "go", templates[0].Name)
	assert.Equal(t, "pi", templates[0].Agent.Command)
}

func TestDiscoverTemplatesUsesFilenameName(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "filename.json"), []byte(`{"name":"ignored","agent":{"model":"gpt-5"}}`), 0o600))
	templates, err := DiscoverTemplates([]string{dir})
	require.NoError(t, err)
	require.Len(t, templates, 1)
	assert.Equal(t, "filename", templates[0].Name)
}

func TestFindTemplateUsesFilenameName(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "filename.json"), []byte(`{"name":"ignored","agent":{"model":"gpt-5"}}`), 0o600))
	tmpl, err := FindTemplate([]string{dir}, "filename")
	require.NoError(t, err)
	assert.Equal(t, "filename", tmpl.Name)
}

func TestRenderPiArgvAlwaysUsesRPCMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"agent":{"mode":"tui"}}`), 0o600))
	tmpl, err := LoadTemplate(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"pi", "--mode", "rpc"}, RenderPiArgv(tmpl.Agent))
}

func TestDiscoverTemplates_DuplicateNamesFail(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir1, "same.json"), []byte(`{}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir2, "same.json"), []byte(`{}`), 0o600))
	_, err := DiscoverTemplates([]string{dir1, dir2})
	assert.ErrorContains(t, err, "duplicate template")
}

func TestFindTemplate_Default(t *testing.T) {
	tmpl, err := FindTemplate(nil, "")
	require.NoError(t, err)
	assert.Equal(t, "pi", tmpl.Agent.Command)
}

func TestRenderPiArgv(t *testing.T) {
	argv := RenderPiArgv(AgentTemplate{Command: "pi", Provider: "anthropic", Model: "claude", Thinking: "medium", Tools: []string{"bash"}, Extensions: []string{"x.ts"}, DisableContextFiles: true})
	assert.Equal(t, []string{"pi", "--mode", "rpc", "--provider", "anthropic", "--model", "claude", "--thinking", "medium", "--tools", "bash", "--extension", "x.ts", "--no-context-files"}, argv)
}

func TestApplyAgentOverrides(t *testing.T) {
	base := AgentTemplate{Provider: "anthropic", Model: "claude", Thinking: "medium", Tools: []string{"bash"}, Extensions: []string{"old"}}
	overrides := AgentTemplate{Model: "gpt-5", Tools: []string{"git"}, DisableAllTools: true, Extensions: []string{"new"}, SessionDir: "/tmp/sessions"}
	got := ApplyAgentOverrides(base, overrides)
	assert.Equal(t, "anthropic", got.Provider)
	assert.Equal(t, "gpt-5", got.Model)
	assert.Equal(t, []string{"git"}, got.Tools)
	assert.True(t, got.DisableAllTools)
	assert.Equal(t, []string{"new"}, got.Extensions)
	assert.Equal(t, "/tmp/sessions", got.SessionDir)
}

func TestRenderPiArgvSessionDir(t *testing.T) {
	argv := RenderPiArgv(AgentTemplate{SessionDir: "/tmp/pi"})
	assert.Contains(t, argv, "--session-dir")
	assert.Contains(t, argv, "/tmp/pi")
}
