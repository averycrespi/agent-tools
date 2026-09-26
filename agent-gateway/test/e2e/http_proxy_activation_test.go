//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

func httpMaterialBinary(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	material := filepath.Join(root, "material")
	require.NoError(t, os.Mkdir(material, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(material, ".fixture"), []byte("agent-gateway-disposable-e2e-material\n"), 0o600))
	binary := filepath.Join(root, "agent-gateway")
	runner, err := testutil.NewBinaryRunner(30*time.Second, 64*1024)
	require.NoError(t, err)
	flags := "-X github.com/averycrespi/agent-tools/agent-gateway/internal/composition.e2eMaterialDirectory=" + material
	result, err := runner.Run(t.Context(), "go", "-C", "../..", "build", "-tags=e2e", "-ldflags", flags, "-o", binary, "./cmd/agent-gateway")
	require.NoError(t, err, "fixture build: %s", result.Stderr)
	return binary, material
}

func createHTTPCA(t *testing.T, h *gatewayHarness) []byte {
	t.Helper()
	h.Start()
	var status contract.SystemStatus
	response := h.AdminJSON("GET", "/api/v2/system-status", "", nil, &status)
	require.Equal(t, 200, response.StatusCode)
	require.NoError(t, response.Body.Close())
	h.Stop(syscall.SIGTERM)
	// Init used process-local material; deliberately select material owned by
	// this test's link-time persistent fixture before enabling interception.
	_, err := h.runner.Run(h.ctx, h.binary, "http", "ca", "replace", "--data-dir", h.root, "--confirm")
	require.NoError(t, err)
	exported, err := h.runner.Run(h.ctx, h.binary, "http", "ca", "export", "--data-dir", h.root, "--stdout")
	require.NoError(t, err)
	return exported.Stdout
}

