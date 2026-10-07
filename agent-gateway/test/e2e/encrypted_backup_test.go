//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestCLIEncryptedBackupRestoresWithFreshAccess(t *testing.T) {
	h := newGatewayHarness(t)
	h.Start()
	defer func() {
		if h.process != nil {
			h.Stop(syscall.SIGTERM)
		}
	}()
	bearer := filepath.Join(t.TempDir(), "old-bearer")
	require.NoError(t, os.WriteFile(bearer, []byte(h.bearer+"\n"), 0600))
	principal := h.CreatePrincipal("Restore access", contract.VisibilityAll)
	credential := h.IssueCredential(principal)
	result := runOnlineCLI(t, h, bearer, true, "backup", "create", "--idempotency-key", "encrypted", "--output", "json")
	var artifact contract.Backup
	require.NoError(t, json.Unmarshal(result.Stdout, &artifact))
	h.Stop(syscall.SIGTERM)
	sink := filepath.Join(t.TempDir(), "restored-bearer")
	dry, err := h.runner.Run(h.ctx, h.binary, "maintenance", "restore-backup", artifact.ID, "--data-dir", h.root, "--secret-output", sink, "--dry-run", "--json")
	require.NoError(t, err)
	require.Contains(t, string(dry.Stdout), "Recover encrypted upstream credentials")
	require.Contains(t, string(dry.Stdout), "default admin-bearer file is retained but invalid")
	_, err = os.Lstat(sink)
	require.ErrorIs(t, err, os.ErrNotExist)
	restored, err := h.runner.Run(h.ctx, h.binary, "maintenance", "restore-backup", artifact.ID, "--data-dir", h.root, "--secret-output", sink, "--confirm", "--json")
	require.NoError(t, err)
	require.Contains(t, string(restored.Stdout), `"ok":true`)
	h.bearer = readBearer(t, sink)
	h.Start()
	runOnlineCLI(t, h, sink, true, "backup", "list", "--output", "json")
	rejected := runOnlineCLI(t, h, bearer, false, "backup", "list", "--output", "json")
	require.NotEqual(t, 0, rejected.ExitCode)
	require.Equal(t, http.StatusUnauthorized, h.ModernList(credential.Bearer, json.RawMessage(`"old-agent"`), "").StatusCode)
}
