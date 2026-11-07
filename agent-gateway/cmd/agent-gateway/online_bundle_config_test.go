package main

import (
	"bytes"
	"encoding/json"
	"fmt"
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

func cliBundle(t *testing.T) (string, []contract.ConfigDefinition) {
	t.Helper()
	defs := []contract.ConfigDefinition{}
	for _, kind := range []string{"agent", "mcp_server", "mcp_grant", "http_credential", "http_grant", "git_credential", "git_repository", "git_grant"} {
		var d contract.ConfigDefinition
		require.NoError(t, json.Unmarshal(resourceDefinition(t, kind), &d))
		defs = append(defs, d)
	}
	defs = append(defs, contract.ConfigDefinition{Format: contract.ConfigExportFormat, Version: 2, Kind: "git_routing", Spec: json.RawMessage(`{"origins":[]}`)}, contract.ConfigDefinition{Format: contract.ConfigExportFormat, Version: 2, Kind: "agent", Key: "reserved-key", Reserved: true, Spec: json.RawMessage(`null`)})
	raw, err := json.Marshal(contract.ConfigBundle{Format: contract.ConfigExportFormat, Version: 2, Resources: defs})
	require.NoError(t, err)
	return string(raw), defs
}
func cliBundleResponse(defs []contract.ConfigDefinition, dry bool) any {
	if dry {
		p := contract.ConfigPreview{Advisory: true, Creations: []contract.ConfigCreation{}}
		for _, d := range defs {
			p.Creations = append(p.Creations, contract.ConfigCreation{Kind: d.Kind, Key: d.Key, Reserved: d.Reserved, Dependencies: []contract.ConfigReference{}, Requirements: []string{}})
		}
		p.Creations[2].Dependencies = []contract.ConfigReference{{Kind: "agent", Key: "existing-agent"}, {Kind: "mcp_server", Key: "existing-server"}}
		p.Creations[1].Requirements = []string{"supply_static_credentials", "activate_after_commit"}
		p.Creations[8].Requirements = []string{"initialize_untouched_routing_seed"}
		return p
	}
	r := contract.ConfigImportResult{Configuration: "committed", Activation: "pending", Creations: []contract.ConfigCreated{}}
	for i, d := range defs {
		id := "0000000000000000000000000" + string(rune('0'+i))
		if d.Kind == "git_routing" {
			id = ""
		}
		r.Creations = append(r.Creations, contract.ConfigCreated{Kind: d.Kind, Key: d.Key, Reserved: d.Reserved, ID: id})
	}
	return r
}
func TestCLIBundleImportFileStdinCompleteInventoryAndHumanPreview(t *testing.T) {
	body, defs := cliBundle(t)
	bearer := resourceBearerFile(t)
	for _, dry := range []bool{true, false} {
		for _, jsonMode := range []bool{true, false} {
			for _, stdin := range []bool{true, false} {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					require.Equal(t, http.MethodPost, r.Method)
					require.Equal(t, "Bearer "+testAdministratorBearer, r.Header.Get("Authorization"))
					raw, err := io.ReadAll(r.Body)
					require.NoError(t, err)
					require.Equal(t, body, string(raw))
					w.Header().Set("Content-Type", contract.MediaTypeJSON)
					if dry {
						require.Equal(t, contract.ConfigPreviewPath, r.URL.Path)
					} else {
						require.Equal(t, contract.ConfigImportPath, r.URL.Path)
						w.WriteHeader(201)
					}
					require.NoError(t, json.NewEncoder(w).Encode(cliBundleResponse(defs, dry)))
				}))
				path := "-"
				if !stdin {
					path = filepath.Join(t.TempDir(), "bundle.json")
					require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
				}
				args := []string{"import", "--file", path, "--admin-bearer-file", bearer}
				if dry {
					args = append(args, "--dry-run")
				} else {
					args = append(args, "--confirm")
				}
				if jsonMode {
					args = append(args, "--json")
				}
				var out bytes.Buffer
				stderr, err := executeResourceCLI(t, server.URL, body, &out, args...)
				require.NoError(t, err, stderr)
				require.Empty(t, stderr)
				require.Equal(t, 1, calls)
				if jsonMode {
					expected, _ := json.Marshal(cliBundleResponse(defs, dry))
					require.JSONEq(t, string(expected), out.String())
				} else {
					for _, d := range defs {
						require.Contains(t, out.String(), d.Kind)
						if d.Key != "" {
							require.Contains(t, out.String(), d.Key)
						}
					}
					require.Contains(t, out.String(), "RESERVED")
					if dry {
						for _, value := range []string{"existing-agent", "existing-server", "supply_static_credentials", "activate_after_commit", "initialize_untouched_routing_seed", "advisory"} {
							require.Contains(t, out.String(), value)
						}
					} else {
						require.Contains(t, out.String(), "committed")
						require.Contains(t, out.String(), "pending")
						require.Contains(t, out.String(), "unverified")
					}
				}
				server.Close()
			}
		}
	}
}
func TestCLIBundleImportRefusalAndClosedGrammar(t *testing.T) {
	body, _ := cliBundle(t)
	cases := []struct {
		body  string
		flags []string
	}{
		{body, nil}, {body, []string{"--confirm=false"}}, {body, []string{"--dry-run", "--confirm"}},
		{strings.Replace(body, `"version":2`, `"version":1`, 1), []string{"--confirm"}},
		{`{"format":"agent-gateway-config","version":2,"resources":null}`, []string{"--confirm"}},
		{body + `{}`, []string{"--confirm"}},
		{`{"format":"agent-gateway-config","version":2,"resources":[],"kind":"agent"}`, []string{"--confirm"}},
		{strings.Replace(body, `"resources":`, `"resources":[],"resources":`, 1), []string{"--dry-run"}},
		{strings.Replace(body, `"display_name":"Example"`, `"display_name":"secret-canary","secret":"secret-canary"`, 1), []string{"--confirm"}},
	}
	for _, test := range cases {
		var out bytes.Buffer
		stderr, err := executeResourceCLI(t, "http://127.0.0.1:1", test.body, &out, append([]string{"import", "--file", "-", "--admin-bearer-file", "/must-not-open"}, test.flags...)...)
		require.Error(t, err)
		require.Equal(t, 2, commandExitCode(err), stderr)
		require.Empty(t, out.String())
		require.NotContains(t, stderr, "secret-canary")
		require.NotContains(t, stderr, "must-not-open")
	}
	var out bytes.Buffer
	stderr, err := executeResourceCLI(t, "http://127.0.0.1:1", body, &out, "import", "--file", "-", "--dry-run", "--admin-bearer-stdin")
	require.Error(t, err)
	require.Contains(t, stderr, "both configuration")
	for _, args := range [][]string{{"import", "--help"}, {"export", "config", "--help"}, {"__complete", "import", "--"}, {"__complete", "init", "--"}} {
		root := newRootCmd()
		out.Reset()
		root.SetOut(&out)
		root.SetArgs(args)
		require.NoError(t, root.ExecuteContext(t.Context()))
		require.NotContains(t, out.String(), "init --from")
		require.NotContains(t, out.String(), "version-one")
		require.NotContains(t, out.String(), "skip-identical")
		require.NotEmpty(t, out.String())
		if args[0] == "__complete" {
			require.NotContains(t, out.String(), "--from")
			if args[1] == "import" {
				require.Contains(t, out.String(), "--confirm")
				require.Contains(t, out.String(), "--dry-run")
			}
		}
	}
	leaf, _, err := newRootCmd().Find([]string{"import"})
	require.NoError(t, err)
	require.Contains(t, leaf.Long, "Initialize and start")
	require.Contains(t, leaf.Long, "export config")
	for _, flag := range []string{"skip-identical", "update", "remap", "filter", "yes", "from"} {
		require.Nil(t, leaf.Flags().Lookup(flag))
	}
}
func TestCLIBundleImportRejectsIncompleteOrContradictoryResults(t *testing.T) {
	body, defs := cliBundle(t)
	bearer := resourceBearerFile(t)
	preview := cliBundleResponse(defs, true).(contract.ConfigPreview)
	result := cliBundleResponse(defs, false).(contract.ConfigImportResult)
	for _, dry := range []bool{true, false} {
		for _, fault := range []string{"missing", "duplicate", "wrong-key", "wrong-reservation", "null", "activation-or-advisory"} {
			var reply any
			if dry {
				p := preview
				p.Creations = append([]contract.ConfigCreation{}, preview.Creations...)
				switch fault {
				case "missing":
					p.Creations = p.Creations[1:]
				case "duplicate":
					p.Creations[1] = p.Creations[0]
				case "wrong-key":
					p.Creations[0].Key = "different"
				case "wrong-reservation":
					p.Creations[0].Reserved = true
				case "null":
					p.Creations[0].Dependencies = nil
				default:
					p.Advisory = false
				}
				reply = p
			} else {
				r := result
				r.Creations = append([]contract.ConfigCreated{}, result.Creations...)
				switch fault {
				case "missing":
					r.Creations = r.Creations[1:]
				case "duplicate":
					r.Creations[1] = r.Creations[0]
				case "wrong-key":
					r.Creations[0].Key = "different"
				case "wrong-reservation":
					r.Creations[0].Reserved = true
				case "null":
					r.Creations[0].ID = ""
				default:
					r.Activation = "not_required"
				}
				reply = r
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.Header().Set("Content-Type", contract.MediaTypeJSON)
				if !dry {
					w.WriteHeader(201)
				}
				_ = json.NewEncoder(w).Encode(reply)
			}))
			flag := "--confirm"
			if dry {
				flag = "--dry-run"
			}
			var out bytes.Buffer
			stderr, err := executeResourceCLI(t, server.URL, body, &out, "import", "--file", "-", flag, "--json", "--admin-bearer-file", bearer)
			require.Error(t, err)
			require.Empty(t, out.String())
			require.Equal(t, 1, calls)
			if !dry {
				require.Equal(t, 8, commandExitCode(err), stderr)
				require.Contains(t, stderr, "uncertain")
			}
			server.Close()
		}
	}
}
func TestCLIBundleImportLargeCompletePreviewAndShortCommitOutput(t *testing.T) {
	bearer := resourceBearerFile(t)
	defs := make([]contract.ConfigDefinition, 10000)
	preview := contract.ConfigPreview{Advisory: true, Creations: make([]contract.ConfigCreation, len(defs))}
	for i := range defs {
		key := fmt.Sprintf("key-%05d-%s", i, strings.Repeat("k", 40))
		defs[i] = contract.ConfigDefinition{Format: contract.ConfigExportFormat, Version: 2, Kind: "agent", Key: key, Reserved: true, Spec: json.RawMessage(`null`)}
		preview.Creations[i] = contract.ConfigCreation{Kind: "agent", Key: key, Reserved: true, Dependencies: []contract.ConfigReference{}, Requirements: []string{}}
	}
	body, err := json.Marshal(contract.ConfigBundle{Format: contract.ConfigExportFormat, Version: 2, Resources: defs})
	require.NoError(t, err)
	reply, err := json.Marshal(preview)
	require.NoError(t, err)
	require.Greater(t, len(reply), 1<<20)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, err := io.Copy(io.Discard, r.Body)
		require.NoError(t, err)
		w.Header().Set("Content-Type", contract.MediaTypeJSON)
		_, _ = w.Write(reply)
	}))
	var out bytes.Buffer
	stderr, err := executeResourceCLI(t, server.URL, string(body), &out, "import", "--file", "-", "--dry-run", "--json", "--admin-bearer-file", bearer)
	require.NoError(t, err, stderr)
	require.Equal(t, string(reply)+"\n", out.String())
	require.Equal(t, 1, calls)
	server.Close()
	input, smallDefs := cliBundle(t)
	calls = 0
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", contract.MediaTypeJSON)
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(cliBundleResponse(smallDefs, false))
	}))
	defer server.Close()
	stderr, err = executeResourceCLI(t, server.URL, input, shortConfigWriter{}, "import", "--file", "-", "--confirm", "--json", "--admin-bearer-file", bearer)
	require.Error(t, err)
	require.Contains(t, stderr, "Configuration committed; output delivery failed")
	require.Contains(t, stderr, "do not replay")
	require.Equal(t, 1, calls)
}
func TestCLIBundleInputFilesystemCauseIsActionable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-config.json")
	var out bytes.Buffer
	stderr, err := executeResourceCLI(t, "http://127.0.0.1:1", "", &out, "import", "--file", path, "--dry-run", "--admin-bearer-file", "/must-not-open")
	require.Error(t, err)
	require.Equal(t, 2, commandExitCode(err))
	require.Empty(t, out.String())
	require.Contains(t, stderr, "missing-config.json")
	require.Contains(t, stderr, "no such file")
	require.NotContains(t, stderr, "must-not-open")
}
func TestCLIBundleImportLostResponseAndFailuresNeverReplay(t *testing.T) {
	body, _ := cliBundle(t)
	bearer := resourceBearerFile(t)
	for _, status := range []int{0, 400, 401, 409, 503} {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if status == 0 {
				c, _, err := w.(http.Hijacker).Hijack()
				require.NoError(t, err)
				require.NoError(t, c.Close())
				return
			}
			w.Header().Set("Content-Type", contract.MediaTypeProblemJSON)
			w.WriteHeader(status)
			code, title := "storage_unavailable", "Storage is unavailable."
			switch status {
			case 400:
				code, title = "malformed_request", "The request is malformed."
			case 401:
				code, title = "unauthorized", "Authentication is required."
			case 409:
				code, title = "conflict", "The resource conflicts with current state."
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "code": code, "title": title})
		}))
		var out bytes.Buffer
		stderr, err := executeResourceCLI(t, server.URL, body, &out, "import", "--file", "-", "--confirm", "--json", "--admin-bearer-file", bearer)
		require.Error(t, err)
		require.Empty(t, out.String())
		require.Equal(t, 1, calls)
		require.NotContains(t, stderr, testAdministratorBearer)
		if status == 0 || status == 503 {
			require.Equal(t, 8, commandExitCode(err), stderr)
		}
		server.Close()
	}
}
