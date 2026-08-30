//go:build e2e

package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const stoppedMatrixID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

type discoveredOnlineLeaf struct {
	path    []string
	example string
	help    string
}

func TestCLIAdminCredentialRotation(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	oldBearerPath := filepath.Join(t.TempDir(), "old-admin")
	require.NoError(t, os.WriteFile(oldBearerPath, []byte(harness.bearer+"\n"), 0o600))

	listed := runOnlineCLI(t, harness, oldBearerPath, true, "admin", "credential", "list", "--output", "json")
	var page contract.Collection[contract.AdminCredential]
	require.NoError(t, json.Unmarshal(listed.Stdout, &page))
	require.Len(t, page.Items, 1)
	oldID := page.Items[0].ID

	existingPath := filepath.Join(t.TempDir(), "existing")
	require.NoError(t, os.WriteFile(existingPath, []byte("preserve\n"), 0o600))
	refused := runOnlineCLI(t, harness, oldBearerPath, false, "admin", "credential", "rotate", oldID, "--secret-output", existingPath, "--yes", "--output", "json")
	assert.Equal(t, 2, refused.ExitCode)
	assert.Equal(t, "preserve\n", string(mustReadFile(t, existingPath)))
	stillAuthorized := runOnlineCLI(t, harness, oldBearerPath, true, "admin", "credential", "get", oldID, "--output", "json")
	assert.Contains(t, string(stillAuthorized.Stdout), `"status":"active"`)

	newBearerPath := filepath.Join(t.TempDir(), "new-admin")
	rotated := runOnlineCLI(t, harness, oldBearerPath, true, "admin", "credential", "rotate", oldID, "--secret-output", newBearerPath, "--yes", "--output", "json")
	var success struct {
		Result        string                   `json:"result"`
		OldCredential contract.AdminCredential `json:"old_credential"`
		NewCredential contract.AdminCredential `json:"new_credential"`
	}
	require.NoError(t, json.Unmarshal(rotated.Stdout, &success))
	assert.Equal(t, "rotated", success.Result)
	assert.Equal(t, oldID, success.OldCredential.ID)
	assert.Equal(t, contract.CredentialRevoked, success.OldCredential.Status)
	assert.Equal(t, contract.CredentialActive, success.NewCredential.Status)
	assert.True(t, success.NewCredential.NonExpiring)
	assert.Nil(t, success.NewCredential.ExpiresAt)
	assert.NotContains(t, string(rotated.Stdout), contract.AdminBearerPrefix)
	newInfo, err := os.Stat(newBearerPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), newInfo.Mode().Perm())

	oldRejected := runOnlineCLI(t, harness, oldBearerPath, false, "admin", "credential", "list", "--output", "json")
	assert.Equal(t, 3, oldRejected.ExitCode)
	newAuthorized := runOnlineCLI(t, harness, newBearerPath, true, "admin", "credential", "get", success.NewCredential.ID, "--output", "json")
	assert.Contains(t, string(newAuthorized.Stdout), `"status":"active"`)
	oldObserved := runOnlineCLI(t, harness, newBearerPath, true, "admin", "credential", "get", oldID, "--output", "json")
	assert.Contains(t, string(oldObserved.Stdout), `"status":"revoked"`)

	harness.Stop(syscall.SIGTERM)
	for _, result := range []testutil.ProcessResult{listed, refused, stillAuthorized, rotated, oldRejected, newAuthorized, oldObserved} {
		assert.NotContains(t, string(result.Stdout), harness.bearer)
		assert.NotContains(t, string(result.Stderr), harness.bearer)
		assert.True(t, result.Cleanup.Reaped)
		assert.False(t, result.Cleanup.Survived)
	}
	assert.Len(t, harness.results, 1)
}

