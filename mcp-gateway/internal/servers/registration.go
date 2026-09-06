package servers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
)

type OAuthRegistrationAuthority struct {
	Revision                string
	Mode                    contract.RegistrationMode
	Issuer                  string
	ClientID                string
	CallbackURL             string
	ResourceURL             string
	TokenEndpointAuthMethod contract.TokenEndpointAuthMethod
	CreatedAt               string
	ClientSecretExpiresAt   *string
}

type RegistrationFence struct {
	ServerID                     string
	ExpectedDesiredRevision      string
	ExpectedRegistrationRevision string
	ExpectedOAuthClientRevision  string
	ExpectedAuthFlowID           string
}

func (repository *Repository) OAuthRegistration(ctx context.Context, serverID string) (OAuthRegistrationAuthority, error) {
	if !validID(serverID) {
		return OAuthRegistrationAuthority{}, ErrNotFound
	}
	var registration OAuthRegistrationAuthority
	err := repository.store.View(ctx, func(transaction *sql.Tx) error {
		var revision int64
		var mode, issuer, clientID, callbackURL, resourceURL, method, createdAt, expires sql.NullString
		if err := transaction.QueryRowContext(ctx, `
			SELECT revision, mode, issuer, client_id, callback_url, resource_url,
			       token_endpoint_auth_method, created_at, client_secret_expires_at
			FROM server_oauth_registrations WHERE server_id = ?`, serverID).Scan(
			&revision, &mode, &issuer, &clientID, &callbackURL, &resourceURL, &method, &createdAt, &expires,
		); err != nil {
			return err
		}
		registration.Revision = strconv.FormatInt(revision, 10)
		if revision == 0 {
			return nil
		}
		registration.Mode = contract.RegistrationMode(mode.String)
		registration.Issuer = issuer.String
		registration.ClientID = clientID.String
		registration.CallbackURL = callbackURL.String
		registration.ResourceURL = resourceURL.String
		registration.TokenEndpointAuthMethod = contract.TokenEndpointAuthMethod(method.String)
		registration.CreatedAt = createdAt.String
		if expires.Valid {
			value := expires.String
			registration.ClientSecretExpiresAt = &value
		}
		return validateRegistrationAuthority(registration)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return OAuthRegistrationAuthority{}, ErrNotFound
	}
	return registration, mapViewError(err)
}

func (repository *Repository) InvalidateOAuthRegistrationForDelete(ctx context.Context, serverID, expectedRevision string) (string, error) {
	if !validID(serverID) {
		return "", ErrNotFound
	}
	expected, err := parseRevision(expectedRevision)
	if err != nil {
		return "", ErrStaleRevision
	}
	var revision int64
	err = repository.store.Mutate(ctx, func(transaction *sql.Tx) error {
		var state contract.DesiredServerState
		if err := transaction.QueryRowContext(ctx, `SELECT desired_state FROM servers WHERE id = ?`, serverID).Scan(&state); err != nil {
			return err
		}
		if state != contract.DesiredServerDeleted {
			return ErrInvalidOperation
		}
		if err := transaction.QueryRowContext(ctx, `
			UPDATE server_oauth_registrations SET
				revision = revision + 1, mode = NULL, issuer = NULL, client_id = NULL,
				callback_url = NULL, resource_url = NULL, token_endpoint_auth_method = NULL,
				created_at = NULL, client_secret_expires_at = NULL
			WHERE server_id = ? AND revision = ? AND revision > 0
			RETURNING revision`, serverID, expected).Scan(&revision); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrStaleRevision
			}
			return err
		}
		return audit.MutationTx(audit.WithSystem(ctx), transaction, repository.clock.Now(), "oauth", "invalidate_registration", contract.AuditTarget{Type: "server", ID: serverID})
	})
	return formatRevision(revision), mapMutationError(err)
}

func (repository *Repository) PublishPublicRegistration(ctx context.Context, fence RegistrationFence, registration OAuthRegistrationAuthority) (OAuthRegistrationAuthority, error) {
	if err := validateRegistrationFence(fence); err != nil || registration.ClientSecretExpiresAt != nil {
		return OAuthRegistrationAuthority{}, ErrInvalidInput
	}
	if err := validateRegistrationAuthority(registration); err != nil {
		return OAuthRegistrationAuthority{}, err
	}
	var published OAuthRegistrationAuthority
	err := repository.store.Mutate(ctx, func(transaction *sql.Tx) error {
		if err := validateRegistrationCurrent(ctx, transaction, fence, registration); err != nil {
			return err
		}
		updated, err := updateRegistration(ctx, transaction, fence, registration)
		if err != nil {
			return err
		}
		published = registration
		published.Revision = updated
		return audit.MutationTx(audit.WithSystem(ctx), transaction, repository.clock.Now(), "oauth", "publish_registration", contract.AuditTarget{Type: "server", ID: fence.ServerID})
	})
	return published, mapMutationError(err)
}

