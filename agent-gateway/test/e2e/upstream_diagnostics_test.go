//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

type upstreamDiagnosticRecord struct {
	ProcessID   string `json:"process_id"`
	Action      string `json:"action"`
	Event       string `json:"event"`
	Level       string `json:"level"`
	Upstream    uint64 `json:"upstream_ref"`
	Attempt     uint64 `json:"attempt_ref"`
	Phase       string `json:"phase"`
	Reason      string `json:"reason"`
	Disposition string `json:"disposition"`
}

func upstreamDiagnosticRecords(t *testing.T, output []byte) []upstreamDiagnosticRecord {
	t.Helper()
	var records []upstreamDiagnosticRecord
	for _, line := range bytes.Split(bytes.TrimSpace(output), []byte{'\n'}) {
		var record upstreamDiagnosticRecord
		require.NoError(t, json.Unmarshal(line, &record))
		records = append(records, record)
	}
	return records
}

func assertOAuthDiagnosticSequence(t *testing.T, output []byte) {
	t.Helper()
	var ref uint64
	required, completed := false, false
	for _, record := range upstreamDiagnosticRecords(t, output) {
		if record.Event == "oauth_required" {
			required, ref = true, record.Upstream
			require.NotZero(t, record.Attempt)
		}
		if record.Event == "oauth_completed" {
			require.True(t, required)
			require.Equal(t, ref, record.Upstream)
			require.NotZero(t, record.Attempt)
			completed = true
		}
	}
	require.NotZero(t, ref)
	require.True(t, completed)
}

