//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServeSignalDrainRestartAndForcedExit(t *testing.T) {
	ctx := context.Background()
	binary := buildGateway(t, ctx)
	root := filepath.Join(t.TempDir(), "gateway")
	secretPath := filepath.Join(t.TempDir(), "admin")
	runner, err := testutil.NewBinaryRunner(15*time.Second, 64*1024)
	require.NoError(t, err)
	initialized, err := runner.Run(ctx, binary, "initialize", "--data-dir", root, "--secret-output", secretPath)
	require.NoError(t, err, "initialize: %s", initialized.Stderr)
	bearer := readBearer(t, secretPath)
	authority := unusedAuthority(t)
	client := &http.Client{Timeout: 2 * time.Second}

	process := startGateway(t, runner, ctx, binary, root, authority)
	ready := requestGateway(t, client, http.MethodGet, authority, "/readyz", "", nil)
	assert.Equal(t, http.StatusOK, ready.StatusCode)
	_ = ready.Body.Close()
	session := requestGateway(t, client, http.MethodPost, authority, "/api/v1/admin-sessions", `{}`, map[string]string{
		"Authorization": "Bearer " + bearer, "Content-Type": contract.MediaTypeJSON,
	})
	require.Equal(t, http.StatusCreated, session.StatusCode)
	cookies := session.Cookies()
	require.Len(t, cookies, 1)
	_ = session.Body.Close()
	require.NoError(t, process.Signal(syscall.SIGTERM))
	result, err := process.Wait()
	require.NoError(t, err, "graceful stderr: %s", result.Stderr)
	assert.Equal(t, 0, result.ExitCode)

	process = startGateway(t, runner, ctx, binary, root, authority)
	stale := requestGateway(t, client, http.MethodGet, authority, "/api/v1/system-status", "", map[string]string{
		"Cookie": cookies[0].String(), "Origin": "http://" + authority,
	})
	assert.Equal(t, http.StatusUnauthorized, stale.StatusCode)
	_ = stale.Body.Close()
	require.NoError(t, process.Signal(os.Interrupt))
	result, err = process.Wait()
	require.NoError(t, err, "restart shutdown: %s", result.Stderr)

	process = startGateway(t, runner, ctx, binary, root, authority)
	blocked, err := net.Dial("tcp", authority)
	require.NoError(t, err)
	defer func() { _ = blocked.Close() }()
	_, err = fmt.Fprintf(blocked, "POST /api/v1/backups HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nIdempotency-Key: blocked\r\nContent-Length: 100\r\n\r\n", authority, bearer)
	require.NoError(t, err)
	waitForAdminOccupancy(t, client, authority, bearer, 2)
	require.NoError(t, process.Signal(syscall.SIGTERM))
	waitForListenerClose(t, authority)
	require.NoError(t, process.Signal(syscall.SIGTERM))
	result, err = process.Wait()
	require.Error(t, err)
	assert.Equal(t, 2, result.ExitCode)

	process = startGateway(t, runner, ctx, binary, root, authority)
	verified := requestGateway(t, client, http.MethodGet, authority, "/readyz", "", nil)
	assert.Equal(t, http.StatusOK, verified.StatusCode)
	_ = verified.Body.Close()
	require.NoError(t, process.Signal(syscall.SIGTERM))
	result, err = process.Wait()
	require.NoError(t, err, "post-force restart: %s", result.Stderr)
}

