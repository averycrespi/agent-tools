package invocation

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"sync/atomic"
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
	observation := recordMCP(t, traffic, trafficPrepared(1))
	afterPinned = func() error {
		if err := control.Mutate(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE gateway_meta SET revision=revision+1 WHERE singleton=1`)
			return err
		}); err != nil {
			return err
		}
		err := traffic.ObserveMCPCompletion(observation, trafficCompletion(), nil)
		waitTraffic(t, traffic)
		return err
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

func TestTrafficVisibleTerminalPrecedesWriterSettlement(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var block atomic.Bool
	committed, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	traffic, owner := trafficFixture(t, nil, func(point string) error {
		if point == "acknowledgment" && block.Load() {
			close(committed)
			<-release
		}
		return nil
	})
	t.Cleanup(unblock)
	control, err := storage.Initialize(ctx, owner, invocationTestInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, control.Close()) }()
	require.NoError(t, control.SelectTraffic(ctx, "", invocationID(90)))
	observation := recordMCP(t, traffic, trafficPrepared(1))
	block.Store(true)
	require.NoError(t, traffic.ObserveMCPCompletion(observation, trafficCompletion(), nil))
	select {
	case <-committed:
	case <-ctx.Done():
		t.Fatal("terminal did not reach the post-commit barrier")
	}
	history, err := traffic.History(ctx, 0, 10)
	require.NoError(t, err)
	require.Len(t, history.Records, 1)
	require.NotNil(t, history.Records[0].CompletedAt)
	root := t.TempDir()
	// This intentionally refused probe demonstrates why a visible terminal is
	// not a backup readiness barrier. It is not a retry of an uncertain write.
	err = traffic.BackupPair(ctx, control, filepath.Join(root, "refused-control"), filepath.Join(root, "refused-traffic"))
	require.ErrorIs(t, err, ErrTrafficCapacity)
	unblock()
	require.NoError(t, traffic.Close())
	reopened, err := openTraffic(ctx, owner, invocationTestInstallationID, invocationID(90), traffic.config, false, nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	controlPath, trafficPath := filepath.Join(root, "control"), filepath.Join(root, "traffic")
	require.NoError(t, reopened.BackupPair(ctx, control, controlPath, trafficPath))
	db, err := sql.Open("sqlite3", "file:"+trafficPath+"?mode=ro&immutable=1")
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	var completed sql.NullString
	require.NoError(t, db.QueryRowContext(ctx, `SELECT completed_at FROM invocations`).Scan(&completed))
	require.True(t, completed.Valid, "the settled terminal survives into the backup")
	require.False(t, control.Latched())
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
	recordMCP(t, traffic, trafficPrepared(1))
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
			require.NotNil(t, traffic.ObserveMCP(trafficPrepared(id)))
			waitTraffic(t, traffic)
			traffic.mu.Lock()
			refused := traffic.quotaRefusals > 0
			traffic.mu.Unlock()
			if refused {
				assert.Less(t, time.Since(started), time.Second)
				assert.True(t, traffic.Healthy())
				assert.False(t, control.Latched())
				return nil
			}
		}
		t.Fatal("fixture did not reach pinned WAL pressure")
		return nil
	}
	root := t.TempDir()
	require.NoError(t, traffic.BackupPair(ctx, control, filepath.Join(root, "control"), filepath.Join(root, "traffic")))
	recordMCP(t, traffic, trafficPrepared(101))
}

func TestTrafficPairedSnapshotRefusalDoesNotLatch(t *testing.T) {
	traffic, owner := trafficFixture(t, nil, nil)
	control, err := storage.Initialize(t.Context(), owner, invocationTestInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, control.Close()) }()
	traffic.writerGate.Lock()
	err = traffic.BackupPair(t.Context(), control, filepath.Join(t.TempDir(), "control"), filepath.Join(t.TempDir(), "traffic"))
	traffic.writerGate.Unlock()
	require.ErrorIs(t, err, ErrTrafficCapacity)
	assert.True(t, traffic.Healthy())
	assert.False(t, control.Latched())
	recordMCP(t, traffic, trafficPrepared(1))
}
