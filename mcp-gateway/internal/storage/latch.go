package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/ncruces/go-sqlite3"
)

const markerBytesMaximum = 512

type FaultPoint string

const (
	FaultArmCreate                FaultPoint = "arm_create"
	FaultArmWrite                 FaultPoint = "arm_write"
	FaultArmFileSync              FaultPoint = "arm_file_sync"
	FaultArmRename                FaultPoint = "arm_rename"
	FaultArmDirectorySync         FaultPoint = "arm_directory_sync"
	FaultAfterCommit              FaultPoint = "after_commit"
	FaultDisarmRename             FaultPoint = "disarm_rename"
	FaultDisarmDirectorySync      FaultPoint = "disarm_directory_sync"
	FaultDisarmDelete             FaultPoint = "disarm_delete"
	FaultDisarmFinalDirectorySync FaultPoint = "disarm_final_directory_sync"
)

var (
	ErrStorageLatched = errors.New("storage is latched")
	ErrMutationBusy   = errors.New("storage mutation capacity is exhausted")
)

type markerDocument struct {
	InstallationID string `json:"installation_id"`
	State          string `json:"state"`
}

type mutationMarker struct {
	root      string
	intent    string
	temporary string
	tombstone string
	fault     func(FaultPoint) error
}

func newMutationMarker(layout gatewaypaths.Layout, fault func(FaultPoint) error) mutationMarker {
	return mutationMarker{
		root:      layout.Root,
		intent:    layout.MutationMarker,
		temporary: layout.MutationMarker + ".tmp",
		tombstone: layout.MutationMarker + ".cleared",
		fault:     fault,
	}
}

func (store *Store) Latched() bool {
	return store.latched.Load()
}

func (store *Store) Mutate(ctx context.Context, mutate func(*sql.Tx) error) error {
	select {
	case store.mutationSlot <- struct{}{}:
		defer func() { <-store.mutationSlot }()
	default:
		return ErrMutationBusy
	}
	if store.Latched() {
		return ErrStorageLatched
	}
	overLimit, err := store.overDatabaseLimit(ctx)
	if err != nil || overLimit {
		return store.latch(fmt.Errorf("database size check failed: %w", errorForLimit(err, overLimit)))
	}
	identity, err := store.Identity(ctx)
	if err != nil {
		return store.latch(err)
	}
	if err := store.marker.arm(identity.InstallationID); err != nil {
		return store.latch(err)
	}

	transaction, err := store.database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return store.latch(fmt.Errorf("begin security mutation: %w", err))
	}
	mutationErr := mutate(transaction)
	if mutationErr != nil {
		rollbackErr := transaction.Rollback()
		if rollbackErr != nil || isStorageFailure(mutationErr) {
			return store.latch(errors.Join(mutationErr, rollbackErr))
		}
		if err := store.marker.disarm(); err != nil {
			return store.latch(errors.Join(mutationErr, err))
		}
		return mutationErr
	}
	if err := transaction.Commit(); err != nil {
		_ = transaction.Rollback()
		return store.latch(fmt.Errorf("commit security mutation: %w", err))
	}
	if err := store.inject(FaultAfterCommit); err != nil {
		return store.latch(err)
	}
	if err := store.marker.disarm(); err != nil {
		return store.latch(err)
	}
	return nil
}

func (store *Store) latch(cause error) error {
	store.latched.Store(true)
	return fmt.Errorf("%w: %w", ErrStorageLatched, cause)
}

func (store *Store) inject(point FaultPoint) error {
	if store.fault == nil {
		return nil
	}
	if err := store.fault(point); err != nil {
		return fmt.Errorf("injected fault at %s: %w", point, err)
	}
	return nil
}

func configureConnectionSizeLimit(connection *sqlite3.Conn, limit int64) error {
	pageSize, err := readConnectionPragma(connection, `PRAGMA page_size`)
	if err != nil {
		return fmt.Errorf("read SQLite page size: %w", err)
	}
	if pageSize <= 0 {
		return fmt.Errorf("invalid SQLite page size")
	}
	maximumPages := limit / pageSize
	if maximumPages < 1 {
		maximumPages = 1
	}
	if err := connection.Exec(`PRAGMA max_page_count = ` + strconv.FormatInt(maximumPages, 10)); err != nil {
		return fmt.Errorf("set SQLite page limit: %w", err)
	}
	return nil
}

