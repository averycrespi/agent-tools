package authorization

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"strconv"
	"strings"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

type CredentialBinding struct {
	PrincipalID           string
	PrincipalRevision     string
	Visibility            contract.PrincipalVisibility
	CredentialID          string
	CredentialRevision    string
	CredentialFingerprint string
}

type credentialCompare func([]byte, []byte) int

type authenticationCandidate struct {
	binding  CredentialBinding
	verifier []byte
}

func (repository *Repository) Authenticate(ctx context.Context, bearer string) (CredentialBinding, error) {
	return repository.authenticate(ctx, bearer, subtle.ConstantTimeCompare)
}

func (repository *Repository) authenticate(
	ctx context.Context,
	bearer string,
	compare credentialCompare,
) (CredentialBinding, error) {
	if strings.HasPrefix(bearer, contract.AdminBearerPrefix) {
		return CredentialBinding{}, ErrCredentialDomainMismatch
	}
	if !validAgentBearer(bearer) {
		return CredentialBinding{}, ErrAuthenticationRequired
	}
	verifier := agentVerifierForBearer(bearer)
	var matched CredentialBinding
	found := false
	err := repository.view(ctx, func(transaction *sql.Tx) error {
		candidates, err := loadAuthenticationCandidates(ctx, transaction)
		if err != nil {
			return err
		}
		matches := 0
		for _, candidate := range candidates {
			if compare(candidate.verifier, verifier[:]) == 1 {
				matched = candidate.binding
				matches++
			}
		}
		if matches > 1 {
			return ErrAuthorizationUnavailable
		}
		found = matches == 1
		return nil
	})
	if err != nil {
		return CredentialBinding{}, err
	}
	if !found {
		return CredentialBinding{}, ErrAuthenticationRequired
	}
	return matched, nil
}

func validAgentBearer(bearer string) bool {
	if !strings.HasPrefix(bearer, contract.AgentBearerPrefix) {
		return false
	}
	encoded := strings.TrimPrefix(bearer, contract.AgentBearerPrefix)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	return err == nil && len(decoded) == agentBearerEntropyBytes && base64.RawURLEncoding.EncodeToString(decoded) == encoded
}

func loadAuthenticationCandidates(ctx context.Context, transaction *sql.Tx) ([]authenticationCandidate, error) {
	maximum := mustLimit("principals")
	rows, err := transaction.QueryContext(ctx, `
		SELECT insertion_sequence, id, state, visibility, revision, credential_revision,
		       credential_id, credential_verifier, credential_fingerprint,
		       credential_created_at, created_at, updated_at
		FROM principals
		ORDER BY insertion_sequence, id
		LIMIT ?`, maximum+1)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	candidates := make([]authenticationCandidate, 0)
	count := int64(0)
	var previousSequence int64
	for rows.Next() {
		if count >= maximum {
			return nil, ErrAuthorizationUnavailable
		}
		count++
		var (
			sequence                       int64
			principalID, state, visibility string
			principalRevision              int64
			credentialRevision             int64
			credentialID                   sql.NullString
			verifier                       []byte
			fingerprint                    sql.NullString
			credentialCreatedAt            sql.NullString
			createdAt, updatedAt           string
		)
		if err := rows.Scan(
			&sequence, &principalID, &state, &visibility, &principalRevision, &credentialRevision,
			&credentialID, &verifier, &fingerprint, &credentialCreatedAt, &createdAt, &updatedAt,
		); err != nil {
			return nil, ErrAuthorizationUnavailable
		}
		if sequence <= previousSequence || !validOpaqueID(principalID) ||
			!validPrincipalState(contract.PrincipalState(state)) || !validVisibility(contract.PrincipalVisibility(visibility)) ||
			principalRevision < 1 || credentialRevision < 0 {
			return nil, ErrAuthorizationUnavailable
		}
		created, createdValid := canonicalTimestamp(createdAt)
		updated, updatedValid := canonicalTimestamp(updatedAt)
		if !createdValid || !updatedValid || updated.Before(created) {
			return nil, ErrAuthorizationUnavailable
		}
		credentialPresent := credentialID.Valid || verifier != nil || fingerprint.Valid || credentialCreatedAt.Valid
		credentialComplete := credentialID.Valid && len(verifier) == 32 && fingerprint.Valid && credentialCreatedAt.Valid
		if credentialPresent != credentialComplete || credentialComplete && credentialRevision == 0 {
			return nil, ErrAuthorizationUnavailable
		}
		if credentialComplete {
			issued, issuedValid := canonicalTimestamp(credentialCreatedAt.String)
			if state != string(contract.PrincipalActive) || !validOpaqueID(credentialID.String) || !validFingerprint(fingerprint.String) ||
				!issuedValid || issued.Before(created) || issued.After(updated) {
				return nil, ErrAuthorizationUnavailable
			}
			candidates = append(candidates, authenticationCandidate{
				binding: CredentialBinding{
					PrincipalID: principalID, PrincipalRevision: strconv.FormatInt(principalRevision, 10),
					Visibility: contract.PrincipalVisibility(visibility), CredentialID: credentialID.String,
					CredentialRevision: strconv.FormatInt(credentialRevision, 10), CredentialFingerprint: fingerprint.String,
				},
				verifier: append([]byte(nil), verifier...),
			})
		}
		previousSequence = sequence
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return candidates, nil
}
