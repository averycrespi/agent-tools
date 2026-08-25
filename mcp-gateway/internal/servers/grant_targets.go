package servers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

type GrantTargetKind string

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

func grantTargetStorageError(err error) error {
	return fmt.Errorf("%w: validate grant target: %w", ErrStorageUnavailable, err)
}
