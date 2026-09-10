//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

type upstreamDiagnosticRecord struct {
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
	harness.serveArgs = append(harness.serveArgs, "--log-level", "debug")
	harness.Start()
	request, err := json.Marshal(map[string]any{"namespace": "private-diagnostic-namespace", "display_name": "Private diagnostic display", "enabled": true, "transport": map[string]any{"kind": "streamable_http", "url": endpoint.URL + "/mcp", "protocol_mode": "modern", "authentication": map[string]string{"mode": "none"}}})
	require.NoError(t, err)
	var created stdioCreation
	response := harness.adminSnapshotWithHeaders(http.MethodPost, "/api/v1/servers", request, map[string]string{"Idempotency-Key": "upstream-diagnostic-retry"})
	decodeSnapshot(t, response, http.StatusCreated, &created)
	waitForStdioServer(t, harness, created.Server.ID, func(server stdioServerView) bool { return server.Runtime.State == contract.RuntimeRetryWait })
	failing.Store(false)
	waitForStdioServer(t, harness, created.Server.ID, func(server stdioServerView) bool {
		return server.Runtime.State == contract.RuntimeActive && server.Catalog.ActiveState == contract.ActiveCatalogCurrent
	})
	result := harness.Stop(syscall.SIGTERM)
	var ref uint64
	var failed, retry, recovered bool
	attempts := map[uint64]bool{}
	for _, record := range upstreamDiagnosticRecords(t, result.Stderr) {
		switch record.Event {
		case "upstream_attempt_start":
			attempts[record.Attempt] = true
		case "upstream_unhealthy":
			require.Equal(t, "WARN", record.Level)
			require.Equal(t, "mcp_initialization", record.Phase)
			require.Equal(t, "retry_scheduled", record.Disposition)
			ref, failed = record.Upstream, true
		case "upstream_retry_scheduled":
			retry = true
		case "upstream_recovered":
			require.True(t, failed)
			require.Equal(t, ref, record.Upstream)
			recovered = true
		}
	}
	require.NotZero(t, ref)
	require.True(t, retry)
	require.True(t, recovered)
	require.GreaterOrEqual(t, len(attempts), 2)
	for _, canary := range []string{endpoint.URL, created.Server.ID, "private-downstream-failure-canary", "private-diagnostic-tool", "private-diagnostic-namespace", "Private diagnostic display"} {
		require.NotContains(t, string(result.Stderr), canary)
	}
}
