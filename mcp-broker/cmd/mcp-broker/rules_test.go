package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

func TestRulesCommandIsRegistered(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"rules", "path"})
	require.NoError(t, err)
	require.Equal(t, "path", cmd.Name())
}

func TestRulesFilePathUsesEffectiveConfigPath(t *testing.T) {
	oldCfgFile := cfgFile
	cfgFile = filepath.Join(t.TempDir(), "profile", "config.json")
	t.Cleanup(func() { cfgFile = oldCfgFile })

	path, err := rulesFilePath()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(filepath.Dir(cfgFile), "rules.json"), path)
}

func TestRefreshRulesFileCreatesCanonicalRulesDocument(t *testing.T) {
	oldCfgFile := cfgFile
	cfgFile = filepath.Join(t.TempDir(), "config.json")
	t.Cleanup(func() { cfgFile = oldCfgFile })

	path, err := refreshRulesFile()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(filepath.Dir(cfgFile), "rules.json"), path)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"rules"`)
	require.Contains(t, string(raw), `"require-approval"`)
}

func TestWarnRulesLoadResultWritesIgnoredLegacyWarning(t *testing.T) {
	output := captureStderr(t, func() {
		warnRulesLoadResult(config.RulesLoadResult{Path: "/tmp/rules.json", IgnoredLegacy: true})
	})

	require.Contains(t, output, "legacy config rules ignored")
	require.Contains(t, output, "/tmp/rules.json")
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = oldStderr })

	fn()
	require.NoError(t, w.Close())
	data, err := io.ReadAll(r)
	require.NoError(t, err)
	require.NoError(t, r.Close())
	os.Stderr = oldStderr
	return string(data)
}
