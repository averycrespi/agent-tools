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
	"runtime"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

var (
	gatewayBinaryPath string
	gatewayBuildRoot  string
)

func TestMain(testingMain *testing.M) {
	if os.Getenv(stdioFixtureEnvironment) == "1" {
		os.Exit(testingMain.Run())
	}
	if err := prepareGatewayBinary(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		_ = os.RemoveAll(gatewayBuildRoot)
		os.Exit(1)
	}
	exitCode := testingMain.Run()
	if err := os.RemoveAll(gatewayBuildRoot); err != nil && exitCode == 0 {
		_, _ = fmt.Fprintf(os.Stderr, "remove E2E build root: %v\n", err)
		exitCode = 1
	}
	os.Exit(exitCode)
}

type gatewayHarness struct {
	t                  *testing.T
	ctx                context.Context
	binary             string
	root               string
	authority          string
	bearer             string
	runner             *testutil.BinaryRunner
	client             *http.Client
	process            *testutil.RunningProcess
	initialization     testutil.ProcessResult
	initializationArgs []string
	serveArgs          []string
}

func newGatewayHarness(t *testing.T) *gatewayHarness {
	t.Helper()
	ctx := context.Background()
	runner, err := testutil.NewBinaryRunner(20*time.Second, 128*1024)
	require.NoError(t, err)
	harness := &gatewayHarness{
		t: t, ctx: ctx, binary: gatewayBinary(t), root: filepath.Join(t.TempDir(), "gateway"),
		authority: unusedAuthority(t), runner: runner, client: &http.Client{Timeout: 3 * time.Second},
	}
	secretPath := filepath.Join(t.TempDir(), "admin")
	harness.initializationArgs = []string{"initialize", "--data-dir", harness.root, "--secret-output", secretPath}
	harness.serveArgs = []string{"serve", "--data-dir", harness.root, "--listen", harness.authority}
	initialized, err := runner.Run(ctx, harness.binary, harness.initializationArgs...)
	require.NoError(t, err, "initialize: %s", initialized.Stderr)
	require.False(t, initialized.StdoutTruncated)
	require.False(t, initialized.StderrTruncated)
	harness.bearer = readBearer(t, secretPath)
	harness.initialization = initialized
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
	process, err := harness.runner.Start(harness.ctx, harness.binary, harness.serveArgs...)
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
		response := harness.adminSnapshot(http.MethodGet, "/api/v1/servers/"+serverID+"/operations/"+operationID, nil)
		if response.StatusCode == http.StatusOK {
			require.NoError(harness.t, json.Unmarshal(response.Body, &operation))
		}
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

func prepareGatewayBinary() error {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		return fmt.Errorf("locate E2E harness source")
	}
	moduleRoot := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	var err error
	gatewayBuildRoot, err = os.MkdirTemp("", "mcp-gateway-e2e-")
	if err != nil {
		return fmt.Errorf("create E2E build root: %w", err)
	}
	gatewayBinaryPath = filepath.Join(gatewayBuildRoot, "mcp-gateway")
	runner, err := testutil.NewBinaryRunner(30*time.Second, 64*1024)
	if err != nil {
		return fmt.Errorf("create E2E build runner: %w", err)
	}
	result, err := runner.Run(context.Background(), "go", "-C", moduleRoot, "build", "-tags=e2e", "-o", gatewayBinaryPath, "./cmd/mcp-gateway")
	if err != nil {
		return fmt.Errorf("build E2E Gateway: %w: %s", err, result.Stderr)
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return fmt.Errorf("build E2E Gateway output was truncated")
	}
	return nil
}

func gatewayBinary(t *testing.T) string {
	t.Helper()
	require.NotEmpty(t, gatewayBinaryPath)
	return gatewayBinaryPath
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
