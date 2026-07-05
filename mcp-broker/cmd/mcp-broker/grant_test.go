package main

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/mcp-broker/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/broker"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/config"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/grants"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/rules"
	"github.com/averycrespi/agent-tools/mcp-broker/internal/server"
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

func TestMCPHandlerGrantHeaderChangesPolicy(t *testing.T) {
	store, err := grants.Open(filepath.Join(t.TempDir(), "grants.db"))
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	minted, err := store.Mint(context.Background(), grants.MintOptions{
		Name:   "allow-push",
		TTL:    time.Hour,
		MaxTTL: time.Hour,
		Rules:  []config.RuleConfig{{Tool: "git.push", Verdict: "allow"}},
	})
	require.NoError(t, err)

	baseRules, err := rules.New([]config.RuleConfig{{Tool: "git.*", Verdict: "deny", Reason: "base deny"}})
	require.NoError(t, err)
	mgr := &grantHeaderTestServerManager{}
	b := broker.NewWithGrants(mgr, baseRules, grantHeaderTestAuditor{}, nil, store, nil)
	handler := makeMCPHandler(b)

	withoutGrant, err := handler(context.Background(), gomcp.CallToolRequest{
		Header: http.Header{},
		Params: gomcp.CallToolParams{Name: "git.push", Arguments: map[string]any{"branch": "main"}},
	})
	require.NoError(t, err)
	require.True(t, withoutGrant.IsError)
	require.False(t, mgr.called)

	withGrant, err := handler(context.Background(), gomcp.CallToolRequest{
		Header: http.Header{"Mcp-Broker-Grant": {minted.Token}},
		Params: gomcp.CallToolParams{Name: "git.push", Arguments: map[string]any{"branch": "main"}},
	})
	require.NoError(t, err)
	require.False(t, withGrant.IsError)
	require.True(t, mgr.called)
}

func TestMCPHandlerMalformedGrantHeaderAuditsAndBlocksProxy(t *testing.T) {
	baseRules, err := rules.New([]config.RuleConfig{{Tool: "git.*", Verdict: "allow"}})
	require.NoError(t, err)
	mgr := &grantHeaderTestServerManager{}
	auditor := &reloadTestAudit{}
	b := broker.NewWithGrants(mgr, baseRules, auditor, nil, nil, nil)
	handler := makeMCPHandler(b)

	result, err := handler(context.Background(), gomcp.CallToolRequest{
		Header: http.Header{"Mcp-Broker-Grant": {"one", "two"}},
		Params: gomcp.CallToolParams{Name: "git.push", Arguments: map[string]any{"branch": "main"}},
	})
	require.NoError(t, err)
	require.True(t, result.IsError)
	require.False(t, mgr.called)
	require.Len(t, auditor.records, 1)
	require.Equal(t, "deny", auditor.records[0].Verdict)
	require.Equal(t, "invalid", auditor.records[0].GrantStatus)
	require.Equal(t, "none/default", auditor.records[0].RuleSource)
	require.Contains(t, auditor.records[0].Error, "invalid grant header")
}

type grantHeaderTestServerManager struct{ called bool }

func (m *grantHeaderTestServerManager) Tools() []server.Tool { return nil }

func (m *grantHeaderTestServerManager) Call(_ context.Context, tool string, args map[string]any) (*server.ToolResult, error) {
	m.called = true
	return &server.ToolResult{Content: map[string]any{"ok": true}}, nil
}

type grantHeaderTestAuditor struct{}

func (grantHeaderTestAuditor) Record(context.Context, audit.Record) error { return nil }

func (grantHeaderTestAuditor) Query(context.Context, audit.QueryOpts) ([]audit.Record, int, error) {
	return []audit.Record{}, 0, nil
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
