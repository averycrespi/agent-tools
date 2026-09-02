package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
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
	InstallationID string          `json:"installation_id"`
	State          string          `json:"state"`
	Recovery       *recoveryAction `json:"recovery,omitempty"`
}

type recoveryAction struct {
	Action             string `json:"action"`
	Owner              string `json:"owner,omitempty"`
	Kind               string `json:"kind,omitempty"`
	PrincipalID        string `json:"principal_id,omitempty"`
	CredentialID       string `json:"credential_id,omitempty"`
	PrincipalRevision  int64  `json:"principal_revision,omitempty"`
	CredentialRevision int64  `json:"credential_revision,omitempty"`
}

type AgentCredentialCandidate struct {
	PrincipalID        string
	CredentialID       string
	PrincipalRevision  int64
	CredentialRevision int64
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
	return store.mutate(ctx, nil, mutate)
}

// ActivateKeyringAuthority removes a durable authority fence. Stopped recovery
// restores that fence if the activation commit has an uncertain outcome.
func (store *Store) ActivateKeyringAuthority(
	ctx context.Context,
	owner string,
	kind string,
	mutate func(*sql.Tx) error,
) error {
	if !validKeyringOwner(owner) || !validKeyringRecordKind(kind) {
		return fmt.Errorf("invalid keyring authority activation")
	}
	return store.mutate(ctx, &recoveryAction{
		Action: "restore_keyring_authority_fence",
		Owner:  owner,
		Kind:   kind,
	}, mutate)
}

// MutateAgentCredentialCandidate records enough safe authority identity for
// stopped recovery to invalidate an uncertain committed candidate.
func (store *Store) MutateAgentCredentialCandidate(
	ctx context.Context,
	candidate AgentCredentialCandidate,
	mutate func(*sql.Tx) error,
) error {
	recovery := recoveryActionFromCandidate(candidate)
	if !validRecoveryAction(recovery) {
		return fmt.Errorf("invalid agent credential candidate")
	}
	return store.mutate(ctx, &recovery, mutate)
}

