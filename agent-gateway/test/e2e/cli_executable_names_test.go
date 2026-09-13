//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLIExecutableNames(t *testing.T) {
	buildDir := filepath.Join(t.TempDir(), "build output")
	installDir := filepath.Join(t.TempDir(), "installed binaries")
	module, err := filepath.Abs("../..")
	require.NoError(t, err)
	builder, err := testutil.NewBinaryRunner(300*time.Second, 128*1024)
	require.NoError(t, err)
	// The real Make targets publish both names; only the existing provider seam
	// is replaced so lifecycle evidence never touches the native keyring.
	built, err := builder.Run(t.Context(), "make", "-C", module, "build", "install",
		"AGENT_GATEWAY_BUILD_DIR="+buildDir, "AGENT_GATEWAY_INSTALL_DIR="+installDir, "GOFLAGS=-tags=e2e")
	require.NoError(t, err, "build/install: %s", built.Stderr)
	assertSettledResult(t, built)

	runner := firstRunRunner(t)
	var help []byte
	for _, directory := range []string{buildDir, installDir} {
		for _, name := range []string{"agent-gateway", "mcp-gateway"} {
			result, runErr := runner.Run(t.Context(), filepath.Join(directory, name), "--help")
			require.NoError(t, runErr)
			assertSettledResult(t, result)
			assert.Empty(t, result.Stderr)
			assert.Contains(t, string(result.Stdout), "Agent Gateway")
			assert.Contains(t, string(result.Stdout), "mcp-gateway executable remains supported")
			assert.Contains(t, string(result.Stdout), name+" [command]")
			normalized := strings.ReplaceAll(string(result.Stdout), "mcp-gateway", "agent-gateway")
			if help == nil {
				help = []byte(normalized)
			} else {
				assert.Equal(t, string(help), normalized)
			}
			completion, completionErr := runner.Run(t.Context(), filepath.Join(directory, name), "completion", "bash")
			require.NoError(t, completionErr)
			assertSettledResult(t, completion)
			assert.Empty(t, completion.Stderr)
			assert.Contains(t, string(completion.Stdout), "__start_"+name+" "+name)
			rootCompletions, completeErr := runner.Run(t.Context(), filepath.Join(directory, name), "__complete", "")
			require.NoError(t, completeErr)
			assert.Contains(t, string(rootCompletions.Stdout), "mcp\t")
			assert.NotContains(t, string(rootCompletions.Stdout), "\nserver\t")
			assert.NotContains(t, string(rootCompletions.Stdout), "\ncatalog\t")
			assert.Contains(t, string(rootCompletions.Stdout), "storage\t")
			assert.NotContains(t, string(rootCompletions.Stdout), "\nrestore\t")
			for _, family := range []struct{ name, leaf string }{{"storage", "verify"}, {"backup", "restore"}} {
				leaves, leafErr := runner.Run(t.Context(), filepath.Join(directory, name), "__complete", family.name, "")
				require.NoError(t, leafErr)
				assert.Contains(t, string(leaves.Stdout), family.leaf+"\t")
			}
			for _, retired := range []string{"server", "catalog", "restore"} {
				rejected, rejectedErr := runner.Run(t.Context(), filepath.Join(directory, name), retired, "list", "--json")
				require.Error(t, rejectedErr)
				assert.Empty(t, rejected.Stdout)
				assert.Equal(t, 2, rejected.ExitCode)
				assert.Contains(t, string(rejected.Stderr), "Usage: agent-gateway --help")
			}
		}
	}

	legacy := filepath.Join(installDir, "mcp-gateway")
	preferred := filepath.Join(installDir, "agent-gateway")
	// Exercise account-home, XDG, and explicit selection without using the
	// actual account home, even if a future selector regression ignores XDG.
	home := t.TempDir()
	xdg := t.TempDir()
	t.Setenv(e2eAccountHomeEnvironment, home)
	t.Setenv("XDG_DATA_HOME", xdg)
	root := filepath.Join(xdg, "mcp-gateway")
	initialized, err := runner.Run(t.Context(), legacy, "initialize", "--json")
	require.NoError(t, err, "initialize: %s", initialized.Stderr)
	assertSettledResult(t, initialized)
	assert.True(t, json.Valid(initialized.Stdout))
	bearer := assertDefaultBearer(t, filepath.Join(root, "admin-bearer"))

	for _, args := range [][]string{
		{"serve", "EXTRA", "--json"},
		{"initialize", "--unknown", "--output", "json"},
		{"admin", "unknown"},
		{"status", "--output", "invalid"},
		{"initialize", "--json"},
	} {
		oldResult, oldErr := runner.Run(t.Context(), legacy, args...)
		newResult, newErr := runner.Run(t.Context(), preferred, args...)
		require.Error(t, oldErr)
		require.Error(t, newErr)
		assertSettledResult(t, oldResult)
		assertSettledResult(t, newResult)
		assert.Equal(t, oldResult.ExitCode, newResult.ExitCode)
		assert.Equal(t, string(oldResult.Stdout), string(newResult.Stdout))
		assert.Equal(t, string(oldResult.Stderr), string(newResult.Stderr))
		assert.Empty(t, newResult.Stdout)
	}

	var artifact contract.Backup
	for _, names := range [][2]string{{preferred, legacy}, {legacy, preferred}} {
		t.Run(filepath.Base(names[0])+" owns", func(t *testing.T) {
			authority := unusedAuthority(t)
			process, startErr := runner.Start(t.Context(), names[0], "serve", "--listen", authority, "--json")
			require.NoError(t, startErr)
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
			var statusJSON []byte
			for _, binary := range []string{names[0], names[1]} {
				status, statusErr := runner.Run(t.Context(), binary, "--data-dir", root, "status", "--address", "http://"+authority, "--json")
				require.NoError(t, statusErr, "status: %s", status.Stderr)
				assertSettledResult(t, status)
				assert.Empty(t, status.Stderr)
				assert.True(t, json.Valid(status.Stdout))
				assert.NotContains(t, string(status.Stdout), strings.TrimSpace(string(bearer)))
				if statusJSON == nil {
					statusJSON = status.Stdout
				} else {
					assert.JSONEq(t, string(statusJSON), string(status.Stdout))
				}
			}
			blocked, blockedErr := runner.Run(t.Context(), names[1], "storage", "verify", "--json")
			require.Error(t, blockedErr)
			assertSettledResult(t, blocked)
			assert.Empty(t, blocked.Stdout)
			assert.Equal(t, 5, blocked.ExitCode)
			assert.JSONEq(t, `{"status":null,"code":"gateway_running","title":"The Gateway is running. Stop it before verifying current storage.","exit_code":5,"uncertain":false}`, string(blocked.Stderr))
			created, createErr := runner.Run(t.Context(), names[0], "backup", "create", "--address", "http://"+authority, "--json")
			require.NoError(t, createErr, "%s", created.Stderr)
			require.NoError(t, json.Unmarshal(created.Stdout, &artifact))
			unusedSecret := filepath.Join(t.TempDir(), "blocked-replacement")
			refused, restoreErr := runner.Run(t.Context(), names[1], "backup", "restore", artifact.ID, "--secret-output", unusedSecret, "--json")
			require.Error(t, restoreErr)
			assert.Equal(t, 5, refused.ExitCode)
			assert.Empty(t, refused.Stdout)
			assert.JSONEq(t, `{"status":null,"code":"gateway_running","title":"The Gateway is running. Stop it before restoring a backup.","exit_code":5,"uncertain":false}`, string(refused.Stderr))
			_, statErr := os.Lstat(unusedSecret)
			assert.ErrorIs(t, statErr, os.ErrNotExist)
			require.NoError(t, process.Signal(syscall.SIGTERM))
			served, waitErr := process.Wait()
			running = false
			require.NoError(t, waitErr, "serve: %s", served.Stderr)
			assertSettledResult(t, served)
			assert.True(t, json.Valid(served.Stdout))
			verified, verifyErr := runner.Run(t.Context(), names[1], "storage", "verify", "--json")
			require.NoError(t, verifyErr, "verify after owner exit: %s", verified.Stderr)
			assertSettledResult(t, verified)
			assert.True(t, json.Valid(verified.Stdout))
		})
	}
	for _, binary := range []string{preferred, legacy} {
		secret := filepath.Join(t.TempDir(), "replacement")
		restored, restoreErr := runner.Run(t.Context(), binary, "backup", "restore", artifact.ID, "--secret-output", secret, "--json")
		require.NoError(t, restoreErr, "%s", restored.Stderr)
		assertSettledResult(t, restored)
		assert.Empty(t, restored.Stderr)
		var result map[string]any
		require.NoError(t, json.Unmarshal(restored.Stdout, &result))
		assert.Len(t, result, 5)
		assert.Equal(t, "backup_restore", result["operation"])
		assert.Equal(t, artifact.InstallationID, result["installation_id"])
		assert.Equal(t, artifact.ID, result["backup_id"])
	}
	assertDirectoryEntries(t, xdg, []string{"mcp-gateway"})
	assertDirectoryEntries(t, home, nil)
	preserved, err := os.ReadFile(filepath.Join(root, "admin-bearer"))
	require.NoError(t, err)
	assert.True(t, string(bearer) == string(preserved), "original bearer must remain unchanged")

	t.Setenv("XDG_DATA_HOME", "")
	accountInitialized, err := runner.Run(t.Context(), preferred, "initialize", "--json")
	require.NoError(t, err, "initialize account default: %s", accountInitialized.Stderr)
	assertSettledResult(t, accountInitialized)
	assertDefaultBearer(t, filepath.Join(home, ".local", "share", "mcp-gateway", "admin-bearer"))
	assertDirectoryEntries(t, filepath.Join(home, ".local", "share"), []string{"mcp-gateway"})
}
