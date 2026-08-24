// Package credentialauthority validates current server-scoped authority before activation.
package credentialauthority

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/oauth"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/runtimes"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servercredentials"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
)

type Repository interface {
	Get(context.Context, string) (servers.Server, error)
	Authority(context.Context, string) (servers.AuthorityMetadata, error)
	OAuthRegistration(context.Context, string) (servers.OAuthRegistrationAuthority, error)
}

type Coordinator interface {
	ReadActive(context.Context, keyring.Namespace) ([]byte, keyring.CutoverResult, error)
}

type Resolver struct {
	repository     Repository
	coordinator    Coordinator
	installationID string
	now            func() time.Time
}

func New(repository Repository, coordinator Coordinator, installationID string, now func() time.Time) (*Resolver, error) {
	if repository == nil || coordinator == nil || now == nil {
		return nil, errors.New("credential authority dependencies are incomplete")
	}
	if _, err := keyring.NewNamespace(installationID, installationID, keyring.RecordStaticCredential); err != nil {
		return nil, err
	}
	return &Resolver{repository: repository, coordinator: coordinator, installationID: installationID, now: now}, nil
}

func (resolver *Resolver) Resolve(ctx context.Context, candidate runtimes.Candidate) runtimes.AuthorityOutcome {
	configuration, err := parseRequirements(candidate.Server.Transport)
	if err != nil {
		return rejected(contract.ServerCredentialUnavailable, contract.ReasonConfigurationInvalid, false)
	}
	switch configuration.mode {
	case requirementNone:
		return runtimes.AuthorityOutcome{CredentialState: contract.ServerCredentialNotRequired}
	case requirementStatic:
		return resolver.resolveStatic(ctx, candidate, configuration.slots)
	case requirementOAuth:
		return resolver.resolveOAuth(ctx, candidate)
	default:
		return rejected(contract.ServerCredentialUnavailable, contract.ReasonConfigurationInvalid, false)
	}
}

func (resolver *Resolver) resolveStatic(ctx context.Context, candidate runtimes.Candidate, slots map[string]struct{}) runtimes.AuthorityOutcome {
	authority := candidate.Authority
	if authority.StaticCredentialHandle == nil || authority.CredentialRevisions.StaticCredential == "0" {
		return rejected(contract.ServerCredentialAbsent, contract.ReasonCredentialAbsent, false)
	}
	contents, result, err := resolver.read(ctx, candidate.Server.ID, keyring.RecordStaticCredential)
	if err != nil {
		return mapReadError(err)
	}
	defer clear(contents)
	if string(result.Handle) != *authority.StaticCredentialHandle || result.Revision != authority.CredentialRevisions.StaticCredential {
		return rejected(contract.ServerCredentialAbsent, contract.ReasonCredentialAbsent, false)
	}
	generation, err := servercredentials.DecodeStaticGeneration(contents)
	if err != nil || !sameSlots(generation.Values, slots) {
		return rejected(contract.ServerCredentialUnavailable, contract.ReasonKeyringUnavailable, false)
	}
	if !resolver.staticCurrent(ctx, candidate) {
		return rejected(contract.ServerCredentialUnavailable, contract.ReasonSuperseded, false)
	}
	lease, err := runtimes.NewMaterialLease(candidate.Key(), map[contract.ServerCredentialKind][]byte{contract.ServerCredentialStatic: contents})
	if err != nil {
		return rejected(contract.ServerCredentialUnavailable, contract.ReasonKeyringUnavailable, false)
	}
	return runtimes.AuthorityOutcome{CredentialState: contract.ServerCredentialReady, Lease: lease}
}

func (resolver *Resolver) staticCurrent(ctx context.Context, candidate runtimes.Candidate) bool {
	server, err := resolver.repository.Get(ctx, candidate.Server.ID)
	if err != nil {
		return false
	}
	authority, err := resolver.repository.Authority(ctx, candidate.Server.ID)
	if err != nil {
		return false
	}
	current := candidate
	current.Server = server
	current.Authority = authority
	return current.Key() == candidate.Key() && sameStringPointer(authority.StaticCredentialHandle, candidate.Authority.StaticCredentialHandle)
}

