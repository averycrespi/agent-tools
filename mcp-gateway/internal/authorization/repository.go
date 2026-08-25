// Package authorization owns all online S3 principal, credential, and grant SQL.
package authorization

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strconv"
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
	ErrStorageUnavailable       = errors.New("authorization storage is unavailable")
)

type Clock interface {
	Now() time.Time
}

type Repository struct {
	store   *storage.Store
	clock   Clock
	entropy io.Reader
}

type SyntheticIdentity struct {
	ServerID  string
	Namespace string
}

func New(store *storage.Store, clock Clock, entropy io.Reader) (*Repository, error) {
	if store == nil || clock == nil || entropy == nil {
		return nil, errors.New("authorization repository dependencies are incomplete")
	}
	return &Repository{store: store, clock: clock, entropy: entropy}, nil
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
	return errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrIdentityUnavailable) || errors.Is(err, ErrResourceLimit) || errors.Is(err, ErrStaleRevision) || errors.Is(err, ErrConflict) || errors.Is(err, ErrStaleCursor) || errors.Is(err, ErrInvalidState) || errors.Is(err, ErrAuthenticationRequired) || errors.Is(err, ErrCredentialDomainMismatch) || errors.Is(err, ErrAuthorizationUnavailable) || errors.Is(err, ErrStorageUnavailable)
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
