//go:build e2e && browser

package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestBrowserHTTPCredentials(t *testing.T) {
	runHTTPBrowserScenario(t, "http-credentials", "http_credentials_complete")
}

func TestBrowserHTTPTraffic(t *testing.T) {
	runHTTPBrowserScenario(t, "http-traffic", "http_traffic_complete")
}

func TestBrowserHTTPGrants(t *testing.T) {
	runHTTPBrowserScenario(t, "http-grants", "http_grants_complete")
}

func runHTTPBrowserScenario(t *testing.T, scenario, eventName string) {
	t.Helper()
	assertBrowserEnvironmentManifest(t)
	harness := newGatewayHarness(t)
	proxy := ""
	if scenario == "http-traffic" {
		harness.binary, _ = httpMaterialBinary(t)
		createHTTPCA(t, harness)
		proxy = unusedAuthority(t)
		harness.serveArgs = append(harness.serveArgs, "--http-proxy-listen", proxy)
	}
	harness.Start()
	if proxy != "" {
		seedBrowserHTTPRejections(t, harness, proxy)
	}
	runner, err := testutil.NewBinaryRunner(75*time.Second, 32*1024)
	require.NoError(t, err)
	process, input, err := runner.StartWithInputPipe(context.Background(), "node", browserBridgePath(t))
	require.NoError(t, err)
	finished := false
	t.Cleanup(func() {
		if !finished {
			require.NoError(t, process.Stop())
			result, _ := process.Wait()
			require.True(t, result.Cleanup.Reaped)
			require.False(t, result.Cleanup.Survived)
		}
	})
	require.NoError(t, json.NewEncoder(input).Encode(map[string]any{"version": 1, "scenario": scenario, "base_url": "http://" + harness.authority, "admin_bearer": harness.bearer}))
	require.NoError(t, input.Close())
	result, err := process.Wait()
	finished = true
	require.NoError(t, err, "HTTP credential browser scenario: %s", result.Stderr)
	require.Empty(t, result.Stderr)
	require.False(t, result.StdoutTruncated)
	require.True(t, result.Cleanup.Reaped)
	require.False(t, result.Cleanup.Survived)
	require.NotContains(t, string(result.Stdout), harness.bearer)
	var event struct {
		Event       string `json:"event"`
		Requests    int    `json:"requests"`
		Screenshots string `json:"screenshots"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(string(result.Stdout))), &event))
	require.Equal(t, eventName, event.Event)
	require.Positive(t, event.Requests)
	require.NotEmpty(t, event.Screenshots)
	t.Logf("HTTP credential screenshots: %s", event.Screenshots)
	harness.Stop(os.Interrupt)
	if proxy == "" {
		require.Len(t, harness.results, 1)
	} else {
		require.Len(t, harness.results, 2)
	}
}

func seedBrowserHTTPRejections(t *testing.T, h *gatewayHarness, proxy string) {
	t.Helper()
	principal := h.CreatePrincipal("HTTP diagnostics fixture", contract.VisibilityRequestable)
	credential := h.IssueCredential(principal)
	for _, request := range []struct{ line, header string }{
		{"GET http://example.com/path-secret?query-secret", "Upgrade: private-upgrade\r\n"},
		{"GET /path-secret?query-secret", ""},
		{"GET http://example.com/%2fpath-secret?query-secret", ""},
	} {
		conn, err := net.DialTimeout("tcp", proxy, 3*time.Second)
		require.NoError(t, err)
		require.NoError(t, conn.SetDeadline(time.Now().Add(3*time.Second)))
		_, err = fmt.Fprintf(conn, "%s HTTP/1.1\r\nHost: example.com\r\nProxy-Authorization: %s\r\n%s\r\n", request.line, credential.Bearer.authorizationHeader(), request.header)
		require.NoError(t, err)
		response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "GET"})
		require.NoError(t, err)
		require.Equal(t, http.StatusBadRequest, response.StatusCode)
		_, err = io.Copy(io.Discard, response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		require.NoError(t, conn.Close())
	}
	page := h.adminSnapshot("GET", "/api/v2/http/traffic", nil)
	require.Equal(t, 200, page.StatusCode)
	require.NotContains(t, string(page.Body), "secret")
	credential.Bearer.assertAbsent(t, "HTTP rejection history", strings.NewReader(string(page.Body)))
	var traffic contract.HTTPTrafficPage
	require.NoError(t, json.Unmarshal(page.Body, &traffic))
	require.Len(t, traffic.Items, 3)
	for _, item := range traffic.Items {
		require.NotNil(t, item.Rejection)
		require.Equal(t, "gateway", item.ResponseSource)
	}
}
