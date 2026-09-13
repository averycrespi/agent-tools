//go:build e2e

package e2e

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecoveryCommandRefusalsPreserveGenerationRealBinary(t *testing.T) {
	ctx := t.Context()
	binary, runner := gatewayBinary(t), firstRunRunner(t)
	root := filepath.Join(t.TempDir(), "gateway")
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	layout := ownership.Layout()
	store, err := storage.Initialize(ctx, ownership, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	require.NoError(t, err)
	require.NoError(t, store.Close())
	require.NoError(t, ownership.Close())
	original, err := os.ReadFile(layout.Database)
	require.NoError(t, err)
	marker := []byte(`{"installation_id":"01ARZ3NDEKTSV4RRFFQ69G5FAV","state":"armed","recovery":{"action":"unknown"}}`)
	require.NoError(t, os.WriteFile(layout.MutationMarker, marker, 0o600))
	corruptID := "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	corruptRoot := filepath.Join(layout.Backups, corruptID)
	require.NoError(t, os.MkdirAll(corruptRoot, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(corruptRoot, "metadata.json"), []byte("not-json"), 0o600))
	secret := filepath.Join(t.TempDir(), "unused-secret")
	for _, test := range []struct {
		name string
		args []string
		exit int
		code string
	}{
		{"retired restore", []string{"restore", corruptID, "--secret-output", secret}, 2, ""},
		{"retired verification", []string{"restore", "--verify-current"}, 2, ""},
		{"missing ID", []string{"backup", "restore", "--json", "--secret-output", secret}, 2, "client_invalid_input"},
		{"invalid ID", []string{"backup", "restore", "../unsafe", "--json", "--secret-output", secret}, 2, "client_invalid_input"},
		{"missing sink", []string{"backup", "restore", corruptID, "--json"}, 2, "client_invalid_input"},
		{"missing backup", []string{"backup", "restore", "01ARZ3NDEKTSV4RRFFQ69G5FAX", "--json", "--secret-output", secret}, 4, "invalid_backup"},
		{"corrupt backup", []string{"backup", "restore", corruptID, "--json", "--secret-output", secret}, 4, "invalid_backup"},
		{"retired flag", []string{"backup", "restore", "--json", "--verify-current"}, 2, "client_invalid_input"},
		{"verification cannot restore", []string{"storage", "verify", corruptID, "--json"}, 2, "client_invalid_input"},
		{"unknown recovery remains latched", []string{"storage", "verify", "--json"}, 7, "storage_unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"--data-dir", root}, test.args...)
			result, runErr := runner.Run(ctx, binary, args...)
			require.Error(t, runErr)
			assertSettledResult(t, result)
			assert.Equal(t, test.exit, result.ExitCode)
			assert.Empty(t, result.Stdout)
			if test.code != "" {
				var problem controlclient.Problem
				require.NoError(t, json.Unmarshal(result.Stderr, &problem))
				assert.Equal(t, test.code, problem.Code)
				assert.Equal(t, test.exit, problem.Exit)
			}
			current, readErr := os.ReadFile(layout.Database)
			require.NoError(t, readErr)
			assert.True(t, bytes.Equal(original, current), "refusal must not replace or mutate the database")
			retained, readErr := os.ReadFile(layout.MutationMarker)
			require.NoError(t, readErr)
			assert.Equal(t, marker, retained)
			_, statErr := os.Lstat(secret)
			assert.ErrorIs(t, statErr, os.ErrNotExist)
		})
	}
}

func TestStorageVerifyExactCandidateRealBinary(t *testing.T) {
	ctx := t.Context()
	binary, runner := gatewayBinary(t), firstRunRunner(t)
	root := filepath.Join(t.TempDir(), "gateway")
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	layout := ownership.Layout()
	const installationID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	const principalID = "01J60000000000000000000011"
	const credentialID = "01J60000000000000000000012"
	store, err := storage.Initialize(ctx, ownership, installationID)
	require.NoError(t, err)
	require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error {
		_, insertErr := tx.ExecContext(ctx, `INSERT INTO principals (
			id, display_name, state, visibility, revision, credential_revision,
			credential_id, credential_verifier, credential_fingerprint, credential_created_at, created_at, updated_at
		) VALUES (?, 'Recovery fixture', 'active', 'requestable', 2, 1, ?, ?, '0123456789abcdef', ?, ?, ?)`,
			principalID, credentialID, make([]byte, 32), "2026-08-25T18:00:00.000000000Z", "2026-08-25T18:00:00.000000000Z", "2026-08-25T18:00:00.000000000Z")
		return insertErr
	}))
	require.NoError(t, store.Close())
	// Model the durable state left by a post-commit candidate-cleanup interruption.
	marker := `{"installation_id":"` + installationID + `","state":"armed","recovery":{"action":"invalidate_agent_credential_candidate","principal_id":"` + principalID + `","credential_id":"` + credentialID + `","principal_revision":2,"credential_revision":1}}`
	require.NoError(t, os.WriteFile(layout.MutationMarker, []byte(marker), 0o600))
	require.NoError(t, ownership.Close())
	result, err := runner.Run(ctx, binary, "storage", "verify", "--data-dir", root, "--json")
	require.NoError(t, err, "%s", result.Stderr)
	assertSettledResult(t, result)
	assert.Empty(t, result.Stderr)
	assert.JSONEq(t, `{"ok":true,"operation":"storage_verify","installation_id":"`+installationID+`","revision":"0"}`, string(result.Stdout))
	_, err = os.Lstat(layout.MutationMarker)
	assert.ErrorIs(t, err, os.ErrNotExist)
	ownership, err = gatewaypaths.Acquire(root)
	require.NoError(t, err)
	defer func() { require.NoError(t, ownership.Close()) }()
	store, err = storage.Open(ctx, ownership)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	var revision, credentialRevision int
	var slot sql.NullString
	require.NoError(t, store.View(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT revision, credential_revision, credential_id FROM principals WHERE id = ?`, principalID).Scan(&revision, &credentialRevision, &slot)
	}))
	assert.Equal(t, 3, revision)
	assert.Equal(t, 2, credentialRevision)
	assert.False(t, slot.Valid)
}
