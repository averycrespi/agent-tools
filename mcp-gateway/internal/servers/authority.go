package servers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
)

func (repository *Repository) Authority(ctx context.Context, serverID string) (AuthorityMetadata, error) {
	if !validID(serverID) {
		return AuthorityMetadata{}, ErrNotFound
	}
	var authority AuthorityMetadata
	err := repository.store.View(ctx, func(transaction *sql.Tx) error {
		var err error
		authority, err = authorityTx(ctx, transaction, serverID)
		return err
	})
	return authority, mapViewError(err)
}

func (repository *Repository) CredentialAuthorityCallback(fence CredentialFence) (keyring.AuthorityCallback, error) {
	if !validID(fence.ServerID) {
		return nil, ErrNotFound
	}
	if _, err := contract.ParseServerCredentialKind(string(fence.Kind)); err != nil {
		return nil, ErrInvalidInput
	}
	desiredRevision, err := parseRevision(fence.ExpectedDesiredRevision)
	if err != nil || desiredRevision < 1 {
		return nil, ErrStaleRevision
	}
	credentialRevision, err := parseRevision(fence.ExpectedCredentialRevision)
	if err != nil {
		return nil, ErrStaleRevision
	}
	var registrationRevision *int64
	if fence.ExpectedRegistrationRevision != "" {
		parsed, parseErr := parseRevision(fence.ExpectedRegistrationRevision)
		if parseErr != nil {
			return nil, ErrStaleRevision
		}
		registrationRevision = &parsed
	}
	recordKind := recordKindForCredential(fence.Kind)
	return func(ctx context.Context, transaction *sql.Tx, update keyring.AuthorityUpdate) (string, error) {
		if transaction == nil || update.Owner != fence.ServerID || update.Kind != recordKind {
			return "", ErrInvalidInput
		}
		if update.ValidateOnly && (update.Handle != nil || update.PriorPublishedRevision != "" || update.ExactInvalidation) {
			return "", ErrInvalidInput
		}
		if update.ActivateOnly && !update.ValidateOnly {
			return "", ErrInvalidInput
		}
		if update.ExactPublishedRevision != "" && !update.ValidateOnly {
			return "", ErrInvalidInput
		}
		expectedCredentialRevision := credentialRevision
		if update.PriorPublishedRevision != "" {
			parsed, parseErr := parseRevision(update.PriorPublishedRevision)
			if parseErr != nil || parsed != credentialRevision+1 {
				return "", ErrStaleRevision
			}
			expectedCredentialRevision = parsed
		}
		if update.ExactPublishedRevision != "" {
			parsed, parseErr := parseRevision(update.ExactPublishedRevision)
			if parseErr != nil || parsed != credentialRevision+1 {
				return "", ErrStaleRevision
			}
			expectedCredentialRevision = parsed
		}
		exactInvalidation := update.ExactInvalidation && update.Handle == nil
		if !exactInvalidation {
			var currentDesired int64
			var desiredState contract.DesiredServerState
			if err := transaction.QueryRowContext(ctx, `
				SELECT desired_revision, desired_state FROM servers WHERE id = ?`, fence.ServerID).Scan(&currentDesired, &desiredState); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return "", ErrNotFound
				}
				return "", fmt.Errorf("read server desired fence: %w", err)
			}
			if currentDesired != desiredRevision || (update.Handle != nil && desiredState == contract.DesiredServerDeleted) {
				return "", ErrStaleRevision
			}
			if registrationRevision != nil {
				var currentRegistration int64
				if err := transaction.QueryRowContext(ctx, `
					SELECT revision FROM server_oauth_registrations WHERE server_id = ?`, fence.ServerID).Scan(&currentRegistration); err != nil {
					return "", fmt.Errorf("read OAuth registration fence: %w", err)
				}
				if currentRegistration != *registrationRevision {
					return "", ErrStaleRevision
				}
			}
		}
		if update.ValidateOnly {
			var currentCredential int64
			if err := transaction.QueryRowContext(ctx, `
				SELECT revision FROM server_credentials
				WHERE server_id = ? AND kind = ?`, fence.ServerID, fence.Kind).Scan(&currentCredential); err != nil {
				return "", fmt.Errorf("read server credential fence: %w", err)
			}
			if currentCredential != expectedCredentialRevision {
				return "", ErrStaleRevision
			}
			return strconv.FormatInt(currentCredential, 10), nil
		}
		var storedHandle any
		if update.Handle != nil {
			if _, err := keyring.ParseHandle(string(*update.Handle)); err != nil {
				return "", ErrInvalidInput
			}
			storedHandle = string(*update.Handle)
		}
		var updated int64
		if err := transaction.QueryRowContext(ctx, `
			UPDATE server_credentials SET revision = revision + 1, handle = ?
			WHERE server_id = ? AND kind = ? AND revision = ?
			RETURNING revision`, storedHandle, fence.ServerID, fence.Kind, expectedCredentialRevision).Scan(&updated); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", ErrStaleRevision
			}
			return "", fmt.Errorf("publish server credential metadata: %w", err)
		}
		return strconv.FormatInt(updated, 10), nil
	}, nil
}

