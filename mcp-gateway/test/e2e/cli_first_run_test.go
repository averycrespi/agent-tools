//go:build e2e

package e2e

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const e2eAccountHomeEnvironment = "MCP_GATEWAY_E2E_ACCOUNT_HOME"

var administratorBearerFilePattern = regexp.MustCompile(`^mgw_admin_[A-Za-z0-9_-]{43}\n$`)

func TestCLIFirstRun(t *testing.T) {
	runner := firstRunRunner(t)
	home := filepath.Join(t.TempDir(), "account-home")
	require.NoError(t, os.Mkdir(home, 0o700))
	ambientHome := filepath.Join(t.TempDir(), "ambient-home-decoy")
	require.NoError(t, os.Mkdir(ambientHome, 0o700))
	t.Setenv(e2eAccountHomeEnvironment, home)
	t.Setenv("HOME", ambientHome)
	t.Setenv("XDG_DATA_HOME", "")

	initialized, err := runner.Run(t.Context(), gatewayBinary(t), "initialize")
	require.NoError(t, err, "initialize: %s", initialized.Stderr)
	assertSettledResult(t, initialized)
	root := filepath.Join(home, ".local", "share", gatewaypaths.InstallationName)
	bearerPath := filepath.Join(root, gatewaypaths.AdminBearerName)
	bearer := assertDefaultBearer(t, bearerPath)
	assertMode(t, root, 0o700)
	assert.Contains(t, string(initialized.Stdout), "Gateway initialized successfully.")
	assert.Contains(t, string(initialized.Stdout), root)
	assert.Contains(t, string(initialized.Stdout), bearerPath)
	assert.Contains(t, string(initialized.Stdout), "mcp-gateway serve")
	assert.NotContains(t, string(initialized.Stdout), strings.TrimSpace(string(bearer)))
	assert.NotContains(t, string(initialized.Stderr), strings.TrimSpace(string(bearer)))
	assert.Empty(t, initialized.Stderr)
	assertDirectoryEntries(t, ambientHome, nil)
	assertDirectoryEntries(t, home, []string{".local"})

	listener, err := net.Listen("tcp", "127.0.0.1:8210")
	require.NoError(t, err, "default listener must be free for first-run evidence")
	require.NoError(t, listener.Close())
	process, err := runner.Start(t.Context(), gatewayBinary(t), "serve")
	require.NoError(t, err)
	running := true
	t.Cleanup(func() {
		if running {
			_ = process.Stop()
			_, _ = process.Wait()
		}
	})
	select {
	case <-process.StdoutReady():
	case <-time.After(5 * time.Second):
		t.Fatal("default serve did not acknowledge startup")
	}
	status, err := runner.Run(t.Context(), gatewayBinary(t), "status")
	require.NoError(t, err, "default status: %s", status.Stderr)
	assertSettledResult(t, status)
	assert.Contains(t, string(status.Stdout), "principal_credentials")
	assert.Empty(t, status.Stderr)
	assert.NotContains(t, string(status.Stdout), strings.TrimSpace(string(bearer)))
	require.NoError(t, process.Signal(syscall.SIGTERM))
	served, err := process.Wait()
	running = false
	require.NoError(t, err, "serve: %s", served.Stderr)
	assertSettledResult(t, served)
	assert.Equal(t, 1, strings.Count(string(served.Stdout), "Gateway started successfully."))
	assert.Contains(t, string(served.Stdout), "http://127.0.0.1:8210/")
	assert.Empty(t, served.Stderr)
	assert.NotContains(t, string(served.Stdout), strings.TrimSpace(string(bearer)))
}

