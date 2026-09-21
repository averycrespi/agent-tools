package invocation

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrafficPairedSnapshotContinuityAndWriterRelease(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var afterPinned func() error
	traffic, owner := trafficFixture(t, nil, func(point string) error {
		if point == "snapshots_pinned" {
			return afterPinned()
		}
		return nil
	})
	control, err := storage.Initialize(ctx, owner, invocationTestInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, control.Close()) }()
	require.NoError(t, control.SelectTraffic(ctx, "", invocationID(90)))
	receipt, err := traffic.Admit(ctx, trafficPrepared(1))
	require.NoError(t, err)
	require.True(t, traffic.Confirm(ctx, receipt))
	afterPinned = func() error {
		if err := control.Mutate(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE gateway_meta SET revision=revision+1 WHERE singleton=1`)
			return err
		}); err != nil {
			return err
		}
		return traffic.Complete(ctx, receipt, trafficCompletion())
	}
	root := t.TempDir()
	controlPath, trafficPath := filepath.Join(root, "control.db"), filepath.Join(root, "traffic.db")
	require.NoError(t, traffic.BackupPair(ctx, control, controlPath, trafficPath))
	identity, err := storage.VerifyBackup(ctx, controlPath)
	require.NoError(t, err)
	assert.EqualValues(t, 0, identity.Revision, "snapshot precedes the later control mutation")
	assert.Equal(t, invocationID(90), identity.TrafficGeneration)
	require.NoError(t, VerifyTrafficFile(ctx, trafficPath, invocationTestInstallationID, invocationID(90), traffic.config))
	db, err := sql.Open("sqlite3", "file:"+trafficPath+"?mode=ro&immutable=1")
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	var completed sql.NullString
	require.NoError(t, db.QueryRowContext(ctx, `SELECT completed_at FROM invocations`).Scan(&completed))
	assert.False(t, completed.Valid, "later completion must not enter a previously pinned paired snapshot")
	history, err := traffic.History(ctx, 0, 10)
	require.NoError(t, err)
	require.NotNil(t, history.Records[0].CompletedAt)
}

func TestTrafficSnapshotPauseOverrunSettlesWithoutLatching(t *testing.T) {
	traffic, owner := trafficFixture(t, nil, func(point string) error {
		if point == "snapshot_fence" {
			timer := time.NewTimer(1050 * time.Millisecond)
			defer timer.Stop()
			<-timer.C
		}
		return nil
	})
	control, err := storage.Initialize(t.Context(), owner, invocationTestInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, control.Close()) }()
	root := t.TempDir()
	err = traffic.BackupPair(t.Context(), control, filepath.Join(root, "control"), filepath.Join(root, "traffic"))
	require.ErrorIs(t, err, ErrTrafficDeadline)
	assert.True(t, traffic.Healthy())
	assert.False(t, control.Latched())
	require.NoError(t, control.Mutate(t.Context(), func(tx *sql.Tx) error { _, err := tx.Exec(`UPDATE gateway_meta SET revision=revision+1`); return err }))
	receipt, err := traffic.Admit(t.Context(), trafficPrepared(1))
	require.NoError(t, err)
	traffic.Release(receipt)
}

func TestTrafficPinnedSnapshotPressureRefusesWithoutWaiting(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var afterPinned func() error
	traffic, owner := trafficFixture(t, func(config *TrafficConfig) { config.BudgetBytes = 1 << 20 }, func(point string) error {
		if point == "snapshots_pinned" {
			return afterPinned()
		}
		return nil
	})
	control, err := storage.Initialize(ctx, owner, invocationTestInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, control.Close()) }()
	require.NoError(t, control.SelectTraffic(ctx, "", invocationID(90)))
	afterPinned = func() error {
		for id := 1; id <= 100; id++ {
			started := time.Now()
			receipt, err := traffic.Admit(ctx, trafficPrepared(id))
			if err != nil {
				require.ErrorIs(t, err, ErrTrafficCapacity)
				assert.Less(t, time.Since(started), time.Second)
				assert.True(t, traffic.Healthy())
				assert.False(t, control.Latched())
				return nil
			}
			traffic.Release(receipt)
		}
		t.Fatal("fixture did not reach pinned WAL pressure")
		return nil
	}
	root := t.TempDir()
	require.NoError(t, traffic.BackupPair(ctx, control, filepath.Join(root, "control"), filepath.Join(root, "traffic")))
	receipt, err := traffic.Admit(ctx, trafficPrepared(101))
	require.NoError(t, err, "released snapshot permits checkpoint and subsequent admission")
	traffic.Release(receipt)
}

func TestTrafficPairedSnapshotRefusalDoesNotLatch(t *testing.T) {
	traffic, owner := trafficFixture(t, nil, nil)
	control, err := storage.Initialize(t.Context(), owner, invocationTestInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, control.Close()) }()
	traffic.admissionGate.RLock()
	err = traffic.BackupPair(t.Context(), control, filepath.Join(t.TempDir(), "control"), filepath.Join(t.TempDir(), "traffic"))
	traffic.admissionGate.RUnlock()
	require.ErrorIs(t, err, ErrTrafficCapacity)
	assert.True(t, traffic.Healthy())
	assert.False(t, control.Latched())
	receipt, err := traffic.Admit(t.Context(), trafficPrepared(1))
	require.NoError(t, err)
	traffic.Release(receipt)
}
