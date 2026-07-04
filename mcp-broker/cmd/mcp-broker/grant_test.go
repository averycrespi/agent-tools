package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
)

func TestGrantCLI_MintListRevokeOffline(t *testing.T) {
	oldCfgFile := cfgFile
	t.Cleanup(func() { cfgFile = oldCfgFile })

	dir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Grants.Path = filepath.Join(dir, "grants.db")
	cfgPath := filepath.Join(dir, "config.json")
	_, err := config.Save(cfg, cfgPath)
	require.NoError(t, err)
	cfgFile = cfgPath

	rulesPath := filepath.Join(dir, "rules.json")
	require.NoError(t, os.WriteFile(rulesPath, []byte(`[{"tool":"git.push","verdict":"allow","reason":"test"}]`), 0o600))

	mintOut := &bytes.Buffer{}
	mintCmd := newGrantMintTestCommand(mintOut)
	require.NoError(t, mintCmd.Flags().Set("name", "release"))
	require.NoError(t, mintCmd.Flags().Set("ttl", "1h"))
	require.NoError(t, mintCmd.Flags().Set("rules-file", rulesPath))
	require.NoError(t, runGrantMint(mintCmd, nil))

	token := tokenFromMintOutput(t, mintOut.String())
	require.Len(t, token, 64)
	require.Equal(t, 1, strings.Count(mintOut.String(), token))

	listOut := &bytes.Buffer{}
	listCmd := newGrantListTestCommand(listOut)
	require.NoError(t, runGrantList(listCmd, nil))
	require.Contains(t, listOut.String(), "release")
	require.Contains(t, listOut.String(), "active")
	require.NotContains(t, listOut.String(), token)

	fingerprint := fingerprintFromListOutput(t, listOut.String())
	revokeOut := &bytes.Buffer{}
	revokeCmd := newGrantRevokeTestCommand(revokeOut)
	require.NoError(t, runGrantRevoke(revokeCmd, []string{fingerprint}))
	require.Contains(t, revokeOut.String(), "Revoked grant")
	require.NotContains(t, revokeOut.String(), token)

	listAfterRevokeOut := &bytes.Buffer{}
	listAfterRevokeCmd := newGrantListTestCommand(listAfterRevokeOut)
	require.NoError(t, runGrantList(listAfterRevokeCmd, nil))
	require.Contains(t, listAfterRevokeOut.String(), "revoked")
	require.NotContains(t, listAfterRevokeOut.String(), token)
}

func TestGrantCLI_MintRejectsTTLAboveConfiguredMaximum(t *testing.T) {
	oldCfgFile := cfgFile
	t.Cleanup(func() { cfgFile = oldCfgFile })

	dir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.Grants.Path = filepath.Join(dir, "grants.db")
	cfg.Grants.MaxTTLSeconds = 60
	cfgPath := filepath.Join(dir, "config.json")
	_, err := config.Save(cfg, cfgPath)
	require.NoError(t, err)
	cfgFile = cfgPath

	cmd := newGrantMintTestCommand(&bytes.Buffer{})
	cmd.SetIn(strings.NewReader(`[{"tool":"*","verdict":"allow"}]`))
	require.NoError(t, cmd.Flags().Set("name", "too-long"))
	require.NoError(t, cmd.Flags().Set("ttl", "2m"))
	require.NoError(t, cmd.Flags().Set("rules-file", "-"))
	err = runGrantMint(cmd, nil)
	require.ErrorContains(t, err, "exceeds maximum")
}

func newGrantMintTestCommand(out *bytes.Buffer) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	cmd.Flags().String("name", "", "")
	cmd.Flags().String("ttl", "", "")
	cmd.Flags().String("rules-file", "", "")
	cmd.Flags().String("description", "", "")
	cmd.Flags().Bool("json", false, "")
	return cmd
}

func newGrantListTestCommand(out *bytes.Buffer) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	cmd.Flags().Bool("json", false, "")
	return cmd
}

func newGrantRevokeTestCommand(out *bytes.Buffer) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	cmd.Flags().Bool("json", false, "")
	return cmd
}

func tokenFromMintOutput(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if token, ok := strings.CutPrefix(line, "Token: "); ok {
			return token
		}
	}
	t.Fatalf("mint output did not contain token line: %s", out)
	return ""
}

func fingerprintFromListOutput(t *testing.T, out string) string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.GreaterOrEqual(t, len(lines), 2, out)
	fields := strings.Fields(lines[1])
	require.GreaterOrEqual(t, len(fields), 2, out)
	return fields[1]
}