func (repository *Repository) RegistrationAuthorityCallback(fence RegistrationFence, registration OAuthRegistrationAuthority) (keyring.AuthorityCallback, error) {
	if err := validateRegistrationFence(fence); err != nil || registration.TokenEndpointAuthMethod == contract.TokenEndpointAuthNone {
		return nil, ErrInvalidInput
	}
	if err := validateRegistrationAuthority(registration); err != nil {
		return nil, err
	}
	return func(ctx context.Context, transaction *sql.Tx, update keyring.AuthorityUpdate) (string, error) {
		if transaction == nil || update.Owner != fence.ServerID || update.Kind != keyring.RecordOAuthClient || update.PriorPublishedRevision != "" || update.ExactPublishedRevision != "" || update.ExactInvalidation {
			return "", ErrInvalidInput
		}
		if err := validateRegistrationCurrent(ctx, transaction, fence, registration); err != nil {
			return "", err
		}
		if update.ValidateOnly {
			if update.Handle != nil {
				return "", ErrInvalidInput
			}
			return fence.ExpectedOAuthClientRevision, nil
		}
		if update.Handle == nil {
			return "", ErrInvalidInput
		}
		if _, err := keyring.ParseHandle(string(*update.Handle)); err != nil {
			return "", ErrInvalidInput
		}
		if _, err := updateRegistration(ctx, transaction, fence, registration); err != nil {
			return "", err
		}
		expectedCredential, _ := parseRevision(fence.ExpectedOAuthClientRevision)
		var revision int64
		if err := transaction.QueryRowContext(ctx, `
			UPDATE server_credentials SET revision = revision + 1, handle = ?
			WHERE server_id = ? AND kind = ? AND revision = ? AND handle IS NULL
			RETURNING revision`, string(*update.Handle), fence.ServerID, contract.ServerCredentialOAuthClient, expectedCredential).Scan(&revision); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", ErrStaleRevision
			}
			return "", fmt.Errorf("publish OAuth client authority: %w", err)
		}
		if err := audit.MutationTx(audit.WithSystem(ctx), transaction, repository.clock.Now(), "oauth", "publish_registration", contract.AuditTarget{Type: "server", ID: fence.ServerID}); err != nil {
			return "", err
		}
		if err := audit.MutationTx(audit.WithSystem(ctx), transaction, repository.clock.Now(), "server_credential", "replace", contract.AuditTarget{Type: "server", ID: fence.ServerID}); err != nil {
			return "", err
		}
		return strconv.FormatInt(revision, 10), nil
	}, nil
}

func validateRegistrationFence(fence RegistrationFence) error {
	if !validID(fence.ServerID) {
		return ErrNotFound
	}
	desired, err := parseRevision(fence.ExpectedDesiredRevision)
	if err != nil || desired < 1 {
		return ErrStaleRevision
	}
	if _, err := parseRevision(fence.ExpectedRegistrationRevision); err != nil {
		return ErrStaleRevision
	}
	if _, err := parseRevision(fence.ExpectedOAuthClientRevision); err != nil {
		return ErrStaleRevision
	}
	if fence.ExpectedAuthFlowID != "" && !validID(fence.ExpectedAuthFlowID) {
		return ErrStaleRevision
	}
	return nil
}

