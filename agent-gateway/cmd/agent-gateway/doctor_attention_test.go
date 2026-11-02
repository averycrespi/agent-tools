package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/stretchr/testify/require"
)

func initializedDoctorRoot(t *testing.T) (gatewaypaths.Layout, gatewaypaths.InitializationPlan) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "gateway")
	command := newTestRootCmd(t)
	command.SetOut(new(bytes.Buffer))
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"init", "--confirm", "--data-dir", root})
	require.NoError(t, command.ExecuteContext(t.Context()))
	layout, err := gatewaypaths.Resolve(root)
	require.NoError(t, err)
	plan, complete, err := gatewaypaths.ReadInitialization(layout)
	require.NoError(t, err)
	require.True(t, complete)
	return layout, plan
}

func executeDoctorJSON(t *testing.T, ctx context.Context, root, address string, extra ...string) doctorResult {
	t.Helper()
	command := newRootCmd()
	out := new(bytes.Buffer)
	command.SetOut(out)
	command.SetErr(new(bytes.Buffer))
	command.SetArgs(append([]string{"doctor", "--json", "--data-dir", root, "--address", address}, extra...))
	require.NoError(t, command.ExecuteContext(ctx))
	var result doctorResult
	require.NoError(t, json.Unmarshal(out.Bytes(), &result))
	return result
}

func doctorCheckByName(t *testing.T, result doctorResult, name string) doctorCheck {
	t.Helper()
	for _, c := range result.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("missing check %q: %+v", name, result.Checks)
	return doctorCheck{}
}

