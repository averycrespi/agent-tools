package contract

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func configurationDocument() ConfigExport {
	return ConfigExport{Format: ConfigExportFormat, Version: 1, Agents: []ConfigAgent{{Ref: "agent/1", DisplayName: "Agent", State: PrincipalActive, Visibility: VisibilityAllowedOnly, HTTPDefault: HTTPDefaultBlock}}, MCP: ConfigMCP{Servers: []ConfigServer{{Ref: "mcp/example", Namespace: "example", DisplayName: "Example", Transport: json.RawMessage(`{"kind":"stdio","executable":"/bin/example","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{"TOKEN":"api"}}`), Credentials: ConfigServerCredentials{Static: map[string]string{"api": "mcp/example/static/api"}}}}, Grants: []ConfigMCPGrant{{Agent: "agent/1", Server: "mcp/example", Effect: GrantAllow, Constraint: json.RawMessage("null")}}}, HTTP: ConfigHTTP{Credentials: []ConfigHTTPCredential{}, Grants: []ConfigHTTPGrant{}}, Git: ConfigGit{Credentials: []ConfigGitCredential{}, Repositories: []ConfigGitRepository{}, Grants: []ConfigGitGrant{}, RoutingOrigins: []string{}}}
}

func TestConfigurationFormatGraphAndRequirements(t *testing.T) {
	require.NoError(t, ValidateConfigExport(configurationDocument()))
	mutations := map[string]func(*ConfigExport){
		"unknown format":                func(v *ConfigExport) { v.Format = "other" },
		"unknown version":               func(v *ConfigExport) { v.Version = 2 },
		"null inventory":                func(v *ConfigExport) { v.HTTP.Grants = nil },
		"installation agent identity":   func(v *ConfigExport) { v.Agents[0].Ref = "01ARZ3NDEKTSV4RRFFQ69G5FAV" },
		"dangling grant":                func(v *ConfigExport) { v.MCP.Grants[0].Agent = "agent/2" },
		"dangling target":               func(v *ConfigExport) { v.MCP.Grants[0].Server = "mcp/absent" },
		"dropped slot":                  func(v *ConfigExport) { delete(v.MCP.Servers[0].Credentials.Static, "api") },
		"unexpected slot":               func(v *ConfigExport) { v.MCP.Servers[0].Credentials.Static["extra"] = "mcp/example/static/extra" },
		"material instead of reference": func(v *ConfigExport) { v.MCP.Servers[0].Credentials.Static["api"] = "secret-canary" },
		"wrong oauth requirement":       func(v *ConfigExport) { v.MCP.Servers[0].Credentials.OAuthAuthorization = true },
		"deleted active target":         func(v *ConfigExport) { v.MCP.Servers[0].Deleted = true },
		"read-only deny":                func(v *ConfigExport) { v.MCP.Grants[0].Effect = GrantDeny; v.MCP.Grants[0].ReadOnly = true },
		"invalid HTTP union": func(v *ConfigExport) {
			v.HTTP.Grants = append(v.HTTP.Grants, ConfigHTTPGrant{Agent: "agent/1", Policy: ConfigHTTPPolicy{Version: 1, Type: HTTPAllowRequests}})
		},
		"invalid Git union": func(v *ConfigExport) {
			v.Git.Repositories = append(v.Git.Repositories, ConfigGitRepository{Ref: "repository/1", Name: "Repo", URL: "https://example.com:443/repo", Aliases: []string{}})
			v.Git.Grants = append(v.Git.Grants, ConfigGitGrant{Agent: "agent/1", Repository: "repository/1", Policy: GitPolicy{Version: 1, Read: false, Refs: []GitRefRule{{Ref: GitRefSelector{Kind: "exact", Value: "refs/heads/main"}, Actions: []string{"update"}}}}})
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			document := configurationDocument()
			mutate(&document)
			require.Error(t, ValidateConfigExport(document))
		})
	}
}

func TestConfigurationRouteAndCapability(t *testing.T) {
	route, ok := RouteForPath(ConfigExportPath)
	require.True(t, ok)
	require.Equal(t, AuthorityAdmin, route.Authority)
	require.Equal(t, []string{"GET"}, route.Methods)
	rows := ConfigurationControlPlaneCapabilityManifest()
	require.Len(t, rows, 1)
	require.Equal(t, []string{"export config"}, rows[0].CLIUses)
	require.Empty(t, rows[0].WebControl)
	rows[0].CLIUses[0] = "changed"
	require.Equal(t, []string{"export config"}, ConfigurationControlPlaneCapabilityManifest()[0].CLIUses)
}
