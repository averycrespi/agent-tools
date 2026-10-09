//go:build e2e && browser

package e2e

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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
	var ca []byte
	if scenario == "http-traffic" {
		harness.binary = gatewayBinary(t)
		ca = createHTTPCA(t, harness)
		proxy = unusedAuthority(t)
		harness.serveArgs = append(harness.serveArgs, "--clear-http-proxy-listen=false", "--http-proxy-listen", proxy)
	}
	harness.Start()
	if proxy != "" {
		seedBrowserHTTPSearch(t, harness, proxy)
		seedBrowserHTTPRejections(t, harness, proxy)
		seedBrowserHTTPConnect(t, harness, proxy, ca)
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

// Search matches sit outside the initial 50-row page. Every row uses real proxy
// admission with default denial, so no external host is contacted.
func seedBrowserHTTPSearch(t *testing.T, h *gatewayHarness, proxy string) {
	t.Helper()
	for _, fixture := range []struct {
		name, host string
		count      int
	}{
		{"Café Investigator", "api.github.com", 2},
		{"Unrelated fixture", "example.com", 52},
	} {
		principal := h.CreatePrincipal(fixture.name, contract.VisibilityRequestable)
		credential := h.IssueCredential(principal)
		proxyURL := &url.URL{Scheme: "http", Host: proxy, User: url.UserPassword("agent", strings.TrimPrefix(credential.Bearer.authorizationHeader(), "Bearer "))}
		transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
		client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
		for range fixture.count {
			response, err := client.Get("http://" + fixture.host + "/not-retained")
			require.NoError(t, err)
			require.Equal(t, http.StatusForbidden, response.StatusCode)
			_, err = io.Copy(io.Discard, response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
		}
		transport.CloseIdleConnections()
	}
}

// The inner request is denied by the default policy; no upstream fixture or
// external service is contacted. Both rows and their link are real evidence.
func seedBrowserHTTPConnect(t *testing.T, h *gatewayHarness, proxy string, ca []byte) {
	t.Helper()
	principal := h.CreatePrincipal("CONNECT evidence fixture", contract.VisibilityRequestable)
	credential := h.IssueCredential(principal)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(ca))
	proxyURL := &url.URL{Scheme: "http", Host: proxy, User: url.UserPassword("agent", strings.TrimPrefix(credential.Bearer.authorizationHeader(), "Bearer "))}
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}} //nolint:gosec // Supported TLS 1.2 with fixture CA trust.
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	response, err := client.Get("https://" + unusedAuthority(t) + "/inner")
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, response.StatusCode)
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
}

func seedBrowserHTTPRejections(t *testing.T, h *gatewayHarness, proxy string) {
	t.Helper()
	principal := h.CreatePrincipal("HTTP diagnostics fixture", contract.VisibilityRequestable)
	credential := h.IssueCredential(principal)
	for _, request := range []struct{ line, header string }{
		{"GET http://example.com/path-secret?query-secret", "Upgrade: private-upgrade\r\n"},
		{"GET /path-secret?query-secret", ""},
		{"GET http://example.com/%5cpath-secret?query-secret", ""},
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
	var traffic contract.HTTPTrafficPage
	// Traffic persistence is asynchronous; an immediate unfiltered read can
	// return the earlier search fixtures rather than these rejection records.
	require.Eventually(t, func() bool {
		page := h.adminSnapshot("GET", "/api/v2/http/traffic?principal_id="+principal.Resource.ID, nil)
		require.Equal(t, 200, page.StatusCode)
		require.NotContains(t, string(page.Body), "secret")
		credential.Bearer.assertAbsent(t, "HTTP rejection history", strings.NewReader(string(page.Body)))
		require.NoError(t, json.Unmarshal(page.Body, &traffic))
		return len(traffic.Items) == 3
	}, 3*time.Second, 10*time.Millisecond)
	require.Len(t, traffic.Items, 3)
	rejections := make([]contract.HTTPRejection, 0, 3)
	for _, item := range traffic.Items {
		require.Equal(t, principal.Resource.ID, item.PrincipalID)
		require.NotNil(t, item.Rejection)
		require.Equal(t, "gateway", item.ResponseSource)
		rejections = append(rejections, *item.Rejection)
	}
	require.ElementsMatch(t, []contract.HTTPRejection{
		{Stage: "headers", Reason: "upgrade_unsupported"},
		{Stage: "request_form", Reason: "absolute_http_required"},
		{Stage: "target", Reason: "forbidden_path"},
	}, rejections)
}
