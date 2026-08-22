package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
)

// InspectBaseIdentity reads immutable installation identity without consulting stale WAL sidecars.
func InspectBaseIdentity(ctx context.Context, path string) (Identity, error) {
	if err := gatewaypaths.ValidateOwnerOnlyFile(path); err != nil {
		return Identity{}, err
	}
	uri := &url.URL{Scheme: "file", Path: path}
	query := uri.Query()
	query.Set("mode", "ro")
	query.Set("immutable", "1")
	uri.RawQuery = query.Encode()
	database, err := sql.Open("sqlite3", uri.String())
	if err != nil {
		return Identity{}, err
	}
	defer func() { _ = database.Close() }()
	var identity Identity
	var applicationID int
	var revision int64
	if err := database.QueryRowContext(ctx, `SELECT (SELECT application_id FROM pragma_application_id), (SELECT user_version FROM pragma_user_version), installation_id, revision FROM gateway_meta WHERE singleton = 1`).Scan(&applicationID, &identity.SchemaVersion, &identity.InstallationID, &revision); err != nil {
		return Identity{}, fmt.Errorf("read stopped installation identity: %w", err)
	}
	if applicationID != ApplicationID || identity.SchemaVersion != CurrentSchema || !installationIDPattern.MatchString(identity.InstallationID) || revision < 0 {
		return Identity{}, ErrInvalidDatabase
	}
	identity.Revision = uint64(revision)
	return identity, nil
}

// OpenReplacement opens and fully verifies a staged database generation under stopped-process ownership.
func OpenReplacement(ctx context.Context, ownership *gatewaypaths.Ownership, path string) (*Store, error) {
	layout, err := ownership.ActiveLayout()
	if err != nil {
		return nil, err
	}
	if filepath.Dir(path) != layout.Root {
		return nil, fmt.Errorf("%w: replacement is outside the installation root", ErrInvalidDatabase)
	}
	if err := gatewaypaths.ValidateOwnerOnlyFile(path); err != nil {
		return nil, fmt.Errorf("%w: replacement path: %w", ErrInvalidDatabase, err)
	}
	layout.Database = path
	layout.MutationMarker = path + ".mutation"
	store, err := openConfigured(ctx, layout, testOptions{})
	if err != nil {
		return nil, err
	}
	if err := store.verify(ctx); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

// Checkpoint closes the replacement's WAL into its database file before generation publication.
func (store *Store) Checkpoint(ctx context.Context) error {
	var busy, logFrames, checkpointed int
	if err := store.database.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointed); err != nil {
		return fmt.Errorf("checkpoint replacement database: %w", err)
	}
	if busy != 0 || logFrames != checkpointed {
		return fmt.Errorf("checkpoint replacement database remained busy")
	}
	return nil
}

// ClearVerifiedMarker clears prior uncertain mutation state only after a replacement verifies and activates.
func ClearVerifiedMarker(ownership *gatewaypaths.Ownership, installationID string) error {
	layout, err := ownership.ActiveLayout()
	if err != nil {
		return err
	}
	return newMutationMarker(layout, nil).clearVerified(installationID)
}

// InstallReplacement atomically selects a closed staged generation and removes sidecars from both generations.
func InstallReplacement(ownership *gatewaypaths.Ownership, staged string) error {
	layout, err := ownership.ActiveLayout()
	if err != nil {
		return err
	}
	if filepath.Dir(staged) != layout.Root {
		return fmt.Errorf("%w: replacement is outside the installation root", ErrInvalidDatabase)
	}
	if err := gatewaypaths.ValidateOwnerOnlyFile(staged); err != nil {
		return err
	}
	rollback := layout.Database + ".pre-restore"
	_ = os.Remove(rollback)
	_ = os.Remove(rollback + "-wal")
	_ = os.Remove(rollback + "-shm")
	if err := os.Rename(layout.Database, rollback); err != nil {
		return fmt.Errorf("stage current database generation: %w", err)
	}
	restored := false
	defer func() {
		if !restored {
			_ = os.Rename(rollback, layout.Database)
		}
	}()
	for _, path := range []string{layout.Database + "-wal", layout.Database + "-shm", staged + "-wal", staged + "-shm"} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove database sidecar: %w", err)
		}
	}
	if err := os.Rename(staged, layout.Database); err != nil {
		return fmt.Errorf("activate replacement database: %w", err)
	}
	if err := syncDirectory(layout.Root); err != nil {
		return fmt.Errorf("sync replacement generation: %w", err)
	}
	restored = true
	if err := os.Remove(rollback); err != nil {
		return fmt.Errorf("remove prior database generation: %w", err)
	}
	if err := syncDirectory(layout.Root); err != nil {
		return fmt.Errorf("sync prior generation removal: %w", err)
	}
	return nil
}
