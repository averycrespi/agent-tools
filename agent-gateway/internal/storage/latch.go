package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/ncruces/go-sqlite3"
	sqliteDriver "github.com/ncruces/go-sqlite3/driver"
)

type FaultPoint string

const (
	FaultBeforeBegin FaultPoint = "before_begin"
	FaultAfterCommit FaultPoint = "after_commit"
)

var (
	ErrStorageLatched = errors.New("storage is latched")
	ErrMutationBusy   = errors.New("storage mutation capacity is exhausted")
)

type AgentCredentialCandidate struct {
	PrincipalID        string
	CredentialID       string
	PrincipalRevision  int64
	CredentialRevision int64
}

func (store *Store) Latched() bool { return store.latched.Load() }
func (store *Store) Mutate(ctx context.Context, mutate func(*sql.Tx) error) error {
	return store.mutate(ctx, mutate)
}

// ActivateKeyringAuthority validates the namespace before committing activation.
// A committed activation remains authority even when acknowledgment was lost.
func (store *Store) ActivateKeyringAuthority(ctx context.Context, owner, kind string, mutate func(*sql.Tx) error) error {
	if !validKeyringOwner(owner) || !validKeyringRecordKind(kind) {
		return fmt.Errorf("invalid keyring authority activation")
	}
	return store.mutate(ctx, mutate)
}

// MutateAgentCredentialCandidate validates safe identity before atomic issuance.
// Lost bearer output requires inspect/revoke/reissue, never restoring old authority.
func (store *Store) MutateAgentCredentialCandidate(ctx context.Context, candidate AgentCredentialCandidate, mutate func(*sql.Tx) error) error {
	if !validRecoveryAction(recoveryActionFromCandidate(candidate)) {
		return fmt.Errorf("invalid agent credential candidate")
	}
	return store.mutate(ctx, mutate)
}

func (store *Store) mutate(ctx context.Context, mutate func(*sql.Tx) error) error {
	ctx = store.mutationContext(ctx)
	if err := store.observedAcquire(ctx); err != nil {
		return err
	}
	result := func() error {
		defer store.observedRelease(ctx, store.diagnosticStart())
		return store.mutateOwned(ctx, mutate)
	}()
	var failure *mutationFailure
	if errors.As(result, &failure) && store.diagnostics != nil {
		detail := diagnostics.Snapshot("control-storage", "mutate", store.path, failure.err)
		store.mutationEvent(ctx, diagnostics.DurabilityFailure, diagnostics.Latched, failure.stage, 0, detail)
		store.mutationEvent(ctx, diagnostics.StorageLatch, diagnostics.Latched, failure.stage, 0)
	}
	return result
}

type mutationFailure struct {
	err   error
	stage diagnostics.Stage
}

func (f *mutationFailure) Error() string { return f.err.Error() }
func (f *mutationFailure) Unwrap() error { return f.err }

