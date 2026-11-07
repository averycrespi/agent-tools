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
	"github.com/stretchr/testify/require"
)

func TestCLIResourceConfigurationRoundtripAndActivationRequirements(t *testing.T) {
	source := newGatewayHarness(t)
	source.Start()
	bearer := filepath.Join(t.TempDir(), "source-bearer")
	require.NoError(t, os.WriteFile(bearer, []byte(source.bearer), 0o600))
	generated := runOnlineCLI(t, source, bearer, true, "agent", "create", "--display-name", "Generated key", "--visibility", "allowed-only", "--json")
	var generatedAgent contract.PrincipalCreation
	require.NoError(t, json.Unmarshal(generated.Stdout, &generatedAgent))
	require.True(t, contract.ValidResourceKey(generatedAgent.Principal.Key))
	require.NotEqual(t, generatedAgent.Principal.ID, generatedAgent.Principal.Key)
	chosen := runOnlineCLI(t, source, bearer, true, "agent", "create", "--display-name", "Portable agent", "--visibility", "allowed-only", "--key", "portable-agent", "--json")
	var agent contract.PrincipalCreation
	require.NoError(t, json.Unmarshal(chosen.Stdout, &agent))
	require.Equal(t, "portable-agent", agent.Principal.Key)
	human := runOnlineCLI(t, source, bearer, true, "agent", "get", agent.Principal.ID)
	require.Contains(t, string(human.Stdout), "portable-agent")
	exported := runOnlineCLI(t, source, bearer, true, "agent", "export", "portable-agent")
	require.Empty(t, exported.Stderr)
	var definition contract.ConfigDefinition
	require.NoError(t, json.Unmarshal(exported.Stdout, &definition))
	require.Equal(t, "agent", definition.Kind)
	require.Equal(t, "portable-agent", definition.Key)
	agentFile := filepath.Join(t.TempDir(), "agent.json")
	require.NoError(t, os.WriteFile(agentFile, exported.Stdout, 0o600))
	// Ordinary MCP create can omit namespace and delegates key generation.
	serverFile := filepath.Join(t.TempDir(), "ordinary-server.json")
	require.NoError(t, os.WriteFile(serverFile, []byte(`{"display_name":"Generated namespace","enabled":false,"transport":{"kind":"stdio","executable":"/bin/cat","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{}}}`), 0o600))
	serverCreate := runOnlineCLI(t, source, bearer, true, "mcp", "server", "create", "--file", serverFile, "--json")
	var server struct {
		Server struct {
			Namespace string `json:"namespace"`
		} `json:"server"`
	}
	require.NoError(t, json.Unmarshal(serverCreate.Stdout, &server))
	require.True(t, contract.ValidResourceKey(server.Server.Namespace))
	require.NotEmpty(t, server.Server.Namespace)
	source.Stop(syscall.SIGTERM)

	target := newGatewayHarness(t)
	target.Start()
	targetBearer := filepath.Join(t.TempDir(), "target-bearer")
	require.NoError(t, os.WriteFile(targetBearer, []byte(target.bearer), 0o600))
	before := target.adminSnapshot(http.MethodGet, contract.ConfigExportPath, nil)
	require.Equal(t, 200, before.StatusCode)
	refusal := runOnlineCLI(t, target, targetBearer, false, "agent", "import", "--file", agentFile)
	require.Empty(t, refusal.Stdout)
	require.Contains(t, string(refusal.Stderr), "--confirm")
	preview := runOnlineCLI(t, target, targetBearer, true, "agent", "import", "--file", agentFile, "--dry-run", "--json")
	var plan contract.ConfigPreview
	require.NoError(t, json.Unmarshal(preview.Stdout, &plan))
	require.True(t, plan.Advisory)
	require.Len(t, plan.Creations, 1)
	require.Contains(t, plan.Creations[0].Requirements, "issue_agent_credential")
	after := target.adminSnapshot(http.MethodGet, contract.ConfigExportPath, nil)
	require.JSONEq(t, string(before.Body), string(after.Body), "preview and no-confirm do not create configuration")
	created := runOnlineCLI(t, target, targetBearer, true, "agent", "import", "--file", agentFile, "--confirm", "--json")
	var result contract.ConfigImportResult
	require.NoError(t, json.Unmarshal(created.Stdout, &result))
	require.Equal(t, "committed", result.Configuration)
	require.Equal(t, "not_required", result.Activation)
	require.Len(t, result.Creations, 1)
	require.NotEqual(t, agent.Principal.ID, result.Creations[0].ID)
	reexport := runOnlineCLI(t, target, targetBearer, true, "agent", "export", "portable-agent")
	require.JSONEq(t, string(exported.Stdout), string(reexport.Stdout))
	conflict := runOnlineCLI(t, target, targetBearer, false, "agent", "import", "--file", agentFile, "--confirm", "--json")
	require.Empty(t, conflict.Stdout)
	require.Contains(t, string(conflict.Stderr), "conflict")
	// A grant resolves an existing typed key; no dependencies or default grants
	// are imported implicitly. A missing key fails without partial creation.
	grantFile := filepath.Join(t.TempDir(), "grant.json")
	grant := `{"format":"agent-gateway-config","version":2,"kind":"mcp_grant","key":"portable-grant","spec":{"agent":{"kind":"agent","key":"portable-agent"},"server":{"kind":"gateway_mcp"},"description":null,"effect":"allow","upstream_name":null,"constraint":null,"read_only":false,"expires_at":null}}`
	require.NoError(t, os.WriteFile(grantFile, []byte(grant), 0o600))
	grantPreview := runOnlineCLI(t, target, targetBearer, true, "mcp", "grant", "import", "--file", grantFile, "--dry-run", "--json")
	require.NoError(t, json.Unmarshal(grantPreview.Stdout, &plan))
	require.Contains(t, plan.Creations[0].Dependencies, contract.ConfigReference{Kind: "agent", Key: "portable-agent"})
	missingFile := filepath.Join(t.TempDir(), "missing-dependency.json")
	require.NoError(t, os.WriteFile(missingFile, []byte(strings.ReplaceAll(strings.Replace(grant, `"key":"portable-grant"`, `"key":"missing-grant"`, 1), `"key":"portable-agent"`, `"key":"absent-agent"`)), 0o600))
	missing := runOnlineCLI(t, target, targetBearer, false, "mcp", "grant", "import", "--file", missingFile, "--confirm", "--json")
	require.Empty(t, missing.Stdout)
	grantCreated := runOnlineCLI(t, target, targetBearer, true, "mcp", "grant", "import", "--file", grantFile, "--confirm", "--json")
	var grantResult contract.ConfigImportResult
	require.NoError(t, json.Unmarshal(grantCreated.Stdout, &grantResult))
	// Enabled configuration with missing material commits; preview explains setup,
	// and the result does not claim runtime readiness or credential availability.
	enabledFile := filepath.Join(t.TempDir(), "enabled.json")
	enabled := `{"format":"agent-gateway-config","version":2,"kind":"mcp_server","key":"needs-material","spec":{"display_name":"Needs material","enabled":true,"deleted":false,"transport":{"kind":"stdio","executable":"/bin/cat","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{"TOKEN":"primary"}},"credentials":{"static":{"primary":"mcp/needs-material/static/primary"},"oauth_client_secret":null,"oauth_authorization":false}}}`
	require.NoError(t, os.WriteFile(enabledFile, []byte(enabled), 0o600))
	needs := runOnlineCLI(t, target, targetBearer, true, "mcp", "server", "import", "--file", enabledFile, "--dry-run", "--json")
	require.NoError(t, json.Unmarshal(needs.Stdout, &plan))
	require.Contains(t, plan.Creations[0].Requirements, "supply_static_credentials")
	require.Contains(t, plan.Creations[0].Requirements, "activate_after_commit")
	committed := runOnlineCLI(t, target, targetBearer, true, "mcp", "server", "import", "--file", enabledFile, "--confirm", "--json")
	require.NoError(t, json.Unmarshal(committed.Stdout, &result))
	require.Equal(t, "committed", result.Configuration)
	require.Equal(t, "pending", result.Activation)
	metadata := runOnlineCLI(t, target, targetBearer, true, "mcp", "server", "get", result.Creations[0].ID, "--json")
	var state struct {
		CredentialState contract.ServerCredentialState `json:"credential_state"`
		Namespace       string                         `json:"namespace"`
	}
	require.NoError(t, json.Unmarshal(metadata.Stdout, &state))
	require.Equal(t, contract.ServerCredentialAbsent, state.CredentialState)
	require.Equal(t, "needs-material", state.Namespace)
	inventory := target.adminSnapshot(http.MethodGet, contract.ConfigExportPath, nil)
	require.Equal(t, 200, inventory.StatusCode)
	var bundle contract.ConfigBundle
	require.NoError(t, json.Unmarshal(inventory.Body, &bundle))
	require.Len(t, bundle.Resources, 4, "exact explicit agent/grant/server and seeded routing, no dependencies or implicit default grant")
	runOnlineCLI(t, target, targetBearer, true, "mcp", "grant", "delete", grantResult.Creations[0].ID, "--yes")
	reservedConflict := runOnlineCLI(t, target, targetBearer, false, "mcp", "grant", "import", "--file", grantFile, "--confirm", "--json")
	require.Empty(t, reservedConflict.Stdout)
	require.Contains(t, string(reservedConflict.Stderr), "conflict")
	target.Stop(syscall.SIGTERM)
}