func TestHTTPProxyProductionActivation(t *testing.T) {
	h := newGatewayHarness(t)
	h.binary, _ = httpMaterialBinary(t)
	certificate := createHTTPCA(t, h)
	require.NotEmpty(t, certificate)
	proxyAuthority := unusedAuthority(t)
	h.serveArgs = append(h.serveArgs, "--http-proxy-listen", proxyAuthority)
	h.Start()
	defer func() {
		if h.process != nil {
			h.Stop(syscall.SIGTERM)
		}
	}()
	p := h.CreatePrincipal("HTTP production", contract.VisibilityRequestable)
	credential := h.IssueCredential(p)
	credentialText := strings.TrimPrefix(credential.Bearer.authorizationHeader(), "Bearer ")
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Empty(t, r.Header.Get("Proxy-Authorization"))
		calls.Add(1)
		_, _ = io.WriteString(w, "fixture response")
	}))
	defer upstream.Close()
	request := func(token, target string) int {
		proxy := &url.URL{Scheme: "http", Host: proxyAuthority, User: url.UserPassword("agent", token)}
		transport := &http.Transport{Proxy: http.ProxyURL(proxy), DisableKeepAlives: true}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
		req, err := http.NewRequestWithContext(h.ctx, "GET", target, nil)
		require.NoError(t, err)
		response, err := client.Do(req)
		require.NoError(t, err)
		_, err = io.Copy(io.Discard, response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		if response.StatusCode == 407 {
			require.Equal(t, `Basic realm="Agent Gateway"`, response.Header.Get("Proxy-Authenticate"))
		}
		return response.StatusCode
	}
	require.Equal(t, 407, request(h.bearer, upstream.URL))
	require.Equal(t, 403, request(credentialText, upstream.URL))
	require.Zero(t, calls.Load())
	defaultPath := "/api/v2/principals/" + p.Resource.ID
	defaultRead := h.adminSnapshot("GET", defaultPath, nil)
	require.Equal(t, 200, defaultRead.StatusCode)
	require.Contains(t, string(defaultRead.Body), `"http_default":"block"`)
	defaultAllow := h.adminSnapshotWithHeaders("PATCH", defaultPath, []byte(`{"http_default":"allow"}`), map[string]string{"If-Match": defaultRead.Header.Get("ETag")})
	require.Equal(t, 200, defaultAllow.StatusCode)
	require.Equal(t, 403, request(credentialText, upstream.URL), "default allow cannot grant private-network access")
	address := netip.MustParseAddrPort(upstream.Listener.Addr().String())
	body := fmt.Sprintf(`{"principal_id":%q,"description":null,"expires_at":null,"policy":{"version":1,"type":"allow_requests","allow_private":true,"request":{"origin":{"scheme":"http","host":%q,"port":%d},"methods":{"any":true},"path":{"kind":"any"}}}}`, p.Resource.ID, address.Addr().String(), address.Port())
	grant := h.adminSnapshot("POST", "/api/v2/http/grants", []byte(body))
	require.Equal(t, 201, grant.StatusCode, "grant failed: %s", grant.Body)
	require.Equal(t, 200, request(credentialText, upstream.URL+"/private-path?omitted=yes"))
	require.EqualValues(t, 1, calls.Load())
	for _, target := range []string{"http://" + h.authority + "/api/v2/system-status", "http://" + proxyAuthority + "/"} {
		require.Equal(t, 403, request(credentialText, target))
	}
	status := h.adminSnapshot("GET", "/api/v2/system-status", nil)
	var parsed contract.SystemStatus
	require.NoError(t, json.Unmarshal(status.Body, &parsed))
	require.NotNil(t, parsed.HTTPProxy)
	require.True(t, parsed.HTTPProxy.Ready)
	require.True(t, parsed.HTTPProxy.CAReady)
	require.Equal(t, proxyAuthority, parsed.HTTPProxy.Authority)
	require.NotNil(t, parsed.Traffic)
	require.True(t, parsed.Traffic.Ready)
	mcp := h.ModernList(credential.Bearer, json.RawMessage(`"mixed"`), "")
	require.Equal(t, 200, mcp.StatusCode)
	replacement := h.IssueCredential(h.GetPrincipal(p.Resource.ID))
	replacementText := strings.TrimPrefix(replacement.Bearer.authorizationHeader(), "Bearer ")
	require.Equal(t, 407, request(credentialText, upstream.URL))
	require.Equal(t, 200, request(replacementText, upstream.URL))
	h.Restart()
	require.Equal(t, 200, request(replacementText, upstream.URL))
	h.RevokeCredential(replacement.Principal)
	require.Equal(t, 407, request(replacementText, upstream.URL))
	history := h.adminSnapshot("GET", "/api/v2/http/traffic", nil)
	require.Equal(t, 200, history.StatusCode)
	require.NotContains(t, string(history.Body), "private-path")
	require.NotContains(t, string(history.Body), "omitted=yes")
	credential.Bearer.assertAbsent(t, "HTTP history", strings.NewReader(string(history.Body)))
	h.Stop(syscall.SIGTERM)
	for _, authority := range []string{h.authority, proxyAuthority} {
		listener, err := net.Listen("tcp4", authority)
		require.NoError(t, err)
		require.NoError(t, listener.Close())
	}
}

func TestHTTPProxyStartupFailureCleansPartialBinds(t *testing.T) {
	h := newGatewayHarness(t)
	proxy := unusedAuthority(t)
	args := append(append([]string(nil), h.serveArgs...), "--http-proxy-listen", proxy)
	result, err := h.runner.Run(context.Background(), h.binary, args...)
	require.Error(t, err)
	require.Empty(t, result.Stdout)
	for _, authority := range []string{h.authority, proxy} {
		listener, err := net.Listen("tcp4", authority)
		require.NoError(t, err)
		require.NoError(t, listener.Close())
	}
	// Missing optional material never prevents an explicitly MCP-only process.
	h.Start()
	h.Stop(syscall.SIGTERM)
	occupied, err := net.Listen("tcp4", proxy)
	require.NoError(t, err)
	defer occupied.Close()
	result, err = h.runner.Run(context.Background(), h.binary, args...)
	require.Error(t, err)
	require.Empty(t, result.Stdout)
	listener, err := net.Listen("tcp4", h.authority)
	require.NoError(t, err)
	require.NoError(t, listener.Close())
}
