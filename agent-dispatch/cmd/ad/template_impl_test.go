package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	adconfig "github.com/averycrespi/agent-tools/agent-dispatch/internal/config"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestListTemplatesPrintsDiscoveredTemplates(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.json"), []byte(`{"name":"go","description":"Go agent"}`), 0o600))
	oldCfg := cfg
	oldJSON := jsonOut
	cfg = adconfig.Config{TemplateDirs: []string{dir}}
	jsonOut = false
	defer func() { cfg = oldCfg; jsonOut = oldJSON }()

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)

	require.NoError(t, listTemplates(cmd, nil))
	require.Contains(t, out.String(), "go")
	require.Contains(t, out.String(), "Go agent")
}
