package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/invocation"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

// New creation is security-only. Legacy artifacts remain explicit fixtures so
// verification and import tests cannot silently become format-3-only coverage.
func legacyArtifact(t *testing.T, manager *Manager, traffic *invocation.TrafficStore) contract.Backup {
	t.Helper()
	id, err := admin.NewID(manager.clock.Now(), manager.entropy)
	require.NoError(t, err)
	directory := filepath.Join(manager.layout.Backups, id)
	require.NoError(t, os.Mkdir(directory, 0700))
	controlPath := filepath.Join(directory, databaseFile)
	if traffic == nil {
		require.NoError(t, manager.store.BackupTo(t.Context(), controlPath))
	} else {
		require.NoError(t, traffic.BackupPair(t.Context(), manager.store, controlPath, filepath.Join(directory, "traffic.db")))
	}
	identity, err := storage.VerifyBackup(t.Context(), controlPath)
	require.NoError(t, err)
	info, err := os.Stat(controlPath)
	require.NoError(t, err)
	digest, err := digestFile(controlPath)
	require.NoError(t, err)
	metadata := artifactMetadata{Backup: contract.Backup{ID: id, CreatedAt: manager.clock.Now().UTC().Format(time.RFC3339Nano), InstallationID: identity.InstallationID, SchemaVersion: fmt.Sprint(identity.SchemaVersion), SourceRevision: fmt.Sprint(identity.Revision), SizeBytes: info.Size(), SHA256: digest}, AuthorityHash: digestText("authority"), KeyHash: digestText("legacy-fixture"), InputHash: digestText("{}")}
	if traffic != nil {
		metadata.Format, metadata.TrafficGeneration, metadata.TrafficBudgetBytes = 2, identity.TrafficGeneration, traffic.BudgetBytes()
		metadata.TrafficSHA256, err = digestFile(filepath.Join(directory, "traffic.db"))
		require.NoError(t, err)
		info, err = os.Stat(filepath.Join(directory, "traffic.db"))
		require.NoError(t, err)
		metadata.TrafficSizeBytes = info.Size()
	}
	require.NoError(t, writeMetadata(filepath.Join(directory, metadataFile), metadata))
	_, err = manager.Get(t.Context(), id)
	require.NoError(t, err)
	return metadata.Backup
}
