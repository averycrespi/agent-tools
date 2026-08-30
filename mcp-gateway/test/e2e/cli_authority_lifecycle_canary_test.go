//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLIAuthorityLifecycleCanary(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	bearerPath := filepath.Join(t.TempDir(), "admin-bearer")
	require.NoError(t, os.WriteFile(bearerPath, []byte(harness.bearer+"\n"), 0o600))
	dir := t.TempDir()
	credentialPath := filepath.Join(dir, "agent-bearer")
	results := make([]testutil.ProcessResult, 0, 12)

	status := runOnlineCLI(t, harness, bearerPath, true, "status", "--output", "json")
	admins := runOnlineCLI(t, harness, bearerPath, true, "admin", "credential", "list", "--limit", "10", "--output", "json")
	backupResult := runOnlineCLI(t, harness, bearerPath, true, "backup", "create", "--idempotency-key", "m10-backup", "--output", "json")
	principalResult := runOnlineCLI(t, harness, bearerPath, true, "principal", "create", "--display-name", "M10 principal", "--visibility", "all", "--output", "json")
	results = append(results, status, admins, backupResult, principalResult)
	var backup contract.Backup
	var principalCreation contract.PrincipalCreation
	require.NoError(t, json.Unmarshal(backupResult.Stdout, &backup))
	require.NoError(t, json.Unmarshal(principalResult.Stdout, &principalCreation))
	principalID := principalCreation.Principal.ID
	principalETag := contract.PrincipalETag(principalID, principalCreation.Principal.Revision)

	issued := runOnlineCLI(t, harness, bearerPath, true, "principal", "credential", "issue", principalID, "--etag", principalETag, "--secret-output", credentialPath, "--yes", "--output", "json")
	results = append(results, issued)
	var principal contract.Principal
	require.NoError(t, json.Unmarshal(issued.Stdout, &principal))
	agentBearer, err := os.ReadFile(credentialPath)
	require.NoError(t, err)
	principalETag = contract.PrincipalETag(principalID, principal.Revision)

	grantPath := filepath.Join(dir, "grant.json")
	grantBody := `{"principal_id":"` + principalID + `","effect":"deny","server_id":"` + contract.SyntheticServerID + `","upstream_name":"get_identity","constraint":null,"expires_at":null}`
	require.NoError(t, os.WriteFile(grantPath, []byte(grantBody), 0o600))
	grantResult := runOnlineCLI(t, harness, bearerPath, true, "grant", "create", "--file", grantPath, "--output", "json")
	results = append(results, grantResult)
	var grant contract.Grant
	require.NoError(t, json.Unmarshal(grantResult.Stdout, &grant))

	grants := runOnlineCLI(t, harness, bearerPath, true, "grant", "list", "--principal-id", principalID, "--limit", "10")
	requests := runOnlineCLI(t, harness, bearerPath, true, "grant-request", "list", "--principal-id", principalID, "--state", "pending", "--output", "json")
	invocations := runOnlineCLI(t, harness, bearerPath, true, "invocation", "list", "--principal-id", principalID, "--limit", "1", "--output", "json")
	results = append(results, grants, requests, invocations)
	assert.Contains(t, string(grants.Stdout), grant.ID)
	assert.Contains(t, string(requests.Stdout), `"items":[]`)

	grantDeleted := runOnlineCLI(t, harness, bearerPath, true, "grant", "delete", grant.ID, "--yes", "--output", "json")
	credentialRevoked := runOnlineCLI(t, harness, bearerPath, true, "principal", "credential", "revoke", principalID, "--etag", principalETag, "--yes", "--output", "json")
	backupDeleted := runOnlineCLI(t, harness, bearerPath, true, "backup", "delete", backup.ID, "--yes", "--output", "json")
	results = append(results, grantDeleted, credentialRevoked, backupDeleted)

	harness.Stop(syscall.SIGTERM)
	secret := strings.TrimSpace(string(agentBearer))
	for _, result := range results {
		assert.NotContains(t, string(result.Stdout), secret)
		assert.NotContains(t, string(result.Stderr), secret)
		assert.NotContains(t, string(result.Stdout), harness.bearer)
		assert.NotContains(t, string(result.Stderr), harness.bearer)
		assert.True(t, result.Cleanup.Reaped)
		assert.False(t, result.Cleanup.Survived)
	}
	assert.Len(t, harness.results, 1, "M10 gate must own one Gateway lifecycle")
}
