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
		return CutoverResult{}, err
	}

	if err := coordinator.ensureActive(epoch); err != nil {
		return CutoverResult{}, err
	}
	revision, err := coordinator.commitCandidate(ctx, namespace, handle, epoch)
	if err != nil {
		return CutoverResult{}, err
	}
	result := CutoverResult{Handle: handle, Revision: strconv.FormatUint(revision, 10)}
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

func (coordinator *Coordinator) ReadActive(
	ctx context.Context,
	namespace Namespace,
) ([]byte, CutoverResult, error) {
	if !coordinator.acquireOperation() {
		return nil, CutoverResult{}, ErrWorkLimit
	}
	defer coordinator.releaseOperation()
	epoch, err := coordinator.activeEpoch()
	if err != nil {
		return nil, CutoverResult{}, err
	}
	if err := coordinator.validateNamespace(ctx, namespace); err != nil {
		return nil, CutoverResult{}, err
	}
	var handleValue string
	var revision uint64
	err = coordinator.store.View(ctx, func(transaction *sql.Tx) error {
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

func (coordinator *Coordinator) commitCandidate(
	ctx context.Context,
	namespace Namespace,
	handle Handle,
	epoch uint64,
) (uint64, error) {
	coordinator.stateMu.Lock()
	defer coordinator.stateMu.Unlock()
	if coordinator.draining || coordinator.epoch != epoch {
		return 0, ErrDraining
	}
	var revision uint64
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
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO keyring_authorities (owner, kind, handle, revision)
			VALUES (?, ?, ?, ?)
			ON CONFLICT (owner, kind) DO UPDATE SET
				handle = excluded.handle,
				revision = excluded.revision`,
			namespace.owner, namespace.kind, string(handle), revision); err != nil {
			return fmt.Errorf("commit keyring authority: %w", err)
		}
		return runCutoverHook(coordinator.hooks.beforeCommit)
	})
	return revision, err
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

func runCutoverHook(hook func() error) error {
	if hook == nil {
		return nil
	}
	return hook()
}
