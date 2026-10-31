package configimport

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func portableFixture() string {
	return `{"format":"agent-gateway-config","version":1,"agents":[{"ref":"agent/1","display_name":"Agent","state":"active","visibility":"all","http_default":"block"}],"mcp":{"servers":[{"ref":"mcp/local","namespace":"local","display_name":"Local","enabled":true,"deleted":false,"transport":{"kind":"stdio","executable":"/bin/echo","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{}},"credentials":{"static":{},"oauth_client_secret":null,"oauth_authorization":false}}],"grants":[{"agent":"agent/1","server":"mcp/local","description":null,"effect":"allow","upstream_name":"tool","constraint":null,"read_only":false,"expires_at":null}]},"http":{"credentials":[],"grants":[]},"git":{"credentials":[],"repositories":[],"grants":[],"routing_origins":[]}}`
}

func TestImportStrictCompleteShapeAndDomainPreflight(t *testing.T) {
	base := portableFixture()
	input, err := Decode([]byte(base))
	require.NoError(t, err)
	require.Equal(t, base, string(input.Bytes))
	require.Len(t, input.Digest, 64)
	for name, mutate := range map[string]func(string) string{
		"version": func(s string) string { return strings.Replace(s, `"version":1`, `"version":2`, 1) },
		"unknown": func(s string) string { return strings.Replace(s, `"format":`, `"token":"private","format":`, 1) },
		"case-alias": func(s string) string {
			return strings.Replace(s, `"enabled":true`, `"enabled":true,"Enabled":false`, 1)
		},
		"transport-case-alias": func(s string) string { return strings.Replace(s, `"kind":"stdio"`, `"kind":"stdio","Kind":"stdio"`, 1) },
		"invalid-utf8":         func(s string) string { return s + string([]byte{255}) },
		"duplicate": func(s string) string {
			return strings.Replace(s, `"enabled":true`, `"enabled":true,"enabled":false`, 1)
		},
		"trailing":         func(s string) string { return s + `{}` },
		"missing-enabled":  func(s string) string { return strings.Replace(s, `"enabled":true,`, "", 1) },
		"null-enabled":     func(s string) string { return strings.Replace(s, `"enabled":true`, `"enabled":null`, 1) },
		"missing-nullable": func(s string) string { return strings.Replace(s, `"description":null,`, "", 1) },
		"null-collection":  func(s string) string { return strings.Replace(s, `"routing_origins":[]`, `"routing_origins":null`, 1) },
		"dangling":         func(s string) string { return strings.Replace(s, `"server":"mcp/local"`, `"server":"mcp/missing"`, 1) },
		"bad-transport": func(s string) string {
			return strings.Replace(s, `"executable":"/bin/echo"`, `"executable":"relative"`, 1)
		},
		"bad-name": func(s string) string {
			return strings.Replace(s, `"display_name":"Agent"`, `"display_name":"bad\nname"`, 1)
		},
		"bad-upstream": func(s string) string {
			return strings.Replace(s, `"upstream_name":"tool"`, `"upstream_name":"bad/name"`, 1)
		},
		"unsupported-constraint": func(s string) string {
			return strings.Replace(s, `"constraint":null`, `"constraint":{"version":99}`, 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Decode([]byte(mutate(base)))
			require.ErrorIs(t, err, ErrInvalid)
			require.NotContains(t, err.Error(), "private")
		})
	}
}

func TestImportValidatesEnabledAndRetainedCapacityBeforeStaging(t *testing.T) {
	base := portableFixture()
	var value contract.ConfigExport
	require.NoError(t, json.Unmarshal([]byte(base), &value))
	server := value.MCP.Servers[0]
	for _, count := range []int{32, 33, 64, 65} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			value.MCP.Servers = nil
			for i := 0; i < count; i++ {
				s := server
				s.Namespace = fmt.Sprintf("s%d", i)
				s.Ref = "mcp/" + s.Namespace
				s.Enabled = count < 64
				value.MCP.Servers = append(value.MCP.Servers, s)
			}
			value.MCP.Grants = []contract.ConfigMCPGrant{}
			data, err := json.Marshal(value)
			require.NoError(t, err)
			_, err = Decode(data)
			if count == 32 || count == 64 {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrInvalid)
			}
		})
	}
}

func TestImportRefusesCredentialScopeAndRetainedRepositoryConflicts(t *testing.T) {
	base := `{"format":"agent-gateway-config","version":1,"agents":[{"ref":"agent/1","display_name":"Agent","state":"active","visibility":"all","http_default":"block"}],"mcp":{"servers":[],"grants":[]},"http":{"credentials":[{"ref":"http-credential/1","name":"Credential","boundary":{"host":"example.com","port":443,"allow_wildcard":false},"recipe":{"header":"Authorization","prefix":"Bearer "},"secret_ref":"http-credential/1/secret"}],"grants":[{"agent":"agent/1","description":null,"policy":{"version":1,"type":"allow_requests","request":{"origin":{"scheme":"https","host":"example.com","port":443},"methods":{"any":true},"path":{"kind":"any"}},"allow_private":false,"credential_ref":"http-credential/1"},"expires_at":null}]},"git":{"credentials":[],"repositories":[],"grants":[],"routing_origins":[]}}`
	_, err := Decode([]byte(base))
	require.NoError(t, err)
	_, err = Decode([]byte(strings.Replace(base, `"host":"example.com","port":443},"methods"`, `"host":"*.example.com","port":443},"methods"`, 1)))
	require.ErrorIs(t, err, ErrInvalid)
	var value contract.ConfigExport
	require.NoError(t, json.Unmarshal([]byte(base), &value))
	value.Git.Repositories = []contract.ConfigGitRepository{{Ref: "repository/1", Name: "One", URL: "https://example.com/a.git", Aliases: []string{}, Deleted: true}, {Ref: "repository/2", Name: "Two", URL: "https://example.com/a", Aliases: []string{}}}
	data, err := json.Marshal(value)
	require.NoError(t, err)
	_, err = Decode(data)
	require.ErrorIs(t, err, ErrInvalid)
}
