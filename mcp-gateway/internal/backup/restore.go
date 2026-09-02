package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/grantrequests"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
)

type restoreFaultPoint string

const (
	restoreFaultAfterCopy         restoreFaultPoint = "after_copy"
	restoreFaultAfterMigration    restoreFaultPoint = "after_migration"
	restoreFaultAfterInvalidation restoreFaultPoint = "after_invalidation"
	restoreFaultAfterRekey        restoreFaultPoint = "after_rekey"
	restoreFaultAfterCheckpoint   restoreFaultPoint = "after_checkpoint"
	restoreFaultBeforeInstall     restoreFaultPoint = "before_install"
)

type RestoreOptions struct {
	Root     string
	BackupID string
	Sink     admin.SecretSink
	Clock    admin.Clock
	Entropy  io.Reader
	fault    func(restoreFaultPoint) error
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
	if err := copyOwnerOnly(filepath.Join(layout.Backups, options.BackupID, databaseFile), staged); err != nil {
		return storage.Identity{}, err
	}
	if err := injectRestoreFault(options.fault, restoreFaultAfterCopy); err != nil {
		return storage.Identity{}, err
	}
	replacement, err := storage.OpenReplacement(ctx, ownership, staged)
	if err != nil {
		return storage.Identity{}, err
	}
	if err := injectRestoreFault(options.fault, restoreFaultAfterMigration); err != nil {
		_ = replacement.Close()
		return storage.Identity{}, err
	}
	closed := false
	defer func() {
		if !closed {
			_ = replacement.Close()
		}
	}()
	targets, authority, err := newRestoreValidationOwners(replacement, options.Clock, options.Entropy)
	if err != nil {
		return storage.Identity{}, err
	}
	if err := grantrequests.ValidateStartup(ctx, replacement, authority, targets); err != nil {
		return storage.Identity{}, err
	}
	if err := authorization.InvalidateStagedCredentials(ctx, replacement, targets); err != nil {
		return storage.Identity{}, err
	}
	if err := injectRestoreFault(options.fault, restoreFaultAfterInvalidation); err != nil {
		return storage.Identity{}, err
	}
	service := admin.NewService(replacement, options.Clock, options.Entropy)
	if _, err := service.Reset(ctx, options.Sink); err != nil {
		return storage.Identity{}, err
	}
	if err := injectRestoreFault(options.fault, restoreFaultAfterRekey); err != nil {
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
	if err := injectRestoreFault(options.fault, restoreFaultAfterCheckpoint); err != nil {
		return storage.Identity{}, err
	}
	if err := replacement.Close(); err != nil {
		return storage.Identity{}, err
	}
	closed = true
	if _, err := storage.VerifyBackup(ctx, staged); err != nil {
		return storage.Identity{}, err
	}
	if err := verifyReplacementDomains(ctx, ownership, staged, options.Clock, options.Entropy); err != nil {
		return storage.Identity{}, err
	}
	if err := injectRestoreFault(options.fault, restoreFaultBeforeInstall); err != nil {
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

func verifyReplacementDomains(
	ctx context.Context,
	ownership *gatewaypaths.Ownership,
	staged string,
	clock admin.Clock,
	entropy io.Reader,
) error {
	replacement, err := storage.OpenReplacement(ctx, ownership, staged)
	if err != nil {
		return err
	}
	targets, authority, validationErr := newRestoreValidationOwners(replacement, clock, entropy)
	if validationErr == nil {
		validationErr = authority.ValidateStartup(ctx, targets)
	}
	if validationErr == nil {
		validationErr = grantrequests.ValidateStartup(ctx, replacement, authority, targets)
	}
	closeErr := replacement.Close()
	if validationErr != nil {
		return validationErr
	}
	if closeErr != nil {
		return closeErr
	}
	_, err = storage.VerifyBackup(ctx, staged)
	return err
}

func newRestoreValidationOwners(store *storage.Store, clock admin.Clock, entropy io.Reader) (*servers.Repository, *authorization.Repository, error) {
	targets, err := servers.New(store, clock, entropy)
	if err != nil {
		return nil, nil, err
	}
	authority, err := authorization.New(store, clock, entropy)
	if err != nil {
		return nil, nil, err
	}
	return targets, authority, nil
}

func injectRestoreFault(fault func(restoreFaultPoint) error, point restoreFaultPoint) error {
	if fault == nil {
		return nil
	}
	if err := fault(point); err != nil {
		return fmt.Errorf("restore fault at %s: %w", point, err)
	}
	return nil
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