func readConnectionPragma(connection *sqlite3.Conn, query string) (int64, error) {
	statement, tail, err := connection.Prepare(query)
	if err != nil {
		return 0, err
	}
	if tail != "" {
		_ = statement.Close()
		return 0, fmt.Errorf("unexpected SQL after pragma")
	}
	if !statement.Step() {
		stepErr := statement.Err()
		closeErr := statement.Close()
		if stepErr == nil {
			stepErr = fmt.Errorf("pragma returned no row")
		}
		return 0, errors.Join(stepErr, closeErr)
	}
	value := statement.ColumnInt64(0)
	if err := statement.Close(); err != nil {
		return 0, err
	}
	return value, nil
}

func (store *Store) configureSizeLimit(ctx context.Context) error {
	var pageSize int64
	if err := store.database.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		return fmt.Errorf("read SQLite page size: %w", err)
	}
	if pageSize <= 0 {
		return fmt.Errorf("invalid SQLite page size")
	}
	maximumPages := store.databaseLimit / pageSize
	if maximumPages < 1 {
		return nil
	}
	var applied int64
	if err := store.database.QueryRowContext(ctx, `PRAGMA max_page_count = `+strconv.FormatInt(maximumPages, 10)).Scan(&applied); err != nil {
		return fmt.Errorf("set SQLite page limit: %w", err)
	}
	if applied <= 0 {
		return fmt.Errorf("SQLite returned an invalid page limit")
	}
	return nil
}

func (store *Store) verifySizeLimit(ctx context.Context) error {
	var pageSize, maximumPages, pageCount int64
	if err := store.database.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		return fmt.Errorf("read SQLite page size: %w", err)
	}
	if err := store.database.QueryRowContext(ctx, `PRAGMA max_page_count`).Scan(&maximumPages); err != nil {
		return fmt.Errorf("read SQLite page limit: %w", err)
	}
	if err := store.database.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pageCount); err != nil {
		return fmt.Errorf("read SQLite page count: %w", err)
	}
	if pageSize <= 0 || maximumPages <= 0 || pageCount < 0 {
		return fmt.Errorf("SQLite page bounds are invalid")
	}
	compiledPages := store.databaseLimit / pageSize
	if maximumPages > compiledPages && pageCount <= compiledPages {
		return fmt.Errorf("SQLite page limit exceeds the compiled database bound")
	}
	return nil
}

func (store *Store) overDatabaseLimit(ctx context.Context) (bool, error) {
	info, err := os.Stat(store.path)
	if err != nil {
		return false, fmt.Errorf("inspect database size: %w", err)
	}
	if info.Size() > store.databaseLimit {
		return true, nil
	}
	var pageCount, pageSize int64
	if err := store.database.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pageCount); err != nil {
		return false, fmt.Errorf("read SQLite page count: %w", err)
	}
	if err := store.database.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		return false, fmt.Errorf("read SQLite page size: %w", err)
	}
	return pageCount < 0 || pageSize <= 0 || pageCount > store.databaseLimit/pageSize, nil
}

func compiledDatabaseByteLimit() int64 {
	limit, ok := contract.FixedLimitByName("database_bytes")
	if !ok {
		panic("database_bytes contract limit is missing")
	}
	return limit.Maximum
}

func errorForLimit(err error, overLimit bool) error {
	if err != nil {
		return err
	}
	if overLimit {
		return fmt.Errorf("database exceeds compiled size limit")
	}
	return fmt.Errorf("invalid database size state")
}

func isStorageFailure(err error) bool {
	for _, code := range []error{
		sqlite3.BUSY,
		sqlite3.CORRUPT,
		sqlite3.IOERR,
		sqlite3.FULL,
		sqlite3.READONLY,
		sqlite3.CANTOPEN,
		sqlite3.NOTADB,
	} {
		if errors.Is(err, code) {
			return true
		}
	}
	return false
}

func (marker mutationMarker) hasArtifacts() (bool, error) {
	marked := false
	for _, path := range marker.paths() {
		info, err := os.Lstat(path)
		switch {
		case err == nil:
			if err := gatewaypaths.ValidateOwnerOnlyFile(path); err != nil {
				return false, err
			}
			if info.Size() > markerBytesMaximum {
				return false, fmt.Errorf("mutation marker is too large")
			}
			marked = true
		case errors.Is(err, os.ErrNotExist):
			continue
		default:
			return false, err
		}
	}
	return marked, nil
}

