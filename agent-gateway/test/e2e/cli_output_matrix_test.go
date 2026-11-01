//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLIOutputMatrix(t *testing.T) {
	harness := newGatewayHarness(t)
	bearerPath := filepath.Join(t.TempDir(), "admin-bearer")
	require.NoError(t, os.WriteFile(bearerPath, []byte(harness.bearer+"\n"), 0o600))
	grantID := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	principalID := "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	grant := contract.Grant{ID: grantID, PrincipalID: principalID, ServerID: contract.SyntheticServerID, Effect: contract.GrantDeny, State: contract.GrantActive, Revision: "1", CreatedAt: "2026-10-01T00:00:00Z"}
	createArgs := []string{"mcp", "grant", "create", "--principal-id", principalID, "--effect", "deny", "--server-id", contract.SyntheticServerID}
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		switch {
		case request.Method == http.MethodDelete && request.URL.Path == "/api/v2/mcp/grants/"+grantID:
			writer.WriteHeader(http.StatusNoContent)
		case request.Method == http.MethodPost && request.URL.Path == "/api/v2/mcp/grants":
			writer.Header().Set("Content-Type", contract.MediaTypeJSON)
			writer.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(writer).Encode(grant)
		case request.Method == http.MethodGet && request.URL.Path == "/api/v2/principals":
			writer.Header().Set("Content-Type", contract.MediaTypeProblemJSON)
			writer.WriteHeader(http.StatusNotFound)
			_, _ = writer.Write([]byte(`{"status":404,"code":"not_found","title":"The selected resource was not found."}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	results := make([]testutil.ProcessResult, 0, 16)
	for _, mode := range []string{"human", "json"} {
		args := append([]string(nil), createArgs...)
		if mode == "json" {
			args = append(args, "--json")
		}
		result := runCLIAt(t, harness, bearerPath, server.URL, args...)
		results = append(results, result)
		require.Equal(t, 0, result.ExitCode, string(result.Stderr))
		assert.Empty(t, result.Stderr)
		assert.Contains(t, string(result.Stdout), grantID)
		if mode == "json" {
			assert.True(t, json.Valid(result.Stdout))
		}
	}

	humanNoContent := runCLIAt(t, harness, bearerPath, server.URL, "mcp", "grant", "delete", grantID, "--yes")
	results = append(results, humanNoContent)
	require.Equal(t, 0, humanNoContent.ExitCode, string(humanNoContent.Stderr))
	assert.Contains(t, string(humanNoContent.Stdout), grantID)
	assert.Contains(t, string(humanNoContent.Stdout), "deleted")
	assert.Empty(t, humanNoContent.Stderr)

	jsonNoContent := runCLIAt(t, harness, bearerPath, server.URL, "mcp", "grant", "delete", grantID, "--yes", "--json")
	results = append(results, jsonNoContent)
	require.Equal(t, 0, jsonNoContent.ExitCode, string(jsonNoContent.Stderr))
	assert.Equal(t, "{}\n", string(jsonNoContent.Stdout))
	assert.Empty(t, jsonNoContent.Stderr)

	for _, mode := range []string{"human", "json"} {
		args := []string{"agent", "list"}
		if mode == "json" {
			args = append(args, "--json")
		}
		result := runCLIAt(t, harness, bearerPath, server.URL, args...)
		results = append(results, result)
		assert.Equal(t, 4, result.ExitCode)
		assert.Empty(t, result.Stdout)
		if mode == "json" {
			var problem struct {
				Code string `json:"code"`
			}
			require.NoError(t, json.Unmarshal(result.Stderr, &problem))
			assert.Equal(t, "not_found", problem.Code)
		} else {
			assert.Contains(t, string(result.Stderr), "selected resource was not found")
		}
	}

	unavailable := "http://" + unusedAuthority(t)
	for _, mode := range []string{"human", "json"} {
		args := []string{"agent", "list"}
		if mode == "json" {
			args = append(args, "--json")
		}
		result := runCLIAt(t, harness, bearerPath, unavailable, args...)
		results = append(results, result)
		assert.Equal(t, 9, result.ExitCode)
		assert.Empty(t, result.Stdout)
		if mode == "json" {
			assert.Contains(t, string(result.Stderr), `"code":"gateway_not_running"`)
		} else {
			assert.Contains(t, string(result.Stderr), "Start it with: agent-gateway serve")
		}
	}

	conflict := runCLIAt(t, harness, bearerPath, server.URL, "agent", "list", "--json", "--output", "human")
	results = append(results, conflict)
	assert.Equal(t, 2, conflict.ExitCode)
	assert.Empty(t, conflict.Stdout)
	assert.Contains(t, string(conflict.Stderr), "Choose either --output human or --output json")
	assert.Equal(t, int64(6), requests.Load(), "selector and transport failures must not reach or replay HTTP")

	var redirects atomic.Int64
	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		redirects.Add(1)
		http.Redirect(writer, request, "http://127.0.0.1:1/forbidden", http.StatusFound)
	}))
	defer redirect.Close()
	for _, mode := range []string{"human", "json"} {
		args := []string{"agent", "list"}
		if mode == "json" {
			args = append(args, "--json")
		}
		result := runCLIAt(t, harness, bearerPath, redirect.URL, args...)
		results = append(results, result)
		assert.Equal(t, 10, result.ExitCode)
		assert.Empty(t, result.Stdout)
	}
	assert.Equal(t, int64(2), redirects.Load())

	var uncertainRequests atomic.Int64
	uncertainServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		uncertainRequests.Add(1)
		writer.Header().Set("Content-Type", contract.MediaTypeProblemJSON)
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"status":503,"code":"storage_unavailable","title":"Storage is unavailable."}`))
	}))
	defer uncertainServer.Close()
	for _, mode := range []string{"human", "json"} {
		args := append([]string(nil), createArgs...)
		if mode == "json" {
			args = append(args, "--json")
		}
		result := runCLIAt(t, harness, bearerPath, uncertainServer.URL, args...)
		results = append(results, result)
		assert.Equal(t, 8, result.ExitCode)
		assert.Empty(t, result.Stdout)
		assert.Contains(t, string(result.Stderr), "Nothing was replayed")
	}
	assert.Equal(t, int64(2), uncertainRequests.Load())

	for _, mode := range []string{"human", "json"} {
		secretPath := filepath.Join(t.TempDir(), "lost-secret")
		var secretRequests atomic.Int64
		secretServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			secretRequests.Add(1)
			if err := os.Remove(secretPath); err != nil {
				http.Error(writer, "fixture failure", http.StatusInternalServerError)
				return
			}
			writer.Header().Set("Content-Type", contract.MediaTypeJSON)
			writer.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(writer).Encode(contract.CreatedAdminCredential{
				AdminCredential: contract.AdminCredential{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAX", Fingerprint: "sha256:fingerprint", Status: contract.CredentialActive, Revision: "1"},
				Bearer:          "mgw_admin_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
			})
		}))
		args := []string{"admin", "credential", "create", "--secret-output", secretPath}
		if mode == "json" {
			args = append(args, "--json")
		}
		result := runCLIAt(t, harness, bearerPath, secretServer.URL, args...)
		secretServer.Close()
		results = append(results, result)
		assert.Equal(t, 2, result.ExitCode)
		assert.Empty(t, result.Stdout)
		assert.Contains(t, string(result.Stderr), "could not be published")
		assert.Equal(t, int64(1), secretRequests.Load())
		assert.NoFileExists(t, secretPath)
	}

	for _, result := range results {
		assert.False(t, result.StdoutTruncated)
		assert.False(t, result.StderrTruncated)
		assert.NotContains(t, string(result.Stdout), strings.TrimSpace(harness.bearer))
		assert.NotContains(t, string(result.Stderr), strings.TrimSpace(harness.bearer))
		assert.True(t, result.Cleanup.Reaped)
		assert.False(t, result.Cleanup.Survived)
	}
}