func TestCLICompleteStoppedGatewayMatrix(t *testing.T) {
	runner := firstRunRunner(t)
	binary := gatewayBinary(t)
	root := t.TempDir()
	bearerPath := filepath.Join(root, "admin-bearer")
	require.NoError(t, os.WriteFile(bearerPath, []byte(usabilityTestBearer+"\n"), 0o600))
	leaves := discoverOnlineLeaves(t, runner, binary, nil)
	require.NotEmpty(t, leaves)
	seen := make(map[string]struct{}, len(leaves))
	address := refusedAddress(t, "127.0.0.1:0")
	secretRoot, err := os.MkdirTemp("", "mgw-stopped-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(secretRoot)) })
	for index, leaf := range leaves {
		name := strings.Join(leaf.path, " ")
		if _, duplicate := seen[name]; duplicate {
			t.Fatalf("duplicate discovered online leaf %q", name)
		}
		seen[name] = struct{}{}
		t.Run(name, func(t *testing.T) {
			if name == "server auth-flow start" {
				harness := &gatewayHarness{t: t, runner: runner, binary: binary}
				result := runAuthFlowStartPTYCommand(t, harness, bearerPath, "http://"+address, "https://never.example.invalid/canary", stoppedMatrixID, "", 9, false, false, true)
				require.Zero(t, result.ExitCode, "%s", result.Stderr)
				var problem struct {
					Code     string `json:"code"`
					ExitCode int    `json:"exit_code"`
				}
				require.NoError(t, json.Unmarshal(result.Stderr, &problem))
				assert.Equal(t, "gateway_not_running", problem.Code)
				assert.Equal(t, 9, problem.ExitCode)
				assert.Contains(t, string(result.Stderr), "mcp-gateway serve --listen "+address)
				return
			}
			args := stoppedLeafArguments(t, leaf, secretRoot, index)
			args = append(args, "--admin-bearer-file", bearerPath, "--address", "http://"+address, "--output", "json")
			result, err := runner.Run(t.Context(), binary, args...)
			view := processResultView{result: result, err: err}
			view.requireProblem(t, 9, "gateway_not_running", false)
			title := "MCP Gateway is not running. Start it with: mcp-gateway serve --listen " + address + "."
			assert.Equal(t, fmt.Sprintf("{\"status\":null,\"code\":\"gateway_not_running\",\"title\":%q,\"exit_code\":9,\"uncertain\":false}\n", title), string(result.Stderr))
		})
	}
	assert.Contains(t, seen, "admin credential rotate")
	assert.Contains(t, seen, "principal credential rotate")

	t.Run("default address and data directory guidance", func(t *testing.T) {
		listener, err := net.Listen("tcp4", "127.0.0.1:8210")
		require.NoError(t, err, "the default address must be free for stopped-Gateway coverage")
		require.NoError(t, listener.Close())
		result, err := runner.Run(t.Context(), binary, "status", "--admin-bearer-file", bearerPath)
		require.Error(t, err)
		assert.Equal(t, 9, result.ExitCode)
		assert.Equal(t, "MCP Gateway is not running. Start it with: mcp-gateway serve.\n", string(result.Stderr))
		dataDir := filepath.Join(root, "default address data")
		result, err = runner.Run(t.Context(), binary, "--data-dir", dataDir, "status", "--admin-bearer-file", bearerPath)
		require.Error(t, err)
		assert.Equal(t, 9, result.ExitCode)
		assert.Contains(t, string(result.Stderr), "mcp-gateway serve")
		assert.Contains(t, string(result.Stderr), "--data-dir")
		assert.NotContains(t, string(result.Stderr), "--listen")
	})

	t.Run("address and data directory guidance variants", func(t *testing.T) {
		cases := []struct {
			name       string
			bind       string
			dataDir    string
			wantPieces []string
		}{
			{name: "alternate port", bind: "127.0.0.1:0", wantPieces: []string{"mcp-gateway serve --listen"}},
			{name: "alternate loopback", bind: "127.8.7.6:0", wantPieces: []string{"mcp-gateway serve --listen"}},
			{name: "combined", bind: "127.0.0.1:0", dataDir: filepath.Join(root, "custom data"), wantPieces: []string{"--data-dir", "--listen"}},
		}
		for _, test := range cases {
			t.Run(test.name, func(t *testing.T) {
				selected := refusedAddress(t, test.bind)
				args := []string{"status", "--admin-bearer-file", bearerPath, "--address", "http://" + selected}
				if test.dataDir != "" {
					args = append([]string{"--data-dir", test.dataDir}, args...)
				}
				result, err := runner.Run(t.Context(), binary, args...)
				require.Error(t, err)
				assert.Equal(t, 9, result.ExitCode)
				for _, piece := range test.wantPieces {
					assert.Contains(t, string(result.Stderr), piece)
				}
				assert.Contains(t, string(result.Stderr), selected)
			})
		}
	})
}

func discoverOnlineLeaves(t *testing.T, runner *testutil.BinaryRunner, binary string, path []string) []discoveredOnlineLeaf {
	t.Helper()
	args := append(append([]string(nil), path...), "--help")
	result, err := runner.Run(t.Context(), binary, args...)
	require.NoError(t, err, "%v: %s", args, result.Stderr)
	help := string(result.Stdout)
	if strings.Contains(help, "--address string") {
		marker := "Examples:\n"
		start := strings.Index(help, marker)
		require.NotEqual(t, -1, start, "%v", path)
		example := strings.TrimSpace(strings.SplitN(help[start+len(marker):], "\n\n", 2)[0])
		return []discoveredOnlineLeaf{{path: append([]string(nil), path...), example: example, help: help}}
	}
	children := helpChildren(help)
	leaves := make([]discoveredOnlineLeaf, 0)
	for _, child := range children {
		if child == "completion" || child == "help" {
			continue
		}
		leaves = append(leaves, discoverOnlineLeaves(t, runner, binary, append(append([]string(nil), path...), child))...)
	}
	return leaves
}

func helpChildren(help string) []string {
	scanner := bufio.NewScanner(strings.NewReader(help))
	available := false
	var children []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "Available Commands:" {
			available = true
			continue
		}
		if !available {
			continue
		}
		if line == "" {
			break
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			children = append(children, fields[0])
		}
	}
	return children
}

