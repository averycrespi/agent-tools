package backup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const backupTestInstallationID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

type fixedClock struct{ value time.Time }

func (clock fixedClock) Now() time.Time { return clock.value }

func newBackupManager(t *testing.T, fault func(FaultPoint) error) (*Manager, *storage.Store, *gatewaypaths.Ownership) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err := storage.Initialize(context.Background(), ownership, backupTestInstallationID)
	require.NoError(t, err)
	manager, err := New(Options{
		Store: store, Layout: ownership.Layout(),
		Clock:   fixedClock{value: time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)},
		Entropy: bytes.NewReader(bytes.Repeat([]byte{0x42}, 1024)), Fault: fault,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = store.Close()
		_ = ownership.Close()
	})
	return manager, store, ownership
}

func TestCurrentSchemaBackupCompatibility(t *testing.T) {
	manager, _, ownership := newBackupManager(t, nil)
	created, replay, err := manager.Create(context.Background(), "authority", "schema-eleven")
	require.NoError(t, err)
	assert.False(t, replay)
	assert.Equal(t, "11", created.SchemaVersion)
	identity, err := storage.VerifyBackup(context.Background(), filepath.Join(ownership.Layout().Backups, created.ID, databaseFile))
	require.NoError(t, err)
	assert.Equal(t, 11, identity.SchemaVersion)
}

func TestCreatePublishesVerifiedOwnerOnlyGeneration(t *testing.T) {
	manager, _, ownership := newBackupManager(t, nil)
	created, replay, err := manager.Create(context.Background(), "authority", "retry-1")
	require.NoError(t, err)
	assert.False(t, replay)
	assert.Equal(t, backupTestInstallationID, created.InstallationID)
	assert.Equal(t, strconv.Itoa(storage.CurrentSchema), created.SchemaVersion)
	assert.NotEmpty(t, created.SHA256)
	assert.Positive(t, created.SizeBytes)

	items, err := manager.List(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, created, items[0])

	generation := filepath.Join(ownership.Layout().Backups, created.ID)
	for _, path := range []string{generation, filepath.Join(generation, databaseFile), filepath.Join(generation, metadataFile)} {
		info, statErr := os.Stat(path)
		require.NoError(t, statErr)
		if info.IsDir() {
			assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
		} else {
			assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		}
	}
}

func TestCreateFailureNeverPublishesArtifact(t *testing.T) {
	manager, _, ownership := newBackupManager(t, func(point FaultPoint) error {
		if point == FaultPublish {
			return assert.AnError
		}
		return nil
	})
	_, _, err := manager.Create(context.Background(), "authority", "retry-1")
	require.Error(t, err)
	items, listErr := manager.List(context.Background())
	require.NoError(t, listErr)
	assert.Empty(t, items)
	entries, readErr := os.ReadDir(ownership.Layout().Backups)
	require.NoError(t, readErr)
	assert.Empty(t, entries)
}

func TestIdempotencyReplayExpiresAfterFixedRetention(t *testing.T) {
	manager, store, ownership := newBackupManager(t, nil)
	first, replay, err := manager.Create(context.Background(), "authority", "retry-1")
	require.NoError(t, err)
	assert.False(t, replay)
	replayed, replay, err := manager.Create(context.Background(), "authority", "retry-1")
	require.NoError(t, err)
	assert.True(t, replay)
	assert.Equal(t, first.ID, replayed.ID)

	later, err := New(Options{
		Store: store, Layout: ownership.Layout(),
		Clock:   fixedClock{value: time.Date(2026, 8, 24, 12, 0, 1, 0, time.UTC)},
		Entropy: bytes.NewReader(bytes.Repeat([]byte{0x43}, 1024)),
	})
	require.NoError(t, err)
	created, replay, err := later.Create(context.Background(), "authority", "retry-1")
	require.NoError(t, err)
	assert.False(t, replay)
	assert.NotEqual(t, first.ID, created.ID)
}

func TestCreateRejectsConcurrentWorkWithoutStartingIt(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	manager, _, _ := newBackupManager(t, func(point FaultPoint) error {
		if point == FaultCopy {
			once.Do(func() { close(entered) })
			<-release
		}
		return nil
	})
	done := make(chan error, 1)
	go func() {
		_, _, err := manager.Create(context.Background(), "authority", "first")
		done <- err
	}()
	<-entered
	status := manager.WorkStatus()
	assert.Equal(t, int64(1), status.InUse)
	assert.True(t, status.Saturated)
	_, _, err := manager.Create(context.Background(), "authority", "second")
	assert.ErrorIs(t, err, ErrResourceLimit)
	close(release)
	require.NoError(t, <-done)
}

func TestListRejectsTamperedGeneration(t *testing.T) {
	manager, _, ownership := newBackupManager(t, nil)
	created, _, err := manager.Create(context.Background(), "authority", "retry-1")
	require.NoError(t, err)
	file, err := os.OpenFile(filepath.Join(ownership.Layout().Backups, created.ID, databaseFile), os.O_WRONLY|os.O_APPEND, 0)
	require.NoError(t, err)
	_, err = file.Write([]byte("tamper"))
	require.NoError(t, err)
	require.NoError(t, file.Close())
	_, err = manager.List(context.Background())
	assert.ErrorIs(t, err, ErrInvalidArtifact)
}
