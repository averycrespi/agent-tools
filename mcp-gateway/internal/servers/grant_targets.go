package servers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

type GrantTargetKind string

type NamespaceTarget struct {
	ID        string
	Namespace string
	State     contract.DesiredServerState
}

const (
	GrantTargetSynthetic GrantTargetKind = "synthetic"
	GrantTargetServer    GrantTargetKind = "server"
)

func (repository *Repository) ValidateGrantTargetTx(ctx context.Context, transaction *sql.Tx, serverID string) (GrantTargetKind, error) {
	if transaction == nil {
		return "", fmt.Errorf("%w: grant-target transaction is unavailable", ErrStorageUnavailable)
	}
	if !validID(serverID) {
		return "", ErrNotFound
	}
	if serverID == contract.SyntheticServerID {
		var collisions int
		if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM server_identities WHERE id = ?`, serverID).Scan(&collisions); err != nil {
			return "", grantTargetStorageError(err)
		}
		if collisions != 0 {
			return "", ErrIdentityUnavailable
		}
		return GrantTargetSynthetic, nil
	}

	var state contract.DesiredServerState
	if err := transaction.QueryRowContext(ctx, `SELECT desired_state FROM servers WHERE id = ?`, serverID).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", grantTargetStorageError(err)
	}
	if state == contract.DesiredServerDeleted {
		return "", ErrNotFound
	}
	return GrantTargetServer, nil
}

func (repository *Repository) LookupNamespaceTargetTx(ctx context.Context, transaction *sql.Tx, namespace string) (NamespaceTarget, error) {
	if transaction == nil {
		return NamespaceTarget{}, fmt.Errorf("%w: namespace-target transaction is unavailable", ErrStorageUnavailable)
	}
	if !namespacePattern.MatchString(namespace) || namespace == contract.SyntheticServerNamespace {
		return NamespaceTarget{}, ErrNotFound
	}
	var target NamespaceTarget
	if err := transaction.QueryRowContext(ctx, `
		SELECT identity.id, identity.namespace, server.desired_state
		FROM server_identities AS identity
		JOIN servers AS server ON server.id = identity.id
		WHERE identity.namespace = ?`, namespace).Scan(&target.ID, &target.Namespace, &target.State); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return NamespaceTarget{}, ErrNotFound
		}
		return NamespaceTarget{}, grantTargetStorageError(err)
	}
	if !validID(target.ID) || target.Namespace != namespace {
		return NamespaceTarget{}, ErrStorageUnavailable
	}
	if _, err := contract.ParseDesiredServerState(string(target.State)); err != nil {
		return NamespaceTarget{}, ErrStorageUnavailable
	}
	return target, nil
}

func (repository *Repository) StoredGrantTargetExistsTx(ctx context.Context, transaction *sql.Tx, serverID string) (bool, error) {
	if transaction == nil {
		return false, fmt.Errorf("%w: grant-target transaction is unavailable", ErrStorageUnavailable)
	}
	if !validID(serverID) {
		return false, nil
	}
	if serverID == contract.SyntheticServerID {
		var collisions int
		if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM server_identities WHERE id = ? OR namespace = ?`, serverID, contract.SyntheticServerNamespace).Scan(&collisions); err != nil {
			return false, grantTargetStorageError(err)
		}
		return collisions == 0, nil
	}
	var identities int
	if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM server_identities WHERE id = ?`, serverID).Scan(&identities); err != nil {
		return false, grantTargetStorageError(err)
	}
	return identities == 1, nil
}

func grantTargetStorageError(err error) error {
	return fmt.Errorf("%w: validate grant target: %w", ErrStorageUnavailable, err)
}
