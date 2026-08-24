package keyring

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
)

var (
	ErrNoAuthority     = errors.New("keyring authority was not found")
	ErrCandidateLimit  = errors.New("keyring candidate limit is reached")
	ErrHandleCollision = errors.New("keyring handle collides with existing state")
	ErrDraining        = errors.New("keyring coordinator is draining")
)

type Clock interface {
	Now() time.Time
}

type CutoverResult struct {
	Handle   Handle
	Revision string
}

type AuthorityUpdate struct {
	Owner                  string
	Kind                   RecordKind
	Handle                 *Handle
	PriorPublishedRevision string
	ExactPublishedRevision string
	ValidateOnly           bool
	ActivateOnly           bool
	ExactInvalidation      bool
}

// AuthorityCallback runs inside the coordinator's marker-armed transaction and must not open a nested Store.Mutate.
type AuthorityCallback func(context.Context, *sql.Tx, AuthorityUpdate) (string, error)

type cutoverHooks struct {
	afterCandidate func() error
	afterWrite     func() error
	beforeCommit   func() error
	afterCommit    func() error
}

type Coordinator struct {
	stateMu   sync.Mutex
	operation chan struct{}
	provider  *Provider
	store     *storage.Store
	clock     Clock
	entropy   io.Reader
	hooks     cutoverHooks
	epoch     uint64
	draining  bool
}

func NewCoordinator(provider *Provider, store *storage.Store, clock Clock, entropy io.Reader) *Coordinator {
	return newCoordinatorWithHooks(provider, store, clock, entropy, cutoverHooks{})
}

func newCoordinatorWithHooks(
	provider *Provider,
	store *storage.Store,
	clock Clock,
	entropy io.Reader,
	hooks cutoverHooks,
) *Coordinator {
	return &Coordinator{
		operation: make(chan struct{}, 1),
		provider:  provider,
		store:     store,
		clock:     clock,
		entropy:   entropy,
		hooks:     hooks,
	}
}

func (coordinator *Coordinator) Replace(
	ctx context.Context,
	namespace Namespace,
	secret []byte,
) (CutoverResult, error) {
	return coordinator.ReplaceFenced(ctx, namespace, secret, nil)
}

func (coordinator *Coordinator) ReplaceFenced(
	ctx context.Context,
	namespace Namespace,
	secret []byte,
	callback AuthorityCallback,
) (CutoverResult, error) {
	if !coordinator.acquireOperation() {
		return CutoverResult{}, ErrWorkLimit
	}
	defer coordinator.releaseOperation()

	epoch, err := coordinator.activeEpoch()
	if err != nil {
		return CutoverResult{}, err
	}
	return coordinator.replaceFencedAdmitted(ctx, namespace, secret, callback, epoch, false)
}

func (coordinator *Coordinator) ReplaceFencedAfterAuthorizationSuccess(
	ctx context.Context,
	namespace Namespace,
	secret []byte,
	callback AuthorityCallback,
) (CutoverResult, error) {
	var result CutoverResult
	err := coordinator.WithOperation(ctx, func(operation *Operation) error {
		var replaceErr error
		result, replaceErr = operation.ReplaceFencedAfterAuthorizationSuccess(ctx, namespace, secret, callback)
		return replaceErr
	})
	return result, err
}

type Operation struct {
	coordinator *Coordinator
	epoch       uint64
}

func (coordinator *Coordinator) WithOperation(ctx context.Context, use func(*Operation) error) error {
	if use == nil || !coordinator.acquireOperation() {
		return ErrWorkLimit
	}
	defer coordinator.releaseOperation()
	epoch, err := coordinator.activeEpoch()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return use(&Operation{coordinator: coordinator, epoch: epoch})
}

