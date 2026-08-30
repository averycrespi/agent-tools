//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLIAuthFlows(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	bearerPath := filepath.Join(t.TempDir(), "admin-bearer")
	require.NoError(t, os.WriteFile(bearerPath, []byte(harness.bearer+"\n"), 0o600))
	serverID := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	flowID := "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	flow := contract.ServerAuthFlow{ID: flowID, ServerID: serverID, FlowState: contract.AuthFlowAwaitingCallback, TargetDesiredRevision: "1", RegistrationRevision: "1", CreatedAt: "2026-08-29T00:00:00Z", ExpiresAt: "2026-08-29T00:05:00Z"}
	var flowState atomic.Value
	flowState.Store(contract.AuthFlowAwaitingCallback)
	var authVisits atomic.Int64
	authorization := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authVisits.Add(1)
		assert.Empty(t, request.Header.Get("Referer"))
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer authorization.Close()
	var starts, cancels atomic.Int64
	control := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", contract.MediaTypeJSON)
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/servers/"+serverID+"/auth-flows":
			current := flow
			current.FlowState = flowState.Load().(contract.AuthFlowState)
			_ = json.NewEncoder(writer).Encode(contract.Collection[contract.ServerAuthFlow]{Items: []contract.ServerAuthFlow{current}})
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/servers/"+serverID+"/auth-flows/"+flowID:
			current := flow
			current.FlowState = flowState.Load().(contract.AuthFlowState)
			_ = json.NewEncoder(writer).Encode(current)
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/servers/"+serverID+"/auth-flows":
			starts.Add(1)
			assert.Equal(t, `"server-`+serverID+`-1"`, request.Header.Get("If-Match"))
			assert.Empty(t, request.Header.Get("Idempotency-Key"))
			writer.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(writer).Encode(contract.AuthFlowCreation{Flow: flow, AuthorizationURL: authorization.URL + "/authorize?state=auth-flow-canary"})
		case request.Method == http.MethodDelete && request.URL.Path == "/api/v1/servers/"+serverID+"/auth-flows/"+flowID:
			cancels.Add(1)
			writer.Header().Del("Content-Type")
			writer.WriteHeader(http.StatusNoContent)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer control.Close()
	results := make([]testutil.ProcessResult, 0, 6)

	listed := runCLIAt(t, harness, bearerPath, control.URL, "server", "auth-flow", "list", serverID, "--limit", "10", "--output", "json")
	results = append(results, listed)
	require.Equal(t, 0, listed.ExitCode)
	assert.Contains(t, string(listed.Stdout), flowID)
	got := runCLIAt(t, harness, bearerPath, control.URL, "server", "auth-flow", "get", serverID, flowID)
	results = append(results, got)
	require.Equal(t, 0, got.ExitCode)
	assert.Contains(t, string(got.Stdout), "awaiting_callback")

	jsonRefused := runCLIAt(t, harness, bearerPath, control.URL, "server", "auth-flow", "start", serverID, "--etag", `"server-`+serverID+`-1"`, "--output", "json")
	results = append(results, jsonRefused)
	assert.Equal(t, 2, jsonRefused.ExitCode)
	redirectedRefused := runCLIAt(t, harness, bearerPath, control.URL, "server", "auth-flow", "start", serverID, "--etag", `"server-`+serverID+`-1"`)
	results = append(results, redirectedRefused)
	assert.Equal(t, 2, redirectedRefused.ExitCode)
	assert.Zero(t, starts.Load())

	ptyResult := runAuthFlowStartPTY(t, harness, bearerPath, control.URL, authorization.URL+"/authorize?state=auth-flow-canary", serverID, 0, true, true, false)
	assert.Equal(t, 0, ptyResult.ExitCode, string(ptyResult.Stderr))
	assert.Equal(t, int64(1), starts.Load())
	assert.Equal(t, int64(1), authVisits.Load())
	assert.NotContains(t, string(ptyResult.Stdout), "auth-flow-canary")
	assert.NotContains(t, string(ptyResult.Stderr), "auth-flow-canary")

	jsonPTY := runAuthFlowStartPTY(t, harness, bearerPath, control.URL, authorization.URL+"/authorize?state=auth-flow-canary", serverID, 0, true, false, true)
	assert.Equal(t, 0, jsonPTY.ExitCode, string(jsonPTY.Stderr))
	assert.Equal(t, int64(2), starts.Load())
	var jsonFlow contract.ServerAuthFlow
	require.NoError(t, json.Unmarshal(jsonPTY.Stdout, &jsonFlow))
	assert.Equal(t, flowID, jsonFlow.ID)
	assert.NotContains(t, string(jsonPTY.Stdout), "authorization_url")
	assert.NotContains(t, string(jsonPTY.Stdout), "auth-flow-canary")
	assert.NotContains(t, string(jsonPTY.Stderr), "auth-flow-canary")

	redirected := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, authorization.URL+"/authorize?state=redirect-canary", http.StatusFound)
	}))
	defer redirected.Close()
	redirectResult := runAuthFlowStartPTY(t, harness, bearerPath, redirected.URL, authorization.URL+"/authorize?state=redirect-canary", serverID, 10, false, false, false)
	assert.Equal(t, 0, redirectResult.ExitCode, string(redirectResult.Stderr))
	assert.NotContains(t, string(redirectResult.Stdout), "redirect-canary")
	assert.NotContains(t, string(redirectResult.Stderr), "redirect-canary")

	flowState.Store(contract.AuthFlowExchanging)
	ineligible := runCLIAt(t, harness, bearerPath, control.URL, "server", "auth-flow", "cancel", serverID, flowID, "--yes", "--output", "json")
	results = append(results, ineligible)
	assert.Equal(t, 2, ineligible.ExitCode)
	assert.Zero(t, cancels.Load())
	flowState.Store(contract.AuthFlowAwaitingCallback)
	cancelled := runCLIAt(t, harness, bearerPath, control.URL, "server", "auth-flow", "cancel", serverID, flowID, "--yes", "--output", "json")
	results = append(results, cancelled)
	assert.Equal(t, 0, cancelled.ExitCode)
	assert.JSONEq(t, `{}`, string(cancelled.Stdout))
	assert.Equal(t, int64(1), cancels.Load())

	harness.Stop(syscall.SIGTERM)
	results = append(results, ptyResult, jsonPTY, redirectResult)
	for _, result := range results {
		if result.ExitCode != 0 {
			assert.Empty(t, result.Stdout)
		}
		assert.NotContains(t, string(result.Stdout), "auth-flow-canary")
		assert.NotContains(t, string(result.Stderr), "auth-flow-canary")
		assert.NotContains(t, string(result.Stdout), harness.bearer)
		assert.NotContains(t, string(result.Stderr), harness.bearer)
		assert.True(t, result.Cleanup.Reaped)
		assert.False(t, result.Cleanup.Survived)
	}
	assert.Len(t, harness.results, 1, "T43 must own one Gateway lifecycle")
}

