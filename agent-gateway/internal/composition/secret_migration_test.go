package composition

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpca"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/oauth"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servercredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestStoppedSecretMigrationPreservesCAAndDomainSelections(t *testing.T) {
	t.Parallel()
	const id = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	ctx := audit.WithSystem(t.Context())
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	owner, err := gatewaypaths.AcquireForMaintenance(root)
	require.NoError(t, err)
	store, err := storage.Initialize(ctx, owner, id)
	require.NoError(t, err)
	clock := testutil.NewFakeClock(compositionTime)
	backend := newMemoryBackend()
	native, err := keyring.NewProviderWithBackend(id, backend)
	require.NoError(t, err)
	coordinator := keyring.NewCoordinator(native, store, clock, rand.Reader)
	policies, err := authorization.New(store, clock, rand.Reader)
	require.NoError(t, err)
	ca, err := httpca.New(store, coordinator, id, clock, rand.Reader)
	require.NoError(t, err)
	require.NoError(t, ca.Replace(ctx, "0"))
	certificate, revision, err := ca.PublicCertificate(ctx)
	require.NoError(t, err)
	ca.Close()
	httpRepo, err := httpcredentials.NewRepository(store, clock, rand.Reader, policies)
	require.NoError(t, err)
	h, err := httpcredentials.NewService(httpRepo, coordinator, id)
	require.NoError(t, err)
	hc, err := h.Create(ctx, httpcredentials.Definition{Name: "HTTP", Boundary: httpcredentials.Boundary{Host: "api.example.com", Port: 443}, Recipe: contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: "Bearer "}}, []byte("http-migration-secret"))
	require.NoError(t, err)
	g, err := gitcredentials.NewService(store, coordinator, policies, clock, rand.Reader, id)
	require.NoError(t, err)
	gc, err := g.Create(ctx, contract.GitCredentialDefinition{Name: "Git", Origin: "https://example.com", Recipe: contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: "Bearer "}}, []byte("git-migration-secret"))
	require.NoError(t, err)
	// Model the exact explicit origin labels created by the historical schema
	// upgrade, not a storagefixture or a replacement initialization image.
	require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO secret_generations(handle,owner,kind,custody) SELECT handle,owner,kind,'legacy' FROM keyring_authorities`)
		return err
	}))
	require.NoError(t, store.Close())
	require.NoError(t, owner.Close())
	factory := func(id string) (*keyring.Provider, error) { return keyring.NewProviderWithBackend(id, backend) }
	noProvider := func(string) (*keyring.Provider, error) {
		t.Fatal("verification constructed native provider")
		return nil, nil
	}
	refusal := errors.New("dry run")
	_, err = maintainSecrets(ctx, root, "migrate-secrets", clock, func(context.Context, *gatewaypaths.Ownership) error { return refusal }, noProvider)
	require.ErrorIs(t, err, refusal)
	_, err = maintainSecrets(ctx, root, "verify-secrets", clock, nil, noProvider)
	require.ErrorIs(t, err, keyring.ErrMigrationIncomplete)
	result, err := maintainSecrets(ctx, root, "migrate-secrets", clock, nil, factory)
	require.NoError(t, err)
	require.Equal(t, 3, result.Migrated)
	require.NotEmpty(t, backend.values)
	_, err = maintainSecrets(ctx, root, "verify-secrets", clock, nil, noProvider)
	require.NoError(t, err)
	result, err = maintainSecrets(ctx, root, "migrate-secrets", clock, nil, factory)
	require.NoError(t, err)
	require.Zero(t, result.Migrated)
	_, err = maintainSecrets(ctx, root, "cleanup-native-secrets", clock, nil, factory)
	require.NoError(t, err)
	require.Empty(t, backend.values)
	owner, err = gatewaypaths.AcquireForMaintenance(root)
	require.NoError(t, err)
	defer func() { require.NoError(t, owner.Close()) }()
	store, err = storage.Open(ctx, owner)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	require.NoError(t, native.UseDatabaseCustody(ctx, owner, store))
	actual, actualRevision, err := httpca.PublicCertificate(ctx, store)
	require.NoError(t, err)
	require.Equal(t, certificate, actual)
	require.Equal(t, revision, actualRevision)
	require.NoError(t, store.View(ctx, func(tx *sql.Tx) error {
		var count int
		require.NoError(t, tx.QueryRow(`SELECT count(*) FROM keyring_authorities WHERE owner IN (?,?)`, hc.ID, gc.ID).Scan(&count))
		require.Equal(t, 2, count)
		return verifyMigrationTx(ctx, tx, owner)
	}))
	// Unavailable configured material is not silently omitted from completeness.
	require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM keyring_authorities WHERE owner=?`, hc.ID); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE http_credentials SET handle=NULL WHERE id=?`, hc.ID)
		return err
	}))
	require.ErrorIs(t, store.View(ctx, func(tx *sql.Tx) error { return verifyMigrationTx(ctx, tx, owner) }), keyring.ErrMigrationIncomplete)
}

func TestStoppedSecretMigrationMCPStaticOAuthAndMissingDependencies(t *testing.T) {
	t.Parallel()
	const id = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	ctx := audit.WithSystem(t.Context())
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0700))
	owner, err := gatewaypaths.AcquireForMaintenance(root)
	require.NoError(t, err)
	store, err := storage.Initialize(ctx, owner, id)
	require.NoError(t, err)
	clock := testutil.NewFakeClock(compositionTime)
	backend := newMemoryBackend()
	native, err := keyring.NewProviderWithBackend(id, backend)
	require.NoError(t, err)
	coordinator := keyring.NewCoordinator(native, store, clock, rand.Reader)
	repository, err := servers.New(store, clock, rand.Reader)
	require.NoError(t, err)
	create := func(name string, transport contract.Transport) servers.Server {
		created, err := repository.Create(ctx, servers.CreateRequest{Idempotency: &servers.IdempotencyRequest{AuthorityID: id, Method: "POST", Route: "/api/v2/mcp/servers", Key: name, RequestHash: sha256.Sum256([]byte(name))}, Definition: servers.Definition{Namespace: name, DisplayName: name, Transport: transport}})
		require.NoError(t, err)
		return created.Server
	}
	static := create("static", contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/bin/true", Arguments: []string{}, WorkingDirectory: "/tmp", Environment: map[string]string{}, SecretEnvironment: map[string]string{"TOKEN": "token"}})
	oauthServer := create("oauth", contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: "https://resource.example/mcp", ProtocolMode: contract.ProtocolModern, Authentication: contract.OAuthAuthentication{Mode: contract.AuthenticationOAuth, Registration: contract.DynamicOAuthRegistration{Mode: contract.RegistrationDynamic}, TrustedOrigins: []string{}}})
	require.NoError(t, keyring.SetupCustody(ctx, owner, store, clock))
	require.ErrorIs(t, store.View(ctx, func(tx *sql.Tx) error { return verifyMigrationTx(ctx, tx, owner) }), keyring.ErrMigrationIncomplete)
	publish := func(server servers.Server, kind contract.ServerCredentialKind, payload []byte) {
		ns, err := keyring.NewNamespace(id, server.ID, keyring.RecordKind(kind))
		require.NoError(t, err)
		callback, err := repository.CredentialAuthorityCallback(servers.CredentialFence{ServerID: server.ID, Kind: kind, ExpectedDesiredRevision: "1", ExpectedCredentialRevision: "0"})
		require.NoError(t, err)
		_, err = coordinator.ReplaceFenced(ctx, ns, payload, callback)
		require.NoError(t, err)
	}
	payload, err := servercredentials.EncodeStaticGeneration(map[string]string{"token": "static-secret"})
	require.NoError(t, err)
	publish(static, contract.ServerCredentialStatic, payload)
	registration := servers.OAuthRegistrationAuthority{Revision: "0", Mode: contract.RegistrationDynamic, Issuer: "https://issuer.example", ClientID: "client-id", CallbackURL: "http://127.0.0.1:8210/oauth/callback", ResourceURL: "https://resource.example/mcp", TokenEndpointAuthMethod: contract.TokenEndpointAuthClientSecretBasic, CreatedAt: compositionTime.Format(time.RFC3339Nano)}
	callback, err := repository.RegistrationAuthorityCallback(servers.RegistrationFence{ServerID: oauthServer.ID, ExpectedDesiredRevision: "1", ExpectedRegistrationRevision: "0", ExpectedOAuthClientRevision: "0"}, registration)
	require.NoError(t, err)
	ns, err := keyring.NewNamespace(id, oauthServer.ID, keyring.RecordOAuthClient)
	require.NoError(t, err)
	_, err = coordinator.ReplaceFenced(ctx, ns, []byte("oauth-client-secret"), callback)
	require.NoError(t, err)
	tokens := oauth.TokenGeneration{Version: 1, ServerID: oauthServer.ID, Issuer: registration.Issuer, RegistrationRevision: "1", Resource: registration.ResourceURL, AccessToken: "access-token", IssuedAt: compositionTime.Format(time.RFC3339Nano)}
	payload, err = json.Marshal(tokens)
	require.NoError(t, err)
	publish(oauthServer, contract.ServerCredentialOAuthTokens, payload)
	require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO secret_generations(handle,owner,kind,custody) SELECT handle,owner,kind,'legacy' FROM keyring_authorities`)
		return err
	}))
	require.NoError(t, store.Close())
	require.NoError(t, owner.Close())
	factory := func(id string) (*keyring.Provider, error) { return keyring.NewProviderWithBackend(id, backend) }
	result, err := maintainSecrets(ctx, root, "migrate-secrets", clock, nil, factory)
	require.NoError(t, err)
	require.Equal(t, 3, result.Migrated)
	_, err = maintainSecrets(ctx, root, "verify-secrets", clock, nil, func(string) (*keyring.Provider, error) { t.Fatal("native verification"); return nil, nil })
	require.NoError(t, err)
}
