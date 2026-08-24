package servers

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCredentialReplacementPlanValidatesExactStaticSlotsAndPublishesOperationAtomically(t *testing.T) {
	repository, store, _ := newRepository(t, new(sequenceReader))
	transport := testStdioTransport()
	transport.SecretEnvironment = map[string]string{"TOKEN": "token", "SECOND_TOKEN": "token"}
	created, err := repository.Create(context.Background(), CreateRequest{
		Definition:  Definition{Namespace: "replacement", DisplayName: "Replacement", Enabled: false, Transport: transport},
		Idempotency: idempotency("replacement-create", "replacement-create", ""),
	})
	require.NoError(t, err)

	_, err = repository.PrepareCredentialReplacement(context.Background(), CredentialReplacementRequest{
		ServerID: created.Server.ID, Kind: contract.ServerCredentialStatic, ExpectedDesiredRevision: "1", ExpectedCredentialRevision: "0", Slots: []string{"extra"},
	})
	assert.ErrorIs(t, err, ErrInvalidOperation)

	plan, err := repository.PrepareCredentialReplacement(context.Background(), CredentialReplacementRequest{
		ServerID: created.Server.ID, Kind: contract.ServerCredentialStatic, ExpectedDesiredRevision: "1", ExpectedCredentialRevision: "0", Slots: []string{"token"},
	})
	require.NoError(t, err)
	callback, result := repository.CredentialReplacementCallback(plan, nil)
	handle, err := keyring.NewHandle(new(sequenceReader))
	require.NoError(t, err)
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, callbackErr := callback(context.Background(), transaction, keyring.AuthorityUpdate{Owner: created.Server.ID, Kind: keyring.RecordStaticCredential, Handle: &handle})
		return callbackErr
	}))

	published, ok := result()
	require.True(t, ok)
	assert.Equal(t, "1", published.Revision)
	assert.Equal(t, contract.OperationCredentialReplace, published.Operation.Kind)
	assert.Equal(t, "1", published.Operation.TargetCredentialRevisions.StaticCredential)
	assert.Equal(t, "1", created.Server.DesiredRevision)
	authority, err := repository.Authority(context.Background(), created.Server.ID)
	require.NoError(t, err)
	assert.Equal(t, "1", authority.CredentialRevisions.StaticCredential)
	assert.Equal(t, "0", authority.CredentialRevisions.OAuthClient)
}

func TestCredentialReplacementRechecksDesiredAndDoesNotFenceOnStaleness(t *testing.T) {
	repository, store, _ := newRepository(t, new(sequenceReader))
	transport := testStdioTransport()
	transport.SecretEnvironment = map[string]string{"TOKEN": "token"}
	created, err := repository.Create(context.Background(), CreateRequest{Definition: Definition{Namespace: "replacement-race", DisplayName: "Replacement Race", Enabled: false, Transport: transport}, Idempotency: idempotency("replacement-race", "replacement-race", "")})
	require.NoError(t, err)
	plan, err := repository.PrepareCredentialReplacement(context.Background(), CredentialReplacementRequest{ServerID: created.Server.ID, Kind: contract.ServerCredentialStatic, ExpectedDesiredRevision: "1", ExpectedCredentialRevision: "0", Slots: []string{"token"}})
	require.NoError(t, err)
	name := "Changed"
	_, err = repository.Patch(context.Background(), created.Server.ID, "1", Patch{DisplayName: &name})
	require.NoError(t, err)
	fenced := false
	callback, _ := repository.CredentialReplacementCallback(plan, func() { fenced = true })
	handle, err := keyring.NewHandle(new(sequenceReader))
	require.NoError(t, err)
	err = store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, callbackErr := callback(context.Background(), transaction, keyring.AuthorityUpdate{Owner: created.Server.ID, Kind: keyring.RecordStaticCredential, Handle: &handle})
		return callbackErr
	})
	assert.ErrorIs(t, err, ErrStaleRevision)
	assert.False(t, fenced)
	assert.Equal(t, int64(0), operationCount(t, repository, created.Server.ID))
}

func TestOAuthClientReplacementPublishesBeforeStaticRegistration(t *testing.T) {
	repository, store, _ := newRepository(t, new(sequenceReader))
	created, err := repository.Create(context.Background(), CreateRequest{Definition: Definition{
		Namespace: "confidential-client", DisplayName: "Confidential Client", Enabled: false,
		Transport: contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: "https://resource.example/mcp", ProtocolMode: contract.ProtocolAuto, Authentication: contract.OAuthAuthentication{Mode: contract.AuthenticationOAuth, Registration: contract.StaticOAuthRegistration{Mode: contract.RegistrationStatic, ClientID: "client", TokenEndpointAuthMethod: contract.TokenEndpointAuthClientSecretBasic}, TrustedOrigins: []string{}, RequestOfflineAccess: false}},
	}, Idempotency: idempotency("confidential-create", "confidential-create", "")})
	require.NoError(t, err)
	plan, err := repository.PrepareCredentialReplacement(context.Background(), CredentialReplacementRequest{ServerID: created.Server.ID, Kind: contract.ServerCredentialOAuthClient, ExpectedDesiredRevision: "1", ExpectedCredentialRevision: "0"})
	require.NoError(t, err)
	callback, result := repository.CredentialReplacementCallback(plan, nil)
	handle, err := keyring.NewHandle(new(sequenceReader))
	require.NoError(t, err)
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, callbackErr := callback(context.Background(), transaction, keyring.AuthorityUpdate{Owner: created.Server.ID, Kind: keyring.RecordOAuthClient, Handle: &handle})
		return callbackErr
	}))
	publication, ok := result()
	require.True(t, ok)
	assert.Equal(t, "1", publication.Revision)

	registration, err := repository.PublishPublicRegistration(context.Background(), RegistrationFence{ServerID: created.Server.ID, ExpectedDesiredRevision: "1", ExpectedRegistrationRevision: "0", ExpectedOAuthClientRevision: "1"}, OAuthRegistrationAuthority{Revision: "0", Mode: contract.RegistrationStatic, Issuer: "https://issuer.example", ClientID: "client", CallbackURL: "http://127.0.0.1:8210/oauth/callback", ResourceURL: "https://resource.example/mcp", TokenEndpointAuthMethod: contract.TokenEndpointAuthClientSecretBasic, CreatedAt: testTime.Format(time.RFC3339Nano)})
	require.NoError(t, err)
	assert.Equal(t, "1", registration.Revision)
}

func TestOAuthClientReplacementRequiresStaticConfidentialDesiredMode(t *testing.T) {
	repository, _, _ := newRepository(t, new(sequenceReader))
	server := mustCreateServer(t, repository, "public-client", false)
	_, err := repository.PrepareCredentialReplacement(context.Background(), CredentialReplacementRequest{
		ServerID: server.ID, Kind: contract.ServerCredentialOAuthClient, ExpectedDesiredRevision: "1", ExpectedCredentialRevision: "0",
	})
	assert.ErrorIs(t, err, ErrInvalidOperation)
}
