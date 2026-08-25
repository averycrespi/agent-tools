package authorization

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
)

const agentBearerEntropyBytes = 32

type credentialCandidate struct {
	id          string
	bearer      string
	verifier    [sha256.Size]byte
	fingerprint string
	createdAt   string
}

func (repository *Repository) IssueCredential(
	ctx context.Context,
	principalID string,
	expectedRevision string,
) (contract.AgentCredentialCreation, error) {
	expected, err := parseExpectedRevision(expectedRevision)
	if err != nil {
		return contract.AgentCredentialCreation{}, err
	}
	current, currentCredentialRevision, err := repository.credentialMutationPreflight(ctx, principalID, expected)
	if err != nil {
		return contract.AgentCredentialCreation{}, err
	}
	if current.State != contract.PrincipalActive || expected >= math.MaxInt64-1 || currentCredentialRevision >= math.MaxInt64-1 {
		return contract.AgentCredentialCreation{}, ErrConflict
	}
	candidate, err := repository.prepareCredential(repository.clock.Now().UTC())
	if err != nil {
		return contract.AgentCredentialCreation{}, err
	}
	candidatePrincipalRevision := expected + 1
	candidateCredentialRevision := currentCredentialRevision + 1
	var creation contract.AgentCredentialCreation
	err = repository.store.MutateAgentCredentialCandidate(ctx, storage.AgentCredentialCandidate{
		PrincipalID:        principalID,
		CredentialID:       candidate.id,
		PrincipalRevision:  candidatePrincipalRevision,
		CredentialRevision: candidateCredentialRevision,
	}, func(transaction *sql.Tx) error {
		principal, err := principalByIDTx(ctx, transaction, principalID)
		if err != nil {
			return err
		}
		principalRevision, credentialRevision, err := parsePrincipalRevisions(principal)
		if err != nil {
			return err
		}
		if principalRevision != expected || credentialRevision != currentCredentialRevision {
			return ErrStaleRevision
		}
		if principal.State != contract.PrincipalActive {
			return ErrConflict
		}
		var collision int
		if err := transaction.QueryRowContext(ctx, `
			SELECT count(*) FROM principals
			WHERE credential_id = ? OR credential_verifier = ? OR credential_fingerprint = ?`,
			candidate.id, candidate.verifier[:], candidate.fingerprint).Scan(&collision); err != nil {
			return fmt.Errorf("inspect generated credential identity: %w", err)
		}
		if collision != 0 {
			return ErrIdentityUnavailable
		}
		result, err := transaction.ExecContext(ctx, `
			UPDATE principals
			SET revision = revision + 1,
			    credential_revision = credential_revision + 1,
			    credential_id = ?, credential_verifier = ?,
			    credential_fingerprint = ?, credential_created_at = ?,
			    updated_at = ?
			WHERE id = ? AND revision = ? AND credential_revision = ? AND state = 'active'`,
			candidate.id, candidate.verifier[:], candidate.fingerprint, candidate.createdAt,
			candidate.createdAt, principalID, expected, currentCredentialRevision)
		if err != nil {
			return fmt.Errorf("publish agent credential: %w", err)
		}
		if err := requireOneCredentialMutation(result); err != nil {
			return err
		}
		updated, err := principalByIDTx(ctx, transaction, principalID)
		if err != nil {
			return err
		}
		creation = contract.AgentCredentialCreation{Principal: updated, Bearer: candidate.bearer}
		return nil
	})
	if err != nil {
		return contract.AgentCredentialCreation{}, repository.mapMutationError(err)
	}
	return creation, nil
}