func (operation *Operation) ReplaceFencedAfterAuthorizationSuccess(ctx context.Context, namespace Namespace, secret []byte, callback AuthorityCallback) (CutoverResult, error) {
	coordinator, epoch := operation.coordinator, operation.epoch
	if err := coordinator.validateNamespace(ctx, namespace); err != nil {
		return CutoverResult{}, err
	}
	if err := coordinator.fenceAuthority(ctx, namespace, epoch, callback); err != nil {
		_, invalidationErr := coordinator.invalidateAuthority(ctx, namespace, epoch, callback, "", true)
		return CutoverResult{}, errors.Join(err, invalidationErr)
	}
	result, candidateErr := coordinator.replaceFencedAdmitted(ctx, namespace, secret, callback, epoch, true)
	if candidateErr == nil {
		candidateErr = coordinator.activateAuthority(ctx, namespace, epoch, callback, result.Revision)
	}
	if candidateErr == nil {
		_ = coordinator.cleanupCandidates(ctx, namespace, epoch)
		return result, nil
	}
	_, invalidationErr := coordinator.invalidateAuthority(ctx, namespace, epoch, callback, result.Revision, true)
	if invalidationErr == nil {
		_ = coordinator.cleanupCandidates(ctx, namespace, epoch)
	}
	return CutoverResult{}, errors.Join(candidateErr, invalidationErr)
}

func (operation *Operation) InvalidateFenced(ctx context.Context, namespace Namespace, callback AuthorityCallback) (CutoverResult, error) {
	revision, err := operation.coordinator.invalidateAuthority(ctx, namespace, operation.epoch, callback, "", false)
	return CutoverResult{Revision: revision}, err
}

func (operation *Operation) InvalidateFencedExact(ctx context.Context, namespace Namespace, callback AuthorityCallback) (CutoverResult, error) {
	revision, err := operation.coordinator.invalidateAuthority(ctx, namespace, operation.epoch, callback, "", true)
	return CutoverResult{Revision: revision}, err
}

func (coordinator *Coordinator) replaceFencedAdmitted(
	ctx context.Context,
	namespace Namespace,
	secret []byte,
	callback AuthorityCallback,
	epoch uint64,
	keepFenced bool,
) (CutoverResult, error) {
	if err := coordinator.validateNamespace(ctx, namespace); err != nil {
		return CutoverResult{}, err
	}
	if err := coordinator.cleanupCandidates(ctx, namespace, epoch); err != nil {
		return CutoverResult{}, err
	}
	if err := coordinator.ensureActive(epoch); err != nil {
		return CutoverResult{}, err
	}
	handle, err := NewHandle(coordinator.entropy)
	if err != nil {
		return CutoverResult{}, err
	}
	if err := coordinator.registerCandidate(ctx, namespace, handle); err != nil {
		return CutoverResult{}, err
	}
	if err := runCutoverHook(coordinator.hooks.afterCandidate); err != nil {
		return CutoverResult{}, err
	}
	if err := coordinator.ensureActive(epoch); err != nil {
		return CutoverResult{}, err
	}
	if err := coordinator.provider.WriteGeneration(ctx, namespace, handle, secret); err != nil {
		coordinator.discardFailedCandidate(ctx, namespace, handle, epoch)
		return CutoverResult{}, err
	}
	if err := runCutoverHook(coordinator.hooks.afterWrite); err != nil {
		return CutoverResult{}, err
	}
	if err := coordinator.ensureActive(epoch); err != nil {
		return CutoverResult{}, err
	}
	loaded, err := coordinator.provider.ReadGeneration(ctx, namespace, handle)
	if err != nil || !bytes.Equal(loaded, secret) {
		if err == nil {
			err = ErrIncompleteGeneration
		}
		coordinator.discardFailedCandidate(ctx, namespace, handle, epoch)
		return CutoverResult{}, err
	}

	if err := coordinator.ensureActive(epoch); err != nil {
		return CutoverResult{}, err
	}
	revision, err := coordinator.commitCandidate(ctx, namespace, handle, epoch, callback, keepFenced)
	result := CutoverResult{Handle: handle, Revision: revision}
	if err != nil {
		if !errors.Is(err, storage.ErrStorageLatched) && !errors.Is(err, ErrDraining) {
			coordinator.discardFailedCandidate(ctx, namespace, handle, epoch)
		}
		return result, err
	}
	if err := runCutoverHook(coordinator.hooks.afterCommit); err != nil {
		return result, err
	}
	if !keepFenced {
		_ = coordinator.cleanupCandidates(ctx, namespace, epoch)
	}
	return result, nil
}