func TestCLIAutomaticBearerSelection(t *testing.T) {
	runner := firstRunRunner(t)
	root := filepath.Join(t.TempDir(), "selected", "gateway")
	initialized, err := runner.Run(t.Context(), gatewayBinary(t), "initialize", "--data-dir", root, "--output", "json")
	require.NoError(t, err, "initialize: %s", initialized.Stderr)
	assertSettledResult(t, initialized)
	bearer := assertDefaultBearer(t, filepath.Join(root, gatewaypaths.AdminBearerName))

	decoyXDG := filepath.Join(t.TempDir(), "decoy-xdg")
	require.NoError(t, os.Mkdir(decoyXDG, 0o700))
	t.Setenv("XDG_DATA_HOME", decoyXDG)
	authority := unusedAuthority(t)
	process, err := runner.Start(t.Context(), gatewayBinary(t), "serve", "--data-dir", root, "--listen", authority, "--output", "json")
	require.NoError(t, err)
	running := true
	t.Cleanup(func() {
		if running {
			_ = process.Stop()
			_, _ = process.Wait()
		}
	})
	select {
	case <-process.StdoutReady():
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not acknowledge startup")
	}

	status, err := runner.Run(t.Context(), gatewayBinary(t), "--data-dir", root, "status", "--address", "http://"+authority, "--output", "json")
	require.NoError(t, err, "status without bearer flag: %s", status.Stderr)
	assertSettledResult(t, status)
	assert.Empty(t, status.Stderr)
	assert.True(t, json.Valid(status.Stdout))
	assert.NotContains(t, string(status.Stdout), strings.TrimSpace(string(bearer)))
	assertDirectoryEntries(t, decoyXDG, nil)

	explicitBearer := filepath.Join(t.TempDir(), "explicit-bearer")
	require.NoError(t, os.WriteFile(explicitBearer, bearer, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, gatewaypaths.AdminBearerName), []byte("malformed\n"), 0o600))
	explicit, err := runner.Run(t.Context(), gatewayBinary(t), "--data-dir", root, "status", "--address", "http://"+authority, "--admin-bearer-file", explicitBearer, "--output", "json")
	require.NoError(t, err, "status with explicit bearer file: %s", explicit.Stderr)
	assertSettledResult(t, explicit)
	assert.True(t, json.Valid(explicit.Stdout))
	assert.NotContains(t, string(explicit.Stdout), strings.TrimSpace(string(bearer)))

	stdinProcess, input, err := runner.StartWithInputPipe(t.Context(), gatewayBinary(t), "--data-dir", root, "status", "--address", "http://"+authority, "--admin-bearer-stdin", "--output", "json")
	require.NoError(t, err)
	_, err = input.Write(bearer)
	require.NoError(t, err)
	require.NoError(t, input.Close())
	stdinResult, err := stdinProcess.Wait()
	require.NoError(t, err, "status with stdin bearer: %s", stdinResult.Stderr)
	assertSettledResult(t, stdinResult)
	assert.True(t, json.Valid(stdinResult.Stdout))
	assert.NotContains(t, string(stdinResult.Stdout), strings.TrimSpace(string(bearer)))

	require.NoError(t, process.Signal(syscall.SIGTERM))
	served, err := process.Wait()
	running = false
	require.NoError(t, err, "serve: %s", served.Stderr)
	assertSettledResult(t, served)
}

func TestCLIXDGAndOverrides(t *testing.T) {
	runner := firstRunRunner(t)

	t.Run("absolute XDG avoids account home", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "account")
		xdg := filepath.Join(t.TempDir(), "xdg")
		require.NoError(t, os.Mkdir(home, 0o700))
		require.NoError(t, os.Mkdir(xdg, 0o700))
		t.Setenv(e2eAccountHomeEnvironment, home)
		t.Setenv("XDG_DATA_HOME", xdg)
		result, err := runner.Run(t.Context(), gatewayBinary(t), "initialize", "--output", "json")
		require.NoError(t, err, "initialize XDG: %s", result.Stderr)
		assertSettledResult(t, result)
		root := filepath.Join(xdg, gatewaypaths.InstallationName)
		bearer := assertDefaultBearer(t, filepath.Join(root, gatewaypaths.AdminBearerName))
		assert.NotContains(t, string(result.Stdout), strings.TrimSpace(string(bearer)))
		assertDirectoryEntries(t, home, nil)
	})

	t.Run("explicit data and secret override invalid defaults", func(t *testing.T) {
		explicit := filepath.Join(t.TempDir(), "explicit", "gateway")
		secret := filepath.Join(t.TempDir(), "selected-secret")
		t.Setenv(e2eAccountHomeEnvironment, "")
		t.Setenv("XDG_DATA_HOME", "relative-invalid")
		result, err := runner.Run(t.Context(), gatewayBinary(t), "initialize", "--data-dir", explicit, "--secret-output", secret, "--output", "json")
		require.NoError(t, err, "initialize explicit: %s", result.Stderr)
		assertSettledResult(t, result)
		bearer := assertDefaultBearer(t, secret)
		_, defaultErr := os.Lstat(filepath.Join(explicit, gatewaypaths.AdminBearerName))
		assert.ErrorIs(t, defaultErr, os.ErrNotExist)
		assert.NotContains(t, string(result.Stdout), strings.TrimSpace(string(bearer)))
		assertMode(t, explicit, 0o700)
	})

	t.Run("invalid XDG and unavailable home fail without fallback mutation", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "account")
		require.NoError(t, os.Mkdir(home, 0o700))
		t.Setenv(e2eAccountHomeEnvironment, home)
		t.Setenv("XDG_DATA_HOME", "relative-invalid")
		invalid, err := runner.Run(t.Context(), gatewayBinary(t), "initialize", "--output", "json")
		require.Error(t, err)
		assertSettledResult(t, invalid)
		assert.Empty(t, invalid.Stdout)
		assert.Contains(t, string(invalid.Stderr), "selected data directory is invalid")
		assertDirectoryEntries(t, home, nil)

		t.Setenv(e2eAccountHomeEnvironment, "")
		t.Setenv("XDG_DATA_HOME", "")
		unavailable, err := runner.Run(t.Context(), gatewayBinary(t), "initialize", "--output", "json")
		require.Error(t, err)
		assertSettledResult(t, unavailable)
		assert.Empty(t, unavailable.Stdout)
		assert.Contains(t, string(unavailable.Stderr), "selected data directory is invalid")
		assertDirectoryEntries(t, home, nil)
	})
}

