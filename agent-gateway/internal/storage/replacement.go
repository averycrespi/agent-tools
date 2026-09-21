package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
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

// OpenMigrationSource never upgrades or repairs the original generation. Full
// schema/domain validation follows on the independently owned replacement.
func OpenMigrationSource(ctx context.Context, ownership *gatewaypaths.Ownership) (*Store, error) {
	layout, err := ownership.ActiveLayout()
	if err != nil {
		return nil, err
	}
	version, err := inspectDatabase(ctx, layout.Database)
	if err != nil || version < 3 || version > CurrentSchema {
		return nil, errors.Join(ErrInvalidDatabase, err)
	}
	marker := newMutationMarker(layout, nil)
	marked, err := marker.hasArtifacts()
	if err != nil || marked {
		return nil, errors.Join(ErrStorageLatched, err)
	}
	store, err := openConfigured(ctx, layout, testOptions{})
	if err != nil {
		return nil, err
	}
	settings, err := store.Settings(ctx)
	if err != nil || settings.Integrity != "ok" || settings.ApplicationID != ApplicationID {
		_ = store.Close()
		return nil, errors.Join(ErrInvalidDatabase, err)
	}
	return store, nil
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
	version, err := inspectDatabase(ctx, path)
	if err != nil {
		return nil, err
	}
	if version < 3 || version > CurrentSchema {
		return nil, fmt.Errorf("%w: replacement schema %d is not supported", ErrInvalidDatabase, version)
	}
	layout.Database = path
	layout.MutationMarker = path + ".mutation"
	store, err := openConfigured(ctx, layout, testOptions{})
	if err != nil {
		return nil, err
	}
	if err := store.configureSizeLimit(ctx); err != nil {
		_ = store.Close()
		return nil, err
	}
	if err := store.migrate(ctx, version); err != nil {
		_ = store.Close()
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	current, err := openConfigured(ctx, layout, testOptions{})
	if err != nil {
		return err
	}
	err = errors.Join(current.Checkpoint(ctx), current.Close())
	if err != nil {
		return fmt.Errorf("settle current generation before replacement: %w", err)
	}
	// Retain the checkpointed prior inode without ever removing the active name.
	// Existing recovery evidence refuses replacement rather than being erased.
	rollback := layout.Database + ".previous-" + rand.Text()
	if err := os.Link(layout.Database, rollback); err != nil {
		return err
	}
	if err := syncDirectory(layout.Root); err != nil {
		return err
	}
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
	return nil
}
