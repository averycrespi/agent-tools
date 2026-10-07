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
	Backup         contract.Backup `json:"backup"`
	MasterKeyID    string          `json:"master_key_id,omitempty"`
	History        string          `json:"history"`
	TargetKeyID    string          `json:"target_key_id,omitempty"`
	NewEncryptions int64           `json:"new_encryptions,omitempty"`
	artifact       artifactMetadata
	securityOnly   bool
	recoveryKey    string
	custody        restoreCustody
}

func InspectRestore(ctx context.Context, owner *gatewaypaths.Ownership, id string) (RestoreInspection, error) {
	return InspectRestoreScope(ctx, owner, id, false)
}

func InspectRestoreScope(ctx context.Context, owner *gatewaypaths.Ownership, id string, securityOnly bool) (RestoreInspection, error) {
	return InspectRestoreWithKey(ctx, owner, id, securityOnly, "")
}

// InspectRestoreWithKey accepts a decrypt-only historical key for cross-key recovery.
func InspectRestoreWithKey(ctx context.Context, owner *gatewaypaths.Ownership, id string, securityOnly bool, recoveryKey string) (RestoreInspection, error) {
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
	metadata, err := (&Manager{layout: layout}).readArtifactScope(ctx, filepath.Join(layout.Backups, id), id, securityOnly)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return RestoreInspection{}, ErrNotFound
		}
		return RestoreInspection{}, err
	}
	custody, err := requireRestoreCustody(ctx, owner, metadata, recoveryKey)
	if err != nil {
		return RestoreInspection{}, err
	}
	if _, _, err := inspectRestoreCertificate(ctx, owner, metadata); err != nil {
		return RestoreInspection{}, err
	}
	history := "restored"
	if securityOnly || metadata.Format >= 3 {
		history = "omitted-not-verified"
	}
	return RestoreInspection{Backup: metadata.Backup, MasterKeyID: metadata.MasterKeyID, History: history, TargetKeyID: custody.Current.KeyID, NewEncryptions: custody.RewrapCount, artifact: metadata, securityOnly: securityOnly, recoveryKey: recoveryKey, custody: custody}, nil
}

func requireClosedArtifactScope(directory string, securityOnly bool) error {
	names := []string{databaseFile}
	if !securityOnly {
		names = append(names, "traffic.db")
	}
	for _, name := range names {
		if err := storage.RequireClosedGeneration(filepath.Join(directory, name)); err != nil {
			return errors.Join(ErrInvalidArtifact, err)
		}
	}
	return nil
}

func verifyNoRestoreStaging(layout gatewaypaths.Layout) error {
	staged := layout.Database + ".restore"
	for _, path := range []string{staged, staged + "-wal", staged + "-shm", staged + "-journal", staged + ".mutation", staged + ".mutation.tmp", staged + ".mutation.cleared"} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return storage.ErrPlanChanged
		}
	}
	return nil
}

func (expected RestoreInspection) Revalidate(ctx context.Context, owner *gatewaypaths.Ownership) error {
	current, err := InspectRestoreWithKey(ctx, owner, expected.Backup.ID, expected.securityOnly, expected.recoveryKey)
	if err != nil {
		return err
	}
	if current != expected {
		return storage.ErrPlanChanged
	}
	return nil
}