func (coordinator *Coordinator) InvalidateFenced(
	ctx context.Context,
	namespace Namespace,
	callback AuthorityCallback,
) (CutoverResult, error) {
	return coordinator.invalidateFenced(ctx, namespace, callback, "")
}

func (coordinator *Coordinator) invalidateFenced(
	ctx context.Context,
	namespace Namespace,
	callback AuthorityCallback,
	priorPublishedRevision string,
) (CutoverResult, error) {
	if !coordinator.acquireOperation() {
		return CutoverResult{}, ErrWorkLimit
	}
	defer coordinator.releaseOperation()
	epoch, err := coordinator.activeEpoch()
	if err != nil {
		return CutoverResult{}, err
	}
	if err := coordinator.validateNamespace(ctx, namespace); err != nil {
		return CutoverResult{}, err
	}
	revision, err := coordinator.invalidateAuthority(ctx, namespace, epoch, callback, priorPublishedRevision, false)
	if err != nil {
		return CutoverResult{}, err
	}
	result := CutoverResult{Revision: revision}
	if err := runCutoverHook(coordinator.hooks.afterCommit); err != nil {
		return result, err
	}
	_ = coordinator.cleanupCandidates(ctx, namespace, epoch)
	return result, nil
}

func (coordinator *Coordinator) Drain() {
	coordinator.stateMu.Lock()
	defer coordinator.stateMu.Unlock()
	if coordinator.draining {
		return
	}
	coordinator.draining = true
	coordinator.epoch++
}

func (coordinator *Coordinator) ReadActive(ctx context.Context, namespace Namespace) ([]byte, CutoverResult, error) {
	var secret []byte
	var result CutoverResult
	err := coordinator.WithOperation(ctx, func(operation *Operation) error {
		var readErr error
		secret, result, readErr = operation.ReadActive(ctx, namespace)
		return readErr
	})
	return secret, result, err
}

