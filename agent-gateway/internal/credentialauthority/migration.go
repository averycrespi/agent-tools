package credentialauthority

import (
	"context"
	"database/sql"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/oauth"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servercredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
)

// VerifyMigrationDependenciesTx checks custody completeness, not expiry or remote
// usability. Operational checks remain an explicit operator step.
func VerifyMigrationDependenciesTx(ctx context.Context, tx *sql.Tx) error {
	return servers.MigrationDependenciesTx(ctx, tx, func(d servers.MigrationDependency) error {
		requirements, err := parseRequirements(d.Transport)
		if err != nil {
			return err
		}
		a := d.Authority
		switch requirements.mode {
		case requirementStatic:
			if a.StaticCredentialHandle == nil || a.CredentialRevisions.StaticCredential == "0" {
				return keyring.ErrMigrationIncomplete
			}
		case requirementOAuth:
			if d.Registration.Revision == "0" || !servers.RegistrationMatchesDesired(d.Transport, d.Registration) || a.OAuthTokensHandle == nil || a.CredentialRevisions.OAuthTokens == "0" {
				return keyring.ErrMigrationIncomplete
			}
			if d.Registration.TokenEndpointAuthMethod != contract.TokenEndpointAuthNone && (a.OAuthClientHandle == nil || a.CredentialRevisions.OAuthClient == "0") {
				return keyring.ErrMigrationIncomplete
			}
			if d.Registration.TokenEndpointAuthMethod == contract.TokenEndpointAuthNone && a.OAuthClientHandle != nil {
				return keyring.ErrMigrationIncomplete
			}
		}
		return nil
	})
}

func VerifyMigrationMaterialTx(ctx context.Context, tx *sql.Tx, kind keyring.RecordKind, handle keyring.Handle, payload []byte) error {
	return servers.MigrationMaterialTx(ctx, tx, kind, handle, func(d servers.MigrationDependency) error {
		switch kind {
		case keyring.RecordStaticCredential:
			requirements, err := parseRequirements(d.Transport)
			generation, decodeErr := servercredentials.DecodeStaticGeneration(payload)
			if err != nil || decodeErr != nil || requirements.mode != requirementStatic || !sameSlots(generation.Values, requirements.slots) {
				return keyring.ErrMigrationIncomplete
			}
		case keyring.RecordOAuthClient:
			limit, ok := contract.FixedLimitByName("oauth_client_secret_bytes")
			if !ok || len(payload) == 0 || !utf8.Valid(payload) || int64(len(payload)) > limit.Maximum {
				return keyring.ErrMigrationIncomplete
			}
		case keyring.RecordOAuthTokens:
			tokens, err := oauth.DecodeTokenGeneration(payload)
			registration := d.Registration
			if err != nil || !servers.RegistrationMatchesDesired(d.Transport, registration) || tokens.ServerID != d.ID || tokens.Issuer != registration.Issuer || tokens.RegistrationRevision != registration.Revision || tokens.Resource != registration.ResourceURL {
				return keyring.ErrMigrationIncomplete
			}
		default:
			return keyring.ErrMigrationIncomplete
		}
		return nil
	})
}
