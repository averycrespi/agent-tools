package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/admin"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
)

type RestoreOptions struct {
	Root     string
	BackupID string
	Sink     admin.SecretSink
	Clock    admin.Clock
	Entropy  io.Reader
}

// Restore validates and rekeys one complete backup generation while holding stopped-process ownership.
func Restore(ctx context.Context, options RestoreOptions) (storage.Identity, error) {
	if options.Sink == nil || options.Clock == nil || options.Entropy == nil || !backupIDPattern.MatchString(options.BackupID) {
		return storage.Identity{}, ErrInvalidArtifact
	}
	ownership, err := gatewaypaths.AcquireForMaintenance(options.Root)
	if err != nil {
		return storage.Identity{}, fmt.Errorf("acquire stopped-process ownership: %w", err)
	}
	defer func() { _ = ownership.Close() }()
	layout := ownership.Layout()
	manager := &Manager{layout: layout}
	artifact, err := manager.readArtifact(ctx, filepath.Join(layout.Backups, options.BackupID), options.BackupID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return storage.Identity{}, ErrNotFound
		}
		if errors.Is(err, ErrInvalidArtifact) {
			return storage.Identity{}, err
		}
		return storage.Identity{}, fmt.Errorf("%w: %w", ErrInvalidArtifact, err)
	}
	current, err := storage.InspectBaseIdentity(ctx, layout.Database)
	if err != nil {
		return storage.Identity{}, fmt.Errorf("inspect current installation: %w", err)
	}
	if current.InstallationID != artifact.InstallationID {
		return storage.Identity{}, ErrInvalidArtifact
	}

	staged := layout.Database + ".restore"
	_ = os.Remove(staged)
	_ = os.Remove(staged + "-wal")
	_ = os.Remove(staged + "-shm")
	if err := copyOwnerOnly(filepath.Join(layout.Backups, options.BackupID, databaseFile), staged); err != nil {
		return storage.Identity{}, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(staged)
			_ = os.Remove(staged + "-wal")
			_ = os.Remove(staged + "-shm")
			_ = os.Remove(staged + ".mutation")
			_ = os.Remove(staged + ".mutation.tmp")
			_ = os.Remove(staged + ".mutation.cleared")
		}
	}()
	replacement, err := storage.OpenReplacement(ctx, ownership, staged)
	if err != nil {
		return storage.Identity{}, err
	}
	closed := false
	defer func() {
		if !closed {
			_ = replacement.Close()
		}
	}()
	service := admin.NewService(replacement, options.Clock, options.Entropy)
	if _, err := service.Reset(ctx, options.Sink); err != nil {
		return storage.Identity{}, err
	}
	identity, err := replacement.Identity(ctx)
	if err != nil {
		return storage.Identity{}, err
	}
	if identity.InstallationID != current.InstallationID {
		return storage.Identity{}, ErrInvalidArtifact
	}
	if err := replacement.Checkpoint(ctx); err != nil {
		return storage.Identity{}, err
	}
	if err := replacement.Close(); err != nil {
		return storage.Identity{}, err
	}
	closed = true
	if _, err := storage.VerifyBackup(ctx, staged); err != nil {
		return storage.Identity{}, err
	}
	if err := storage.InstallReplacement(ownership, staged); err != nil {
		return storage.Identity{}, err
	}
	cleanup = false
	if err := storage.ClearVerifiedMarker(ownership, identity.InstallationID); err != nil {
		return storage.Identity{}, err
	}
	if err := ownership.MarkClean(); err != nil {
		return storage.Identity{}, err
	}
	return identity, nil
}

func copyOwnerOnly(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open backup generation: %w", err)
	}
	defer func() { _ = input.Close() }()
	output, err := gatewaypaths.CreateOwnerOnlyFile(destination)
	if err != nil {
		return fmt.Errorf("create replacement generation: %w", err)
	}
	completed := false
	defer func() {
		if !completed {
			_ = output.Close()
			_ = os.Remove(destination)
		}
	}()
	if _, err := io.Copy(output, input); err != nil {
		return fmt.Errorf("copy replacement generation: %w", err)
	}
	if err := output.Sync(); err != nil {
		return fmt.Errorf("sync replacement generation: %w", err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close replacement generation: %w", err)
	}
	completed = true
	return nil
}