func (operation *Operation) ReadActive(ctx context.Context, namespace Namespace) ([]byte, CutoverResult, error) {
	coordinator, epoch := operation.coordinator, operation.epoch
	if coordinator.store != nil && coordinator.store.Latched() {
		return nil, CutoverResult{}, storage.ErrStorageLatched
	}
	if err := coordinator.validateNamespace(ctx, namespace); err != nil {
		return nil, CutoverResult{}, err
	}
	var handleValue string
	var revision uint64
	err := coordinator.store.View(ctx, func(transaction *sql.Tx) error {
		var fenced int
		if err := transaction.QueryRowContext(ctx, `
			SELECT count(*) FROM keyring_authority_fences
			WHERE owner = ? AND kind = ?`, namespace.owner, namespace.kind).Scan(&fenced); err != nil {
			return err
		}
		if fenced != 0 {
			return ErrNoAuthority
		}
		return transaction.QueryRowContext(ctx, `
			SELECT handle, revision FROM keyring_authorities
			WHERE owner = ? AND kind = ?`, namespace.owner, namespace.kind).Scan(&handleValue, &revision)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, CutoverResult{}, ErrNoAuthority
	}
	if err != nil {
		return nil, CutoverResult{}, err
	}
	handle, err := ParseHandle(handleValue)
	if err != nil {
		return nil, CutoverResult{}, ErrIncompleteGeneration
	}
	secret, err := coordinator.provider.ReadGeneration(ctx, namespace, handle)
	if err != nil {
		return nil, CutoverResult{}, err
	}
	if err := coordinator.ensureActive(epoch); err != nil {
		return nil, CutoverResult{}, err
	}
	if coordinator.store.Latched() {
		return nil, CutoverResult{}, storage.ErrStorageLatched
	}
	return secret, CutoverResult{Handle: handle, Revision: strconv.FormatUint(revision, 10)}, nil
}

func (coordinator *Coordinator) CleanupCandidates(ctx context.Context, namespace Namespace) error {
	if !coordinator.acquireOperation() {
		return ErrWorkLimit
	}
	defer coordinator.releaseOperation()
	epoch, err := coordinator.activeEpoch()
	if err != nil {
		return err
	}
	return coordinator.cleanupCandidates(ctx, namespace, epoch)
}

func (coordinator *Coordinator) cleanupCandidates(ctx context.Context, namespace Namespace, epoch uint64) error {
	if err := coordinator.validateNamespace(ctx, namespace); err != nil {
		return err
	}
	candidates, err := coordinator.candidates(ctx, namespace)
	if err != nil {
		return err
	}
	for _, handle := range candidates {
		if err := coordinator.ensureActive(epoch); err != nil {
			return err
		}
		if err := coordinator.provider.DeleteGeneration(ctx, namespace, handle); err != nil {
			return err
		}
		if err := coordinator.removeCandidate(ctx, namespace, handle, epoch); err != nil {
			return err
		}
	}
	return nil
}

func (coordinator *Coordinator) CandidateStatus(ctx context.Context) (contract.LimitStatus, error) {
	limit := mustFixedLimit("keyring_candidates")
	var inUse int64
	err := coordinator.store.View(ctx, func(transaction *sql.Tx) error {
		return transaction.QueryRowContext(ctx, `
			SELECT coalesce(max(candidate_count), 0)
			FROM (
				SELECT count(*) AS candidate_count
				FROM keyring_candidates GROUP BY owner, kind
			)`).Scan(&inUse)
	})
	return contract.LimitStatus{InUse: inUse, Limit: limit, Saturated: inUse >= limit}, err
}

func (coordinator *Coordinator) registerCandidate(ctx context.Context, namespace Namespace, handle Handle) error {
	limit := mustFixedLimit("keyring_candidates")
	return coordinator.store.Mutate(ctx, func(transaction *sql.Tx) error {
		var collisionCount int
		if err := transaction.QueryRowContext(ctx, `
			SELECT count(*) FROM (
				SELECT handle FROM keyring_candidates WHERE handle = ?
				UNION ALL
				SELECT handle FROM keyring_authorities WHERE handle = ?
			)`, string(handle), string(handle)).Scan(&collisionCount); err != nil {
			return fmt.Errorf("check keyring handle collision: %w", err)
		}
		if collisionCount != 0 {
			return ErrHandleCollision
		}
		var count int64
		if err := transaction.QueryRowContext(ctx, `
			SELECT count(*) FROM keyring_candidates
			WHERE owner = ? AND kind = ?`, namespace.owner, namespace.kind).Scan(&count); err != nil {
			return fmt.Errorf("count keyring candidates: %w", err)
		}
		if count >= limit {
			return ErrCandidateLimit
		}
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO keyring_candidates (owner, kind, handle, created_at)
			VALUES (?, ?, ?, ?)`, namespace.owner, namespace.kind, string(handle), coordinator.clock.Now().UTC()); err != nil {
			return fmt.Errorf("register keyring candidate: %w", err)
		}
		return nil
	})
}

func (coordinator *Coordinator) fenceAuthority(
	ctx context.Context,
	namespace Namespace,
	epoch uint64,
	callback AuthorityCallback,
) error {
	coordinator.stateMu.Lock()
	defer coordinator.stateMu.Unlock()
	if coordinator.draining || coordinator.epoch != epoch {
		return ErrDraining
	}
	return coordinator.store.Mutate(ctx, func(transaction *sql.Tx) error {
		if callback != nil {
			revision, err := callback(ctx, transaction, AuthorityUpdate{
				Owner: namespace.owner, Kind: namespace.kind, ValidateOnly: true,
			})
			if err != nil {
				return err
			}
			if !validNonnegativeRevision(revision) {
				return fmt.Errorf("authority callback returned an invalid revision")
			}
		}
		var priorHandle string
		priorErr := transaction.QueryRowContext(ctx, `
			SELECT handle FROM keyring_authorities
			WHERE owner = ? AND kind = ?`, namespace.owner, namespace.kind).Scan(&priorHandle)
		if priorErr != nil && !errors.Is(priorErr, sql.ErrNoRows) {
			return fmt.Errorf("read prior keyring authority: %w", priorErr)
		}
		if priorErr == nil {
			if _, err := transaction.ExecContext(ctx, `
				DELETE FROM keyring_authorities
				WHERE owner = ? AND kind = ?`, namespace.owner, namespace.kind); err != nil {
				return fmt.Errorf("fence prior keyring authority: %w", err)
			}
			if _, err := transaction.ExecContext(ctx, `
				INSERT OR IGNORE INTO keyring_candidates (owner, kind, handle, created_at)
				VALUES (?, ?, ?, ?)`, namespace.owner, namespace.kind, priorHandle, coordinator.clock.Now().UTC()); err != nil {
				return fmt.Errorf("retain fenced keyring generation for cleanup: %w", err)
			}
		}
		if _, err := transaction.ExecContext(ctx, `
			INSERT OR IGNORE INTO keyring_authority_fences (owner, kind)
			VALUES (?, ?)`, namespace.owner, namespace.kind); err != nil {
			return fmt.Errorf("record keyring authority fence: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `
			UPDATE gateway_meta SET revision = revision + 1 WHERE singleton = 1`); err != nil {
			return fmt.Errorf("increment keyring fence revision: %w", err)
		}
		return nil
	})
}

func (coordinator *Coordinator) activateAuthority(
	ctx context.Context,
	namespace Namespace,
	epoch uint64,
	callback AuthorityCallback,
	publishedRevision string,
) error {
	coordinator.stateMu.Lock()
	defer coordinator.stateMu.Unlock()
	if coordinator.draining || coordinator.epoch != epoch {
		return ErrDraining
	}
	return coordinator.store.ActivateKeyringAuthority(ctx, namespace.owner, string(namespace.kind), func(transaction *sql.Tx) error {
		if callback != nil {
			validatedRevision, err := callback(ctx, transaction, AuthorityUpdate{
				Owner: namespace.owner, Kind: namespace.kind, ValidateOnly: true, ActivateOnly: true,
				ExactPublishedRevision: publishedRevision,
			})
			if err != nil {
				return err
			}
			if validatedRevision != publishedRevision || !validRevision(validatedRevision) {
				return fmt.Errorf("authority callback did not validate the published revision")
			}
		}
		result, err := transaction.ExecContext(ctx, `
			DELETE FROM keyring_authority_fences
			WHERE owner = ? AND kind = ?`, namespace.owner, namespace.kind)
		if err != nil {
			return fmt.Errorf("activate keyring authority: %w", err)
		}
		removed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if removed != 1 {
			return ErrNoAuthority
		}
		if _, err := transaction.ExecContext(ctx, `
			UPDATE gateway_meta SET revision = revision + 1 WHERE singleton = 1`); err != nil {
			return fmt.Errorf("increment keyring activation revision: %w", err)
		}
		return nil
	})
}

func (coordinator *Coordinator) commitCandidate(
	ctx context.Context,
	namespace Namespace,
	handle Handle,
	epoch uint64,
	callback AuthorityCallback,
	keepFenced bool,
) (string, error) {
	coordinator.stateMu.Lock()
	defer coordinator.stateMu.Unlock()
	if coordinator.draining || coordinator.epoch != epoch {
		return "", ErrDraining
	}
	var revision uint64
	var domainRevision string
	err := coordinator.store.Mutate(ctx, func(transaction *sql.Tx) error {
		var exists int
		if err := transaction.QueryRowContext(ctx, `
			SELECT count(*) FROM keyring_candidates
			WHERE owner = ? AND kind = ? AND handle = ?`,
			namespace.owner, namespace.kind, string(handle)).Scan(&exists); err != nil {
			return fmt.Errorf("read keyring candidate: %w", err)
		}
		if exists != 1 {
			return ErrNoAuthority
		}

		var priorHandle string
		priorErr := transaction.QueryRowContext(ctx, `
			SELECT handle FROM keyring_authorities
			WHERE owner = ? AND kind = ?`, namespace.owner, namespace.kind).Scan(&priorHandle)
		if priorErr != nil && !errors.Is(priorErr, sql.ErrNoRows) {
			return fmt.Errorf("read prior keyring authority: %w", priorErr)
		}
		if _, err := transaction.ExecContext(ctx, `
			DELETE FROM keyring_candidates
			WHERE owner = ? AND kind = ? AND handle = ?`,
			namespace.owner, namespace.kind, string(handle)); err != nil {
			return fmt.Errorf("consume keyring candidate: %w", err)
		}
		if priorErr == nil {
			if _, err := transaction.ExecContext(ctx, `
				INSERT INTO keyring_candidates (owner, kind, handle, created_at)
				VALUES (?, ?, ?, ?)`, namespace.owner, namespace.kind, priorHandle, coordinator.clock.Now().UTC()); err != nil {
				return fmt.Errorf("retain prior keyring generation for cleanup: %w", err)
			}
		}
		if err := transaction.QueryRowContext(ctx, `
			UPDATE gateway_meta SET revision = revision + 1 WHERE singleton = 1
			RETURNING revision`).Scan(&revision); err != nil {
			return fmt.Errorf("increment keyring authority revision: %w", err)
		}
		authorityRevision := revision
		if callback != nil {
			candidate := handle
			published, err := callback(ctx, transaction, AuthorityUpdate{
				Owner: namespace.owner, Kind: namespace.kind, Handle: &candidate,
			})
			if err != nil {
				return err
			}
			if !validRevision(published) {
				return fmt.Errorf("authority callback returned an invalid revision")
			}
			domainRevision = published
			parsedRevision, parseErr := strconv.ParseUint(published, 10, 64)
			if parseErr != nil {
				return fmt.Errorf("parse authority callback revision: %w", parseErr)
			}
			authorityRevision = parsedRevision
		}
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO keyring_authorities (owner, kind, handle, revision)
			VALUES (?, ?, ?, ?)
			ON CONFLICT (owner, kind) DO UPDATE SET
				handle = excluded.handle,
				revision = excluded.revision`,
			namespace.owner, namespace.kind, string(handle), authorityRevision); err != nil {
			return fmt.Errorf("commit keyring authority: %w", err)
		}
		if !keepFenced {
			if _, err := transaction.ExecContext(ctx, `
				DELETE FROM keyring_authority_fences
				WHERE owner = ? AND kind = ?`, namespace.owner, namespace.kind); err != nil {
				return fmt.Errorf("clear prior keyring authority fence: %w", err)
			}
		}
		return runCutoverHook(coordinator.hooks.beforeCommit)
	})
	if err != nil {
		if callback != nil && domainRevision != "" && errors.Is(err, storage.ErrStorageLatched) {
			return domainRevision, err
		}
		return "", err
	}
	if callback != nil {
		return domainRevision, nil
	}
	return strconv.FormatUint(revision, 10), nil
}

