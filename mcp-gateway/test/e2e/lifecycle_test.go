//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServeSignalDrainRestartAndForcedExit(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	ready := harness.Request(http.MethodGet, "/readyz", "", nil)
	assert.Equal(t, http.StatusOK, ready.StatusCode)
	_ = ready.Body.Close()
	session := harness.Request(http.MethodPost, "/api/v1/admin-sessions", `{}`, map[string]string{
		"Authorization": "Bearer " + harness.bearer, "Content-Type": contract.MediaTypeJSON,
	})
	require.Equal(t, http.StatusCreated, session.StatusCode)
	cookies := session.Cookies()
	require.Len(t, cookies, 1)
	_ = session.Body.Close()
	result := harness.Stop(syscall.SIGTERM)
	assert.Equal(t, 0, result.ExitCode)

	harness.Start()
	stale := harness.Request(http.MethodGet, "/api/v1/system-status", "", map[string]string{
		"Cookie": cookies[0].String(), "Origin": "http://" + harness.authority,
	})
	assert.Equal(t, http.StatusUnauthorized, stale.StatusCode)
	_ = stale.Body.Close()
	harness.Stop(os.Interrupt)

	harness.Start()
	blocked, err := net.Dial("tcp", harness.authority)
	require.NoError(t, err)
	defer func() { _ = blocked.Close() }()
	_, err = fmt.Fprintf(blocked, "POST /api/v1/backups HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nIdempotency-Key: blocked\r\nContent-Length: 100\r\n\r\n", harness.authority, harness.bearer)
	require.NoError(t, err)
	waitForAdminOccupancy(t, harness, 2)
	require.NoError(t, harness.process.Signal(syscall.SIGTERM))
	waitForListenerClose(t, harness.authority)
	require.NoError(t, harness.process.Signal(syscall.SIGTERM))
	result, err = harness.process.Wait()
	harness.process = nil
	require.Error(t, err)
	assert.Equal(t, 2, result.ExitCode)

	harness.Start()
	verified := harness.Request(http.MethodGet, "/readyz", "", nil)
	assert.Equal(t, http.StatusOK, verified.StatusCode)
	_ = verified.Body.Close()
	harness.Stop(syscall.SIGTERM)
}

func TestEnabledServerFailureDoesNotRedefineReadiness(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	defer harness.Stop(syscall.SIGTERM)

	created := harness.AdminJSON(http.MethodPost, "/api/v1/servers", `{"namespace":"failing","display_name":"Failing","enabled":true,"transport":{"kind":"stdio","executable":"/bin/true","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{}}}`, map[string]string{"Idempotency-Key": "failing-server"}, nil)
	require.Equal(t, http.StatusCreated, created.StatusCode)
	var creation struct {
		Server struct {
			ID string `json:"id"`
		} `json:"server"`
	}
	require.NoError(t, json.NewDecoder(created.Body).Decode(&creation))
	_ = created.Body.Close()

	var observedState contract.RuntimeState
	var observedReason *contract.PublicReason
	require.Eventually(t, func() bool {
		response := harness.AdminJSON(http.MethodGet, "/api/v1/servers/"+creation.Server.ID, "", nil, nil)
		defer func() { _ = response.Body.Close() }()
		var server struct {
			Runtime struct {
				State  contract.RuntimeState  `json:"state"`
				Reason *contract.PublicReason `json:"reason"`
			} `json:"runtime"`
		}
		if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&server) != nil {
			return false
		}
		observedState, observedReason = server.Runtime.State, server.Runtime.Reason
		return observedState == contract.RuntimeDegraded && observedReason != nil
	}, 3*time.Second, 10*time.Millisecond)
	assert.Equal(t, contract.ReasonStopUnconfirmed, *observedReason)
	ready := harness.Request(http.MethodGet, "/readyz", "", nil)
	assert.Equal(t, http.StatusOK, ready.StatusCode)
	_ = ready.Body.Close()
	statusResponse := harness.AdminJSON(http.MethodGet, "/api/v1/system-status", "", nil, nil)
	require.Equal(t, http.StatusOK, statusResponse.StatusCode)
	var status contract.SystemStatus
	require.NoError(t, json.NewDecoder(statusResponse.Body).Decode(&status))
	_ = statusResponse.Body.Close()
	assert.Equal(t, int64(1), status.Limits.ServerIdentities.InUse)
	assert.Equal(t, int64(1), status.Limits.Servers.InUse)
	assert.Equal(t, int64(1), status.Limits.S2IdempotencyRecords.InUse)
	assert.Zero(t, status.Limits.DownstreamRuntimes.InUse)
}

func waitForAdminOccupancy(t *testing.T, harness *gatewayHarness, minimum int64) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("blocked request did not occupy admin capacity")
		default:
		}
		response := harness.AdminJSON(http.MethodGet, "/api/v1/system-status", "", nil, nil)
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
