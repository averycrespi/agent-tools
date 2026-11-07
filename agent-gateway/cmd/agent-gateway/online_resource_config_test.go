package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func resourceDefinition(t *testing.T, kind string) []byte {
	t.Helper()
	specs := map[string]string{
		"agent":           `{"display_name":"Example","state":"active","visibility":"allowed-only","http_default":"block"}`,
		"mcp_server":      `{"display_name":"Example","enabled":true,"deleted":false,"transport":{"kind":"stdio","executable":"/bin/cat","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{"TOKEN":"primary"}},"credentials":{"static":{"primary":"mcp/example/static/primary"},"oauth_client_secret":null,"oauth_authorization":false}}`,
		"mcp_grant":       `{"agent":{"kind":"agent","key":"existing-agent"},"server":{"kind":"mcp_server","key":"existing-server"},"description":null,"effect":"allow","upstream_name":null,"constraint":null,"read_only":false,"expires_at":null}`,
		"http_credential": `{"name":"Example","boundary":{"host":"example.com","port":443,"allow_wildcard":false},"recipe":{"header":"Authorization","prefix":"Bearer "},"deleted":false,"secret_ref":"http_credential/example/secret"}`,
		"http_grant":      `{"agent":{"kind":"agent","key":"existing-agent"},"description":null,"policy":{"version":1,"type":"block_destination","destination":{"host":"example.com","port":443}},"expires_at":null}`,
		"git_credential":  `{"name":"Example","origin":"https://example.com:443","recipe":{"header":"Authorization","prefix":"Bearer "},"deleted":false,"secret_ref":"git_credential/example/secret"}`,
		"git_repository":  `{"name":"Example","url":"https://example.com/repo.git","aliases":[],"credential":{"kind":"git_credential","key":"existing-credential"},"deleted":false}`,
		"git_grant":       `{"agent":{"kind":"agent","key":"existing-agent"},"repository":{"kind":"git_repository","key":"existing-repo"},"description":null,"policy":{"version":1,"read":true,"refs":[]},"expires_at":null}`,
	}
	body, err := json.Marshal(contract.ConfigDefinition{Format: contract.ConfigExportFormat, Version: 2, Kind: kind, Key: "example", Spec: json.RawMessage(specs[kind])})
	require.NoError(t, err)
	return body
}
func executeResourceCLI(t *testing.T, address, input string, sink io.Writer, args ...string) (string, error) {
	t.Helper()
	cmd := newRootCmd()
	var stderr bytes.Buffer
	cmd.SetOut(sink)
	cmd.SetErr(&stderr)
	cmd.SetIn(strings.NewReader(input))
	cmd.SetArgs(append(args, "--address", address))
	err := cmd.ExecuteContext(t.Context())
	return stderr.String(), err
}
func resourceBearerFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bearer")
	require.NoError(t, os.WriteFile(path, []byte(testAdministratorBearer), 0o600))
	return path
}
func TestCLIResourceConfigAllNamespacesAndSingleExports(t *testing.T) {
	for namespace, kind := range resourceConfigNamespaces {
		t.Run(kind, func(t *testing.T) {
			body := resourceDefinition(t, kind)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				require.Equal(t, http.MethodGet, r.Method)
				require.Equal(t, contract.ConfigResourcePath+kind+"/example", r.URL.RequestURI())
				require.Equal(t, "Bearer "+testAdministratorBearer, r.Header.Get("Authorization"))
				w.Header().Set("Content-Type", contract.MediaTypeJSON)
				_, _ = w.Write(body)
			}))
			defer server.Close()
			var out bytes.Buffer
			args := append(strings.Fields(namespace), "export", "example", "--admin-bearer-stdin")
			stderr, err := executeResourceCLI(t, server.URL, testAdministratorBearer, &out, args...)
			require.NoError(t, err, stderr)
			require.Empty(t, stderr)
			require.Equal(t, string(body)+"\n", out.String())
			require.Equal(t, 1, calls)
			leaf, _, err := newRootCmd().Find(append(strings.Fields(namespace), "export"))
			require.NoError(t, err)
			for _, flag := range []string{"json", "output", "file", "yes", "confirm", "include-secrets"} {
				require.Nil(t, leaf.Flags().Lookup(flag))
			}
		})
	}
}
func TestCLIResourceImportBackendPreviewAndConfirmedCreation(t *testing.T) {
	bearer := resourceBearerFile(t)
	for namespace, kind := range resourceConfigNamespaces {
		for _, dry := range []bool{true, false} {
			t.Run(kind+map[bool]string{true: "/preview", false: "/create"}[dry], func(t *testing.T) {
				body := resourceDefinition(t, kind)
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					require.Equal(t, http.MethodPost, r.Method)
					require.Equal(t, "Bearer "+testAdministratorBearer, r.Header.Get("Authorization"))
					require.Equal(t, contract.MediaTypeJSON, r.Header.Get("Content-Type"))
					got, err := io.ReadAll(r.Body)
					require.NoError(t, err)
					require.Equal(t, string(body), string(got))
					w.Header().Set("Content-Type", contract.MediaTypeJSON)
					if dry {
						require.Equal(t, contract.ConfigPreviewPath, r.URL.Path)
						_ = json.NewEncoder(w).Encode(contract.ConfigPreview{Advisory: true, Creations: []contract.ConfigCreation{{Kind: kind, Key: "example", Dependencies: []contract.ConfigReference{{Kind: "agent", Key: "existing-agent"}}, Requirements: []string{"supply_credential_value", "activate_after_commit"}}}})
					} else {
						require.Equal(t, contract.ConfigImportPath, r.URL.Path)
						w.WriteHeader(http.StatusCreated)
						activation := "not_required"
						if kind == "mcp_server" {
							activation = "pending"
						}
						_ = json.NewEncoder(w).Encode(contract.ConfigImportResult{Configuration: "committed", Activation: activation, Creations: []contract.ConfigCreated{{Kind: kind, Key: "example", ID: idForSecurityTest()}}})
					}
				}))
				defer server.Close()
				args := append(strings.Fields(namespace), "import", "--file", "-", "--admin-bearer-file", bearer)
				args = append(args, map[bool]string{true: "--dry-run", false: "--confirm"}[dry])
				var out bytes.Buffer
				stderr, err := executeResourceCLI(t, server.URL, string(body), &out, args...)
				require.NoError(t, err, stderr)
				require.Empty(t, stderr)
				require.Equal(t, 1, calls)
				if dry {
					require.Contains(t, out.String(), "advisory")
					require.Contains(t, out.String(), "existing-agent")
					require.Contains(t, out.String(), "supply_credential_value")
					require.Contains(t, out.String(), "activate_after_commit")
				} else {
					require.Contains(t, out.String(), "committed")
					if kind == "mcp_server" {
						require.Contains(t, out.String(), "pending")
					} else {
						require.Contains(t, out.String(), "not_required")
					}
					require.Contains(t, out.String(), "unverified")
				}
			})
		}
	}
}
func TestCLIResourceImportRefusesBeforeAuthenticationOrMutation(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	valid := string(resourceDefinition(t, "agent"))
	cases := []struct {
		body  string
		flags []string
	}{
		{valid, nil}, {valid, []string{"--confirm=false"}}, {valid, []string{"--confirm", "--dry-run"}},
		{string(resourceDefinition(t, "mcp_server")), []string{"--dry-run"}},
		{`{"format":"agent-gateway-config","version":2,"resources":[]}`, []string{"--confirm"}},
		{valid + valid, []string{"--confirm"}},
		{strings.Replace(valid, `"version":2`, `"version":1`, 1), []string{"--confirm"}},
		{strings.Replace(valid, `"key":"example"`, `"key":"example","key":"canary-sensitive"`, 1), []string{"--confirm"}},
	}
	for _, test := range cases {
		var out bytes.Buffer
		args := append([]string{"agent", "import", "--file", "-", "--admin-bearer-file", "/must-not-open"}, test.flags...)
		stderr, err := executeResourceCLI(t, server.URL, test.body, &out, args...)
		require.Error(t, err)
		require.Empty(t, out.String())
		require.NotContains(t, stderr, "canary-sensitive")
		require.NotContains(t, stderr, "/must-not-open")
		require.Zero(t, calls)
	}
	var out bytes.Buffer
	stderr, err := executeResourceCLI(t, server.URL, valid, &out, "agent", "import", "--file", "-", "--confirm", "--admin-bearer-stdin")
	require.Error(t, err)
	require.Contains(t, stderr, "both configuration")
	require.Zero(t, calls)
}
func TestCLIResourceConfigMalformedExportAndSinkNoReplay(t *testing.T) {
	body := string(resourceDefinition(t, "mcp_grant"))
	for _, malformed := range []string{
		strings.Replace(strings.Replace(body, `"upstream_name":null`, `"upstream_name":"tool"`, 1), `"constraint":null`, `"constraint":[]`, 1),
		strings.Replace(strings.Replace(body, `"upstream_name":null`, `"upstream_name":"tool"`, 1), `"constraint":null`, `"constraint":true`, 1),
		strings.Replace(body, `"constraint":null`, `"constraint":{}`, 1),
		strings.Replace(body, `"kind":"mcp_grant"`, `"kind":"agent"`, 1),
		strings.Replace(body, `"key":"example"`, `"key":"wrong"`, 1),
	} {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls++
			w.Header().Set("Content-Type", contract.MediaTypeJSON)
			_, _ = io.WriteString(w, malformed)
		}))
		var out bytes.Buffer
		stderr, err := executeResourceCLI(t, server.URL, testAdministratorBearer, &out, "mcp", "grant", "export", "example", "--admin-bearer-stdin")
		require.Error(t, err)
		require.Empty(t, out.String())
		require.NotEmpty(t, stderr)
		require.Equal(t, 1, calls)
		server.Close()
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", contract.MediaTypeJSON)
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()
	stderr, err := executeResourceCLI(t, server.URL, testAdministratorBearer, shortConfigWriter{}, "mcp", "grant", "export", "example", "--admin-bearer-stdin")
	require.Error(t, err)
	require.Contains(t, stderr, "output could not be written")
	require.Equal(t, 1, calls)
}
func TestCLIResourceImportRejectsContradictoryActivation(t *testing.T) {
	bearer := resourceBearerFile(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", contract.MediaTypeJSON)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(contract.ConfigImportResult{Configuration: "committed", Activation: "not_required", Creations: []contract.ConfigCreated{{Kind: "mcp_server", Key: "example", ID: idForSecurityTest()}}})
	}))
	defer server.Close()
	var out bytes.Buffer
	stderr, err := executeResourceCLI(t, server.URL, string(resourceDefinition(t, "mcp_server")), &out, "mcp", "server", "import", "--file", "-", "--confirm", "--admin-bearer-file", bearer, "--json")
	require.Error(t, err)
	require.Equal(t, 8, commandExitCode(err))
	require.Equal(t, 1, calls)
	require.Empty(t, out.String())
	require.Contains(t, stderr, "Nothing was replayed")
}