func (store *Store) mutate(ctx context.Context, recovery *recoveryAction, mutate func(*sql.Tx) error) error {
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
	if err := store.marker.arm(identity.InstallationID, recovery); err != nil {
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
		if err := store.marker.disarm(false); err != nil {
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
	if err := store.marker.disarm(recovery != nil); err != nil {
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

func (store *Store) DatabaseStatus(ctx context.Context) (contract.LimitStatus, error) {
	var pageCount, pageSize int64
	if err := store.database.QueryRowContext(ctx, `
		SELECT (SELECT page_count FROM pragma_page_count),
		       (SELECT page_size FROM pragma_page_size)`).Scan(&pageCount, &pageSize); err != nil {
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

func (marker mutationMarker) arm(installationID string, recovery *recoveryAction) error {
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
	contents, err := json.Marshal(markerDocument{InstallationID: installationID, State: "armed", Recovery: recovery})
	if err != nil {
		return fmt.Errorf("encode mutation marker: %w", err)
	}
	contents = append(contents, '\n')
	if len(contents) > markerBytesMaximum {
		return fmt.Errorf("mutation marker is too large")
	}
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

func (marker mutationMarker) disarm(preserveRecovery bool) error {
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
	var recoveryContents []byte
	if preserveRecovery {
		contents, err := os.ReadFile(marker.tombstone)
		if err != nil {
			return fmt.Errorf("retain recovery marker before removal: %w", err)
		}
		recoveryContents = contents
	}
	if err := marker.inject(FaultDisarmDelete); err != nil {
		return err
	}
	if err := os.Remove(marker.tombstone); err != nil {
		return fmt.Errorf("remove staged mutation marker: %w", err)
	}
	if err := marker.inject(FaultDisarmFinalDirectorySync); err != nil {
		return errors.Join(err, marker.restoreRecoveryTombstone(recoveryContents))
	}
	if err := syncDirectory(marker.root); err != nil {
		return errors.Join(fmt.Errorf("sync removed mutation marker: %w", err), marker.restoreRecoveryTombstone(recoveryContents))
	}
	return nil
}

func (marker mutationMarker) restoreRecoveryTombstone(contents []byte) error {
	if len(contents) == 0 {
		return nil
	}
	file, err := gatewaypaths.CreateOwnerOnlyFile(marker.tombstone)
	if err != nil {
		return fmt.Errorf("restore recovery marker: %w", err)
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		return fmt.Errorf("rewrite recovery marker: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync restored recovery marker: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close restored recovery marker: %w", err)
	}
	if err := syncDirectory(marker.root); err != nil {
		return fmt.Errorf("sync restored recovery marker directory: %w", err)
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

func (marker mutationMarker) recovery(installationID string) (*recoveryAction, error) {
	var recovered *recoveryAction
	for _, path := range marker.paths() {
		if err := gatewaypaths.ValidateOwnerOnlyFile(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		contents, readErr := io.ReadAll(io.LimitReader(file, markerBytesMaximum+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return nil, errors.Join(readErr, closeErr)
		}
		if len(contents) > markerBytesMaximum {
			return nil, fmt.Errorf("mutation marker is too large")
		}
		var envelope struct {
			Recovery json.RawMessage `json:"recovery"`
		}
		if err := json.Unmarshal(contents, &envelope); err != nil {
			if path == marker.tombstone {
				return nil, fmt.Errorf("mutation recovery tombstone is malformed")
			}
			continue
		}
		if len(envelope.Recovery) == 0 || bytes.Equal(envelope.Recovery, []byte("null")) {
			continue
		}
		var document markerDocument
		if err := strictjson.Decode(contents, &document, strictjson.Options{
			MaxBytes:             markerBytesMaximum,
			MaxDepth:             4,
			RejectUnknownMembers: true,
		}); err != nil {
			return nil, fmt.Errorf("mutation marker has an invalid recovery action")
		}
		if document.State != "armed" || document.InstallationID == "" || document.Recovery == nil || !validRecoveryAction(*document.Recovery) {
			return nil, fmt.Errorf("mutation marker has an invalid recovery action")
		}
		if document.InstallationID != installationID {
			continue
		}
		candidate := document.Recovery
		if recovered != nil && *recovered != *candidate {
			return nil, fmt.Errorf("mutation marker recovery actions disagree")
		}
		copy := *candidate
		recovered = &copy
	}
	return recovered, nil
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

func recoveryActionFromCandidate(candidate AgentCredentialCandidate) recoveryAction {
	return recoveryAction{
		Action:             "invalidate_agent_credential_candidate",
		PrincipalID:        candidate.PrincipalID,
		CredentialID:       candidate.CredentialID,
		PrincipalRevision:  candidate.PrincipalRevision,
		CredentialRevision: candidate.CredentialRevision,
	}
}

func validRecoveryAction(recovery recoveryAction) bool {
	switch recovery.Action {
	case "restore_keyring_authority_fence":
		return validKeyringOwner(recovery.Owner) && validKeyringRecordKind(recovery.Kind) &&
			recovery.PrincipalID == "" && recovery.CredentialID == "" && recovery.PrincipalRevision == 0 && recovery.CredentialRevision == 0
	case "invalidate_agent_credential_candidate":
		return recovery.Owner == "" && recovery.Kind == "" && validOpaqueID(recovery.PrincipalID) && validOpaqueID(recovery.CredentialID) &&
			recovery.PrincipalRevision > 0 && recovery.PrincipalRevision < math.MaxInt64 &&
			recovery.CredentialRevision > 0 && recovery.CredentialRevision < math.MaxInt64
	default:
		return false
	}
}

func validOpaqueID(value string) bool {
	return installationIDPattern.MatchString(value) && value[0] >= '0' && value[0] <= '7'
}

func validKeyringOwner(owner string) bool {
	return validOpaqueID(owner)
}

func validKeyringRecordKind(kind string) bool {
	switch kind {
	case "static_credential", "oauth_client", "oauth_tokens":
		return true
	default:
		return false
	}
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