func runAuthFlowStartPTY(t *testing.T, harness *gatewayHarness, bearerPath, address, expectedURL, serverID string, expectedChildExit int, expectTerminal, openURL, jsonOutput bool) testutil.ProcessResult {
	t.Helper()
	return runAuthFlowStartPTYCommand(t, harness, bearerPath, address, expectedURL, serverID, `"server-`+serverID+`-1"`, expectedChildExit, expectTerminal, openURL, jsonOutput)
}

func runAuthFlowStartPTYWithETag(t *testing.T, harness *gatewayHarness, bearerPath, address, expectedURL, serverID, etag string) testutil.ProcessResult {
	t.Helper()
	return runAuthFlowStartPTYCommand(t, harness, bearerPath, address, expectedURL, serverID, etag, 0, true, true, false)
}

func runAuthFlowStartPTYCommand(t *testing.T, harness *gatewayHarness, bearerPath, address, expectedURL, serverID, etag string, expectedChildExit int, expectTerminal, openURL, jsonOutput bool) testutil.ProcessResult {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("PTY acceptance requires a Unix controlling terminal")
	}
	dir := t.TempDir()
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, opener), []byte("#!/bin/sh\nexec curl -fsS \"$1\" >/dev/null\n"), 0o700))
	wrapper := filepath.Join(dir, "tty_runner.py")
	script := `import os, pty, sys
opener, expected, expected_exit, expect_terminal, out, err, binary, *args = sys.argv[1:]
pid, master = pty.fork()
if pid == 0:
 os.environ["PATH"] = opener + os.pathsep + os.environ.get("PATH", "")
 os.dup2(os.open(out, os.O_WRONLY|os.O_CREAT|os.O_TRUNC, 0o600), 1)
 os.dup2(os.open(err, os.O_WRONLY|os.O_CREAT|os.O_TRUNC, 0o600), 2)
 os.execv(binary, [binary] + args)
data = b""
while True:
 try:
  chunk = os.read(master, 4096)
  if not chunk: break
  data += chunk
 except OSError: break
_, status = os.waitpid(pid, 0)
stdout = open(out, "rb").read(); stderr = open(err, "rb").read(); needle = expected.encode()
if os.waitstatus_to_exitcode(status) != int(expected_exit) or ((needle in data) != (expect_terminal == "yes")) or needle in stdout or needle in stderr:
 sys.stderr.write("auth-flow PTY publication failed\n"); sys.exit(1)
print("auth_flow_terminal_open_ok")
`
	require.NoError(t, os.WriteFile(wrapper, []byte(script), 0o600))
	stdoutPath, stderrPath := filepath.Join(dir, "stdout"), filepath.Join(dir, "stderr")
	expectTerminalValue := "no"
	if expectTerminal {
		expectTerminalValue = "yes"
	}
	args := []string{wrapper, dir, expectedURL, fmt.Sprintf("%d", expectedChildExit), expectTerminalValue, stdoutPath, stderrPath, harness.binary, "server", "auth-flow", "start", serverID}
	if etag != "" {
		args = append(args, "--etag", etag)
	}
	if openURL {
		args = append(args, "--open")
	}
	if jsonOutput {
		args = append(args, "--output", "json")
	}
	args = append(args, "--address", address, "--admin-bearer-file", bearerPath)
	result, _ := harness.runner.Run(context.Background(), "python3", args...)
	if result.ExitCode == 0 {
		result.Stdout, _ = os.ReadFile(stdoutPath)
		result.Stderr, _ = os.ReadFile(stderrPath)
	}
	return result
}
