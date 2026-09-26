package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

// RestoreInspection exposes verified safe metadata, never idempotency authority.
type RestoreInspection struct {
	Backup   contract.Backup `json:"backup"`
	artifact artifactMetadata
}

func InspectRestore(ctx context.Context, owner *gatewaypaths.Ownership, id string) (RestoreInspection, error) {
	if !ValidID(id) {
		return RestoreInspection{}, ErrInvalidArtifact
	}
	layout, err := owner.ActiveLayout()
	if err != nil {
		return RestoreInspection{}, err
	}
	if err := verifyNoRestoreStaging(layout); err != nil {
		return RestoreInspection{}, err
	}
	metadata, err := (&Manager{layout: layout}).readArtifact(ctx, filepath.Join(layout.Backups, id), id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RestoreInspection{}, ErrNotFound
		}
		return RestoreInspection{}, err
	}
	return RestoreInspection{Backup: metadata.Backup, artifact: metadata}, nil
}

func requireClosedArtifact(directory string) error {
	for _, name := range []string{databaseFile, "traffic.db"} {
		if err := storage.RequireClosedGeneration(filepath.Join(directory, name)); err != nil {
			return errors.Join(ErrInvalidArtifact, err)
		}
	}
	return nil
}

func verifyNoRestoreStaging(layout gatewaypaths.Layout) error {
	staged := layout.Database + ".restore"
	for _, path := range []string{staged, staged + "-wal", staged + "-shm", staged + ".mutation", staged + ".mutation.tmp", staged + ".mutation.cleared"} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return storage.ErrPlanChanged
		}
	}
	return nil
}

func (expected RestoreInspection) Revalidate(ctx context.Context, owner *gatewaypaths.Ownership) error {
	current, err := InspectRestore(ctx, owner, expected.Backup.ID)
	if err != nil {
		return err
	}
	if current != expected {
		return storage.ErrPlanChanged
	}
	return nil
}
