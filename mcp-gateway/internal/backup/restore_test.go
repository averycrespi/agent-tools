package backup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/admin"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type captureSink struct{ bearer string }

func (sink *captureSink) Publish(value string) error {
	sink.bearer = value
	return nil
}

func TestRestoreReplacesCompleteGenerationAndRekeysAdminAuthority(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err := storage.Initialize(ctx, ownership, backupTestInstallationID)
	require.NoError(t, err)
	clock := fixedClock{value: time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)}
	initialSink := new(captureSink)
	_, err = admin.NewService(store, clock, bytes.NewReader(bytes.Repeat([]byte{0x11}, 256))).Initialize(ctx, initialSink)
	require.NoError(t, err)
	manager, err := New(Options{Store: store, Layout: ownership.Layout(), Clock: clock, Entropy: bytes.NewReader(bytes.Repeat([]byte{0x22}, 256))})
	require.NoError(t, err)
	artifact, _, err := manager.Create(ctx, "authority", "restore-fixture")
	require.NoError(t, err)
	artifactBytes, err := os.ReadFile(filepath.Join(ownership.Layout().Backups, artifact.ID, databaseFile))
	require.NoError(t, err)
	assert.NotContains(t, string(artifactBytes), initialSink.bearer)
	laterSink := new(captureSink)
	_, err = admin.NewService(store, clock, bytes.NewReader(bytes.Repeat([]byte{0x33}, 256))).Reset(ctx, laterSink)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	require.NoError(t, ownership.Close())

	for _, suffix := range []string{"-wal", "-shm"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, gatewaypaths.DatabaseName)+suffix, []byte("stale"), 0o600))
	}
	replacementSink := new(captureSink)
	identity, err := Restore(ctx, RestoreOptions{
		Root: root, BackupID: artifact.ID, Sink: replacementSink, Clock: clock,
		Entropy: bytes.NewReader(bytes.Repeat([]byte{0x44}, 256)),
	})
	require.NoError(t, err)
	assert.Equal(t, backupTestInstallationID, identity.InstallationID)
	assert.NotEmpty(t, replacementSink.bearer)
	for _, suffix := range []string{"-wal", "-shm"} {
		_, statErr := os.Lstat(filepath.Join(root, gatewaypaths.DatabaseName) + suffix)
		assert.ErrorIs(t, statErr, os.ErrNotExist)
	}

	ownership, err = gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err = storage.Open(ctx, ownership)
	require.NoError(t, err)
	service := admin.NewService(store, clock, bytes.NewReader(nil))
	_, err = service.Authenticate(ctx, replacementSink.bearer)
	require.NoError(t, err)
	_, err = service.Authenticate(ctx, initialSink.bearer)
	assert.ErrorIs(t, err, admin.ErrAuthenticationRequired)
	_, err = service.Authenticate(ctx, laterSink.bearer)
	assert.ErrorIs(t, err, admin.ErrAuthenticationRequired)
	require.NoError(t, store.Close())
	require.NoError(t, ownership.Close())
}

func TestRestoreRefusesRunningOrTamperedArtifact(t *testing.T) {
	ctx := context.Background()
	manager, store, ownership := newBackupManager(t, nil)
	artifact, _, err := manager.Create(ctx, "authority", "restore-fixture")
	require.NoError(t, err)
	_, err = Restore(ctx, RestoreOptions{Root: ownership.Layout().Root, BackupID: artifact.ID, Sink: new(captureSink), Clock: fixedClock{time.Now()}, Entropy: bytes.NewReader(bytes.Repeat([]byte{1}, 256))})
	assert.ErrorIs(t, err, gatewaypaths.ErrInUse)

	require.NoError(t, store.Close())
	require.NoError(t, ownership.Close())
	file, err := os.OpenFile(filepath.Join(ownership.Layout().Backups, artifact.ID, databaseFile), os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = file.Write([]byte("tamper"))
	require.NoError(t, err)
	require.NoError(t, file.Close())
	_, err = Restore(ctx, RestoreOptions{Root: ownership.Layout().Root, BackupID: artifact.ID, Sink: new(captureSink), Clock: fixedClock{time.Now()}, Entropy: bytes.NewReader(bytes.Repeat([]byte{1}, 256))})
	assert.ErrorIs(t, err, ErrInvalidArtifact)
}
