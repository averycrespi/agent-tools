// Package resourcekeys owns permanent portable identity reservations. Resource
// owners reserve keys on their existing mutation transaction, never separately.
package resourcekeys

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

type Kind string

const (
	Agent          Kind = "agent"
	MCPGrant       Kind = "mcp_grant"
	HTTPGrant      Kind = "http_grant"
	GitGrant       Kind = "git_grant"
	HTTPCredential Kind = "http_credential" //nolint:gosec // Public resource-type discriminator, not a credential.
	GitCredential  Kind = "git_credential"  //nolint:gosec // Public resource-type discriminator, not a credential.
	GitRepository  Kind = "git_repository"
)

var (
	ErrInvalid    = errors.New("invalid resource key")
	ErrConflict   = errors.New("resource key or identity is reserved")
	ErrGeneration = errors.New("resource key generation exhausted")
)

func validKind(kind Kind) bool {
	switch kind {
	case Agent, MCPGrant, HTTPGrant, GitGrant, HTTPCredential, GitCredential, GitRepository:
		return true
	}
	return false
}

// Candidate produces a recognizable prefix plus 48 random bits. It is not a
// secret generator; collision checks and the finite retry bound supply identity.
func Candidate(prefix string) string {
	var suffix [6]byte
	_, _ = rand.Read(suffix[:]) // crypto/rand.Read terminates on entropy failure.
	return prefix + "-" + hex.EncodeToString(suffix[:])
}

func ReserveTx(ctx context.Context, tx *sql.Tx, kind Kind, id, requested string) (string, error) {
	return reserveTx(ctx, tx, kind, id, requested, Candidate)
}

func reserveTx(ctx context.Context, tx *sql.Tx, kind Kind, id, requested string, generate func(string) string) (string, error) {
	if !validKind(kind) || !contract.ValidAuditID(id) || requested != "" && !contract.ValidResourceKey(requested) {
		return "", ErrInvalid
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM resource_keys WHERE kind=? AND resource_id=?)`, kind, id).Scan(&exists); err != nil {
		return "", fmt.Errorf("inspect resource identity reservation: %w", err)
	}
	if exists {
		return "", ErrConflict
	}
	for range 8 {
		key := requested
		if key == "" {
			key = generate(strings.ReplaceAll(string(kind), "_", "-"))
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO resource_keys(kind,resource_id,key) VALUES(?,?,?) ON CONFLICT(kind,key) DO NOTHING`, kind, id, key)
		if err != nil {
			return "", fmt.Errorf("reserve resource key: %w", err)
		}
		n, err := result.RowsAffected()
		if err != nil {
			return "", fmt.Errorf("inspect resource key reservation: %w", err)
		}
		if n == 1 {
			return key, nil
		}
		if requested != "" {
			return "", ErrConflict
		}
	}
	return "", ErrGeneration
}
