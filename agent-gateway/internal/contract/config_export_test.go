package contract

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfigurationEnvelopeAndRequirements(t *testing.T) {
	bundle := func() ConfigBundle {
		return ConfigBundle{Format: ConfigExportFormat, Version: 2, Resources: []ConfigDefinition{{Format: ConfigExportFormat, Version: 2, Kind: "git_routing", Spec: json.RawMessage(`{"origins":[]}`)}}}
	}
	require.NoError(t, ValidateConfigBundleEnvelope(bundle()))
	for _, change := range []func(*ConfigBundle){func(b *ConfigBundle) { b.Version = 1 }, func(b *ConfigBundle) { b.Resources = nil }, func(b *ConfigBundle) { b.Resources = append(b.Resources, b.Resources[0]) }, func(b *ConfigBundle) { b.Resources[0].Key = "unexpected" }, func(b *ConfigBundle) { b.Resources[0].Reserved = true }, func(b *ConfigBundle) { b.Resources[0].Spec = json.RawMessage(`null`) }} {
		b := bundle()
		change(&b)
		require.Error(t, ValidateConfigBundleEnvelope(b))
	}
	s := ConfigServer{Ref: "mcp/example", Transport: json.RawMessage(`{"kind":"stdio","secret_environment":{"TOKEN":"api"}}`), Credentials: ConfigServerCredentials{Static: map[string]string{"api": "mcp/example/static/api"}}}
	require.True(t, ValidConfigServerCredentials(s))
	s.Credentials.Static["api"] = "secret-canary"
	require.False(t, ValidConfigServerCredentials(s))
}
func TestConfigurationRouteAndCapability(t *testing.T) {
	for _, test := range []struct{ path, method string }{{ConfigExportPath, "GET"}, {ConfigPreviewPath, "POST"}, {ConfigImportPath, "POST"}, {ConfigResourcePath + "agent/example", "GET"}} {
		route, ok := RouteForPath(test.path)
		require.True(t, ok)
		require.Equal(t, AuthorityAdmin, route.Authority)
		require.Equal(t, []string{test.method}, route.Methods)
	}
	rows := ConfigurationControlPlaneCapabilityManifest()
	require.Len(t, rows, 3)
	require.Equal(t, []string{"import --file PATH [--dry-run | --confirm]"}, rows[1].CLIUses)
	require.Len(t, rows[2].CLIUses, 16)
	require.Equal(t, []string{"export config"}, rows[0].CLIUses)
	require.Empty(t, rows[0].WebControl)
	require.Empty(t, rows[1].WebControl, "bundle import remains CLI-only")
	require.Equal(t, "browser.resource-config", rows[2].WebScenario)
	for _, control := range []string{"Import", "Copy config", "Download config"} {
		require.Contains(t, rows[2].WebControl, control)
	}
	rows[0].CLIUses[0] = "changed"
	require.Equal(t, []string{"export config"}, ConfigurationControlPlaneCapabilityManifest()[0].CLIUses)
}
