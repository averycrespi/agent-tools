package backup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/invocation"
	"github.com/stretchr/testify/require"
)

func TestAccountingTracksCreationDeletionAndRetention(t *testing.T) {
	manager, _, owner := newBackupManager(t, nil)
	check := func(records, retries int64) {
		t.Helper()
		actual, retained, err := manager.AccountingStatus(t.Context())
		require.NoError(t, err)
		require.Equal(t, records, actual.InUse)
		require.Equal(t, retries, retained.InUse)
		limit, _ := contract.FixedLimitByName("backup_records")
		require.Equal(t, limit.Maximum, actual.Limit)
		require.Equal(t, records >= actual.Limit, actual.Saturated)
	}
	check(0, 0)
	require.NoError(t, os.Mkdir(filepath.Join(owner.Layout().Backups, ".in-progress.staging"), 0o700))
	check(0, 0)
	created, _, err := manager.Create(t.Context(), "authority", "accounting")
	require.NoError(t, err)
	check(1, 1)
	now := manager.clock.Now()
	manager.clock = fixedClock{value: now.Add(contract.IdempotencyRetention)}
	check(1, 1)
	manager.clock = fixedClock{value: now.Add(contract.IdempotencyRetention + time.Nanosecond)}
	check(1, 0)
	require.NoError(t, manager.Delete(t.Context(), created.ID))
	check(0, 0)
}

func TestAccountingNeverReadsDatabaseContentsAndRetainsVerification(t *testing.T) {
	for _, paired := range []bool{false, true} {
		name := "legacy"
		if paired {
			name = "paired"
		}
		t.Run(name, func(t *testing.T) {
			manager, control, owner := newBackupManager(t, nil)
			if paired {
				generation := "01ARZ3NDEKTSV4RRFFQ69G5FA0"
				traffic, err := invocation.CreateTraffic(t.Context(), owner, backupTestInstallationID, generation, invocation.DefaultTrafficConfig())
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, traffic.Close()) })
				require.NoError(t, control.SelectTraffic(t.Context(), "", generation))
				manager.traffic = traffic
			}
			created, _, err := manager.Create(t.Context(), "authority", "no-content")
			require.NoError(t, err)
			directory := filepath.Join(owner.Layout().Backups, created.ID)
			// Preserve metadata, but remove every database: any SQLite/hash/content path
			// must fail, while metadata occupancy must still report the artifact.
			require.NoError(t, os.Remove(filepath.Join(directory, databaseFile)))
			if paired {
				require.NoError(t, os.Remove(filepath.Join(directory, "traffic.db")))
			}
			records, retries, err := manager.AccountingStatus(t.Context())
			require.NoError(t, err)
			require.Equal(t, int64(1), records.InUse)
			require.Equal(t, int64(1), retries.InUse)
			_, err = manager.List(t.Context())
			require.ErrorIs(t, err, ErrInvalidArtifact)
			_, err = manager.Get(t.Context(), created.ID)
			require.ErrorIs(t, err, ErrInvalidArtifact)
			require.ErrorIs(t, manager.Delete(t.Context(), created.ID), ErrInvalidArtifact)
			_, _, err = manager.Create(t.Context(), "authority", "no-content")
			require.ErrorIs(t, err, ErrInvalidArtifact)
		})
	}
}

func TestAccountingReportsSaturationFromMetadata(t *testing.T) {
	manager, _, owner := newBackupManager(t, nil)
	created, _, err := manager.Create(t.Context(), "authority", "saturation")
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(owner.Layout().Backups, created.ID, metadataFile))
	require.NoError(t, err)
	var metadata artifactMetadata
	require.NoError(t, json.Unmarshal(data, &metadata))
	require.NoError(t, manager.Delete(t.Context(), created.ID))
	limit, _ := contract.FixedLimitByName("backup_records")
	for index := int64(0); index < limit.Maximum; index++ {
		metadata.ID = fmt.Sprintf("%s%02d", created.ID[:24], index)
		directory := filepath.Join(owner.Layout().Backups, metadata.ID)
		require.NoError(t, os.Mkdir(directory, 0o700))
		require.NoError(t, writeMetadata(filepath.Join(directory, metadataFile), metadata))
	}
	records, retries, err := manager.AccountingStatus(t.Context())
	require.NoError(t, err)
	require.Equal(t, limit.Maximum, records.InUse)
	require.True(t, records.Saturated)
	require.Equal(t, limit.Maximum, retries.InUse)
	retryLimit, _ := contract.FixedLimitByName("idempotency_records")
	require.Equal(t, retryLimit.Maximum, retries.Limit)
	require.Equal(t, retries.InUse >= retries.Limit, retries.Saturated)
}

func TestAccountingRejectsUnsafeRequiredMetadata(t *testing.T) {
	manager, _, owner := newBackupManager(t, nil)
	created, _, err := manager.Create(t.Context(), "authority", "metadata-errors")
	require.NoError(t, err)
	path := filepath.Join(owner.Layout().Backups, created.ID, metadataFile)
	original, err := os.ReadFile(path)
	require.NoError(t, err)
	cases := map[string]func(){
		"missing":            func() { require.NoError(t, os.Remove(path)) },
		"unreadable":         func() { require.NoError(t, os.Chmod(path, 0)) },
		"unsafe permissions": func() { require.NoError(t, os.Chmod(path, 0o644)) },
		"symlink":            func() { require.NoError(t, os.Remove(path)); require.NoError(t, os.Symlink("absent", path)) },
		"malformed":          func() { require.NoError(t, os.WriteFile(path, []byte("{"), 0o600)) },
		"oversized":          func() { require.NoError(t, os.WriteFile(path, []byte(strings.Repeat(" ", 8193)), 0o600)) },
		"duplicate": func() {
			require.NoError(t, os.WriteFile(path, append([]byte(`{"id":"duplicate",`), original[1:]...), 0o600))
		},
		"timestamp": func() {
			var metadata artifactMetadata
			require.NoError(t, json.Unmarshal(original, &metadata))
			metadata.CreatedAt = "invalid"
			data, err := json.Marshal(metadata)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, data, 0o600))
		},
	}
	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			corrupt()
			records, retries, err := manager.AccountingStatus(t.Context())
			require.ErrorIs(t, err, ErrInvalidArtifact)
			require.Equal(t, contract.LimitStatus{}, records)
			require.Equal(t, contract.LimitStatus{}, retries)
			require.NoError(t, os.RemoveAll(path))
			require.NoError(t, os.WriteFile(path, original, 0o600))
		})
	}
}
