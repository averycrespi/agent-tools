//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAllowedHostnameAdministrationLifecycle(t *testing.T) {
	harness := newGatewayHarness(t)
	baseArgs := append([]string(nil), harness.serveArgs...)
	harness.serveArgs = append(harness.serveArgs, "--allowed-host", "HOST.LIMA.INTERNAL", "--allowed-host", "localhost")
	harness.Start()
	bearerPath := filepath.Join(t.TempDir(), "operator")
	require.NoError(t, os.WriteFile(bearerPath, []byte(harness.bearer+"\n"), 0o600))
	sandboxPath := filepath.Join(t.TempDir(), "sandbox")
	created := runOnlineCLI(t, harness, bearerPath, true, "admin", "credential", "create", "--secret-output", sandboxPath, "--output", "json")
	var credential contract.AdminCredential
	require.NoError(t, json.Unmarshal(created.Stdout, &credential))
	sandboxBearer := readBearer(t, sandboxPath)
	agent := harness.IssueCredential(harness.CreatePrincipal("Sandbox agent", contract.PrincipalVisibility("requestable")))

	// Controlled resolution models forwarding without trusting DNS or requiring a VM.
	forwardedPort := "18210"
	if strings.HasSuffix(harness.authority, ":"+forwardedPort) {
		forwardedPort = "18211"
	}
	request := func(host, bearer, path string, headers http.Header) controlclient.Response {
		t.Helper()
		client, err := controlclient.New("http://"+host+":"+forwardedPort, controlclient.TransportOptions{
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				assert.Equal(t, host+":"+forwardedPort, address)
				return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, harness.authority)
			},
		})
		require.NoError(t, err)
		if headers == nil {
			headers = make(http.Header)
		}
		if bearer != "" {
			headers.Set("Authorization", bearer)
		}
		response, err := client.Do(t.Context(), controlclient.Request{Method: http.MethodGet, Path: path, Header: headers})
		require.NoError(t, err)
		assert.Empty(t, response.Header.Get("Access-Control-Allow-Origin"))
		return response
	}
	status := func(host, bearer string, want int) {
		t.Helper()
		response := request(host, bearer, "/api/v1/system-status", nil)
		assert.Equal(t, want, response.StatusCode, "%s: %s", host, response.Body)
		assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	}
	status("Host.Lima.Internal", "Bearer "+sandboxBearer, 200)
	status("host.lima.internal", "", 401)
	status("host.lima.internal", "Bearer wrong", 401)
	status("host.lima.internal", agent.Bearer.authorizationHeader(), 403)
	status("unlisted.internal", "", 421)
	status("host.lima.internal.evil", "Bearer "+sandboxBearer, 421)
	assert.Equal(t, 403, request("host.lima.internal", "Bearer "+sandboxBearer, "/api/v1/system-status", http.Header{"Origin": {"http://host.lima.internal:" + forwardedPort}}).StatusCode)
	assert.Equal(t, 400, request("host.lima.internal", "Bearer "+sandboxBearer, "/api/v1/system-status", http.Header{"X-Forwarded-Host": {"host.lima.internal"}}).StatusCode)
	_, port, err := net.SplitHostPort(harness.authority)
	require.NoError(t, err)
	cli := runCLIAt(t, harness, sandboxPath, "http://localhost:"+port, "status", "--output", "json")
	require.Zero(t, cli.ExitCode, "%s", cli.Stderr)
	assert.NotContains(t, string(cli.Stdout)+string(cli.Stderr), sandboxBearer)

	harness.Stop(syscall.SIGTERM)
	harness.serveArgs = baseArgs
	harness.Start()
	status("host.lima.internal", "Bearer "+sandboxBearer, 421)
	runOnlineCLI(t, harness, sandboxPath, true, "status", "--output", "json")
	harness.Stop(syscall.SIGTERM)
	harness.serveArgs = append(append([]string(nil), baseArgs...), "--allowed-host", "host.lima.internal")
	harness.Start()
	status("host.lima.internal", "Bearer "+sandboxBearer, 200)
	runOnlineCLI(t, harness, bearerPath, true, "admin", "credential", "revoke", credential.ID, "--yes", "--output", "json")
	status("host.lima.internal", "Bearer "+sandboxBearer, 401)
	status("host.lima.internal", "Bearer "+harness.bearer, 200)
	runOnlineCLI(t, harness, bearerPath, true, "status", "--output", "json")
	harness.Stop(syscall.SIGTERM)
}