func (repository *Repository) RevokeCredential(
	ctx context.Context,
	principalID string,
	expectedRevision string,
) (contract.Principal, error) {
	expected, err := parseExpectedRevision(expectedRevision)
	if err != nil {
		return contract.Principal{}, err
	}
	var revoked contract.Principal
	err = repository.store.Mutate(ctx, func(transaction *sql.Tx) error {
		principal, err := principalByIDTx(ctx, transaction, principalID)
		if err != nil {
			return err
		}
		principalRevision, credentialRevision, err := parsePrincipalRevisions(principal)
		if err != nil {
			return err
		}
		if principalRevision != expected {
			return ErrStaleRevision
		}
		if principal.Credential == nil {
			return ErrConflict
		}
		if principalRevision == math.MaxInt64 || credentialRevision == math.MaxInt64 {
			return ErrConflict
		}
		result, err := transaction.ExecContext(ctx, `
			UPDATE principals
			SET revision = revision + 1,
			    credential_revision = credential_revision + 1,
			    credential_id = NULL, credential_verifier = NULL,
			    credential_fingerprint = NULL, credential_created_at = NULL,
			    updated_at = ?
			WHERE id = ? AND revision = ? AND credential_revision = ? AND credential_id IS NOT NULL`,
			formatAuthorizationTime(repository.clock.Now()), principalID, expected, credentialRevision)
		if err != nil {
			return fmt.Errorf("revoke agent credential: %w", err)
		}
		if err := requireOneCredentialMutation(result); err != nil {
			return err
		}
		revoked, err = principalByIDTx(ctx, transaction, principalID)
		return err
	})
	return revoked, repository.mapMutationError(err)
}

func (repository *Repository) credentialMutationPreflight(
	ctx context.Context,
	principalID string,
	expectedRevision int64,
) (contract.Principal, int64, error) {
	var principal contract.Principal
	err := repository.view(ctx, func(transaction *sql.Tx) error {
		var err error
		principal, err = principalByIDTx(ctx, transaction, principalID)
		return err
	})
	if err != nil {
		return contract.Principal{}, 0, err
	}
	principalRevision, credentialRevision, err := parsePrincipalRevisions(principal)
	if err != nil {
		return contract.Principal{}, 0, err
	}
	if principalRevision != expectedRevision {
		return contract.Principal{}, 0, ErrStaleRevision
	}
	return principal, credentialRevision, nil
}

func (repository *Repository) prepareCredential(now time.Time) (credentialCandidate, error) {
	secret := make([]byte, agentBearerEntropyBytes)
	if _, err := io.ReadFull(repository.entropy, secret); err != nil {
		return credentialCandidate{}, fmt.Errorf("%w: generate agent bearer: %w", ErrStorageUnavailable, err)
	}
	credentialID, err := repository.newID(now)
	if err != nil {
		if errors.Is(err, ErrIdentityUnavailable) {
			return credentialCandidate{}, err
		}
		return credentialCandidate{}, fmt.Errorf("%w: generate agent credential identity: %w", ErrStorageUnavailable, err)
	}
	bearer := contract.AgentBearerPrefix + base64.RawURLEncoding.EncodeToString(secret)
	verifier := agentVerifierForBearer(bearer)
	fingerprintDigest := sha256.Sum256(append([]byte("mcp-gateway/agent-fingerprint/v1\x00"), verifier[:]...))
	return credentialCandidate{
		id: credentialID, bearer: bearer, verifier: verifier,
		fingerprint: hex.EncodeToString(fingerprintDigest[:8]), createdAt: formatAuthorizationTime(now),
	}, nil
}

func agentVerifierForBearer(bearer string) [sha256.Size]byte {
	return sha256.Sum256(append([]byte("mcp-gateway/agent-verifier/v1\x00"), bearer...))
}

func parsePrincipalRevisions(principal contract.Principal) (int64, int64, error) {
	principalRevision, err := strconv.ParseInt(principal.Revision, 10, 64)
	if err != nil || principalRevision < 1 {
		return 0, 0, errorsInvalidState("principal revision is malformed")
	}
	credentialRevision, err := strconv.ParseInt(principal.CredentialRevision, 10, 64)
	if err != nil || credentialRevision < 0 {
		return 0, 0, errorsInvalidState("principal credential revision is malformed")
	}
	return principalRevision, credentialRevision, nil
}

func requireOneCredentialMutation(result sql.Result) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect agent credential mutation: %w", err)
	}
	if changed != 1 {
		return errorsInvalidState("agent credential mutation changed an invalid row count")
	}
	return nil
}
