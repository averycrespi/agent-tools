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

func TestHTTPProxyRestoreAndKeyLossRequireExplicitCAReprovisioning(t *testing.T) {
	h := newGatewayHarness(t)
	binary, material := httpMaterialBinary(t)
	h.binary = binary
	originalCA := createHTTPCA(t, h)
	baseArgs := append([]string(nil), h.serveArgs...)
	h.serveArgs = append(h.serveArgs, "--http-proxy-listen", unusedAuthority(t))
	h.Start()
	defer func() {
		if h.process != nil {
			h.Stop(syscall.SIGTERM)
		}
	}()
	principal := h.CreatePrincipal("Restore HTTP policy", contract.VisibilityRequestable)
	credential := h.IssueCredential(principal)
	putProxyTestGrant(t, h, principal.Resource.ID, contract.HTTPPolicy{Version: 1, Type: contract.HTTPBlockDestination, Destination: &contract.HTTPDestinationSelector{Host: "example.invalid", Port: 443}})
	h.Restart()
	backupResponse := h.adminSnapshotWithHeaders("POST", "/api/v2/backups", []byte(`{}`), map[string]string{"Idempotency-Key": "http-activation-restore"})
	var artifact contract.Backup
	decodeSnapshot(t, backupResponse, http.StatusCreated, &artifact)
	h.RevokeCredential(credential.Principal)
	h.Stop(syscall.SIGTERM)
	secret := filepath.Join(t.TempDir(), "restored-admin")
	restored, err := h.runner.Run(h.ctx, binary, "maintenance", "restore-backup", "--confirm", artifact.ID, "--data-dir", h.root, "--secret-output", secret, "--json")
	require.NoError(t, err, "restore: %s", restored.Stderr)
	h.bearer = readBearer(t, secret)
	// Old physical fixture keys remain, but restored handles are not authority.
	refused, err := h.runner.Run(h.ctx, binary, h.serveArgs...)
	require.Error(t, err)
	require.Empty(t, refused.Stdout)
	enabledArgs := h.serveArgs
	h.serveArgs = baseArgs
	h.Start()
	old := h.ModernList(credential.Bearer, json.RawMessage(`"restored-old"`), "")
	require.Equal(t, 401, old.StatusCode)
	grants := h.adminSnapshot("GET", "/api/v2/http/grants", nil)
	require.Equal(t, 200, grants.StatusCode)
	require.Contains(t, string(grants.Body), "example.invalid")
	h.Stop(syscall.SIGTERM)
	_, err = h.runner.Run(h.ctx, binary, "http", "ca", "replace", "--data-dir", h.root, "--installation-id", artifact.InstallationID, "--confirm")
	require.NoError(t, err)
	exported, err := h.runner.Run(h.ctx, binary, "http", "ca", "export", "--data-dir", h.root, "--installation-id", artifact.InstallationID)
	require.NoError(t, err)
	require.NotEqual(t, originalCA, exported.Stdout)
	h.serveArgs = enabledArgs
	h.Start()
	h.Stop(syscall.SIGTERM)
	// Deleting only this test's fake keyring material simulates key loss. Metadata
	// export remains possible, but explicit activation must not regenerate keys.
	entries, err := os.ReadDir(material)
	require.NoError(t, err)
	for _, entry := range entries {
		if entry.Name() != ".fixture" {
			require.NoError(t, os.Remove(filepath.Join(material, entry.Name())))
		}
	}
	refused, err = h.runner.Run(h.ctx, binary, h.serveArgs...)
	require.Error(t, err)
	require.Empty(t, refused.Stdout)
	public, err := h.runner.Run(h.ctx, binary, "http", "ca", "export", "--data-dir", h.root, "--installation-id", artifact.InstallationID)
	require.NoError(t, err)
	require.Equal(t, exported.Stdout, public.Stdout)
}