func (coordinator *Coordinator) invalidateAuthority(
	ctx context.Context,
	namespace Namespace,
	epoch uint64,
	callback AuthorityCallback,
	priorPublishedRevision string,
	exact bool,
) (string, error) {
	coordinator.stateMu.Lock()
	defer coordinator.stateMu.Unlock()
	if coordinator.draining || coordinator.epoch != epoch {
		return "", ErrDraining
	}
	var revision uint64
	var domainRevision string
	err := coordinator.store.Mutate(ctx, func(transaction *sql.Tx) error {
		var priorHandle string
		priorErr := transaction.QueryRowContext(ctx, `
			SELECT handle FROM keyring_authorities
			WHERE owner = ? AND kind = ?`, namespace.owner, namespace.kind).Scan(&priorHandle)
		if priorErr != nil && !errors.Is(priorErr, sql.ErrNoRows) {
			return fmt.Errorf("read prior keyring authority: %w", priorErr)
		}
		if priorErr == nil {
			if _, err := transaction.ExecContext(ctx, `
				DELETE FROM keyring_authorities
				WHERE owner = ? AND kind = ?`, namespace.owner, namespace.kind); err != nil {
				return fmt.Errorf("invalidate keyring authority: %w", err)
			}
			if _, err := transaction.ExecContext(ctx, `
				INSERT OR IGNORE INTO keyring_candidates (owner, kind, handle, created_at)
				VALUES (?, ?, ?, ?)`, namespace.owner, namespace.kind, priorHandle, coordinator.clock.Now().UTC()); err != nil {
				return fmt.Errorf("retain invalidated generation for cleanup: %w", err)
			}
		}
		if err := transaction.QueryRowContext(ctx, `
			UPDATE gateway_meta SET revision = revision + 1 WHERE singleton = 1
			RETURNING revision`).Scan(&revision); err != nil {
			return fmt.Errorf("increment keyring authority revision: %w", err)
		}
		if callback != nil {
			published, err := callback(ctx, transaction, AuthorityUpdate{
				Owner: namespace.owner, Kind: namespace.kind, PriorPublishedRevision: priorPublishedRevision,
				ExactInvalidation: exact,
			})
			if err != nil {
				return err
			}
			if !validRevision(published) {
				return fmt.Errorf("authority callback returned an invalid revision")
			}
			domainRevision = published
		}
		return runCutoverHook(coordinator.hooks.beforeCommit)
	})
	if err != nil {
		return "", err
	}
	if callback != nil {
		return domainRevision, nil
	}
	return strconv.FormatUint(revision, 10), nil
}

