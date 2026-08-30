//go:build e2e

package e2e

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLIControlFoundationCanary(t *testing.T) {
	runner := firstRunRunner(t)
	binary := gatewayBinary(t)
	root := t.TempDir()
	bearerPath := filepath.Join(root, "admin-bearer")
	require.NoError(t, os.WriteFile(bearerPath, []byte(usabilityTestBearer+"\n"), 0o600))
	id := "01ARZ3NDEKTSV4RRFFQ69G5FAV"

	run := func(t *testing.T, address string, args ...string) processResultView {
		t.Helper()
		command := append([]string(nil), args...)
		command = append(command, "--admin-bearer-file", bearerPath, "--address", address, "--output", "json")
		result, err := runner.Run(t.Context(), binary, command...)
		return processResultView{result: result, err: err}
	}

	t.Run("refusal is direct and never replayed", func(t *testing.T) {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		require.NoError(t, err)
		address := "http://" + listener.Addr().String()
		require.NoError(t, listener.Close())

		read := run(t, address, "status")
		read.requireProblem(t, 9, "gateway_not_running", false)
		assert.Contains(t, string(read.result.Stderr), "mcp-gateway serve --listen "+listener.Addr().String())
		mutation := run(t, address, "backup", "create", "--idempotency-key", "foundation-refusal")
		mutation.requireProblem(t, 9, "gateway_not_running", false)
	})

	t.Run("validated api problem is preserved", func(t *testing.T) {
		var requests atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			response.Header().Set("Content-Type", "application/problem+json")
			response.WriteHeader(http.StatusServiceUnavailable)
			_, _ = response.Write([]byte(`{"status":503,"code":"storage_unavailable","title":"Storage is unavailable."}`))
		}))
		defer server.Close()
		result := run(t, server.URL, "status")
		result.requireProblem(t, 7, "storage_unavailable", false, http.StatusServiceUnavailable)
		assert.Equal(t, int64(1), requests.Load())
	})

	t.Run("malformed and post-handoff mutation failures are uncertain", func(t *testing.T) {
		var malformedRequests atomic.Int64
		malformed := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			malformedRequests.Add(1)
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(http.StatusCreated)
			_, _ = response.Write([]byte(`{"id":`))
		}))
		defer malformed.Close()
		result := run(t, malformed.URL, "backup", "create", "--idempotency-key", "foundation-malformed")
		result.requireProblem(t, 8, "client_outcome_uncertain", true)
		assert.Contains(t, string(result.result.Stderr), "Nothing was replayed")
		assert.Equal(t, int64(1), malformedRequests.Load())

		var abruptRequests atomic.Int64
		abrupt := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			abruptRequests.Add(1)
			connection, _, err := response.(http.Hijacker).Hijack()
			if err == nil {
				_ = connection.Close()
			}
		}))
		defer abrupt.Close()
		result = run(t, abrupt.URL, "backup", "create", "--idempotency-key", "foundation-abrupt")
		result.requireProblem(t, 8, "client_outcome_uncertain", true)
		assert.Equal(t, int64(1), abruptRequests.Load())
	})

	t.Run("local intent stops before bearer and http", func(t *testing.T) {
		var requests atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
		defer server.Close()
		patch := filepath.Join(root, "patch.json")
		require.NoError(t, os.WriteFile(patch, []byte(`{"display_name":"from-file"}`), 0o600))
		missingBearer := filepath.Join(root, "missing-bearer")
		result, err := runner.Run(t.Context(), binary, "server", "update", id, "--etag", contract.ServerETag(id, "7"), "--file", patch, "--display-name", "direct", "--admin-bearer-file", missingBearer, "--address", server.URL, "--output", "json")
		require.Error(t, err)
		assert.Equal(t, 2, result.ExitCode)
		assert.Contains(t, string(result.Stderr), `"code":"client_invalid_input"`)
		assert.NotContains(t, string(result.Stderr), "bearer")
		assert.Zero(t, requests.Load())
	})

	t.Run("item etag is validated and exposed", func(t *testing.T) {
		body := []byte(`{"id":"` + id + `","desired_revision":"7"}`)
		var malformed atomic.Bool
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			response.Header().Set("Content-Type", "application/json")
			if malformed.Load() {
				response.Header().Set("ETag", `W/"server-`+id+`-7"`)
			} else {
				response.Header().Set("ETag", contract.ServerETag(id, "7"))
			}
			_, _ = response.Write(body)
		}))
		defer server.Close()

		result, err := runner.Run(t.Context(), binary, "server", "get", id, "--admin-bearer-file", bearerPath, "--address", server.URL, "--output", "human")
		require.NoError(t, err, "%s", result.Stderr)
		assert.Contains(t, string(result.Stdout), "ETAG")
		assert.Contains(t, string(result.Stdout), contract.ServerETag(id, "7"))
		malformed.Store(true)
		invalid := run(t, server.URL, "server", "get", id)
		invalid.requireProblem(t, 10, "client_response_invalid", false)
	})
}

type processResultView struct {
	result testutil.ProcessResult
	err    error
}

func (view processResultView) requireProblem(t *testing.T, exit int, code string, uncertain bool, status ...int) {
	t.Helper()
	require.Error(t, view.err)
	assert.Equal(t, exit, view.result.ExitCode)
	assert.Empty(t, view.result.Stdout)
	assertSettledResult(t, view.result)
	var problem struct {
		Status    *int   `json:"status"`
		Code      string `json:"code"`
		ExitCode  int    `json:"exit_code"`
		Uncertain bool   `json:"uncertain"`
	}
	require.NoError(t, json.Unmarshal(view.result.Stderr, &problem))
	if len(status) == 0 {
		assert.Nil(t, problem.Status)
	} else {
		require.NotNil(t, problem.Status)
		assert.Equal(t, status[0], *problem.Status)
	}
	assert.Equal(t, code, problem.Code)
	assert.Equal(t, exit, problem.ExitCode)
	assert.Equal(t, uncertain, problem.Uncertain)
	assert.NotContains(t, string(view.result.Stderr), usabilityTestBearer)
}
