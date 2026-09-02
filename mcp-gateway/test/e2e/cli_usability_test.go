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

	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const usabilityTestBearer = "mgw_admin_CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"

func TestCLICredentialFailureProblems(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Header.Get("Authorization") != "Bearer "+usabilityTestBearer {
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		writer.Header().Set("Content-Type", "application/problem+json")
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"status":401,"code":"authentication_required","title":"Authentication is required."}`))
	}))
	defer server.Close()

	cases := []struct {
		name         string
		code         string
		exit         int
		humanSnippet string
		prepare      func(*testing.T, string)
	}{
		{name: "missing", code: "client_bearer_missing", exit: 2, humanSnippet: "Run mcp-gateway initialize"},
		{name: "symlink", code: "client_bearer_symlink", exit: 2, humanSnippet: "regular owner-only bearer file", prepare: func(t *testing.T, path string) {
			target := filepath.Join(t.TempDir(), "target")
			require.NoError(t, os.WriteFile(target, []byte(usabilityTestBearer+"\n"), 0o600))
			require.NoError(t, os.Symlink(target, path))
		}},
		{name: "overpermissive", code: "client_bearer_permissions", exit: 2, humanSnippet: "permissions 0400 or 0600", prepare: func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, []byte(usabilityTestBearer+"\n"), 0o600))
			require.NoError(t, os.Chmod(path, 0o644))
		}},
		{name: "malformed", code: "client_bearer_malformed", exit: 2, humanSnippet: "exact bearer file", prepare: func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, []byte("not-a-bearer\n"), 0o600))
		}},
		{name: "unreadable", code: "client_bearer_unreadable", exit: 2, humanSnippet: "owner-read permission", prepare: func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, []byte(usabilityTestBearer+"\n"), 0o600))
			require.NoError(t, os.Chmod(path, 0o000))
		}},
		{name: "rejected", code: "client_bearer_rejected", exit: 3, humanSnippet: "current owner-only bearer file", prepare: func(t *testing.T, path string) {
			require.NoError(t, os.WriteFile(path, []byte(usabilityTestBearer+"\n"), 0o600))
		}},
	}

	runner := firstRunRunner(t)
	for _, test := range cases {
		for _, mode := range []string{"human", "json"} {
			t.Run(test.name+"/"+mode, func(t *testing.T) {
				root := filepath.Join(t.TempDir(), "gateway")
				require.NoError(t, os.Mkdir(root, 0o700))
				bearerPath := filepath.Join(root, gatewaypaths.AdminBearerName)
				if test.prepare != nil {
					test.prepare(t, bearerPath)
				}
				result, err := runner.Run(t.Context(), gatewayBinary(t), "--data-dir", root, "status", "--address", server.URL, "--output", mode)
				require.Error(t, err)
				assert.Equal(t, test.exit, result.ExitCode)
				assert.Empty(t, result.Stdout)
				assertSettledResult(t, result)
				assert.NotContains(t, string(result.Stderr), usabilityTestBearer)
				if mode == "json" {
					var problem struct {
						Code string `json:"code"`
					}
					require.NoError(t, json.Unmarshal(result.Stderr, &problem))
					assert.Equal(t, test.code, problem.Code)
				} else {
					assert.Contains(t, string(result.Stderr), test.humanSnippet)
				}
			})
		}
	}
	assert.Equal(t, int64(2), requests.Load(), "file failures must stop before HTTP and API rejection must not replay")
}

func TestCLICommandErrors(t *testing.T) {
	runner := firstRunRunner(t)
	jsonResult, err := runner.Run(t.Context(), gatewayBinary(t), "server", "get", "--json")
	require.Error(t, err)
	assert.Equal(t, 2, jsonResult.ExitCode)
	assert.Empty(t, jsonResult.Stdout)
	assertSettledResult(t, jsonResult)
	var problem struct {
		Code  string `json:"code"`
		Title string `json:"title"`
	}
	require.NoError(t, json.Unmarshal(jsonResult.Stderr, &problem))
	assert.Equal(t, "client_invalid_input", problem.Code)
	assert.Contains(t, problem.Title, "Usage: mcp-gateway server get ID")

	humanResult, err := runner.Run(t.Context(), gatewayBinary(t), "status", "--output", "human", "--definitely-invalid")
	require.Error(t, err)
	assert.Equal(t, 2, humanResult.ExitCode)
	assert.Empty(t, humanResult.Stdout)
	assertSettledResult(t, humanResult)
	assert.Contains(t, string(humanResult.Stderr), "flag is invalid or incomplete")
	assert.Contains(t, string(humanResult.Stderr), "Usage: mcp-gateway status")
}

func TestCLIHelpTree(t *testing.T) {
	runner := firstRunRunner(t)
	root, err := runner.Run(t.Context(), gatewayBinary(t), "--help")
	require.NoError(t, err, "%s", root.Stderr)
	assertSettledResult(t, root)
	assert.Empty(t, root.Stderr)
	for _, example := range []string{"mcp-gateway initialize", "mcp-gateway serve", "mcp-gateway status"} {
		assert.Contains(t, string(root.Stdout), example)
	}
	assert.NotContains(t, string(root.Stdout), "Online Gateway control commands")

	leaf, err := runner.Run(t.Context(), gatewayBinary(t), "server", "credential", "replace", "--help")
	require.NoError(t, err, "%s", leaf.Stderr)
	assertSettledResult(t, leaf)
	assert.Empty(t, leaf.Stderr)
	assert.Contains(t, string(leaf.Stdout), "Replace a server credential")
	assert.Contains(t, string(leaf.Stdout), "--etag")
	assert.Contains(t, string(leaf.Stdout), "--file")
	assert.NotContains(t, strings.ToLower(string(leaf.Stdout)), "operate the local gateway")
}
