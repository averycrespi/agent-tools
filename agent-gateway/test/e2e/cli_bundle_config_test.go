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
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func bundleCLIFile(t *testing.T, name string, value any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	return path
}
func bundleCLIDef(kind, key, spec string) contract.ConfigDefinition {
	return contract.ConfigDefinition{Format: contract.ConfigExportFormat, Version: 2, Kind: kind, Key: key, Spec: json.RawMessage(spec)}
}
func bundleCLIGraph() contract.ConfigBundle {
	defs := []contract.ConfigDefinition{
		bundleCLIDef("agent", "builder", `{"display_name":"Builder","state":"active","visibility":"allowed-only","http_default":"block"}`),
		bundleCLIDef("mcp_server", "deleted", `{"display_name":"Deleted","enabled":false,"deleted":true,"transport":null,"credentials":{"static":{},"oauth_client_secret":null,"oauth_authorization":false}}`),
		bundleCLIDef("mcp_grant", "expired", `{"agent":{"kind":"agent","key":"builder"},"server":{"kind":"mcp_server","key":"deleted"},"description":null,"effect":"allow","upstream_name":"tool","constraint":{"version":2,"equals":{"/integer":9007199254740993,"/decimal":1.0}},"read_only":false,"expires_at":"2020-01-01T00:00:00.000000000Z"}`),
		bundleCLIDef("http_credential", "http", `{"name":"HTTP","boundary":{"host":"example.com","port":443,"allow_wildcard":false},"recipe":{"header":"Authorization","prefix":"Bearer "},"deleted":false,"secret_ref":"http_credential/http/secret"}`),
		bundleCLIDef("http_grant", "web", `{"agent":{"kind":"agent","key":"builder"},"description":null,"policy":{"version":1,"type":"allow_requests","request":{"origin":{"scheme":"https","host":"example.com","port":443},"methods":{"any":true},"path":{"kind":"any"}},"allow_private":false,"credential":{"kind":"http_credential","key":"http"}},"expires_at":"2020-01-01T00:00:00.000000000Z"}`),
		bundleCLIDef("git_credential", "git", `{"name":"Git","origin":"https://example.com:443","recipe":{"header":"Authorization","prefix":"Bearer "},"deleted":false,"secret_ref":"git_credential/git/secret"}`),
		bundleCLIDef("git_repository", "repo", `{"name":"Repo","url":"https://example.com:443/org/repo","aliases":[],"credential":{"kind":"git_credential","key":"git"},"deleted":true}`),
		bundleCLIDef("git_grant", "read", `{"agent":{"kind":"agent","key":"builder"},"repository":{"kind":"git_repository","key":"repo"},"description":null,"policy":{"version":1,"read":true,"refs":[]},"expires_at":"2020-01-01T00:00:00.000000000Z"}`),
		bundleCLIDef("git_routing", "", `{"origins":[]}`),
		bundleCLIDef("mcp_server", "needs-material", `{"display_name":"Needs material","enabled":true,"deleted":false,"transport":{"kind":"stdio","executable":"/bin/cat","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{"TOKEN":"primary"}},"credentials":{"static":{"primary":"mcp/needs-material/static/primary"},"oauth_client_secret":null,"oauth_authorization":false}}`),
		bundleCLIDef("mcp_server", "startup-fails", `{"display_name":"Startup fails","enabled":true,"deleted":false,"transport":{"kind":"stdio","executable":"/bin/false","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{}},"credentials":{"static":{},"oauth_client_secret":null,"oauth_authorization":false}}`),
	}
	for _, index := range []int{2, 4, 7} {
		d := defs[index]
		d.Key += "-duplicate"
		defs = append(defs, d)
	}
	for _, kind := range []string{"agent", "mcp_server", "mcp_grant", "http_credential", "git_repository"} {
		d := bundleCLIDef(kind, "reserved-"+strings.ReplaceAll(kind, "_", "-"), `null`)
		d.Reserved = true
		defs = append(defs, d)
	}
	return contract.ConfigBundle{Format: contract.ConfigExportFormat, Version: 2, Resources: defs}
}
func TestCLIBundleConfigurationWholeGatewayRoundtrip(t *testing.T) {
	source := newGatewayHarness(t)
	source.Start()
	bearer := filepath.Join(t.TempDir(), "source-bearer")
	require.NoError(t, os.WriteFile(bearer, []byte(source.bearer), 0o600))
	seed := bundleCLIGraph()
	seedFile := bundleCLIFile(t, "seed.json", seed)
	seeded := runOnlineCLI(t, source, bearer, true, "import", "--file", seedFile, "--confirm", "--json")
	var result contract.ConfigImportResult
	require.NoError(t, json.Unmarshal(seeded.Stdout, &result))
	require.Equal(t, "committed", result.Configuration)
	require.Len(t, result.Creations, len(seed.Resources))
	require.Equal(t, "pending", result.Activation)
	// Include an ordinary-created agent's explicit default grant in the snapshot.
	ordinary := runOnlineCLI(t, source, bearer, true, "agent", "create", "--key", "ordinary", "--display-name", "Ordinary", "--visibility", "allowed-only", "--json")
	var original contract.PrincipalCreation
	require.NoError(t, json.Unmarshal(ordinary.Stdout, &original))
	exported := runOnlineCLI(t, source, bearer, true, "export", "config")
	var bundle contract.ConfigBundle
	require.NoError(t, json.Unmarshal(exported.Stdout, &bundle))
	require.NoError(t, contract.ValidateConfigBundleEnvelope(bundle))
	require.Len(t, bundle.Resources, len(seed.Resources)+2)
	require.NotContains(t, string(exported.Stdout), source.bearer)
	require.NotContains(t, string(exported.Stdout), original.Principal.ID)
	for _, forbidden := range []string{`"runtime"`, `"history"`, `"revision"`, `"installation_id"`, `"credential_id"`, `"operation_id"`} {
		require.NotContains(t, string(exported.Stdout), forbidden)
	}
	source.Stop(syscall.SIGTERM)

	target := newGatewayHarness(t)
	target.Start()
	targetBearer := filepath.Join(t.TempDir(), "target-bearer")
	require.NoError(t, os.WriteFile(targetBearer, []byte(target.bearer), 0o600))
	full := bundleCLIFile(t, "whole-gateway.json", bundle)
	before := runOnlineCLI(t, target, targetBearer, true, "export", "config")
	refused := runOnlineCLI(t, target, targetBearer, false, "import", "--file", full)
	require.Contains(t, string(refused.Stderr), "--confirm")
	require.Empty(t, refused.Stdout)
	preview := runOnlineCLI(t, target, targetBearer, true, "import", "--file", full, "--dry-run", "--json")
	var plan contract.ConfigPreview
	require.NoError(t, json.Unmarshal(preview.Stdout, &plan))
	require.True(t, plan.Advisory)
	require.Len(t, plan.Creations, len(bundle.Resources))
	plans := map[string]contract.ConfigCreation{}
	for _, c := range plan.Creations {
		plans[c.Kind+"/"+c.Key] = c
	}
	for _, d := range bundle.Resources {
		c, ok := plans[d.Kind+"/"+d.Key]
		require.True(t, ok)
		require.Equal(t, d.Reserved, c.Reserved)
	}
	require.Contains(t, plans["mcp_grant/expired"].Dependencies, contract.ConfigReference{Kind: "agent", Key: "builder"})
	require.Contains(t, plans["mcp_grant/expired"].Dependencies, contract.ConfigReference{Kind: "mcp_server", Key: "deleted"})
	require.Contains(t, plans["http_grant/web"].Dependencies, contract.ConfigReference{Kind: "http_credential", Key: "http"})
	require.Contains(t, plans["git_repository/repo"].Dependencies, contract.ConfigReference{Kind: "git_credential", Key: "git"})
	require.Contains(t, plans["mcp_server/needs-material"].Requirements, "supply_static_credentials")
	require.Contains(t, plans["mcp_server/needs-material"].Requirements, "activate_after_commit")
	afterPreview := runOnlineCLI(t, target, targetBearer, true, "export", "config")
	require.JSONEq(t, string(before.Stdout), string(afterPreview.Stdout))
	committed := runOnlineCLI(t, target, targetBearer, true, "import", "--file", full, "--confirm", "--json")
	require.NoError(t, json.Unmarshal(committed.Stdout, &result))
	require.Equal(t, "committed", result.Configuration)
	require.Equal(t, "pending", result.Activation)
	require.Len(t, result.Creations, len(bundle.Resources))
	byKey := map[string]contract.ConfigCreated{}
	for _, c := range result.Creations {
		byKey[c.Kind+"/"+c.Key] = c
	}
	require.NotEqual(t, original.Principal.ID, byKey["agent/ordinary"].ID)
	metadata := runOnlineCLI(t, target, targetBearer, true, "mcp", "server", "get", byKey["mcp_server/needs-material"].ID, "--json")
	var server struct {
		CredentialState contract.ServerCredentialState `json:"credential_state"`
	}
	require.NoError(t, json.Unmarshal(metadata.Stdout, &server))
	require.Equal(t, contract.ServerCredentialAbsent, server.CredentialState)
	require.Eventually(t, func() bool {
		response := target.adminSnapshot(http.MethodGet, "/api/v2/mcp/servers/"+byKey["mcp_server/startup-fails"].ID, nil)
		var s struct {
			Runtime struct {
				State  contract.RuntimeState  `json:"state"`
				Reason *contract.PublicReason `json:"reason"`
			} `json:"runtime"`
		}
		return response.StatusCode == 200 && json.Unmarshal(response.Body, &s) == nil && s.Runtime.Reason != nil
	}, 5*time.Second, 20*time.Millisecond, "startup failure remains separate from committed definitions")
	reexport := runOnlineCLI(t, target, targetBearer, true, "export", "config")
	require.JSONEq(t, string(exported.Stdout), string(reexport.Stdout), "all explicit keys, duplicate/expired grants, deleted targets and reservations; no renewed expiry, resurrection or extra default grants")
	for _, lexical := range []string{`9007199254740993`, `1.0`, `2020-01-01T00:00:00.000000000Z`} {
		require.Contains(t, string(exported.Stdout), lexical)
		require.Contains(t, string(reexport.Stdout), lexical)
	}
	conflict := runOnlineCLI(t, target, targetBearer, false, "import", "--file", full, "--confirm", "--json")
	require.Empty(t, conflict.Stdout)
	require.Contains(t, string(conflict.Stderr), "conflict")
	unchanged := runOnlineCLI(t, target, targetBearer, true, "export", "config")
	require.JSONEq(t, string(reexport.Stdout), string(unchanged.Stdout))
	target.Stop(syscall.SIGTERM)
}
func TestCLIBundleConfigurationExistingDependenciesAndPreviewRace(t *testing.T) {
	h := newGatewayHarness(t)
	h.Start()
	bearer := filepath.Join(t.TempDir(), "bearer")
	require.NoError(t, os.WriteFile(bearer, []byte(h.bearer), 0o600))
	agent := bundleCLIDef("agent", "existing", `{"display_name":"Existing","state":"active","visibility":"allowed-only","http_default":"block"}`)
	runOnlineCLI(t, h, bearer, true, "import", "--file", bundleCLIFile(t, "agent.json", agent), "--confirm")
	grant := bundleCLIDef("mcp_grant", "new-grant", `{"agent":{"kind":"agent","key":"existing"},"server":{"kind":"gateway_mcp"},"description":null,"effect":"allow","upstream_name":null,"constraint":null,"read_only":false,"expires_at":null}`)
	newAgent := agent
	newAgent.Key = "same-submission"
	secondGrant := grant
	secondGrant.Key = "same-grant"
	secondGrant.Spec = json.RawMessage(strings.ReplaceAll(string(grant.Spec), `"key":"existing"`, `"key":"same-submission"`))
	partial := contract.ConfigBundle{Format: contract.ConfigExportFormat, Version: 2, Resources: []contract.ConfigDefinition{grant, newAgent, secondGrant}}
	partialFile := bundleCLIFile(t, "partial.json", partial)
	runOnlineCLI(t, h, bearer, true, "import", "--file", partialFile, "--dry-run")
	// Competing creation after advisory preview invalidates the whole submission.
	runOnlineCLI(t, h, bearer, true, "import", "--file", bundleCLIFile(t, "racer.json", newAgent), "--confirm")
	before := runOnlineCLI(t, h, bearer, true, "export", "config")
	race := runOnlineCLI(t, h, bearer, false, "import", "--file", partialFile, "--confirm")
	require.Contains(t, string(race.Stderr), "typed key")
	require.Empty(t, race.Stdout)
	after := runOnlineCLI(t, h, bearer, true, "export", "config")
	require.JSONEq(t, string(before.Stdout), string(after.Stdout))
	partial.Resources = []contract.ConfigDefinition{grant, secondGrant}
	runOnlineCLI(t, h, bearer, true, "import", "--file", bundleCLIFile(t, "existing-deps.json", partial), "--confirm")
	missing := agent
	missing.Key = "must-not-create"
	dangling := grant
	dangling.Key = "dangling"
	dangling.Spec = json.RawMessage(strings.ReplaceAll(string(grant.Spec), `"key":"existing"`, `"key":"absent"`))
	partial.Resources = []contract.ConfigDefinition{missing, dangling}
	before = runOnlineCLI(t, h, bearer, true, "export", "config")
	bad := runOnlineCLI(t, h, bearer, false, "import", "--file", bundleCLIFile(t, "dangling.json", partial), "--confirm")
	require.Empty(t, bad.Stdout)
	require.Contains(t, string(bad.Stderr), "references")
	after = runOnlineCLI(t, h, bearer, true, "export", "config")
	require.JSONEq(t, string(before.Stdout), string(after.Stdout))
	// A permanently reserved key also invalidates a mixed new graph, identically.
	reserved := agent
	reserved.Key = "reserved-agent"
	reserved.Reserved = true
	reserved.Spec = json.RawMessage(`null`)
	runOnlineCLI(t, h, bearer, true, "import", "--file", bundleCLIFile(t, "reservation.json", reserved), "--confirm")
	partial.Resources = []contract.ConfigDefinition{missing, reserved}
	before = runOnlineCLI(t, h, bearer, true, "export", "config")
	bad = runOnlineCLI(t, h, bearer, false, "import", "--file", bundleCLIFile(t, "reserved-conflict.json", partial), "--confirm")
	require.Contains(t, string(bad.Stderr), "reserved")
	after = runOnlineCLI(t, h, bearer, true, "export", "config")
	require.JSONEq(t, string(before.Stdout), string(after.Stdout))
	h.Stop(syscall.SIGTERM)
}
