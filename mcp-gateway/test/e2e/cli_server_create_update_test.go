//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runCLIServerInputMatrix(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	bearerPath := filepath.Join(t.TempDir(), "admin-bearer")
	require.NoError(t, os.WriteFile(bearerPath, []byte(harness.bearer+"\n"), 0o600))
	writeInput := func(name, body string) string {
		path := filepath.Join(t.TempDir(), name)
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
		return path
	}
	createPath := writeInput("create.json", `{"namespace":"cli-create","display_name":"CLI create","enabled":false,"transport":{"kind":"stdio","executable":"/bin/cat","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{}}}`)
	results := make([]testutil.ProcessResult, 0, 10)
	created := runOnlineCLI(t, harness, bearerPath, true, "server", "create", "--file", createPath, "--idempotency-key", "cli-create-key", "--output", "json")
	results = append(results, created)
	var creation struct {
		Server struct {
			ID              string `json:"id"`
			DesiredRevision string `json:"desired_revision"`
			DisplayName     string `json:"display_name"`
		} `json:"server"`
		Operation *contract.ServerOperation `json:"operation"`
	}
	require.NoError(t, json.Unmarshal(created.Stdout, &creation))
	require.NotEmpty(t, creation.Server.ID)
	assert.Equal(t, "1", creation.Server.DesiredRevision)
	assert.Nil(t, creation.Operation)

	replayed := runOnlineCLI(t, harness, bearerPath, true, "server", "create", "--file", createPath, "--idempotency-key", "cli-create-key", "--output", "json")
	results = append(results, replayed)
	assert.JSONEq(t, string(created.Stdout), string(replayed.Stdout))
	conflictPath := writeInput("conflict.json", strings.ReplaceAll(string(mustReadFile(t, createPath)), "CLI create", "Different"))
	conflict := runOnlineCLI(t, harness, bearerPath, false, "server", "create", "--file", conflictPath, "--idempotency-key", "cli-create-key", "--output", "json")
	results = append(results, conflict)
	assert.Equal(t, 5, conflict.ExitCode)
	assert.Contains(t, string(conflict.Stderr), `"code":"idempotency_conflict"`)

	get := harness.adminSnapshot(http.MethodGet, "/api/v1/servers/"+creation.Server.ID, nil)
	etag := get.Header.Get("ETag")
	display := runOnlineCLI(t, harness, bearerPath, true, "server", "update", creation.Server.ID, "--display-name", "CLI renamed", "--output", "json")
	results = append(results, display)
	var displayMutation struct {
		Server struct {
			DesiredRevision string `json:"desired_revision"`
			DisplayName     string `json:"display_name"`
		} `json:"server"`
		Operation *contract.ServerOperation `json:"operation"`
	}
	require.NoError(t, json.Unmarshal(display.Stdout, &displayMutation))
	assert.Equal(t, "CLI renamed", displayMutation.Server.DisplayName)
	assert.Nil(t, displayMutation.Operation, "display-only update must not invent behavioral work")

	get = harness.adminSnapshot(http.MethodGet, "/api/v1/servers/"+creation.Server.ID, nil)
	etag = get.Header.Get("ETag")
	behaviorPath := writeInput("behavior.json", `{"enabled":true}`)
	refused := runOnlineCLI(t, harness, bearerPath, false, "server", "update", creation.Server.ID, "--etag", etag, "--file", behaviorPath, "--output", "json")
	results = append(results, refused)
	assert.Equal(t, 2, refused.ExitCode)
	assert.Contains(t, string(refused.Stderr), `"code":"client_invalid_input"`)
	behavior := runOnlineCLI(t, harness, bearerPath, true, "server", "update", creation.Server.ID, "--etag", etag, "--file", behaviorPath, "--yes", "--output", "json")
	results = append(results, behavior)
	var behaviorMutation struct {
		Operation *contract.ServerOperation `json:"operation"`
	}
	require.NoError(t, json.Unmarshal(behavior.Stdout, &behaviorMutation))
	require.NotNil(t, behaviorMutation.Operation)
	assert.Equal(t, contract.OperationActivate, behaviorMutation.Operation.Kind)

	unknownNestedPath := writeInput("unknown.json", `{"namespace":"never-sent","display_name":"Never","enabled":false,"transport":{"kind":"stdio","executable":"/bin/cat","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{},"secret":"forbidden"}}`)
	unknown := runOnlineCLI(t, harness, bearerPath, false, "server", "create", "--file", unknownNestedPath, "--idempotency-key", "unknown-key", "--output", "json")
	results = append(results, unknown)
	assert.Equal(t, 2, unknown.ExitCode)
	assert.Contains(t, string(unknown.Stderr), `"code":"invalid_server_configuration"`)
	assert.Contains(t, string(unknown.Stderr), `"context":{"field":"transport","rule":"invalid"}`)
	missing := harness.adminSnapshot(http.MethodGet, "/api/v1/servers?limit=100", nil)
	assert.NotContains(t, string(missing.Body), "never-sent")

	fake := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", contract.MediaTypeJSON)
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte(`{"malformed":true}`))
	}))
	defer fake.Close()
	uncertain := runCLIAt(t, harness, bearerPath, fake.URL, "server", "create", "--file", createPath, "--idempotency-key", "recover-this-key", "--output", "json")
	results = append(results, uncertain)
	assert.Equal(t, 8, uncertain.ExitCode)
	assert.Contains(t, string(uncertain.Stderr), `"uncertain":true`)
	assert.Contains(t, string(uncertain.Stderr), "recover-this-key")
	assert.Contains(t, string(uncertain.Stderr), "sha256:")

	harness.Stop(syscall.SIGTERM)
	preHandoff := runOnlineCLI(t, harness, bearerPath, false, "server", "update", creation.Server.ID, "--etag", etag, "--file", behaviorPath, "--yes", "--output", "json")
	results = append(results, preHandoff)
	assert.Equal(t, 9, preHandoff.ExitCode)
	for _, result := range results {
		if result.ExitCode != 0 {
			assert.Empty(t, result.Stdout, "failed commands must have empty stdout")
		}
		assert.NotContains(t, string(result.Stdout), harness.bearer)
		assert.NotContains(t, string(result.Stderr), harness.bearer)
		assert.True(t, result.Cleanup.Reaped)
		assert.False(t, result.Cleanup.Survived)
	}
	assert.Len(t, harness.results, 1, "T39 must own one Gateway lifecycle")
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	return contents
}

func runCLIAt(t *testing.T, harness *gatewayHarness, bearerPath, address string, args ...string) testutil.ProcessResult {
	t.Helper()
	args = append(args, "--address", address, "--admin-bearer-file", bearerPath)
	result, _ := harness.runner.Run(context.Background(), harness.binary, args...)
	return result
}
