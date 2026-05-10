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
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.json"), []byte(`{"name":"go","agent":{"model":"gpt-5","thinking":"high"}}`), 0o600))
	templates, err := DiscoverTemplates([]string{dir})
	require.NoError(t, err)
	require.Len(t, templates, 1)
	assert.Equal(t, "go", templates[0].Name)
	assert.Equal(t, "pi", templates[0].Agent.Command)
	assert.Equal(t, "rpc", templates[0].Agent.Mode)
}

func TestDiscoverTemplates_DuplicateNamesFail(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir1, "a.json"), []byte(`{"name":"same"}`), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir2, "b.json"), []byte(`{"name":"same"}`), 0o600))
	_, err := DiscoverTemplates([]string{dir1, dir2})
	assert.ErrorContains(t, err, "duplicate template")
}

func TestFindTemplate_Default(t *testing.T) {
	tmpl, err := FindTemplate(nil, "")
	require.NoError(t, err)
	assert.Equal(t, "pi", tmpl.Agent.Command)
	assert.Equal(t, "rpc", tmpl.Agent.Mode)
}

func TestRenderPiArgv(t *testing.T) {
	argv := RenderPiArgv(AgentTemplate{Command: "pi", Mode: "rpc", Provider: "anthropic", Model: "claude", Thinking: "medium", Tools: []string{"bash"}, Extensions: []string{"x.ts"}, DisableContextFiles: true})
	assert.Equal(t, []string{"pi", "--mode", "rpc", "--provider", "anthropic", "--model", "claude", "--thinking", "medium", "--tools", "bash", "--extension", "x.ts", "--no-context-files"}, argv)
}
