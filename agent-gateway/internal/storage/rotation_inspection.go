package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"

	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/ncruces/go-sqlite3"
	sqliteDriver "github.com/ncruces/go-sqlite3/driver"
)

// InspectRotation observes committed WAL under exclusive installation ownership.
// It never checkpoints or opens a writer; both retained rotation keys remain
// available while the caller authenticates the database-selected generation.
func InspectRotation(ctx context.Context, owner *gatewaypaths.Ownership, view func(*sql.Tx) error) (result Inspection, resultErr error) {
	layout, err := owner.ActiveLayout()
	if err != nil {
		return result, err
	}
	if err := gatewaypaths.ValidateOwnerOnlyFile(layout.Database); err != nil {
		return result, err
	}
	if err := RequireClosedGeneration(layout.Database); err == nil {
		return InspectMaintenance(ctx, owner, view)
	} else if !errors.Is(err, ErrInspectionUnavailable) {
		return result, err
	}
	if info, err := os.Lstat(layout.Database + "-journal"); err == nil && info.Size() != 0 {
		return result, ErrInspectionUnavailable
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := gatewaypaths.ValidateSQLiteSidecar(layout.Database + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return result, err
		}
	}
	// A read-only WAL open must not create the shared-memory index during a dry
	// run. Missing crash-sidecars require a separately qualified stopped plan.
	if info, err := os.Stat(layout.Database + "-wal"); err == nil && info.Size() > 0 {
		if err := gatewaypaths.ValidateSQLiteSidecar(layout.Database + "-shm"); err != nil {
			return result, errors.Join(ErrInspectionUnavailable, err)
		}
	}
	seal := func() ([32]byte, error) {
		var result [32]byte
		hash := sha256.New()
		paths := append([]string{layout.Database, layout.Database + "-wal"}, newMutationMarker(layout, nil).paths()...)
		for index, path := range paths {
			file, err := os.Open(path)
			if errors.Is(err, os.ErrNotExist) {
				_, _ = fmt.Fprintf(hash, "%d:absent;", index)
				continue
			}
			if err != nil {
				return result, err
			}
			_, _ = fmt.Fprintf(hash, "%d:", index)
			count, readErr := io.Copy(hash, io.LimitReader(file, (2<<30)+1))
			if err := errors.Join(readErr, file.Close()); err != nil {
				return result, err
			}
			if count > 2<<30 {
				return result, ErrInvalidDatabase
			}
			_, _ = fmt.Fprintf(hash, ";%d;", count)
		}
		copy(result[:], hash.Sum(nil))
		return result, nil
	}
	before, err := seal()
	if err != nil {
		return result, err
	}
	db, err := sqliteDriver.Open(dataSource(layout.Database, true, true), func(connection *sqlite3.Conn) error {
		return configureConnectionSizeLimit(connection, compiledDatabaseByteLimit())
	})
	if err != nil {
		return result, err
	}
	defer func() { resultErr = errors.Join(resultErr, db.Close()) }()
	store := &Store{database: db, path: layout.Database, databaseLimit: compiledDatabaseByteLimit()}
	if err := store.verify(ctx); err != nil {
		return result, err
	}
	result.Identity, err = store.Identity(ctx)
	if err != nil {
		return result, err
	}
	if result.Identity.SchemaVersion != CurrentSchema {
		return result, ErrInvalidDatabase
	}
	if err := newMutationMarker(layout, nil).validateHistorical(result.Identity.InstallationID); err != nil {
		return result, errors.Join(ErrStorageLatched, err)
	}
	if err := store.View(ctx, func(tx *sql.Tx) error {
		if err := RequireNoLegacyCustodyTx(ctx, tx); err != nil {
			return err
		}
		if view != nil {
			return view(tx)
		}
		return nil
	}); err != nil {
		return result, err
	}
	after, err := seal()
	if err != nil {
		return result, err
	}
	if before != after {
		return result, fmt.Errorf("rotation inspection changed control/WAL evidence: %w", ErrPlanChanged)
	}
	result.seal = after
	result.walAware = true
	return result, nil
}