func TestDoctorAutomaticIdentityAndPassiveBoundary(t *testing.T) {
	for _, scenario := range []string{"healthy", "wrong-installation", "wrong-directory", "old-listener", "wrong-bearer", "missing-bearer", "legacy-running", "history-only"} {
		t.Run(scenario, func(t *testing.T) {
			layout, plan := initializedDoctorRoot(t)
			rootIdentity, err := gatewaypaths.DiagnosticRootIdentity(layout.Root)
			require.NoError(t, err)
			status := contract.SystemStatus{InstallationID: plan.Installation, InstallationRoot: rootIdentity, Process: contract.ProcessStatus{State: contract.ProcessReady, Ready: true}, SQLite: contract.SQLiteStatus{State: contract.SQLiteReady}, Keyring: contract.KeyringStatus{Capability: contract.KeyringReady}, Setup: &contract.CredentialSetupStatus{State: "ok", Facts: []contract.CredentialSetupFact{}}, HTTPProxy: &contract.HTTPProxyStatus{Enabled: false}, Endpoints: &contract.ControlEndpointsStatus{API: contract.EndpointReady, MCP: contract.EndpointDisabled}}
			switch scenario {
			case "wrong-installation":
				status.InstallationID = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
			case "wrong-directory":
				status.InstallationRoot = "foreign:directory"
			case "old-listener":
				status.InstallationID = ""
				status.InstallationRoot = ""
			case "missing-bearer":
				require.NoError(t, os.Remove(layout.AdminBearer))
			case "legacy-running":
				require.NoError(t, os.RemoveAll(filepath.Join(layout.Root, gatewaypaths.InitializationName)))
			case "history-only":
				status.Traffic = &contract.TrafficStatus{Ready: false, Health: "degraded", State: "unavailable"}
			}
			// Existing ownership models a running installation without a writer or run
			// marker; the legacy case must not open its database while held.
			owner, err := gatewaypaths.AcquireStoppedControl(layout.Root)
			require.NoError(t, err)
			defer func() { require.NoError(t, owner.Close()) }()
			before := treeBytes(t, layout.Root)
			var mu sync.Mutex
			requests := []string{}
			listener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				requests = append(requests, r.Method+" "+r.URL.Path)
				mu.Unlock()
				require.Equal(t, http.MethodGet, r.Method)
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/readyz":
					_, _ = w.Write([]byte(`{"status":"ready"}`))
				case "/api/v2/system-status":
					if scenario == "wrong-bearer" {
						w.Header().Set("Content-Type", "application/problem+json")
						w.WriteHeader(http.StatusUnauthorized)
						_, _ = w.Write([]byte(`{"status":401,"code":"unauthorized","title":"withheld"}`))
						return
					}
					require.NotEmpty(t, r.Header.Get("Authorization"))
					require.NoError(t, json.NewEncoder(w).Encode(status))
				case "/api/v2/mcp/servers":
					require.NoError(t, json.NewEncoder(w).Encode(contract.Collection[serverWire]{Items: []serverWire{}}))
				default:
					t.Errorf("unexpected dispatch: %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer listener.Close()
			result := executeDoctorJSON(t, t.Context(), layout.Root, listener.URL)
			require.Equal(t, before, treeBytes(t, layout.Root), "doctor must preserve database, keys, markers, modes and lock inventory")
			live := doctorCheckByName(t, result, "authenticated live status")
			switch scenario {
			case "wrong-installation", "wrong-directory":
				require.Nil(t, result.System)
				require.Equal(t, "failed", live.State)
				require.Equal(t, "attention", result.Result)
			case "old-listener":
				require.Nil(t, result.System)
				require.Equal(t, "unverified", live.State)
				require.Equal(t, "incomplete", result.Result)
			case "missing-bearer", "wrong-bearer":
				require.Nil(t, result.System)
				require.Equal(t, "unavailable", live.State)
				require.NotEqual(t, "healthy", result.Result)
			default:
				require.NotNil(t, result.System)
				require.Equal(t, "ok", live.State)
				require.Equal(t, "inactive", doctorCheckByName(t, result, "HTTP proxy").State)
				require.Equal(t, "inactive", doctorCheckByName(t, result, "MCP endpoint").State)
				if scenario == "history-only" {
					require.Equal(t, "attention", result.Result)
					require.Equal(t, "ok", doctorCheckByName(t, result, "serving").State)
					require.Equal(t, "degraded", doctorCheckByName(t, result, "traffic history").State)
				} else {
					require.Equal(t, "healthy", result.Result)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			for _, request := range requests {
				require.Contains(t, []string{"GET /readyz", "GET /api/v2/system-status", "GET /api/v2/mcp/servers"}, request)
			}
			if result.System == nil {
				require.NotContains(t, requests, "GET /api/v2/mcp/servers")
			}
			compact, verbose := renderDoctorChecks(result, false), renderDoctorChecks(result, true)
			require.Contains(t, compact, "Data directory:")
			require.Contains(t, compact, "Gateway")
			require.Contains(t, compact, "Live status")
			require.NotContains(t, compact, "No protected-keyring probe")
			require.Contains(t, verbose, "No protected-keyring probe")
			require.NotContains(t, compact, "verify-and-recover-storage")
			bearer, _ := os.ReadFile(layout.AdminBearer)
			if len(bearer) > 0 {
				require.NotContains(t, compact, strings.TrimSpace(string(bearer)))
				require.NotContains(t, verbose, strings.TrimSpace(string(bearer)))
			}
		})
	}
}

func TestDoctorImportedUnsetCredentialsArePassiveSetupTasks(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	source := filepath.Join(t.TempDir(), "config.json")
	document := strings.Replace(cliImportDocument, `"http":{"credentials":[]`, `"http":{"credentials":[{"ref":"http-credential/1","name":"Imported","boundary":{"host":"example.com","port":443,"allow_wildcard":false},"recipe":{"header":"Authorization","prefix":"Bearer "},"secret_ref":"http-credential/1/secret"}]`, 1)
	require.NoError(t, os.WriteFile(source, []byte(document), 0600))
	command := newTestRootCmd(t)
	command.SetOut(new(bytes.Buffer))
	command.SetErr(new(bytes.Buffer))
	command.SetArgs([]string{"init", "--from", source, "--confirm", "--data-dir", root})
	require.NoError(t, command.ExecuteContext(t.Context()))
	before := treeBytes(t, root)
	listener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"not_ready"}`))
	}))
	defer listener.Close()
	result := executeDoctorJSON(t, t.Context(), root, listener.URL)
	found := false
	for _, check := range result.Checks {
		if strings.HasPrefix(check.Name, "setup http_credential ") {
			found = true
			require.Equal(t, "incomplete", check.State)
			require.Contains(t, check.Next, "http credential rotate")
		}
	}
	require.True(t, found, "actual imported unset metadata must be actionable without material access")
	require.Equal(t, before, treeBytes(t, root))
}

func TestDoctorTimeoutPreservesLocalEvidence(t *testing.T) {
	layout, _ := initializedDoctorRoot(t)
	listener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer listener.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	before := treeBytes(t, layout.Root)
	result := executeDoctorJSON(t, ctx, layout.Root, listener.URL)
	require.NotEqual(t, "healthy", result.Result)
	require.Equal(t, "present", doctorCheckByName(t, result, "storage").State)
	require.Equal(t, "unavailable", doctorCheckByName(t, result, "authenticated live status").State)
	require.Equal(t, before, treeBytes(t, layout.Root))
}

func TestDoctorServerReadUsesContractPageBoundAndReportsIncompleteTraversal(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	bearer := filepath.Join(root, "bearer")
	require.NoError(t, os.WriteFile(bearer, []byte(testAdministratorBearer), 0600))
	for _, complete := range []bool{true, false} {
		calls := 0
		listener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			require.Equal(t, http.MethodGet, r.Method)
			require.Equal(t, "50", r.URL.Query().Get("limit"))
			w.Header().Set("Content-Type", "application/json")
			cursor := "next-page"
			page := contract.Collection[serverWire]{Items: []serverWire{}, NextCursor: &cursor}
			if calls == 2 && complete {
				page.NextCursor = nil
			}
			require.NoError(t, json.NewEncoder(w).Encode(page))
		}))
		_, err := doctorServers(t.Context(), listener.URL, bearer)
		listener.Close()
		require.Equal(t, 2, calls)
		if complete {
			require.NoError(t, err)
		} else {
			require.ErrorContains(t, err, "incomplete")
		}
	}
}

func TestDoctorSetupAndServerConditionsAreIndependent(t *testing.T) {
	const id = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	facts := []contract.CredentialSetupFact{{ID: id, Kind: "static_credential", Action: "supply_value", Slots: []string{"TOKEN"}, Inactive: true}, {ID: id, Kind: "http_credential", Action: "supply_value"}, {ID: id, Kind: "oauth_tokens", Action: "sign_in"}}
	result := doctorResult{}
	appendDoctorSetup(&result, facts)
	require.Equal(t, "inactive", doctorCheckByName(t, result, "setup static_credential "+id).State)
	require.Empty(t, doctorCheckByName(t, result, "setup static_credential "+id).Next)
	require.Contains(t, doctorCheckByName(t, result, "setup http_credential "+id).Next, "http credential rotate "+id)
	require.Contains(t, doctorCheckByName(t, result, "setup oauth_tokens "+id).Next, "auth-flow start "+id)
	for _, desired := range []contract.DesiredServerState{contract.DesiredServerDisabled, contract.DesiredServerEnabled} {
		value := doctorResult{}
		appendDoctorServers(&value, []serverWire{{ID: id, DesiredState: desired, CredentialState: contract.ServerCredentialReauthenticationRequired, Runtime: contract.ServerRuntime{State: contract.RuntimeAuthenticationRequired}}})
		check := doctorCheckByName(t, value, "MCP server "+id)
		if desired == contract.DesiredServerDisabled {
			require.Equal(t, "inactive", check.State)
			require.Empty(t, check.Next)
			require.NotContains(t, renderDoctorChecks(value, false), id)
		} else {
			require.Equal(t, "incomplete", check.State)
			require.Contains(t, check.Next, "auth-flow start")
		}
	}
}

func TestDoctorUnknownInitializationAndUnsafeFilesDoNotMutate(t *testing.T) {
	layout, _ := initializedDoctorRoot(t)
	require.NoError(t, os.WriteFile(filepath.Join(layout.Root, gatewaypaths.InitializationName, "unknown"), []byte("private-input-never-render"), 0600))
	require.NoError(t, os.Chmod(layout.AdminBearer, 0666))
	before := treeBytes(t, layout.Root)
	listener := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ready"}`))
	}))
	defer listener.Close()
	result := executeDoctorJSON(t, t.Context(), layout.Root, listener.URL)
	require.Equal(t, "failed", doctorCheckByName(t, result, "initialization").State)
	require.Equal(t, "failed", doctorCheckByName(t, result, "administrator credential").State)
	require.Equal(t, "attention", result.Result)
	text := renderDoctorChecks(result, false)
	require.Contains(t, text, "0666")
	require.NotContains(t, text, "private-input-never-render")
	require.NotContains(t, text, "init --resume")
	require.Equal(t, before, treeBytes(t, layout.Root))
	info, err := os.Stat(layout.AdminBearer)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0666), info.Mode().Perm())
}
