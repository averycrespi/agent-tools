// Package authorization owns all online S3 principal, credential, and grant SQL.
package authorization

import (
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
	ErrNotFound                 = errors.New("authorization record was not found")
	ErrInvalidInput             = errors.New("authorization input is invalid")
	ErrIdentityUnavailable      = errors.New("authorization identity is unavailable")
	ErrResourceLimit            = errors.New("authorization resource limit is reached")
	ErrStaleRevision            = errors.New("principal revision is stale")
	ErrConflict                 = errors.New("authorization mutation conflicts with current state")
	ErrStaleCursor              = errors.New("authorization cursor snapshot is stale")
	ErrInvalidState             = errors.New("authorization durable state is invalid")
	ErrAuthenticationRequired   = errors.New("agent authentication is required")
	ErrCredentialDomainMismatch = errors.New("credential is for a different authority")
	ErrAuthorizationUnavailable = errors.New("authorization is unavailable")
	ErrAdmissionUnavailable     = errors.New("admission detachment is unavailable")
	ErrApprovalUnavailable      = errors.New("grant request approval is unavailable")
	ErrShuttingDown             = errors.New("authorization is shutting down")
	ErrStorageUnavailable       = errors.New("authorization storage is unavailable")
)

type Clock interface {
	Now() time.Time
}

type Repository struct {
	store     *storage.Store
	clock     Clock
	entropy   io.Reader
	authority *authorityRegistry

	constraintCache struct {
		sync.Mutex
		revision string
		entries  map[string]CompiledConstraint
	}
}

type SyntheticIdentity struct {
	ServerID  string
	Namespace string
}

func New(store *storage.Store, clock Clock, entropy io.Reader) (*Repository, error) {
	if store == nil || clock == nil || entropy == nil {
		return nil, errors.New("authorization repository dependencies are incomplete")
	}
	return &Repository{store: store, clock: clock, entropy: entropy, authority: newAuthorityRegistry(store)}, nil
}

func (repository *Repository) compileConstraint(revision, source string) (CompiledConstraint, error) {
	repository.constraintCache.Lock()
	if repository.constraintCache.revision == revision {
		if compiled, present := repository.constraintCache.entries[source]; present {
			repository.constraintCache.Unlock()
			return compiled, nil
		}
	}
	repository.constraintCache.Unlock()

	compiled, err := CompileConstraint([]byte(source))
	if err != nil {
		return CompiledConstraint{}, err
	}
	repository.constraintCache.Lock()
	defer repository.constraintCache.Unlock()
	if repository.constraintCache.revision != revision {
		cachedRevision, _ := strconv.ParseInt(repository.constraintCache.revision, 10, 64)
		incomingRevision, _ := strconv.ParseInt(revision, 10, 64)
		if cachedRevision > incomingRevision {
			return compiled, nil
		}
		repository.constraintCache.revision = revision
		repository.constraintCache.entries = make(map[string]CompiledConstraint)
	}
	if int64(len(repository.constraintCache.entries)) < mustLimit("grants") {
		repository.constraintCache.entries[source] = compiled
	}
	return compiled, nil
}

func (repository *Repository) SyntheticIdentity(ctx context.Context) (SyntheticIdentity, error) {
	var identity SyntheticIdentity
	err := repository.view(ctx, func(transaction *sql.Tx) error {
		return transaction.QueryRowContext(ctx, `
			SELECT server_id, namespace
			FROM synthetic_server_identity
			WHERE singleton = 1`).Scan(&identity.ServerID, &identity.Namespace)
	})
	return identity, err
}

func (repository *Repository) AuthorizationRevision(ctx context.Context) (string, error) {
	var revision int64
	err := repository.view(ctx, func(transaction *sql.Tx) error {
		return transaction.QueryRowContext(ctx, `
			SELECT revision
			FROM authorization_meta
			WHERE singleton = 1`).Scan(&revision)
	})
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(revision, 10), nil
}

func (repository *Repository) Occupancy(ctx context.Context) (contract.LimitStatus, contract.LimitStatus, error) {
	var principalCount, grantCount int64
	err := repository.view(ctx, func(transaction *sql.Tx) error {
		if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM principals`).Scan(&principalCount); err != nil {
			return fmt.Errorf("count principals: %w", err)
		}
		if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM grants`).Scan(&grantCount); err != nil {
			return fmt.Errorf("count grants: %w", err)
		}
		return nil
	})
	principalLimit := mustLimit("principals")
	grantLimit := mustLimit("grants")
	return contract.LimitStatus{InUse: principalCount, Limit: principalLimit, Saturated: principalCount >= principalLimit},
		contract.LimitStatus{InUse: grantCount, Limit: grantLimit, Saturated: grantCount >= grantLimit}, err
}

func (repository *Repository) view(ctx context.Context, callback func(*sql.Tx) error) error {
	if repository.store.Latched() {
		return ErrStorageUnavailable
	}
	err := repository.store.View(ctx, func(transaction *sql.Tx) error {
		if repository.store.Latched() {
			return ErrStorageUnavailable
		}
		return callback(transaction)
	})
	if repository.store.Latched() {
		return ErrStorageUnavailable
	}
	if err == nil || isRepositoryError(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return fmt.Errorf("%w: %w", ErrStorageUnavailable, err)
}

func isRepositoryError(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrIdentityUnavailable) || errors.Is(err, ErrResourceLimit) || errors.Is(err, ErrStaleRevision) || errors.Is(err, ErrConflict) || errors.Is(err, ErrStaleCursor) || errors.Is(err, ErrInvalidState) || errors.Is(err, ErrAuthenticationRequired) || errors.Is(err, ErrCredentialDomainMismatch) || errors.Is(err, ErrAuthorizationUnavailable) || errors.Is(err, ErrAdmissionUnavailable) || errors.Is(err, ErrApprovalUnavailable) || errors.Is(err, ErrShuttingDown) || errors.Is(err, ErrStorageUnavailable)
}

func validatePageLimit(limit int) error {
	maximum := mustLimit("admin_list_page")
	if int64(limit) < 1 || int64(limit) > maximum {
		return ErrInvalidInput
	}
	return nil
}

func mustLimit(name string) int64 {
	limit, ok := contract.FixedLimitByName(name)
	if !ok {
		panic("missing fixed limit: " + name)
	}
	return limit.Maximum
}