func recordKindForCredential(kind contract.ServerCredentialKind) keyring.RecordKind {
	switch kind {
	case contract.ServerCredentialStatic:
		return keyring.RecordStaticCredential
	case contract.ServerCredentialOAuthClient:
		return keyring.RecordOAuthClient
	case contract.ServerCredentialOAuthTokens:
		return keyring.RecordOAuthTokens
	default:
		return ""
	}
}

func authorityTx(ctx context.Context, transaction *sql.Tx, serverID string) (AuthorityMetadata, error) {
	if err := ensureServerExists(ctx, transaction, serverID); err != nil {
		return AuthorityMetadata{}, err
	}
	var registrationRevision int64
	if err := transaction.QueryRowContext(ctx, `
		SELECT revision FROM server_oauth_registrations WHERE server_id = ?`, serverID).Scan(&registrationRevision); err != nil {
		return AuthorityMetadata{}, fmt.Errorf("read OAuth registration fence: %w", err)
	}
	authority := AuthorityMetadata{RegistrationRevision: strconv.FormatInt(registrationRevision, 10)}
	rows, err := transaction.QueryContext(ctx, `
		SELECT kind, revision, handle FROM server_credentials
		WHERE server_id = ? ORDER BY kind`, serverID)
	if err != nil {
		return AuthorityMetadata{}, fmt.Errorf("read server credential metadata: %w", err)
	}
	defer func() { _ = rows.Close() }()
	count := 0
	for rows.Next() {
		count++
		var kind string
		var revision int64
		var handle sql.NullString
		if err := rows.Scan(&kind, &revision, &handle); err != nil {
			return AuthorityMetadata{}, err
		}
		revisionValue := strconv.FormatInt(revision, 10)
		var handleValue *string
		if handle.Valid {
			parsed, err := keyring.ParseHandle(handle.String)
			if err != nil {
				return AuthorityMetadata{}, fmt.Errorf("invalid opaque credential handle: %w", err)
			}
			value := string(parsed)
			handleValue = &value
		}
		switch contract.ServerCredentialKind(kind) {
		case contract.ServerCredentialStatic:
			authority.CredentialRevisions.StaticCredential = revisionValue
			authority.StaticCredentialHandle = handleValue
		case contract.ServerCredentialOAuthClient:
			authority.CredentialRevisions.OAuthClient = revisionValue
			authority.OAuthClientHandle = handleValue
		case contract.ServerCredentialOAuthTokens:
			authority.CredentialRevisions.OAuthTokens = revisionValue
			authority.OAuthTokensHandle = handleValue
		default:
			return AuthorityMetadata{}, fmt.Errorf("invalid server credential kind")
		}
	}
	if err := rows.Err(); err != nil {
		return AuthorityMetadata{}, err
	}
	if count != 3 {
		return AuthorityMetadata{}, fmt.Errorf("server credential metadata is incomplete")
	}
	return authority, nil
}