func TestCLIResourceImportLostResponseNeverReplays(t *testing.T) {
	bearer := resourceBearerFile(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		connection, _, err := w.(http.Hijacker).Hijack()
		require.NoError(t, err)
		require.NoError(t, connection.Close())
	}))
	defer server.Close()
	var out bytes.Buffer
	stderr, err := executeResourceCLI(t, server.URL, string(resourceDefinition(t, "agent")), &out, "agent", "import", "--file", "-", "--confirm", "--admin-bearer-file", bearer, "--json")
	require.Error(t, err)
	require.Equal(t, 8, commandExitCode(err))
	require.Equal(t, 1, calls)
	require.Empty(t, out.String())
	require.Contains(t, stderr, "Nothing was replayed")
}

func TestCLIResourceImportFailuresNeverReplay(t *testing.T) {
	bearer := resourceBearerFile(t)
	for _, status := range []int{401, 409, 503, 201} {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls++
			switch status {
			case 401:
				w.Header().Set("Content-Type", contract.MediaTypeProblemJSON)
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"status":401,"code":"unauthorized","title":"Authentication is required."}`)
			case 409:
				w.Header().Set("Content-Type", contract.MediaTypeProblemJSON)
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"status":409,"code":"conflict","title":"The resource conflicts with current state."}`)
			default:
				w.Header().Set("Content-Type", contract.MediaTypeJSON)
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"malformed":"sensitive-canary"}`)
			}
		}))
		var out bytes.Buffer
		stderr, err := executeResourceCLI(t, server.URL, string(resourceDefinition(t, "agent")), &out, "agent", "import", "--file", "-", "--confirm", "--admin-bearer-file", bearer, "--json")
		require.Error(t, err)
		require.Empty(t, out.String())
		require.NotContains(t, stderr, "sensitive-canary")
		require.Equal(t, 1, calls)
		if status == 401 {
			require.Equal(t, 3, commandExitCode(err))
		}
		if status == 409 {
			require.Equal(t, 5, commandExitCode(err))
		}
		if status == 201 || status == 503 {
			require.Equal(t, 8, commandExitCode(err))
			require.Contains(t, stderr, "Nothing was replayed")
		}
		server.Close()
	}
}
func TestCLIResourceOrdinaryCreateTransmitsExplicitAndOmittedKeys(t *testing.T) {
	bearer := resourceBearerFile(t)
	fixtures := []struct {
		namespace, body string
		direct          []string
	}{
		{namespace: "agent", direct: []string{"--display-name", "Example", "--visibility", "allowed-only"}},
		{namespace: "mcp server", body: `{"display_name":"Example","enabled":false,"transport":{"kind":"stdio","executable":"/bin/cat","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{}}}`},
		{namespace: "mcp grant", body: `{"principal_id":"` + idForSecurityTest() + `","server_id":"` + idForSecurityTest() + `","description":null,"effect":"allow","upstream_name":null,"constraint":null,"expires_at":null}`},
		{namespace: "http credential", body: `{"name":"Example","boundary":{"host":"example.com","port":443,"allow_wildcard":false},"recipe":{"header":"Authorization","prefix":"Bearer "},"secret":"fixture-only"}`},
		{namespace: "http grant", body: `{"principal_id":"` + idForSecurityTest() + `","description":null,"policy":{"version":1,"type":"block_destination","destination":{"host":"example.com","port":443}},"expires_at":null}`},
		{namespace: "git credential", body: `{"name":"Example","origin":"https://example.com:443","recipe":{"header":"Authorization","prefix":"Bearer "},"secret":"fixture-only"}`},
		{namespace: "git repository", body: `{"name":"Example","url":"https://example.com/repo.git","aliases":[],"credential_id":null}`},
		{namespace: "git grant", body: `{"principal_id":"` + idForSecurityTest() + `","repository_id":"` + idForSecurityTest() + `","description":null,"policy":{"version":1,"read":true,"refs":[]},"expires_at":null}`},
	}
	for _, fixture := range fixtures {
		for _, explicit := range []bool{false, true} {
			t.Run(fixture.namespace+map[bool]string{true: "/explicit", false: "/generated"}[explicit], func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					require.Equal(t, http.MethodPost, r.Method)
					var input map[string]json.RawMessage
					require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
					member := "key"
					if fixture.namespace == "mcp server" {
						member = "namespace"
					}
					if explicit {
						require.Equal(t, `"selected-key"`, string(input[member]))
					} else {
						require.Nil(t, input[member], "backend generates, CLI must not invent keys")
					}
					w.Header().Set("Content-Type", contract.MediaTypeProblemJSON)
					w.WriteHeader(401)
					_, _ = io.WriteString(w, `{"status":401,"code":"unauthorized","title":"Authentication is required."}`)
				}))
				defer server.Close()
				args := append(strings.Fields(fixture.namespace), "create")
				args = append(args, fixture.direct...)
				if fixture.body != "" {
					file := filepath.Join(t.TempDir(), "input.json")
					require.NoError(t, os.WriteFile(file, []byte(fixture.body), 0o600))
					args = append(args, "--file", file)
				}
				if explicit {
					args = append(args, "--key", "selected-key")
				}
				if strings.HasPrefix(fixture.namespace, "http ") || strings.HasPrefix(fixture.namespace, "git ") {
					args = append(args, "--yes")
				}
				args = append(args, "--admin-bearer-file", bearer)
				var out bytes.Buffer
				stderr, err := executeResourceCLI(t, server.URL, "", &out, args...)
				require.Error(t, err)
				require.Equal(t, 1, calls, stderr)
				require.Empty(t, out.String())
			})
		}
	}
}

func TestCLIResourceCreateOptionalKeys(t *testing.T) {
	// Preparation is the shared pre-auth boundary used by every ordinary create.
	for _, spec := range onlineCommandSpecs() {
		if resourceConfigKind(spec) == "" || spec.Path[len(spec.Path)-1] != "create" {
			continue
		}
		command, _, err := newRootCmd().Find(spec.Path)
		require.NoError(t, err)
		require.NotNil(t, command.Flags().Lookup("key"))
	}
	for _, key := range []string{"chosen-key", "INVALID KEY", ""} {
		command := newRootCmd()
		var out, stderr bytes.Buffer
		command.SetOut(&out)
		command.SetErr(&stderr)
		command.SetArgs([]string{"agent", "create", "--display-name", "Example", "--visibility", "allowed-only", "--key", key, "--admin-bearer-file", "/missing-bearer"})
		err := command.ExecuteContext(t.Context())
		require.Error(t, err)
		if key != "chosen-key" {
			require.Contains(t, stderr.String(), "key is invalid")
			require.NotContains(t, stderr.String(), "/missing-bearer")
		}
	}
}