func (marker mutationMarker) arm(installationID string) error {
	marked, err := marker.hasArtifacts()
	if err != nil {
		return err
	}
	if marked {
		return ErrStorageLatched
	}
	if err := marker.inject(FaultArmCreate); err != nil {
		return err
	}
	file, err := gatewaypaths.CreateOwnerOnlyFile(marker.temporary)
	if err != nil {
		return fmt.Errorf("create mutation marker: %w", err)
	}
	keepTemporary := true
	defer func() {
		_ = file.Close()
		if keepTemporary {
			_ = os.Remove(marker.temporary)
		}
	}()
	contents, err := json.Marshal(markerDocument{InstallationID: installationID, State: "armed"})
	if err != nil {
		return fmt.Errorf("encode mutation marker: %w", err)
	}
	contents = append(contents, '\n')
	if err := marker.inject(FaultArmWrite); err != nil {
		return err
	}
	if _, err := file.Write(contents); err != nil {
		return fmt.Errorf("write mutation marker: %w", err)
	}
	if err := marker.inject(FaultArmFileSync); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync mutation marker: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close mutation marker: %w", err)
	}
	if err := marker.inject(FaultArmRename); err != nil {
		return err
	}
	if err := os.Rename(marker.temporary, marker.intent); err != nil {
		return fmt.Errorf("publish mutation marker: %w", err)
	}
	keepTemporary = false
	if err := marker.inject(FaultArmDirectorySync); err != nil {
		return err
	}
	if err := syncDirectory(marker.root); err != nil {
		return fmt.Errorf("sync armed mutation marker: %w", err)
	}
	return nil
}

func (marker mutationMarker) disarm() error {
	if err := marker.inject(FaultDisarmRename); err != nil {
		return err
	}
	if err := os.Rename(marker.intent, marker.tombstone); err != nil {
		return fmt.Errorf("stage mutation marker removal: %w", err)
	}
	if err := marker.inject(FaultDisarmDirectorySync); err != nil {
		return err
	}
	if err := syncDirectory(marker.root); err != nil {
		return fmt.Errorf("sync staged marker removal: %w", err)
	}
	if err := marker.inject(FaultDisarmDelete); err != nil {
		return err
	}
	if err := os.Remove(marker.tombstone); err != nil {
		return fmt.Errorf("remove staged mutation marker: %w", err)
	}
	if err := marker.inject(FaultDisarmFinalDirectorySync); err != nil {
		return err
	}
	if err := syncDirectory(marker.root); err != nil {
		return fmt.Errorf("sync removed mutation marker: %w", err)
	}
	return nil
}

func (marker mutationMarker) verifyBindingOrMalformed(installationID string) error {
	for _, path := range marker.paths() {
		if err := gatewaypaths.ValidateOwnerOnlyFile(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		contents, readErr := io.ReadAll(io.LimitReader(file, markerBytesMaximum+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return errors.Join(readErr, closeErr)
		}
		if len(contents) > markerBytesMaximum {
			return fmt.Errorf("mutation marker is too large")
		}
		var document markerDocument
		if json.Unmarshal(contents, &document) == nil && document.State == "armed" &&
			document.InstallationID != "" && document.InstallationID != installationID {
			return fmt.Errorf("mutation marker belongs to another installation")
		}
	}
	return nil
}

func (marker mutationMarker) clearVerified(installationID string) error {
	if err := marker.verifyBindingOrMalformed(installationID); err != nil {
		return err
	}
	for _, path := range marker.paths() {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove verified marker %s: %w", filepath.Base(path), err)
		}
	}
	if err := syncDirectory(marker.root); err != nil {
		return fmt.Errorf("sync verified marker removal: %w", err)
	}
	return nil
}

func (marker mutationMarker) inject(point FaultPoint) error {
	if marker.fault == nil {
		return nil
	}
	if err := marker.fault(point); err != nil {
		return fmt.Errorf("injected marker fault at %s: %w", point, err)
	}
	return nil
}

func (marker mutationMarker) paths() []string {
	return []string{marker.intent, marker.temporary, marker.tombstone}
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return err
	}
	return directory.Close()
}
