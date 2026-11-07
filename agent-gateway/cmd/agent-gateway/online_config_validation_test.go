package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestCLIConfigurationExportRejectsInvalidDefinitionResponses(t *testing.T) {
	for _, test := range []struct{ name, kind, key, spec string }{
		{"missing routing fields", "git_routing", "", `{}`},
		{"null routing collection", "git_routing", "", `{"origins":null}`},
		{"unknown routing member", "git_routing", "", `{"origins":[],"token":"private-response-canary"}`},
		{"missing agent fields", "agent", "builder", `{"display_name":"Missing"}`},
		{"dangling typed reference", "mcp_grant", "access", `{"agent":{"kind":"agent","key":"missing"},"server":{"kind":"gateway_mcp"},"description":null,"effect":"allow","upstream_name":null,"constraint":null,"read_only":false,"expires_at":null}`},
		{"invalid credential requirement", "http_credential", "api", `{"name":"API","boundary":{"host":"example.com","port":443,"allow_wildcard":false},"recipe":{"header":"Authorization","prefix":"Bearer "},"deleted":false,"secret_ref":"private-response-canary"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			b := emptyConfig()
			d := contract.ConfigDefinition{Format: contract.ConfigExportFormat, Version: 2, Kind: test.kind, Key: test.key, Spec: json.RawMessage(test.spec)}
			if d.Kind == "git_routing" {
				b.Resources[0] = d
			} else {
				b.Resources = append(b.Resources, d)
			}
			raw, err := json.Marshal(b)
			require.NoError(t, err)
			var stdout bytes.Buffer
			stderr, calls, err := configCommand(t, string(raw), &stdout)
			require.Error(t, err)
			require.Empty(t, stdout.String())
			require.NotContains(t, stderr, "private-response-canary")
			require.Equal(t, 1, calls)
		})
	}
}

func TestCLIConfigurationExportPreservesOmittedHTTPPrivateDefault(t *testing.T) {
	for _, private := range []string{"", `,"allow_private":false`} {
		bundle := emptyConfig()
		bundle.Resources = append(bundle.Resources,
			contract.ConfigDefinition{Format: contract.ConfigExportFormat, Version: 2, Kind: "agent", Key: "builder", Spec: json.RawMessage(`{"display_name":"Builder","state":"active","visibility":"allowed-only","http_default":"block"}`)},
			contract.ConfigDefinition{Format: contract.ConfigExportFormat, Version: 2, Kind: "http_grant", Key: "web", Spec: json.RawMessage(`{"agent":{"kind":"agent","key":"builder"},"description":null,"policy":{"version":1,"type":"allow_requests","request":{"origin":{"scheme":"https","host":"example.com","port":443},"methods":{"any":true},"path":{"kind":"any"}}` + private + `},"expires_at":null}`)},
		)
		raw, err := json.Marshal(bundle)
		require.NoError(t, err)
		var stdout bytes.Buffer
		stderr, calls, err := configCommand(t, string(raw), &stdout)
		require.NoError(t, err)
		require.Empty(t, stderr)
		require.Equal(t, 1, calls)
		require.JSONEq(t, string(raw), stdout.String())
	}
}
