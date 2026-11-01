package servers

import (
	"database/sql"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestCredentialSetupFactsSurviveRestartWithoutMaterial(t *testing.T) {
	repo, store, owner := newRepository(t, new(sequenceReader))
	ctx := t.Context()
	transports := []string{
		`{"kind":"stdio","executable":"/bin/true","arguments":[],"working_directory":"/tmp","environment":{},"secret_environment":{"TOKEN":"api","OTHER":"api"}}`,
		`{"kind":"streamable_http","url":"https://resource.example/mcp","protocol_mode":"modern","authentication":{"mode":"bearer"}}`,
		`{"kind":"streamable_http","url":"https://resource.example/mcp","protocol_mode":"modern","authentication":{"mode":"oauth","registration":{"mode":"static","issuer":null,"client_id":"client","token_endpoint_auth_method":"client_secret_basic"},"trusted_origins":[],"request_offline_access":false}}`,
		`{"kind":"streamable_http","url":"https://public.example/mcp","protocol_mode":"modern","authentication":{"mode":"none"}}`,
	}
	ids := []string{}
	for i, raw := range transports {
		transport, err := DecodeTransport([]byte(raw))
		require.NoError(t, err)
		namespace := []string{"local", "bearer", "oauth", "public"}[i]
		created, err := repo.Create(ctx, CreateRequest{Definition: Definition{Namespace: namespace, DisplayName: namespace, Transport: transport}, Idempotency: idempotency("setup-"+namespace, "setup-"+namespace, "")})
		require.NoError(t, err)
		ids = append(ids, created.Server.ID)
	}
	require.NoError(t, store.Close())
	reopened, err := storage.Open(ctx, owner)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	repo, err = New(reopened, &mutableClock{now: testTime}, new(sequenceReader))
	require.NoError(t, err)
	require.NoError(t, reopened.View(ctx, func(tx *sql.Tx) error {
		facts, err := SetupFactsTx(ctx, tx)
		require.NoError(t, err)
		require.ElementsMatch(t, []contract.CredentialSetupFact{
			{ID: ids[0], Kind: "static_credential", Action: "supply_value", Slots: []string{"api"}},
			{ID: ids[1], Kind: "static_credential", Action: "supply_value", Slots: []string{"bearer"}},
			{ID: ids[2], Kind: "oauth_client", Action: "supply_value"},
			{ID: ids[2], Kind: "oauth_tokens", Action: "sign_in"},
		}, facts)
		return nil
	}))
	for i, id := range ids {
		server, err := repo.Get(ctx, id)
		require.NoError(t, err)
		authority, err := repo.Authority(ctx, id)
		require.NoError(t, err)
		require.Nil(t, authority.StaticCredentialHandle)
		require.Nil(t, authority.OAuthTokensHandle)
		require.Nil(t, authority.OAuthClientHandle)
		transport, err := DecodeTransport(server.Transport)
		require.NoError(t, err)
		if i == 3 {
			require.Equal(t, contract.ServerCredentialNotRequired, CredentialSetupState(transport, authority))
		} else {
			require.Equal(t, contract.ServerCredentialAbsent, CredentialSetupState(transport, authority))
		}
	}
}

func TestCredentialSetupStateDoesNotInferReadyFromUnrelatedOrInvalidatedMaterial(t *testing.T) {
	bearer, err := DecodeTransport([]byte(`{"kind":"streamable_http","url":"https://resource.example/mcp","protocol_mode":"modern","authentication":{"mode":"bearer"}}`))
	require.NoError(t, err)
	a := AuthorityMetadata{CredentialRevisions: contract.CredentialRevisions{StaticCredential: "0", OAuthTokens: "7"}, OAuthTokensHandle: stringPointer("unrelated")}
	require.Equal(t, contract.ServerCredentialAbsent, CredentialSetupState(bearer, a))
	a.CredentialRevisions.StaticCredential = "2"
	require.Equal(t, contract.ServerCredentialUnavailable, CredentialSetupState(bearer, a))
}