func TestCLIServeOutputLifecycle(t *testing.T) {
	runner := firstRunRunner(t)
	root := filepath.Join(t.TempDir(), "gateway")
	secret := filepath.Join(t.TempDir(), "secret")
	initialized, err := runner.Run(t.Context(), gatewayBinary(t), "initialize", "--data-dir", root, "--secret-output", secret, "--output", "json")
	require.NoError(t, err, "initialize: %s", initialized.Stderr)
	assertSettledResult(t, initialized)

	for _, mode := range []string{"human", "json"} {
		t.Run(mode, func(t *testing.T) {
			authority := unusedAuthority(t)
			process, err := runner.Start(t.Context(), gatewayBinary(t), "serve", "--data-dir", root, "--listen", authority, "--output", mode)
			require.NoError(t, err)
			select {
			case <-process.StdoutReady():
			case <-time.After(5 * time.Second):
				_ = process.Stop()
				_, _ = process.Wait()
				t.Fatal("serve did not acknowledge startup")
			}
			require.NoError(t, process.Signal(syscall.SIGTERM))
			result, waitErr := process.Wait()
			require.NoError(t, waitErr, "serve %s: %s", mode, result.Stderr)
			assertSettledResult(t, result)
			assert.Empty(t, result.Stderr)
			if mode == "json" {
				var startup struct {
					OK        bool   `json:"ok"`
					Operation string `json:"operation"`
					Authority string `json:"authority"`
				}
				require.NoError(t, json.Unmarshal(result.Stdout, &startup))
				assert.True(t, startup.OK)
				assert.Equal(t, "serve", startup.Operation)
				assert.Equal(t, authority, startup.Authority)
				assert.Equal(t, 1, strings.Count(strings.TrimSpace(string(result.Stdout)), "\n")+1)
			} else {
				assert.Equal(t, 1, strings.Count(string(result.Stdout), "Gateway started successfully."))
				assert.Contains(t, string(result.Stdout), "http://"+authority+"/")
			}
		})
	}

	missing := filepath.Join(t.TempDir(), "missing-installation")
	failed, err := runner.Run(t.Context(), gatewayBinary(t), "serve", "--data-dir", missing, "--listen", unusedAuthority(t), "--output", "json")
	require.Error(t, err)
	assertSettledResult(t, failed)
	assert.Empty(t, failed.Stdout)
	var problem struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(failed.Stderr, &problem))
	assert.Equal(t, "storage_unavailable", problem.Code)
}

func firstRunRunner(t *testing.T) *testutil.BinaryRunner {
	t.Helper()
	runner, err := testutil.NewBinaryRunner(20*time.Second, 64*1024)
	require.NoError(t, err)
	return runner
}

func assertDefaultBearer(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Regexp(t, administratorBearerFilePattern, string(contents))
	assertMode(t, path, 0o600)
	return contents
}

func assertMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	require.NoError(t, err)
	assert.Equal(t, mode, info.Mode().Perm())
}

func assertDirectoryEntries(t *testing.T, path string, want []string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	require.NoError(t, err)
	actual := make([]string, 0, len(entries))
	for _, entry := range entries {
		actual = append(actual, entry.Name())
	}
	if len(want) == 0 {
		assert.Empty(t, actual)
		return
	}
	assert.Equal(t, want, actual)
}

func assertSettledResult(t *testing.T, result testutil.ProcessResult) {
	t.Helper()
	assert.True(t, result.Cleanup.Reaped)
	assert.False(t, result.Cleanup.Survived)
	assert.False(t, result.StdoutTruncated)
	assert.False(t, result.StderrTruncated)
}