func (store *Store) mutateOwned(ctx context.Context, mutate func(*sql.Tx) error) (result error) {
	stage := diagnostics.SizeCheck
	wasLatched := store.Latched()
	defer func() {
		if result != nil && !wasLatched && store.Latched() {
			result = &mutationFailure{err: result, stage: stage}
		}
	}()
	if store.Latched() {
		store.mutationEvent(ctx, diagnostics.StorageReject, diagnostics.Latched, diagnostics.NoStage, 0)
		return ErrStorageLatched
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	overLimit, err := store.overDatabaseLimit(ctx)
	if err != nil || overLimit {
		return store.latch(fmt.Errorf("database size check failed: %w", errorForLimit(err, overLimit)))
	}
	stage = diagnostics.IdentityCheck
	if _, err := store.Identity(ctx); err != nil {
		return store.latch(err)
	}
	connection, err := store.database.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, store.finishConnection(connection)) }()
	stage = diagnostics.TransactionBegin
	if err := store.inject(FaultBeforeBegin); err != nil {
		return err
	}
	// Statements retain the caller deadline. Only the transaction lifetime is
	// detached, preventing database/sql from installing an async rollback owner.
	tx, err := connection.BeginTx(context.WithoutCancel(ctx), &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("begin security mutation: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // Settle even a panicking callback.
	stage = diagnostics.TransactionBody
	mutationErr := mutate(tx)
	if mutationErr == nil {
		mutationErr = ctx.Err()
	}
	if mutationErr != nil {
		rollbackErr := tx.Rollback()
		if rollbackErr != nil || isStorageFailure(mutationErr) {
			if rollbackErr != nil {
				stage = diagnostics.TransactionRollback
			}
			return store.latch(errors.Join(mutationErr, rollbackErr))
		}
		return mutationErr
	}
	stage = diagnostics.TransactionCommit
	if err := tx.Commit(); err != nil {
		return store.latch(fmt.Errorf("commit security mutation: %w", err))
	}
	if err := store.inject(FaultAfterCommit); err != nil {
		return store.latch(err)
	}
	return nil
}

// finishConnection runs only after the synchronous transaction owner returned.
// ErrTxDone, cancellation and readable rows are not settlement receipts. The
// actual driver's autocommit must also be observed before returning to the pool.
func (store *Store) finishConnection(connection *sql.Conn) error {
	settled := false
	err := connection.Raw(func(raw any) error {
		if driver, ok := raw.(sqliteDriver.Conn); ok {
			settled = driver.Raw().GetAutocommit()
		}
		return nil
	})
	if err != nil || !settled {
		store.quarantineMu.Lock()
		store.pendingConnections = append(store.pendingConnections, connection)
		store.quarantineMu.Unlock()
		return store.latch(errors.Join(err, errors.New("storage connection settlement is unresolved; connection quarantined")))
	}
	return connection.Close()
}

// ConfirmHealthy linearizes final process-local admission with fault fencing.
func (store *Store) ConfirmHealthy(detach func() bool) bool {
	store.faultFence.Lock()
	defer store.faultFence.Unlock()
	return !store.Latched() && detach != nil && detach()
}
func (store *Store) latch(cause error) error {
	store.faultFence.Lock()
	store.latched.Store(true)
	store.faultFence.Unlock()
	return &latchedFailure{cause: cause}
}

// Render dependency errors only after releasing mutation admission.
type latchedFailure struct{ cause error }

func (e *latchedFailure) Error() string   { return ErrStorageLatched.Error() + ": " + e.cause.Error() }
func (e *latchedFailure) Unwrap() []error { return []error{ErrStorageLatched, e.cause} }

type faultContext struct {
	operation string
	cause     error
}

func (e *faultContext) Error() string { return e.operation + ": " + e.cause.Error() }
func (e *faultContext) Unwrap() error { return e.cause }
func (store *Store) inject(point FaultPoint) error {
	if store.fault == nil {
		return nil
	}
	if err := store.fault(point); err != nil {
		return &faultContext{operation: "injected fault at " + string(point), cause: err}
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
func (store *Store) DatabaseStatus(ctx context.Context) (contract.LimitStatus, error) {
	var pageCount, pageSize int64
	if err := store.database.QueryRowContext(ctx, `SELECT (SELECT page_count FROM pragma_page_count),(SELECT page_size FROM pragma_page_size)`).Scan(&pageCount, &pageSize); err != nil {
		return contract.LimitStatus{}, fmt.Errorf("read database occupancy: %w", err)
	}
	if pageCount < 0 || pageSize <= 0 {
		return contract.LimitStatus{}, ErrInvalidDatabase
	}
	inUse := pageCount * pageSize
	return contract.LimitStatus{InUse: inUse, Limit: store.databaseLimit, Saturated: inUse >= store.databaseLimit}, nil
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
	for _, code := range []error{sqlite3.BUSY, sqlite3.CORRUPT, sqlite3.IOERR, sqlite3.FULL, sqlite3.READONLY, sqlite3.CANTOPEN, sqlite3.NOTADB} {
		if errors.Is(err, code) {
			return true
		}
	}
	return false
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