func (coordinator *Coordinator) candidates(ctx context.Context, namespace Namespace) ([]Handle, error) {
	items := make([]Handle, 0)
	err := coordinator.store.View(ctx, func(transaction *sql.Tx) error {
		rows, err := transaction.QueryContext(ctx, `
			SELECT handle FROM keyring_candidates
			WHERE owner = ? AND kind = ?
			ORDER BY created_at, handle`, namespace.owner, namespace.kind)
		if err != nil {
			return fmt.Errorf("list keyring candidates: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				return err
			}
			handle, err := ParseHandle(value)
			if err != nil {
				return ErrIncompleteGeneration
			}
			items = append(items, handle)
		}
		return rows.Err()
	})
	return items, err
}

func (coordinator *Coordinator) removeCandidate(
	ctx context.Context,
	namespace Namespace,
	handle Handle,
	epoch uint64,
) error {
	coordinator.stateMu.Lock()
	defer coordinator.stateMu.Unlock()
	if coordinator.draining || coordinator.epoch != epoch {
		return ErrDraining
	}
	return coordinator.store.Mutate(ctx, func(transaction *sql.Tx) error {
		_, err := transaction.ExecContext(ctx, `
			DELETE FROM keyring_candidates
			WHERE owner = ? AND kind = ? AND handle = ?`,
			namespace.owner, namespace.kind, string(handle))
		return err
	})
}