func stoppedLeafArguments(t *testing.T, leaf discoveredOnlineLeaf, root string, index int) []string {
	t.Helper()
	fields := strings.Fields(strings.TrimPrefix(leaf.example, "mcp-gateway "))
	name := strings.Join(leaf.path, " ")
	args := make([]string, 0, len(fields)+6)
	optional := false
	for _, field := range fields {
		if strings.HasPrefix(field, "[") {
			optional = true
		}
		if !optional {
			args = append(args, stoppedMatrixValue(field, root, name))
		}
		if strings.HasSuffix(field, "]") {
			optional = false
		}
	}
	switch name {
	case "server update", "principal update":
		args = append(args, "--display-name", "stopped-matrix")
	case "grant-request approve":
		args = append(args, "--acknowledge-future-tools")
	case "server auth-flow start":
		args = append(args, "--open")
	}
	if strings.Contains(leaf.help, "--secret-output string") && !containsArgument(args, "--secret-output") {
		args = append(args, "--secret-output", filepath.Join(root, "secret-"+strings.ReplaceAll(name, " ", "-")+string(rune('a'+index%26))))
	}
	if strings.Contains(leaf.help, "--yes") && !containsArgument(args, "--yes") {
		args = append(args, "--yes")
	}
	return args
}

func stoppedMatrixValue(value, root, command string) string {
	value = strings.Trim(value, "[]")
	switch value {
	case "ID", "BACKUP_ID", "OPERATION_ID", "FLOW_ID", "TOOL_ID", "REQUEST_ID", "INVOCATION_ID", "OLD_CREDENTIAL_ID":
		return stoppedMatrixID
	case "PATH":
		if command == "server credential replace" {
			return writeStoppedMatrixCredentialFile(root)
		}
		return writeStoppedMatrixServerFile(root)
	case "NEW_PATH":
		return filepath.Join(root, "new-secret")
	case "NAME":
		return "stopped-matrix"
	case "VISIBILITY":
		return "all"
	case "EFFECT":
		return "allow"
	case "KIND":
		return "reload"
	case "SCOPE":
		return "server"
	case "TARGET":
		return stoppedMatrixID
	case "REASON":
		return "scope_too_broad"
	default:
		return value
	}
}

func writeStoppedMatrixServerFile(root string) string {
	path := filepath.Join(root, "server.json")
	_ = os.WriteFile(path, []byte(`{"namespace":"stopped-matrix","display_name":"Stopped matrix","enabled":false,"transport":{"kind":"stdio","executable":"/bin/cat","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{}}}`), 0o600)
	return path
}

func writeStoppedMatrixCredentialFile(root string) string {
	path := filepath.Join(root, "credential.json")
	_ = os.WriteFile(path, []byte(`{"kind":"static_credential","expected_revision":"0","values":{"primary":"matrix-secret"}}`), 0o600)
	return path
}

func containsArgument(args []string, wanted string) bool {
	for _, arg := range args {
		if arg == wanted {
			return true
		}
	}
	return false
}

func refusedAddress(t *testing.T, bind string) string {
	t.Helper()
	listener, err := net.Listen("tcp4", bind)
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	return address
}
