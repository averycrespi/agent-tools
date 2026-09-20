package backup

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/invocation"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestPairedBackupRejectsResidualControlInvocations(t *testing.T) {
	manager, control, owner := newBackupManager(t, nil)
	generation := "01ARZ3NDEKTSV4RRFFQ69G5FA0"
	traffic, err := invocation.CreateTraffic(t.Context(), owner, backupTestInstallationID, generation, invocation.DefaultTrafficConfig())
	require.NoError(t, err)
	defer func() { require.NoError(t, traffic.Close()) }()
	require.NoError(t, control.SelectTraffic(t.Context(), "", generation))
	manager.traffic = traffic
	created, _, err := manager.Create(t.Context(), "authority", "paired-validation")
	require.NoError(t, err)
	_, err = manager.Get(t.Context(), created.ID)
	require.NoError(t, err)
	items, err := manager.List(t.Context())
	require.NoError(t, err)
	require.Len(t, items, 1)

	root := filepath.Join(owner.Layout().Backups, created.ID)
	path := filepath.Join(root, databaseFile)
	database, err := sql.Open("sqlite3", "file:"+path+"?mode=rw")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `INSERT INTO invocations
		(id,principal_id,credential_id,credential_fingerprint,credential_revision,admitted_at,admission_class)
		VALUES(?,?,?,'0123456789abcdef',1,'2026-08-22T12:00:00.000000000Z','invalid_params')`, backupTestInstallationID, backupTestInstallationID, backupTestInstallationID)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `PRAGMA wal_checkpoint(TRUNCATE)`)
	require.NoError(t, err)
	require.NoError(t, database.Close())
	// Make the closed artifact digest-consistent, so only semantic validation
	// can reject the residual control evidence, not a stale checksum or size.
	metadataPath := filepath.Join(root, metadataFile)
	data, err := os.ReadFile(metadataPath)
	require.NoError(t, err)
	var metadata artifactMetadata
	require.NoError(t, json.Unmarshal(data, &metadata))
	metadata.SHA256, err = digestFile(path)
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	metadata.SizeBytes = info.Size()
	data, err = json.Marshal(metadata)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(metadataPath, data, 0o600))
	_, err = storage.VerifyBackup(t.Context(), path)
	require.ErrorIs(t, err, storage.ErrInvalidDatabase)
	_, err = manager.Get(t.Context(), created.ID)
	require.ErrorIs(t, err, ErrInvalidArtifact)
	_, err = manager.List(t.Context())
	require.ErrorIs(t, err, ErrInvalidArtifact)
}
