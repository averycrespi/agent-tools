package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	pdconfig "github.com/averycrespi/agent-tools/pi-dispatch/internal/config"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestListTemplatesPrintsDiscoveredTemplates(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.json"), []byte(`{"description":"Go agent"}`), 0o600))
	withTemplateTestConfig(t, dir, false)

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)

	require.NoError(t, listTemplates(cmd, nil))
	require.Contains(t, out.String(), "go")
	require.Contains(t, out.String(), "Go agent")
}

func TestValidateTemplatesPrintsOKForOneTemplate(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.json"), []byte(`{"agent":{"model":"gpt-5"}}`), 0o600))
	withTemplateTestConfig(t, dir, false)

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)

	require.NoError(t, validateTemplates(cmd, []string{"go"}))
	require.Contains(t, out.String(), "go\tok")
}

func TestValidateTemplatesFailsForInvalidTemplate(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`{"agent":{"tools":["bash"],"disable_all_tools":true}}`), 0o600))
	withTemplateTestConfig(t, dir, false)

	cmd := &cobra.Command{}

	err := validateTemplates(cmd, []string{"bad"})
	require.ErrorContains(t, err, "disable_all_tools")
}

func TestShowTemplatePrintsTemplateJSON(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.json"), []byte(`{"description":"Go agent","agent":{"model":"gpt-5"}}`), 0o600))
	withTemplateTestConfig(t, dir, false)

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)

	require.NoError(t, showTemplate(cmd, []string{"go"}))
	require.Contains(t, out.String(), `"name": "go"`)
	require.Contains(t, out.String(), `"model": "gpt-5"`)
}

func TestRenderTemplatePrintsPiArgv(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.json"), []byte(`{"agent":{"model":"gpt-5"}}`), 0o600))
	withTemplateTestConfig(t, dir, false)

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)

	require.NoError(t, renderTemplate(cmd, []string{"go"}))
	require.Equal(t, "pi --mode rpc --model gpt-5\n", out.String())
}

func withTemplateTestConfig(t *testing.T, dir string, json bool) {
	t.Helper()
	oldCfg := cfg
	oldJSON := jsonOut
	cfg = pdconfig.Config{TemplateDirs: []string{dir}}
	jsonOut = json
	t.Cleanup(func() { cfg = oldCfg; jsonOut = oldJSON })
}
