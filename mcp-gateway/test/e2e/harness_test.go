//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

type gatewayHarness struct {
	t         *testing.T
	ctx       context.Context
	binary    string
	root      string
	authority string
	bearer    string
	runner    *testutil.BinaryRunner
	client    *http.Client
	process   *testutil.RunningProcess
}

func newGatewayHarness(t *testing.T) *gatewayHarness {
	t.Helper()
	ctx := context.Background()
	runner, err := testutil.NewBinaryRunner(20*time.Second, 128*1024)
	require.NoError(t, err)
	harness := &gatewayHarness{
		t: t, ctx: ctx, binary: buildGateway(t, ctx), root: filepath.Join(t.TempDir(), "gateway"),
		authority: unusedAuthority(t), runner: runner, client: &http.Client{Timeout: 3 * time.Second},
	}
	secretPath := filepath.Join(t.TempDir(), "admin")
	initialized, err := runner.Run(ctx, harness.binary, "initialize", "--data-dir", harness.root, "--secret-output", secretPath)
	require.NoError(t, err, "initialize: %s", initialized.Stderr)
	require.False(t, initialized.StdoutTruncated)
	require.False(t, initialized.StderrTruncated)
	harness.bearer = readBearer(t, secretPath)
	t.Cleanup(func() {
		if harness.process == nil {
			return
		}
		_ = harness.process.Signal(os.Kill)
		_, _ = harness.process.Wait()
		harness.process = nil
	})
	return harness
}

func (harness *gatewayHarness) Start() {
	harness.t.Helper()
	require.Nil(harness.t, harness.process)
	process, err := harness.runner.Start(harness.ctx, harness.binary, "serve", "--data-dir", harness.root, "--listen", harness.authority)
	require.NoError(harness.t, err)
	select {
	case <-process.StdoutReady():
	case <-time.After(5 * time.Second):
		_ = process.Signal(os.Kill)
		result, waitErr := process.Wait()
		harness.t.Fatalf("Gateway did not publish its startup result: wait=%v stderr=%s", waitErr, result.Stderr)
	}
	harness.process = process
}

func (harness *gatewayHarness) Stop(signal os.Signal) testutil.ProcessResult {
	harness.t.Helper()
	require.NotNil(harness.t, harness.process)
	require.NoError(harness.t, harness.process.Signal(signal))
	result, err := harness.process.Wait()
	harness.process = nil
	require.NoError(harness.t, err, "Gateway shutdown: %s", result.Stderr)
	require.False(harness.t, result.StdoutTruncated)
	require.False(harness.t, result.StderrTruncated)
	return result
}

func (harness *gatewayHarness) Request(method, path, body string, headers map[string]string) *http.Response {
	harness.t.Helper()
	request, err := http.NewRequestWithContext(harness.ctx, method, "http://"+harness.authority+path, bytes.NewBufferString(body))
	require.NoError(harness.t, err)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := harness.client.Do(request)
	require.NoError(harness.t, err)
	return response
}

func (harness *gatewayHarness) AdminJSON(method, path, body string, headers map[string]string, target any) *http.Response {
	harness.t.Helper()
	if headers == nil {
		headers = make(map[string]string)
	}
	headers["Authorization"] = "Bearer " + harness.bearer
	if body != "" {
		headers["Content-Type"] = contract.MediaTypeJSON
	}
	response := harness.Request(method, path, body, headers)
	if target != nil {
		require.NoError(harness.t, json.NewDecoder(response.Body).Decode(target))
	}
	return response
}

func (harness *gatewayHarness) OpenEvents() *http.Response {
	harness.t.Helper()
	return harness.AdminJSON(http.MethodGet, "/api/v1/events", "", nil, nil)
}

func (harness *gatewayHarness) WaitOperation(serverID, operationID string, state contract.ServerOperationState) contract.ServerOperation {
	harness.t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var operation contract.ServerOperation
	for {
		response := harness.AdminJSON(http.MethodGet, "/api/v1/servers/"+serverID+"/operations/"+operationID, "", nil, &operation)
		_ = response.Body.Close()
		if response.StatusCode == http.StatusOK && operation.State == state {
			return operation
		}
		if operation.State == contract.OperationSucceeded || operation.State == contract.OperationFailed || operation.State == contract.OperationCancelled || operation.State == contract.OperationSuperseded || operation.State == contract.OperationInterrupted {
			reason := contract.PublicReason("")
			if operation.Reason != nil {
				reason = *operation.Reason
			}
			harness.t.Fatalf("operation %s reached %s instead of %s; reason=%s", operationID, operation.State, state, reason)
		}
		select {
		case <-deadline.C:
			reason := contract.PublicReason("")
			if operation.Reason != nil {
				reason = *operation.Reason
			}
			harness.t.Fatalf("operation %s did not reach %s; observed state=%s reason=%s", operationID, state, operation.State, reason)
		case <-ticker.C:
		}
	}
}

func buildGateway(t *testing.T, ctx context.Context) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	moduleRoot := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	binary := filepath.Join(t.TempDir(), "mcp-gateway")
	runner, err := testutil.NewBinaryRunner(30*time.Second, 64*1024)
	require.NoError(t, err)
	result, err := runner.Run(ctx, "go", "-C", moduleRoot, "build", "-tags=e2e", "-o", binary, "./cmd/mcp-gateway")
	require.NoError(t, err, "build stderr: %s", result.Stderr)
	require.False(t, result.StdoutTruncated)
	require.False(t, result.StderrTruncated)
	return binary
}

func unusedAuthority(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	authority := listener.Addr().String()
	require.NoError(t, listener.Close())
	return authority
}

func readResponseBody(t *testing.T, response *http.Response) []byte {
	t.Helper()
	contents, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	return contents
}