func TestEnabledServerFailureDoesNotRedefineReadiness(t *testing.T) {
	ctx := context.Background()
	binary := buildGateway(t, ctx)
	root := filepath.Join(t.TempDir(), "gateway")
	secretPath := filepath.Join(t.TempDir(), "admin")
	runner, err := testutil.NewBinaryRunner(15*time.Second, 64*1024)
	require.NoError(t, err)
	initialized, err := runner.Run(ctx, binary, "initialize", "--data-dir", root, "--secret-output", secretPath)
	require.NoError(t, err, "initialize: %s", initialized.Stderr)
	bearer := readBearer(t, secretPath)
	authority := unusedAuthority(t)
	client := &http.Client{Timeout: 2 * time.Second}
	process := startGateway(t, runner, ctx, binary, root, authority)

	created := requestGateway(t, client, http.MethodPost, authority, "/api/v1/servers", `{"namespace":"failing","display_name":"Failing","enabled":true,"transport":{"kind":"stdio","executable":"/bin/true","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{}}}`, map[string]string{"Authorization": "Bearer " + bearer, "Content-Type": contract.MediaTypeJSON, "Idempotency-Key": "failing-server"})
	require.Equal(t, http.StatusCreated, created.StatusCode)
	var creation struct {
		Server struct {
			ID string `json:"id"`
		} `json:"server"`
	}
	require.NoError(t, json.NewDecoder(created.Body).Decode(&creation))
	_ = created.Body.Close()

	require.Eventually(t, func() bool {
		response := requestGateway(t, client, http.MethodGet, authority, "/api/v1/servers/"+creation.Server.ID, "", map[string]string{"Authorization": "Bearer " + bearer})
		defer func() { _ = response.Body.Close() }()
		var server struct {
			Runtime struct {
				State  contract.RuntimeState  `json:"state"`
				Reason *contract.PublicReason `json:"reason"`
			} `json:"runtime"`
		}
		return response.StatusCode == http.StatusOK && json.NewDecoder(response.Body).Decode(&server) == nil && server.Runtime.State == contract.RuntimeDegraded && server.Runtime.Reason != nil && *server.Runtime.Reason == contract.ReasonProtocolUnsupported
	}, 3*time.Second, 10*time.Millisecond)
	ready := requestGateway(t, client, http.MethodGet, authority, "/readyz", "", nil)
	assert.Equal(t, http.StatusOK, ready.StatusCode)
	_ = ready.Body.Close()
	statusResponse := requestGateway(t, client, http.MethodGet, authority, "/api/v1/system-status", "", map[string]string{"Authorization": "Bearer " + bearer})
	require.Equal(t, http.StatusOK, statusResponse.StatusCode)
	var status contract.SystemStatus
	require.NoError(t, json.NewDecoder(statusResponse.Body).Decode(&status))
	_ = statusResponse.Body.Close()
	assert.Equal(t, int64(1), status.Limits.ServerIdentities.InUse)
	assert.Equal(t, int64(1), status.Limits.Servers.InUse)
	assert.Equal(t, int64(1), status.Limits.S2IdempotencyRecords.InUse)
	assert.Zero(t, status.Limits.DownstreamRuntimes.InUse)

	require.NoError(t, process.Signal(syscall.SIGTERM))
	result, err := process.Wait()
	require.NoError(t, err, "shutdown stderr: %s", result.Stderr)
}

func startGateway(t *testing.T, runner *testutil.BinaryRunner, ctx context.Context, binary, root, authority string) *testutil.RunningProcess {
	t.Helper()
	process, err := runner.Start(ctx, binary, "serve", "--data-dir", root, "--listen", authority)
	require.NoError(t, err)
	select {
	case <-process.StdoutReady():
	case <-time.After(5 * time.Second):
		t.Fatal("Gateway did not publish its startup result")
	}
	return process
}

func requestGateway(t *testing.T, client *http.Client, method, authority, path, body string, headers map[string]string) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), method, "http://"+authority+path, bytes.NewBufferString(body))
	require.NoError(t, err)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := client.Do(request)
	require.NoError(t, err)
	return response
}

func waitForAdminOccupancy(t *testing.T, client *http.Client, authority, bearer string, minimum int64) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("blocked request did not occupy admin capacity")
		default:
		}
		response := requestGateway(t, client, http.MethodGet, authority, "/api/v1/system-status", "", map[string]string{"Authorization": "Bearer " + bearer})
		contents, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		require.NoError(t, err)
		var status contract.SystemStatus
		if response.StatusCode == http.StatusOK && json.Unmarshal(contents, &status) == nil && status.Limits.HTTPAdmin.InUse >= minimum {
			return
		}
	}
}

func waitForListenerClose(t *testing.T, authority string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		connection, err := net.DialTimeout("tcp", authority, 10*time.Millisecond)
		if err != nil {
			return
		}
		_ = connection.Close()
		select {
		case <-deadline:
			t.Fatal("Gateway listener remained open during drain")
		default:
		}
	}
}

func unusedAuthority(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	authority := listener.Addr().String()
	require.NoError(t, listener.Close())
	return authority
}