func validateRegistrationCurrent(ctx context.Context, transaction *sql.Tx, fence RegistrationFence, registration OAuthRegistrationAuthority) error {
	desired, _ := parseRevision(fence.ExpectedDesiredRevision)
	expectedRegistration, _ := parseRevision(fence.ExpectedRegistrationRevision)
	client, _ := parseRevision(fence.ExpectedOAuthClientRevision)
	var currentDesired, currentRegistration, currentClient int64
	var state contract.DesiredServerState
	var handle sql.NullString
	var transportJSON string
	if err := transaction.QueryRowContext(ctx, `SELECT desired_revision, desired_state, transport_json FROM servers WHERE id = ?`, fence.ServerID).Scan(&currentDesired, &state, &transportJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if err := transaction.QueryRowContext(ctx, `SELECT revision FROM server_oauth_registrations WHERE server_id = ?`, fence.ServerID).Scan(&currentRegistration); err != nil {
		return err
	}
	if err := transaction.QueryRowContext(ctx, `SELECT revision, handle FROM server_credentials WHERE server_id = ? AND kind = ?`, fence.ServerID, contract.ServerCredentialOAuthClient).Scan(&currentClient, &handle); err != nil {
		return err
	}
	confidentialStatic := registration.Mode == contract.RegistrationStatic && (registration.TokenEndpointAuthMethod == contract.TokenEndpointAuthClientSecretBasic || registration.TokenEndpointAuthMethod == contract.TokenEndpointAuthClientSecretPost)
	if state == contract.DesiredServerDeleted || currentDesired != desired || currentRegistration != expectedRegistration || currentClient != client || handle.Valid != (confidentialStatic && currentClient > 0) || !RegistrationMatchesDesired([]byte(transportJSON), registration) {
		return ErrStaleRevision
	}
	if fence.ExpectedAuthFlowID != "" {
		var flowState contract.AuthFlowState
		var flowServerID string
		var flowDesired int64
		if err := transaction.QueryRowContext(ctx, `
			SELECT server_id, flow_state, target_desired_revision
			FROM server_auth_flows WHERE id = ?`, fence.ExpectedAuthFlowID).Scan(&flowServerID, &flowState, &flowDesired); err != nil {
			return ErrStaleRevision
		}
		if flowServerID != fence.ServerID || flowState != contract.AuthFlowPreparing || flowDesired != desired {
			return ErrStaleRevision
		}
	}
	return nil
}

func RegistrationMatchesDesired(contents []byte, authority OAuthRegistrationAuthority) bool {
	decoded, err := DecodeTransport(contents)
	if err != nil {
		return false
	}
	transport, ok := decoded.(contract.StreamableHTTPTransport)
	if !ok || transport.URL != authority.ResourceURL {
		return false
	}
	authentication, ok := transport.Authentication.(contract.OAuthAuthentication)
	if !ok {
		return false
	}
	switch desired := authentication.Registration.(type) {
	case contract.StaticOAuthRegistration:
		if authority.Mode != contract.RegistrationStatic || desired.ClientID != authority.ClientID || desired.TokenEndpointAuthMethod != authority.TokenEndpointAuthMethod {
			return false
		}
		return desired.Issuer == nil || *desired.Issuer == authority.Issuer
	case contract.DynamicOAuthRegistration:
		return authority.Mode == contract.RegistrationDynamic && (desired.Issuer == nil || *desired.Issuer == authority.Issuer)
	default:
		return false
	}
}

func updateRegistration(ctx context.Context, transaction *sql.Tx, fence RegistrationFence, registration OAuthRegistrationAuthority) (string, error) {
	expected, _ := parseRevision(fence.ExpectedRegistrationRevision)
	var revision int64
	if err := transaction.QueryRowContext(ctx, `
		UPDATE server_oauth_registrations
		SET revision = revision + 1, mode = ?, issuer = ?, client_id = ?, callback_url = ?,
		    resource_url = ?, token_endpoint_auth_method = ?, created_at = ?, client_secret_expires_at = ?
		WHERE server_id = ? AND revision = ?
		RETURNING revision`, registration.Mode, registration.Issuer, registration.ClientID, registration.CallbackURL,
		registration.ResourceURL, registration.TokenEndpointAuthMethod, registration.CreatedAt, registration.ClientSecretExpiresAt,
		fence.ServerID, expected).Scan(&revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrStaleRevision
		}
		return "", fmt.Errorf("publish OAuth registration: %w", err)
	}
	return strconv.FormatInt(revision, 10), nil
}

func validateRegistrationAuthority(registration OAuthRegistrationAuthority) error {
	if registration.Revision == "" {
		registration.Revision = "0"
	}
	if _, err := parseRevision(registration.Revision); err != nil || (registration.Mode != contract.RegistrationStatic && registration.Mode != contract.RegistrationDynamic) || registration.Issuer == "" || registration.ClientID == "" || !utf8.ValidString(registration.ClientID) || int64(len(registration.ClientID)) > mustLimit("oauth_client_id_bytes") || registration.CallbackURL == "" || registration.ResourceURL == "" || registration.CreatedAt == "" {
		return ErrInvalidInput
	}
	issuer, err := url.Parse(registration.Issuer)
	if err != nil || issuer.Scheme != "https" || issuer.Host == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" || issuer.String() != registration.Issuer {
		return ErrInvalidInput
	}
	resource, err := parseEndpointURL(registration.ResourceURL)
	if err != nil || resource.Scheme != "https" {
		return ErrInvalidInput
	}
	callback, err := url.Parse(registration.CallbackURL)
	if err != nil || callback.Scheme != "http" || callback.User != nil || callback.RawQuery != "" || callback.Fragment != "" || callback.Path != "/oauth/callback" || callback.Port() == "" || callback.String() != registration.CallbackURL {
		return ErrInvalidInput
	}
	callbackAddress := net.ParseIP(callback.Hostname())
	if callbackAddress == nil || callbackAddress.To4() == nil || !callbackAddress.IsLoopback() {
		return ErrInvalidInput
	}
	if _, err := contract.ParseTokenEndpointAuthMethod(string(registration.TokenEndpointAuthMethod)); err != nil {
		return ErrInvalidInput
	}
	if _, err := time.Parse(time.RFC3339Nano, registration.CreatedAt); err != nil {
		return ErrInvalidInput
	}
	if registration.ClientSecretExpiresAt != nil {
		if _, err := time.Parse(time.RFC3339Nano, *registration.ClientSecretExpiresAt); err != nil {
			return ErrInvalidInput
		}
	}
	return nil
}
