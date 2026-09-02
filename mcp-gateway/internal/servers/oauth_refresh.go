package servers

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
)

type OAuthRefreshFence struct {
	ServerID                     string
	ExpectedDesiredRevision      string
	ExpectedRegistrationRevision string
	ExpectedOAuthClientRevision  string
	ExpectedOAuthTokensRevision  string
}

type OAuthRefreshContext struct {
	Configuration AuthFlowOAuthConfiguration
	Registration  OAuthRegistrationAuthority
	Authority     AuthorityMetadata
	Fence         OAuthRefreshFence
}

func (repository *Repository) PrepareOAuthRefresh(ctx context.Context, serverID string) (OAuthRefreshContext, error) {
	if !validID(serverID) {
		return OAuthRefreshContext{}, ErrNotFound
	}
	var result OAuthRefreshContext
	err := repository.store.View(ctx, func(transaction *sql.Tx) error {
		server, err := serverByIDTx(ctx, transaction, serverID)
		if err != nil {
			return err
		}
		if server.DesiredState != contract.DesiredServerEnabled {
			return ErrInvalidOperation
		}
		configuration, err := authFlowConfiguration(server)
		if err != nil {
			return err
		}
		authority, err := authorityTx(ctx, transaction, serverID)
		if err != nil {
			return err
		}
		if authority.OAuthTokensHandle == nil || authority.CredentialRevisions.OAuthTokens == "0" {
			return ErrInvalidOperation
		}
		registration, err := oauthRegistrationTx(ctx, transaction, serverID)
		if err != nil || registration.Revision == "0" {
			return ErrInvalidOperation
		}
		result = OAuthRefreshContext{Configuration: configuration, Registration: registration, Authority: authority, Fence: OAuthRefreshFence{
			ServerID: serverID, ExpectedDesiredRevision: server.DesiredRevision,
			ExpectedRegistrationRevision: authority.RegistrationRevision,
			ExpectedOAuthClientRevision:  authority.CredentialRevisions.OAuthClient,
			ExpectedOAuthTokensRevision:  authority.CredentialRevisions.OAuthTokens,
		}}
		return nil
	})
	return result, mapViewError(err)
}

func (repository *Repository) OAuthRefreshAuthorityCallback(fence OAuthRefreshFence) (keyring.AuthorityCallback, error) {
	if !validID(fence.ServerID) {
		return nil, ErrNotFound
	}
	base, err := repository.CredentialAuthorityCallback(CredentialFence{ServerID: fence.ServerID, Kind: contract.ServerCredentialOAuthTokens, ExpectedDesiredRevision: fence.ExpectedDesiredRevision, ExpectedCredentialRevision: fence.ExpectedOAuthTokensRevision, ExpectedRegistrationRevision: fence.ExpectedRegistrationRevision})
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, transaction *sql.Tx, update keyring.AuthorityUpdate) (string, error) {
		if transaction == nil || update.Owner != fence.ServerID || update.Kind != keyring.RecordOAuthTokens {
			return "", ErrInvalidInput
		}
		if !update.ExactInvalidation {
			var clientRevision int64
			if err := transaction.QueryRowContext(ctx, `SELECT revision FROM server_credentials WHERE server_id = ? AND kind = 'oauth_client'`, fence.ServerID).Scan(&clientRevision); err != nil {
				return "", err
			}
			expected, err := parseRevision(fence.ExpectedOAuthClientRevision)
			if err != nil || clientRevision != expected {
				return "", ErrStaleRevision
			}
			var expires sql.NullString
			if err := transaction.QueryRowContext(ctx, `SELECT client_secret_expires_at FROM server_oauth_registrations WHERE server_id = ?`, fence.ServerID).Scan(&expires); err != nil {
				return "", err
			}
			if expires.Valid {
				value, err := time.Parse(time.RFC3339Nano, expires.String)
				if err != nil || !repository.clock.Now().Before(value) {
					return "", ErrStaleRevision
				}
			}
		}
		return base(ctx, transaction, update)
	}, nil
}

func oauthRegistrationTx(ctx context.Context, transaction *sql.Tx, serverID string) (OAuthRegistrationAuthority, error) {
	var registration OAuthRegistrationAuthority
	var revision int64
	var mode, issuer, clientID, callbackURL, resourceURL, method, createdAt, expires sql.NullString
	err := transaction.QueryRowContext(ctx, `SELECT revision, mode, issuer, client_id, callback_url, resource_url, token_endpoint_auth_method, created_at, client_secret_expires_at FROM server_oauth_registrations WHERE server_id = ?`, serverID).Scan(&revision, &mode, &issuer, &clientID, &callbackURL, &resourceURL, &method, &createdAt, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return registration, ErrNotFound
	}
	if err != nil {
		return registration, err
	}
	registration = OAuthRegistrationAuthority{Revision: formatRevision(revision), Mode: contract.RegistrationMode(mode.String), Issuer: issuer.String, ClientID: clientID.String, CallbackURL: callbackURL.String, ResourceURL: resourceURL.String, TokenEndpointAuthMethod: contract.TokenEndpointAuthMethod(method.String), CreatedAt: createdAt.String}
	if expires.Valid {
		value := expires.String
		registration.ClientSecretExpiresAt = &value
	}
	if revision != 0 {
		if err := validateRegistrationAuthority(registration); err != nil {
			return OAuthRegistrationAuthority{}, err
		}
	}
	return registration, nil
}

func formatRevision(value int64) string { return strconv.FormatInt(value, 10) }
