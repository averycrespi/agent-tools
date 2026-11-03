//go:build e2e

package e2e

import (
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecoveryCommandRefusalsPreserveGenerationRealBinary(t *testing.T) {
	h := newGatewayHarness(t)
	db := filepath.Join(h.root, gatewaypaths.DatabaseName)
	original, err := os.ReadFile(db)
	require.NoError(t, err)
	marker := []byte(`{"installation_id":"01ARZ3NDEKTSV4RRFFQ69G5FAV","state":"armed","recovery":{"action":"unknown"}}`)
	markerPath := filepath.Join(h.root, gatewaypaths.MutationMarkerName)
	require.NoError(t, os.WriteFile(markerPath, marker, 0600))
	corruptID := "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	corruptRoot := filepath.Join(h.root, "backups", corruptID)
	require.NoError(t, os.MkdirAll(corruptRoot, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(corruptRoot, "metadata.json"), []byte("not-json"), 0600))
	secret := filepath.Join(t.TempDir(), "unused-secret")
	for _, args := range [][]string{
		{"restore", corruptID, "--secret-output", secret},
		{"restore", "--verify-current"},
		{"backup", "create", "--json"}, {"backup", "list", "--json"}, {"backup", "get", corruptID, "--json"}, {"backup", "delete", corruptID, "--yes", "--json"},
		{"maintenance", "restore-backup", "--confirm", corruptID, "--json", "--secret-output", secret},
		{"maintenance", "verify-and-recover-storage", "--confirm", corruptID, "--json"},
		{"maintenance", "verify-and-recover-storage", "--confirm", "--json"},
	} {
		result, runErr := h.runner.Run(t.Context(), h.binary, append([]string{"--data-dir", h.root}, args...)...)
		require.Error(t, runErr)
		assertSettledResult(t, result)
		require.Equal(t, 2, result.ExitCode)
		require.Empty(t, result.Stdout)
		current, err := os.ReadFile(db)
		require.NoError(t, err)
		require.Equal(t, original, current)
		retained, err := os.ReadFile(markerPath)
		require.NoError(t, err)
		require.Equal(t, marker, retained)
		_, err = os.Lstat(secret)
		require.ErrorIs(t, err, os.ErrNotExist)
	}
	// Unknown authority evidence still blocks normal startup, not merely a retired command.
	result, err := h.runner.Run(t.Context(), h.binary, h.serveArgs...)
	require.Error(t, err)
	require.NotZero(t, result.ExitCode)
	retained, err := os.ReadFile(markerPath)
	require.NoError(t, err)
	require.Equal(t, marker, retained)
}

func assertMaintenancePlan(t *testing.T, output []byte, operation string) {
	t.Helper()
	var plan struct {
		Operation      string   `json:"operation"`
		DataDir        string   `json:"data_dir"`
		InstallationID string   `json:"installation_id"`
		Actions        []string `json:"actions"`
	}
	require.NoError(t, json.Unmarshal(output, &plan))
	assert.Equal(t, operation, plan.Operation)
	assert.True(t, filepath.IsAbs(plan.DataDir))
	assert.NotEmpty(t, plan.InstallationID)
	assert.NotEmpty(t, plan.Actions)
}

func TestHistoricalCandidatePreservesValidatedCommittedAuthorityRealBinary(t *testing.T) {
	h := newGatewayHarness(t)
	owner, err := gatewaypaths.AcquireStoppedExisting(h.root)
	require.NoError(t, err)
	store, err := storage.Open(t.Context(), owner)
	require.NoError(t, err)
	repository, err := authorization.New(store, e2eClock{}, rand.Reader)
	require.NoError(t, err)
	principal, err := repository.CreatePrincipal(t.Context(), authorization.CreatePrincipalRequest{DisplayName: "Committed credential", Visibility: contract.VisibilityRequestable})
	require.NoError(t, err)
	issued, err := repository.IssueCredential(t.Context(), principal.Principal.ID, principal.Principal.Revision)
	require.NoError(t, err)
	identity, err := store.Identity(t.Context())
	require.NoError(t, err)
	require.NoError(t, store.Close())
	marker := []byte(`{"installation_id":"` + identity.InstallationID + `","state":"armed","recovery":{"action":"invalidate_agent_credential_candidate","principal_id":"` + principal.Principal.ID + `","credential_id":"` + issued.Principal.Credential.ID + `","principal_revision":` + issued.Principal.Revision + `,"credential_revision":` + issued.Principal.CredentialRevision + `}}`)
	require.NoError(t, os.WriteFile(owner.Layout().MutationMarker, marker, 0600))
	require.NoError(t, owner.Close())
	h.Start()
	h.Stop(syscall.SIGTERM)
	owner, err = gatewaypaths.AcquireStoppedExisting(h.root)
	require.NoError(t, err)
	defer func() { require.NoError(t, owner.Close()) }()
	store, err = storage.Open(t.Context(), owner)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	repository, err = authorization.New(store, e2eClock{}, rand.Reader)
	require.NoError(t, err)
	lease, err := repository.Authenticate(t.Context(), issued.Bearer)
	require.NoError(t, err)
	lease.Release()
	actual, err := os.ReadFile(owner.Layout().MutationMarker)
	require.NoError(t, err)
	require.Equal(t, marker, actual)
}
