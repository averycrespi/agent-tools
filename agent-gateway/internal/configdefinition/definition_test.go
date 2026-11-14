package configdefinition

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func definition(kind, key, spec string) contract.ConfigDefinition {
	return contract.ConfigDefinition{Format: contract.ConfigExportFormat, Version: contract.ConfigV2Version, Kind: kind, Key: key, Spec: json.RawMessage(spec)}
}

const agentSpec = `{"display_name":"Agent","state":"active","visibility":"all","http_default":"block"}`
const grantSpec = `{"agent":{"kind":"agent","key":"builder"},"server":{"kind":"gateway_mcp"},"description":null,"effect":"allow","upstream_name":"tool","constraint":{"equals":{"/n":1.2300e+4}},"read_only":false,"expires_at":"2020-01-01T00:00:00.000000000Z"}`
const transportSpec = `{"display_name":"Server","enabled":true,"deleted":false,"transport":{"kind":"streamable_http","url":"https://example.com/mcp","protocol_mode":"auto","authentication":{"mode":"none"}},"credentials":{"static":{},"oauth_client_secret":null,"oauth_authorization":false}}`

func TestDefinitionStrictVersionKeysFieldsAndBounds(t *testing.T) {
	valid, err := json.Marshal(definition("mcp_grant", "grant-one", grantSpec))
	require.NoError(t, err)
	for _, mutate := range []func(string) string{
		func(s string) string { return strings.Replace(s, `"version":2`, `"version":1`, 1) },
		func(s string) string { return strings.Replace(s, `"key":"grant-one",`, "", 1) },
		func(s string) string { return strings.Replace(s, `"key":"grant-one"`, `"key":"Upper"`, 1) },
		func(s string) string { return strings.Replace(s, `"kind":"agent"`, `"kind":"http_credential"`, 1) },
		func(s string) string {
			return strings.Replace(s, `"kind":"gateway_mcp"`, `"kind":"gateway_mcp","key":"mcp_gateway"`, 1)
		},
		func(s string) string { return strings.Replace(s, `"description":null,`, "", 1) },
		func(s string) string { return strings.Replace(s, `"read_only":false`, `"read_only":null`, 1) },
		func(s string) string { return strings.Replace(s, `"spec":{`, `"spec":{"runtime":{},`, 1) },
		func(s string) string { return strings.Replace(s, `"version":2`, `"version":2,"version":2`, 1) },
		func(s string) string { return strings.Replace(s, `"agent":{`, `"agent":{"id":"SECRET_CANARY",`, 1) },
		func(s string) string { return s + " {}" },
	} {
		_, err := DecodeDefinition([]byte(mutate(string(valid))))
		require.ErrorIs(t, err, ErrInvalid)
		require.NotContains(t, err.Error(), "SECRET_CANARY")
	}
	_, err = DecodeDefinition(valid)
	require.NoError(t, err)
	tooBig := definition("agent", "builder", strings.Replace(agentSpec, "Agent", strings.Repeat("a", 257), 1))
	_, err = Normalize(tooBig)
	require.ErrorIs(t, err, ErrInvalid)
	deep := strings.Repeat("[", 65) + "null" + strings.Repeat("]", 65)
	_, err = DecodeDefinition([]byte(deep))
	require.ErrorIs(t, err, ErrInvalid)
}

func TestComparisonPreservesNumbersOrderOmissionAndDefaults(t *testing.T) {
	a := definition("mcp_grant", "grant-one", grantSpec)
	b := a
	b.Spec = json.RawMessage(strings.Replace(grantSpec, `1.2300e+4`, `12300`, 1))
	equal, err := Equivalent(a, b)
	require.NoError(t, err)
	require.False(t, equal)
	b = a
	b.Spec = json.RawMessage(strings.Replace(grantSpec, `"agent":{"kind":"agent","key":"builder"}`, `"agent":{"key":"builder","kind":"agent"}`, 1))
	equal, err = Equivalent(a, b)
	require.NoError(t, err)
	require.True(t, equal)
	b = a
	b.Key = "other"
	equal, err = Equivalent(a, b)
	require.NoError(t, err)
	require.False(t, equal)
	server := definition("mcp_server", "upstream", transportSpec)
	explicit := server
	explicit.Spec = json.RawMessage(strings.Replace(transportSpec, `"protocol_mode":"auto"`, `"protocol_mode":"auto","allow_private_http":false`, 1))
	equal, err = Equivalent(server, explicit)
	require.NoError(t, err)
	require.True(t, equal)
	oauth := strings.Replace(transportSpec, `{"mode":"none"}`, `{"mode":"oauth","registration":{"mode":"dynamic","issuer":"https://issuer.example"},"trusted_origins":[],"request_offline_access":false}`, 1)
	oauth = strings.Replace(oauth, `"oauth_authorization":false`, `"oauth_authorization":true`, 1)
	omitted := definition("mcp_server", "upstream", oauth)
	empty := omitted
	empty.Spec = json.RawMessage(strings.Replace(oauth, `"request_offline_access":false`, `"request_offline_access":false,"scopes":[]`, 1))
	equal, err = Equivalent(omitted, empty)
	require.NoError(t, err)
	require.False(t, equal)
	ordered := definition("git_grant", "git-access", `{"agent":{"kind":"agent","key":"builder"},"repository":{"kind":"git_repository","key":"repo"},"description":null,"policy":{"version":1,"read":true,"refs":[{"ref":{"kind":"prefix","value":"refs/heads/"},"actions":["create","update"]}]},"expires_at":null}`)
	reversed := ordered
	reversed.Spec = json.RawMessage(strings.Replace(string(ordered.Spec), `["create","update"]`, `["update","create"]`, 1))
	equal, err = Equivalent(ordered, reversed)
	require.NoError(t, err)
	require.False(t, equal)
}

func TestBundleFixtureAndGraphFailures(t *testing.T) {
	raw, err := os.ReadFile("testdata/v2-bundle.json")
	require.NoError(t, err)
	bundle, err := DecodeBundle(raw)
	require.NoError(t, err)
	for _, d := range bundle.Resources {
		single, err := json.Marshal(d)
		require.NoError(t, err)
		_, err = DecodeDefinition(single)
		require.NoError(t, err)
	}
	for _, mutate := range []func(contract.ConfigBundle) contract.ConfigBundle{
		func(b contract.ConfigBundle) contract.ConfigBundle {
			b.Resources = append(b.Resources, b.Resources[0])
			return b
		},
		func(b contract.ConfigBundle) contract.ConfigBundle { b.Resources = b.Resources[1:]; return b },
		func(b contract.ConfigBundle) contract.ConfigBundle {
			b.Resources = b.Resources[:len(b.Resources)-1]
			return b
		},
		func(b contract.ConfigBundle) contract.ConfigBundle {
			b.Resources[0].Reserved = true
			b.Resources[0].Spec = json.RawMessage("null")
			return b
		},
	} {
		var copy contract.ConfigBundle
		require.NoError(t, json.Unmarshal(raw, &copy))
		require.ErrorIs(t, ValidateBundle(mutate(copy)), ErrInvalid)
	}
	require.Equal(t, 2, bundle.Version)
}
