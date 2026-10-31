package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func emptyConfig() contract.ConfigExport {
	return contract.ConfigExport{Format: contract.ConfigExportFormat, Version: 1, Agents: []contract.ConfigAgent{}, MCP: contract.ConfigMCP{Servers: []contract.ConfigServer{}, Grants: []contract.ConfigMCPGrant{}}, HTTP: contract.ConfigHTTP{Credentials: []contract.ConfigHTTPCredential{}, Grants: []contract.ConfigHTTPGrant{}}, Git: contract.ConfigGit{Credentials: []contract.ConfigGitCredential{}, Repositories: []contract.ConfigGitRepository{}, Grants: []contract.ConfigGitGrant{}, RoutingOrigins: []string{}}}
}
func configCommand(t *testing.T, body string, sink io.Writer) (string, int, error) {
	t.Helper()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, contract.ConfigExportPath, r.URL.RequestURI())
		require.Equal(t, "Bearer "+testAdministratorBearer, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()
	command := newRootCmd()
	var stderr bytes.Buffer
	command.SetOut(sink)
	command.SetErr(&stderr)
	command.SetIn(strings.NewReader(testAdministratorBearer + "\n"))
	command.SetArgs([]string{"export", "config", "--address", server.URL, "--admin-bearer-stdin"})
	err := command.ExecuteContext(t.Context())
	return stderr.String(), calls, err
}

func TestCLIConfigurationExportOneAuthenticatedJSONDocument(t *testing.T) {
	document := emptyConfig()
	body, err := json.Marshal(document)
	require.NoError(t, err)
	var out bytes.Buffer
	stderr, calls, err := configCommand(t, string(body), &out)
	require.NoError(t, err)
	require.Empty(t, stderr)
	require.Equal(t, 1, calls)
	require.Equal(t, string(body)+"\n", out.String())
	// Complete configuration is not a one-MiB collection page.
	for i := 0; i < 64; i++ {
		transport, _ := json.Marshal(contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/bin/example", Arguments: []string{strings.Repeat("x", 18000)}, WorkingDirectory: "/tmp", Environment: map[string]string{}, SecretEnvironment: map[string]string{}})
		document.MCP.Servers = append(document.MCP.Servers, contract.ConfigServer{Ref: fmt.Sprintf("mcp/s%d", i), Namespace: fmt.Sprintf("s%d", i), DisplayName: "Server", Transport: transport, Credentials: contract.ConfigServerCredentials{Static: map[string]string{}}})
	}
	body, err = json.Marshal(document)
	require.NoError(t, err)
	require.Greater(t, len(body), 1024*1024)
	out.Reset()
	stderr, calls, err = configCommand(t, string(body), &out)
	require.NoError(t, err)
	require.Empty(t, stderr)
	require.Equal(t, 1, calls)
	require.JSONEq(t, string(body), out.String())
}

func TestCLIConfigurationExportRejectsInvalidDocumentsBeforeStdout(t *testing.T) {
	encoded, err := json.Marshal(emptyConfig())
	require.NoError(t, err)
	valid := string(encoded)
	for _, body := range []string{
		strings.Replace(valid, `"version":1`, `"version":2`, 1),
		strings.Replace(valid, `"version":1`, `"version":null`, 1),
		strings.Replace(valid, `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(valid, `"version":1,`, "", 1),
		strings.Replace(valid, `"agents":[]`, `"agents":null`, 1),
		strings.Replace(valid, `"version":1`, `"version":1,"token":"canary"`, 1),
		valid + "{}",
	} {
		var out bytes.Buffer
		stderr, calls, err := configCommand(t, body, &out)
		require.Error(t, err)
		require.NotEmpty(t, stderr)
		require.Empty(t, out.String())
		require.Equal(t, 1, calls)
	}
}

type shortConfigWriter struct{}

func (shortConfigWriter) Write(body []byte) (int, error) { return len(body) - 1, nil }
func TestCLIConfigurationExportFailedSinkAndNoExtraFlags(t *testing.T) {
	encoded, err := json.Marshal(emptyConfig())
	require.NoError(t, err)
	stderr, calls, err := configCommand(t, string(encoded), shortConfigWriter{})
	require.Error(t, err)
	require.Contains(t, stderr, "configuration output could not be written")
	require.Equal(t, 1, calls)
	for _, flag := range []string{"--file", "--yes", "--include-secrets", "--secret-output", "--protocol", "--json", "--output"} {
		command := newRootCmd()
		var out, stderr bytes.Buffer
		command.SetOut(&out)
		command.SetErr(&stderr)
		command.SetArgs([]string{"export", "config", flag})
		require.Error(t, command.ExecuteContext(t.Context()))
		require.Empty(t, out.String())
		require.NotEmpty(t, stderr.String())
	}
}