func TestUpstreamDiagnosticsRealBinaryRetryAndRecovery(t *testing.T) {
	upstream := newRawHTTPFixtureWithTools(t, "modern", []fixtureTool{{Name: "private-diagnostic-tool", InputSchema: json.RawMessage(`{"type":"object"}`)}})
	var failing atomic.Bool
	failing.Store(true)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing.Load() {
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			defer func() { _ = connection.Close() }()
			_ = connection.SetWriteDeadline(time.Now().Add(time.Second))
			_, _ = io.WriteString(connection, "HTTP/1.1 200 OK\r\nContent-Length: 1000\r\nContent-Type: application/json\r\n\r\nprivate-downstream-failure-canary")
			return
		}
		upstream.server.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(endpoint.Close)
	harness := newGatewayHarness(t)
	harness.Start()
	request, err := json.Marshal(map[string]any{"namespace": "private-diagnostic-namespace", "display_name": "Private diagnostic display", "enabled": true, "transport": map[string]any{"kind": "streamable_http", "url": endpoint.URL + "/mcp", "protocol_mode": "modern", "authentication": map[string]string{"mode": "none"}}})
	require.NoError(t, err)
	var created stdioCreation
	response := harness.adminSnapshotWithHeaders(http.MethodPost, "/api/v2/mcp/servers", request, map[string]string{"Idempotency-Key": "upstream-diagnostic-retry"})
	decodeSnapshot(t, response, http.StatusCreated, &created)
	waitForStdioServer(t, harness, created.Server.ID, func(server stdioServerView) bool { return server.Runtime.State == contract.RuntimeRetryWait })
	failing.Store(false)
	waitForStdioServer(t, harness, created.Server.ID, func(server stdioServerView) bool {
		return server.Runtime.State == contract.RuntimeActive && server.Catalog.ActiveState == contract.ActiveCatalogCurrent
	})
	var current struct {
		Runtime contract.ServerRuntime `json:"runtime"`
	}
	decodeSnapshot(t, harness.adminSnapshot(http.MethodGet, "/api/v2/mcp/servers/"+created.Server.ID, nil), http.StatusOK, &current)
	require.NotNil(t, current.Runtime.DiagnosticCorrelation)
	correlation := current.Runtime.DiagnosticCorrelation
	secondRequest := bytes.Replace(request, []byte("private-diagnostic-namespace"), []byte("private-concurrent-namespace"), 1)
	var second stdioCreation
	decodeSnapshot(t, harness.adminSnapshotWithHeaders(http.MethodPost, "/api/v2/mcp/servers", secondRequest, map[string]string{"Idempotency-Key": "concurrent-diagnostic-server"}), http.StatusCreated, &second)
	waitForStdioServer(t, harness, second.Server.ID, func(server stdioServerView) bool { return server.Runtime.State == contract.RuntimeActive })
	var concurrent struct {
		Runtime contract.ServerRuntime `json:"runtime"`
	}
	decodeSnapshot(t, harness.adminSnapshot(http.MethodGet, "/api/v2/mcp/servers/"+second.Server.ID, nil), http.StatusOK, &concurrent)
	require.NotNil(t, concurrent.Runtime.DiagnosticCorrelation)
	require.Equal(t, correlation.ProcessID, concurrent.Runtime.DiagnosticCorrelation.ProcessID)
	require.NotEqual(t, correlation.UpstreamRef, concurrent.Runtime.DiagnosticCorrelation.UpstreamRef)
	bearerPath := filepath.Join(t.TempDir(), "admin-bearer")
	require.NoError(t, os.WriteFile(bearerPath, []byte(harness.bearer+"\n"), 0o600))
	for _, mode := range []string{"human", "json"} {
		cli, cliErr := harness.runner.Run(t.Context(), harness.binary, "mcp", "server", "get", created.Server.ID, "--address", "http://"+harness.authority, "--admin-bearer-file", bearerPath, "--output", mode)
		require.NoError(t, cliErr)
		require.Empty(t, cli.Stderr)
		require.Contains(t, string(cli.Stdout), correlation.ProcessID)
		if mode == "json" {
			var read struct {
				Runtime contract.ServerRuntime `json:"runtime"`
			}
			require.NoError(t, json.Unmarshal(cli.Stdout, &read))
			require.Equal(t, correlation, read.Runtime.DiagnosticCorrelation)
		} else {
			require.Contains(t, string(cli.Stdout), "UPSTREAM_REF")
		}
	}
	result := harness.Stop(syscall.SIGTERM)
	var ref uint64
	var failed, recovered bool
	for _, record := range upstreamDiagnosticRecords(t, result.Stderr) {
		switch record.Event {
		case "upstream_unhealthy":
			require.Equal(t, "WARN", record.Level)
			require.Equal(t, "mcp_initialization", record.Phase)
			require.Equal(t, "retry_scheduled", record.Disposition)
			require.Equal(t, "wait_scheduled_retry", record.Action)
			require.Equal(t, correlation.ProcessID, record.ProcessID)
			require.Equal(t, correlation.UpstreamRef, strconv.FormatUint(record.Upstream, 10))
			ref, failed = record.Upstream, true
		case "upstream_recovered":
			require.True(t, failed)
			require.Equal(t, ref, record.Upstream)
			require.Equal(t, "WARN", record.Level)
			require.Equal(t, "no_action", record.Action)
			require.False(t, recovered, "one retained incident produces one recovery")
			recovered = true
		}
	}
	require.NotZero(t, ref)
	require.True(t, recovered)
	harness.Start()
	waitForStdioServer(t, harness, created.Server.ID, func(server stdioServerView) bool {
		return server.Runtime.State == contract.RuntimeActive
	})
	var restarted struct {
		Runtime contract.ServerRuntime `json:"runtime"`
	}
	decodeSnapshot(t, harness.adminSnapshot(http.MethodGet, "/api/v2/mcp/servers/"+created.Server.ID, nil), http.StatusOK, &restarted)
	require.NotNil(t, restarted.Runtime.DiagnosticCorrelation)
	require.NotEqual(t, correlation.ProcessID, restarted.Runtime.DiagnosticCorrelation.ProcessID, "the old reference must be scoped to the old process")
	harness.Stop(syscall.SIGTERM)
	for _, canary := range []string{endpoint.URL, created.Server.ID, second.Server.ID, "private-concurrent-namespace", "private-downstream-failure-canary", "private-diagnostic-tool", "private-diagnostic-namespace", "Private diagnostic display"} {
		require.NotContains(t, string(result.Stderr), canary)
	}
}
