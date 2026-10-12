//go:build e2e

package e2e

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGatewayRemoteWildcardServingAndLocalCLI(t *testing.T) {
	harness := newGatewayHarness(t)
	_, port, err := net.SplitHostPort(harness.authority)
	require.NoError(t, err)
	proxyAuthority := unusedAuthority(t)
	_, proxyPort, err := net.SplitHostPort(proxyAuthority)
	require.NoError(t, err)
	harness.serveArgs = []string{"serve", "--data-dir", harness.root, "--listen", "0.0.0.0:" + port, "--http-proxy-listen", "0.0.0.0:" + proxyPort, "--external-origin", "https://gateway.example:8443"}
	harness.Start()
	defer harness.Stop(syscall.SIGTERM)
	response := harness.Request("GET", "/readyz", "", nil)
	require.Equal(t, 200, response.StatusCode)
	readResponseBody(t, response)
	secret := filepath.Join(t.TempDir(), "operator")
	require.NoError(t, os.WriteFile(secret, []byte(harness.bearer+"\n"), 0600))
	result := runCLIAt(t, harness, secret, "http://"+harness.authority, "agent", "list", "--output", "json")
	require.Zero(t, result.ExitCode, string(result.Stderr))
	request, err := http.NewRequestWithContext(t.Context(), "GET", "http://"+harness.authority+"/", nil)
	require.NoError(t, err)
	request.Host = "gateway.example:8443"
	response, err = harness.client.Do(request)
	require.NoError(t, err)
	require.Equal(t, 200, response.StatusCode)
	readResponseBody(t, response)
	request, err = http.NewRequestWithContext(t.Context(), "GET", "http://"+proxyAuthority+"/", nil)
	require.NoError(t, err)
	response, err = harness.client.Do(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusProxyAuthRequired, response.StatusCode)
	readResponseBody(t, response)
}
