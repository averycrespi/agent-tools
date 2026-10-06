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
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/stretchr/testify/require"
)

func TestHTTPProxyEncryptedCustodyRestartAndRecoveryRefusal(t *testing.T) {
	h := newGatewayHarness(t)
	binary, material := httpMaterialBinary(t)
	h.binary = binary
	originalCA := createHTTPCA(t, h)
	h.serveArgs = append(h.serveArgs, "--clear-http-proxy-listen=false", "--http-proxy-listen", unusedAuthority(t))
	h.Start()
	defer func() {
		if h.process != nil {
			h.Stop(syscall.SIGTERM)
		}
	}()
	principal := h.CreatePrincipal("Encrypted HTTP policy", contract.VisibilityRequestable)
	credential := h.IssueCredential(principal)
	putProxyTestGrant(t, h, principal.Resource.ID, contract.HTTPPolicy{Version: 1, Type: contract.HTTPBlockDestination, Destination: &contract.HTTPDestinationSelector{Host: "example.invalid", Port: 443}})
	h.Restart()
	refusal := h.adminSnapshotWithHeaders("POST", "/api/v2/backups", []byte(`{}`), map[string]string{"Idempotency-Key": "encrypted-custody"})
	require.Equal(t, http.StatusConflict, refusal.StatusCode)
	require.Contains(t, string(refusal.Body), `"code":"encrypted_backup_unsupported"`)
	require.Equal(t, "no-store", refusal.Header.Get("Cache-Control"))
	require.Equal(t, http.StatusOK, h.ModernList(credential.Bearer, json.RawMessage(`"still-current"`), "").StatusCode)
	h.Stop(syscall.SIGTERM)
	// The native fixture has no secret material: both stopped CA creation and
	// subsequent processes use the encrypted control generation and file key.
	entries, err := os.ReadDir(material)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, ".fixture", entries[0].Name())
	h.Start()
	h.Stop(syscall.SIGTERM)
	public, err := h.runner.Run(h.ctx, binary, "http", "ca", "export", "--data-dir", h.root, "--stdout")
	require.NoError(t, err)
	require.Equal(t, originalCA, public.Stdout)
	keyPath := filepath.Join(h.root, gatewaypaths.MasterKeyName)
	retainedKey := filepath.Join(t.TempDir(), "retained-key")
	require.NoError(t, os.Rename(keyPath, retainedKey))
	failed, err := h.runner.Run(h.ctx, binary, h.serveArgs...)
	require.Error(t, err)
	require.Empty(t, failed.Stdout)
	require.Contains(t, string(failed.Stderr), "master-key")
	setup, err := h.runner.Run(h.ctx, binary, "maintenance", "setup-secret-storage", "--data-dir", h.root, "--confirm", "--json")
	require.Error(t, err)
	require.Empty(t, setup.Stdout)
	require.Contains(t, string(setup.Stderr), "secret_storage_unavailable")
	_, err = os.Lstat(keyPath)
	require.ErrorIs(t, err, os.ErrNotExist)
	public, err = h.runner.Run(h.ctx, binary, "http", "ca", "export", "--data-dir", h.root, "--stdout")
	require.NoError(t, err)
	require.Equal(t, originalCA, public.Stdout)
	// Restore the exact retained fixture key, not a generated replacement.
	require.NoError(t, os.Rename(retainedKey, keyPath))
	h.Start()
	h.Stop(syscall.SIGTERM)
}