func (coordinator *Coordinator) discardFailedCandidate(
	ctx context.Context,
	namespace Namespace,
	handle Handle,
	epoch uint64,
) {
	if err := coordinator.provider.DeleteGeneration(ctx, namespace, handle); err != nil {
		return
	}
	_ = coordinator.removeCandidate(ctx, namespace, handle, epoch)
}

func (coordinator *Coordinator) validateNamespace(ctx context.Context, namespace Namespace) error {
	if coordinator.provider == nil || coordinator.store == nil || coordinator.clock == nil || coordinator.entropy == nil {
		return fmt.Errorf("keyring coordinator dependencies are incomplete")
	}
	if namespace.installationID != coordinator.provider.installationID {
		return fmt.Errorf("keyring namespace does not belong to this provider")
	}
	identity, err := coordinator.store.Identity(ctx)
	if err != nil {
		return err
	}
	if identity.InstallationID != namespace.installationID {
		return fmt.Errorf("keyring namespace does not belong to this installation")
	}
	return nil
}

func (coordinator *Coordinator) acquireOperation() bool {
	select {
	case coordinator.operation <- struct{}{}:
		return true
	default:
		return false
	}
}

func (coordinator *Coordinator) releaseOperation() {
	<-coordinator.operation
}

func (coordinator *Coordinator) activeEpoch() (uint64, error) {
	coordinator.stateMu.Lock()
	defer coordinator.stateMu.Unlock()
	if coordinator.draining {
		return 0, ErrDraining
	}
	return coordinator.epoch, nil
}

func (coordinator *Coordinator) ensureActive(epoch uint64) error {
	coordinator.stateMu.Lock()
	defer coordinator.stateMu.Unlock()
	if coordinator.draining || coordinator.epoch != epoch {
		return ErrDraining
	}
	return nil
}

func validRevision(value string) bool {
	return value != "0" && validNonnegativeRevision(value)
}

func validNonnegativeRevision(value string) bool {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return false
	}
	_, err := strconv.ParseUint(value, 10, 64)
	return err == nil
}

func runCutoverHook(hook func() error) error {
	if hook == nil {
		return nil
	}
	return hook()
}
