package servers

import (
	"context"
	"database/sql"
	"sort"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

// CredentialSetupState is metadata evidence only; runtime resolution still owns
// material validation, expiry, and dispatch. Invalidation never means first setup.
func CredentialSetupState(transport contract.Transport, authority AuthorityMetadata) contract.ServerCredentialState {
	switch t := transport.(type) {
	case contract.StdioTransport:
		if len(t.SecretEnvironment) == 0 {
			return contract.ServerCredentialNotRequired
		}
	case contract.StreamableHTTPTransport:
		switch auth := t.Authentication.(type) {
		case contract.NoAuthentication:
			return contract.ServerCredentialNotRequired
		case contract.OAuthAuthentication:
			if registration, ok := auth.Registration.(contract.StaticOAuthRegistration); ok && registration.TokenEndpointAuthMethod != contract.TokenEndpointAuthNone && authority.OAuthClientHandle == nil {
				if authority.CredentialRevisions.OAuthClient == "0" {
					return contract.ServerCredentialAbsent
				}
				return contract.ServerCredentialReauthenticationRequired
			}
			if authority.CredentialRevisions.OAuthTokens == "0" {
				return contract.ServerCredentialAbsent
			}
			if authority.OAuthTokensHandle == nil || authority.RegistrationRevision == "0" {
				return contract.ServerCredentialReauthenticationRequired
			}
			return contract.ServerCredentialReady
		}
	default:
		return contract.ServerCredentialNotRequired
	}
	if authority.CredentialRevisions.StaticCredential == "0" {
		return contract.ServerCredentialAbsent
	}
	if authority.StaticCredentialHandle == nil {
		return contract.ServerCredentialUnavailable
	}
	return contract.ServerCredentialReady
}

// SetupFactsTx supplies bounded nonsecret setup facts for stopped doctor/setup
// consumers. It performs no keyring or OAuth discovery and creates no authority.
func SetupFactsTx(ctx context.Context, tx *sql.Tx) ([]contract.CredentialSetupFact, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,transport_json,desired_state FROM servers WHERE desired_state<>'deleted' ORDER BY id LIMIT 65`)
	if err != nil {
		return nil, err
	}
	type definition struct {
		id        string
		transport []byte
		desired   string
	}
	definitions := []definition{}
	for rows.Next() {
		var d definition
		if err := rows.Scan(&d.id, &d.transport, &d.desired); err != nil {
			_ = rows.Close()
			return nil, err
		}
		definitions = append(definitions, d)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(definitions) > 64 {
		return nil, ErrInvalidInput
	}
	out := []contract.CredentialSetupFact{}
	for _, d := range definitions {
		start := len(out)
		t, err := DecodeTransport(d.transport)
		if err != nil {
			return nil, err
		}
		a, err := authorityTx(ctx, tx, d.id)
		if err != nil {
			return nil, err
		}
		slots := []string{}
		switch t := t.(type) {
		case contract.StdioTransport:
			for _, slot := range t.SecretEnvironment {
				slots = append(slots, slot)
			}
		case contract.StreamableHTTPTransport:
			switch auth := t.Authentication.(type) {
			case contract.BearerAuthentication:
				slots = append(slots, "bearer")
			case contract.OAuthAuthentication:
				if a.CredentialRevisions.OAuthTokens == "0" {
					out = append(out, contract.CredentialSetupFact{ID: d.id, Kind: "oauth_tokens", Action: "sign_in"})
				}
				if registration, ok := auth.Registration.(contract.StaticOAuthRegistration); ok && registration.TokenEndpointAuthMethod != contract.TokenEndpointAuthNone && a.CredentialRevisions.OAuthClient == "0" {
					out = append(out, contract.CredentialSetupFact{ID: d.id, Kind: "oauth_client", Action: "supply_value"})
				}
			}
		}
		if len(slots) > 0 && a.CredentialRevisions.StaticCredential == "0" {
			sort.Strings(slots)
			unique := slots[:0]
			for _, slot := range slots {
				if len(unique) == 0 || unique[len(unique)-1] != slot {
					unique = append(unique, slot)
				}
			}
			out = append(out, contract.CredentialSetupFact{ID: d.id, Kind: "static_credential", Action: "supply_value", Slots: unique})
		}
		for i := start; i < len(out); i++ {
			out[i].Inactive = d.desired == "disabled"
		}
	}
	return out, nil
}
