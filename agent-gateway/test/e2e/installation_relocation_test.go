//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/stretchr/testify/require"
)

func TestInstallationRelocationPreservesExistingAuthority(t *testing.T) {
	harness := newGatewayHarness(t)
	source, err := filepath.EvalSymlinks(harness.root)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(filepath.Dir(source), 0o700))
	destination := filepath.Join(filepath.Dir(source), "agent-gateway")
	bearerPath := filepath.Join(source, "admin-bearer")
	require.NoError(t, os.WriteFile(bearerPath, []byte(harness.bearer+"\n"), 0o600))
	harness.Start()
	created := runOnlineCLI(t, harness, bearerPath, true, "principal", "create", "--display-name", "Migration principal", "--visibility", "all", "--json")
	var creation contract.PrincipalCreation
	require.NoError(t, json.Unmarshal(created.Stdout, &creation))
	agentPath := filepath.Join(t.TempDir(), "agent")
	issued := runOnlineCLI(t, harness, bearerPath, true, "principal", "credential", "issue", creation.Principal.ID, "--secret-output", agentPath, "--yes", "--json")
	var principal contract.Principal
	require.NoError(t, json.Unmarshal(issued.Stdout, &principal))
	grantResult := runOnlineCLI(t, harness, bearerPath, true, "mcp", "grant", "create", "--description", "Preserved policy", "--principal-id", principal.ID, "--effect", "deny", "--server-id", contract.SyntheticServerID, "--upstream-name", "get_identity", "--json")
	var grant contract.Grant
	require.NoError(t, json.Unmarshal(grantResult.Stdout, &grant))
	backupResult := runOnlineCLI(t, harness, bearerPath, true, "backup", "create", "--json")
	var backup contract.Backup
	require.NoError(t, json.Unmarshal(backupResult.Stdout, &backup))
	_, err = paths.InspectRelocation(source, destination)
	require.ErrorIs(t, err, paths.ErrInUse)
	harness.Stop(syscall.SIGTERM)

	relocation, err := paths.InspectRelocation(source, destination)
	require.NoError(t, err)
	require.NoError(t, relocation.Commit())
	require.NoError(t, relocation.Close())
	harness.root = destination
	harness.serveArgs[2] = destination
	bearerPath = filepath.Join(destination, "admin-bearer")
	harness.Start()
	afterPrincipal := runOnlineCLI(t, harness, bearerPath, true, "principal", "get", principal.ID, "--json")
	require.JSONEq(t, string(issued.Stdout), string(afterPrincipal.Stdout))
	afterGrant := runOnlineCLI(t, harness, bearerPath, true, "mcp", "grant", "get", grant.ID, "--json")
	require.JSONEq(t, string(grantResult.Stdout), string(afterGrant.Stdout))
	afterBackup := runOnlineCLI(t, harness, bearerPath, true, "backup", "get", backup.ID, "--json")
	require.JSONEq(t, string(backupResult.Stdout), string(afterBackup.Stdout))
	raw, err := os.ReadFile(agentPath)
	require.NoError(t, err)
	agent := newAgentBearer(t, strings.TrimSpace(string(raw)))
	defer agent.destroy()
	require.Equal(t, http.StatusOK, harness.ModernDiscover(agent, json.RawMessage(`1`)).StatusCode, "pre-migration agent authority still authenticates")
	harness.Stop(syscall.SIGTERM)
	preserved, err := os.ReadFile(bearerPath)
	require.NoError(t, err)
	require.Equal(t, harness.bearer+"\n", string(preserved))
	verified, err := harness.runner.Run(t.Context(), harness.binary, "--data-dir", destination, "storage", "verify", "--json")
	require.NoError(t, err, "%s", verified.Stderr)
	replacement := filepath.Join(t.TempDir(), "restore-bearer")
	restored, err := harness.runner.Run(t.Context(), harness.binary, "--data-dir", destination, "backup", "restore", backup.ID, "--secret-output", replacement, "--json")
	require.NoError(t, err, "%s", restored.Stderr)
	require.Contains(t, string(restored.Stdout), backup.InstallationID)
	require.True(t, paths.RelocationCompleted(source, destination))
}