func sameStringPointer(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (resolver *Resolver) resolveOAuth(ctx context.Context, candidate runtimes.Candidate) runtimes.AuthorityOutcome {
	authority := candidate.Authority
	registration, err := resolver.repository.OAuthRegistration(ctx, candidate.Server.ID)
	if err != nil || registration.Revision == "0" || registration.Revision != authority.RegistrationRevision || !servers.RegistrationMatchesDesired(candidate.Server.Transport, registration) {
		return rejected(contract.ServerCredentialReauthenticationRequired, contract.ReasonCredentialAbsent, false)
	}
	if registration.ClientSecretExpiresAt != nil {
		expires, parseErr := time.Parse(time.RFC3339Nano, *registration.ClientSecretExpiresAt)
		if parseErr != nil || !resolver.now().Before(expires) {
			return rejected(contract.ServerCredentialReauthenticationRequired, contract.ReasonRegistrationExpired, false)
		}
	}
	if authority.OAuthTokensHandle == nil || authority.CredentialRevisions.OAuthTokens == "0" {
		return rejected(contract.ServerCredentialReauthenticationRequired, contract.ReasonCredentialAbsent, false)
	}
	var clientSecret []byte
	if registration.TokenEndpointAuthMethod == contract.TokenEndpointAuthNone {
		if authority.OAuthClientHandle != nil {
			return rejected(contract.ServerCredentialReauthenticationRequired, contract.ReasonCredentialAbsent, false)
		}
	} else {
		if authority.OAuthClientHandle == nil || authority.CredentialRevisions.OAuthClient == "0" {
			return rejected(contract.ServerCredentialReauthenticationRequired, contract.ReasonCredentialAbsent, false)
		}
		secret, result, readErr := resolver.read(ctx, candidate.Server.ID, keyring.RecordOAuthClient)
		if readErr != nil {
			return mapReadError(readErr)
		}
		defer clear(secret)
		if string(result.Handle) != *authority.OAuthClientHandle || result.Revision != authority.CredentialRevisions.OAuthClient {
			return rejected(contract.ServerCredentialReauthenticationRequired, contract.ReasonCredentialAbsent, false)
		}
		clientLimit, ok := contract.FixedLimitByName("oauth_client_secret_bytes")
		if !ok || len(secret) == 0 || !utf8.Valid(secret) || int64(len(secret)) > clientLimit.Maximum {
			return rejected(contract.ServerCredentialReauthenticationRequired, contract.ReasonOAuthRejected, false)
		}
		clientSecret = secret
	}
	contents, result, readErr := resolver.read(ctx, candidate.Server.ID, keyring.RecordOAuthTokens)
	if readErr != nil {
		return mapReadError(readErr)
	}
	defer clear(contents)
	if string(result.Handle) != *authority.OAuthTokensHandle || result.Revision != authority.CredentialRevisions.OAuthTokens {
		return rejected(contract.ServerCredentialReauthenticationRequired, contract.ReasonCredentialAbsent, false)
	}
	tokens, decodeErr := oauth.DecodeTokenGeneration(contents)
	if decodeErr != nil || tokens.ServerID != candidate.Server.ID || tokens.Issuer != registration.Issuer || tokens.RegistrationRevision != registration.Revision || tokens.Resource != registration.ResourceURL {
		return rejected(contract.ServerCredentialReauthenticationRequired, contract.ReasonOAuthRejected, false)
	}
	if tokens.ExpiresAt != nil {
		expires, parseErr := time.Parse(time.RFC3339Nano, *tokens.ExpiresAt)
		if parseErr != nil || !resolver.now().Before(expires) {
			return rejected(contract.ServerCredentialReauthenticationRequired, contract.ReasonOAuthExpired, false)
		}
	}
	if !resolver.oauthCurrent(ctx, candidate, registration) {
		return rejected(contract.ServerCredentialReauthenticationRequired, contract.ReasonSuperseded, false)
	}
	accessToken := []byte(tokens.AccessToken)
	defer clear(accessToken)
	metadata := runtimes.OAuthMaterialMetadata{Scopes: tokens.Scopes, ScopeSpecified: tokens.ScopeSpecified, ExpiresAt: tokens.ExpiresAt}
	lease, err := runtimes.NewOAuthMaterialLease(candidate.Key(), clientSecret, accessToken, metadata)
	if err != nil {
		return rejected(contract.ServerCredentialReauthenticationRequired, contract.ReasonKeyringUnavailable, false)
	}
	return runtimes.AuthorityOutcome{CredentialState: contract.ServerCredentialReady, Lease: lease}
}

func (resolver *Resolver) oauthCurrent(ctx context.Context, candidate runtimes.Candidate, registration servers.OAuthRegistrationAuthority) bool {
	server, err := resolver.repository.Get(ctx, candidate.Server.ID)
	if err != nil {
		return false
	}
	authority, err := resolver.repository.Authority(ctx, candidate.Server.ID)
	if err != nil {
		return false
	}
	currentRegistration, err := resolver.repository.OAuthRegistration(ctx, candidate.Server.ID)
	if err != nil || !sameRegistration(currentRegistration, registration) {
		return false
	}
	current := candidate
	current.Server = server
	current.Authority = authority
	if current.Key() != candidate.Key() || !sameStringPointer(authority.OAuthTokensHandle, candidate.Authority.OAuthTokensHandle) {
		return false
	}
	if registration.TokenEndpointAuthMethod == contract.TokenEndpointAuthNone {
		return authority.OAuthClientHandle == nil && candidate.Authority.OAuthClientHandle == nil
	}
	return sameStringPointer(authority.OAuthClientHandle, candidate.Authority.OAuthClientHandle)
}

func sameRegistration(left, right servers.OAuthRegistrationAuthority) bool {
	return left.Revision == right.Revision && left.Mode == right.Mode && left.Issuer == right.Issuer && left.ClientID == right.ClientID && left.CallbackURL == right.CallbackURL && left.ResourceURL == right.ResourceURL && left.TokenEndpointAuthMethod == right.TokenEndpointAuthMethod && left.CreatedAt == right.CreatedAt && sameStringPointer(left.ClientSecretExpiresAt, right.ClientSecretExpiresAt)
}

func (resolver *Resolver) read(ctx context.Context, serverID string, kind keyring.RecordKind) ([]byte, keyring.CutoverResult, error) {
	namespace, err := keyring.NewNamespace(resolver.installationID, serverID, kind)
	if err != nil {
		return nil, keyring.CutoverResult{}, err
	}
	return resolver.coordinator.ReadActive(ctx, namespace)
}

type requirementMode uint8

const (
	requirementNone requirementMode = iota
	requirementStatic
	requirementOAuth
)

type requirements struct {
	mode  requirementMode
	slots map[string]struct{}
}

func parseRequirements(contents []byte) (requirements, error) {
	transport, err := servers.DecodeTransport(contents)
	if err != nil {
		return requirements{}, err
	}
	switch value := transport.(type) {
	case contract.StdioTransport:
		slots := make(map[string]struct{})
		for _, slot := range value.SecretEnvironment {
			slots[slot] = struct{}{}
		}
		if len(slots) == 0 {
			return requirements{mode: requirementNone}, nil
		}
		return requirements{mode: requirementStatic, slots: slots}, nil
	case contract.StreamableHTTPTransport:
		switch value.Authentication.(type) {
		case contract.NoAuthentication:
			return requirements{mode: requirementNone}, nil
		case contract.BearerAuthentication:
			return requirements{mode: requirementStatic, slots: map[string]struct{}{"bearer": {}}}, nil
		case contract.OAuthAuthentication:
			return requirements{mode: requirementOAuth}, nil
		}
	}
	return requirements{}, errors.New("unsupported credential requirements")
}

func sameSlots(values map[string]string, slots map[string]struct{}) bool {
	if len(values) != len(slots) {
		return false
	}
	for slot := range slots {
		if values[slot] == "" {
			return false
		}
	}
	return true
}

func rejected(state contract.ServerCredentialState, reason contract.PublicReason, retry bool) runtimes.AuthorityOutcome {
	return runtimes.AuthorityOutcome{State: contract.RuntimeAuthenticationRequired, CredentialState: state, Reason: &reason, Retryable: retry}
}

func mapReadError(err error) runtimes.AuthorityOutcome {
	if errors.Is(err, keyring.ErrWorkLimit) {
		return rejected(contract.ServerCredentialUnavailable, contract.ReasonResourceLimit, true)
	}
	if errors.Is(err, keyring.ErrNoAuthority) || errors.Is(err, keyring.ErrNotFound) {
		return rejected(contract.ServerCredentialAbsent, contract.ReasonCredentialAbsent, false)
	}
	var capability *keyring.CapabilityError
	if errors.As(err, &capability) {
		switch capability.Capability.State {
		case contract.KeyringAbsent:
			return rejected(contract.ServerCredentialAbsent, contract.ReasonKeyringAbsent, false)
		case contract.KeyringLocked:
			return rejected(contract.ServerCredentialLocked, contract.ReasonKeyringLocked, false)
		case contract.KeyringInteractionRequired:
			return rejected(contract.ServerCredentialInteractionRequired, contract.ReasonKeyringInteractionRequired, false)
		case contract.KeyringUnsupported:
			return rejected(contract.ServerCredentialUnsupported, contract.ReasonKeyringUnsupported, false)
		default:
			return rejected(contract.ServerCredentialUnavailable, contract.ReasonKeyringUnavailable, true)
		}
	}
	return rejected(contract.ServerCredentialUnavailable, contract.ReasonKeyringUnavailable, false)
}
