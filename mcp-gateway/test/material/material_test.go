package material_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/credentialauthority"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/oauth"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/runtimes"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servercredentials"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	installationID = "01JKEYRNGNATVE000000000000"
	serverID       = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	staticCanary   = "STATIC-MATERIAL-CANARY"
	accessCanary   = "OAUTH-ACCESS-MATERIAL-CANARY"
	refreshCanary  = "OAUTH-REFRESH-MATERIAL-CANARY"
)

var now = time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)

type memoryBackend struct {
	mu     sync.Mutex
	values map[string]string
	reads  int
}

func newMemoryBackend() *memoryBackend                     { return &memoryBackend{values: make(map[string]string)} }
func (*memoryBackend) Probe(context.Context, string) error { return nil }
func (backend *memoryBackend) Set(service, user, password string) error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.values[service+"\x00"+user] = password
	return nil
}
func (backend *memoryBackend) Get(service, user string) (string, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.reads++
	value, ok := backend.values[service+"\x00"+user]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return value, nil
}
func (backend *memoryBackend) Delete(service, user string) error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	key := service + "\x00" + user
	if _, ok := backend.values[key]; !ok {
		return keyring.ErrNotFound
	}
	delete(backend.values, key)
	return nil
}
func (backend *memoryBackend) readCount() int {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.reads
}

type fixedClock struct{}

func (fixedClock) Now() time.Time { return now }

type repository struct {
	server       servers.Server
	authority    servers.AuthorityMetadata
	registration servers.OAuthRegistrationAuthority
}

func (repository *repository) Get(context.Context, string) (servers.Server, error) {
	return repository.server, nil
}
func (repository *repository) Authority(context.Context, string) (servers.AuthorityMetadata, error) {
	return repository.authority, nil
}
func (repository *repository) OAuthRegistration(context.Context, string) (servers.OAuthRegistrationAuthority, error) {
	return repository.registration, nil
}

type observingCoordinator struct {
	delegate  *keyring.Coordinator
	afterRead func()
	observed  []byte
}

func (coordinator *observingCoordinator) ReadActive(ctx context.Context, namespace keyring.Namespace) ([]byte, keyring.CutoverResult, error) {
	contents, result, err := coordinator.delegate.ReadActive(ctx, namespace)
	coordinator.observed = contents
	if err == nil && coordinator.afterRead != nil {
		coordinator.afterRead()
	}
	return contents, result, err
}

func TestCompleteCredentialGenerationsAcquireFenceAndCleanMaterial(t *testing.T) {
	ctx := context.Background()
	ownership, err := paths.Acquire(testutil.NewOwnerOnlyDataRoot(t))
	require.NoError(t, err)
	store, err := storage.Initialize(ctx, ownership, installationID)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
		require.NoError(t, ownership.MarkClean())
		require.NoError(t, ownership.Close())
	})
	backend := newMemoryBackend()
	provider, err := keyring.NewProviderWithBackend(installationID, backend)
	require.NoError(t, err)
	coordinator := keyring.NewCoordinator(provider, store, fixedClock{}, rand.Reader)

	t.Run("static", func(t *testing.T) {
		transport := contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/fixture/mcp", Arguments: []string{}, WorkingDirectory: "/", Environment: map[string]string{}, SecretEnvironment: map[string]string{"TOKEN": "api"}}
		material, encodeErr := servercredentials.EncodeStaticGeneration(map[string]string{"api": staticCanary})
		require.NoError(t, encodeErr)
		namespace, namespaceErr := keyring.NewNamespace(installationID, serverID, keyring.RecordStaticCredential)
		require.NoError(t, namespaceErr)
		published, replaceErr := coordinator.Replace(ctx, namespace, material)
		require.NoError(t, replaceErr)
		repository := &repository{server: candidateServer(t, transport), authority: servers.AuthorityMetadata{CredentialRevisions: contract.CredentialRevisions{StaticCredential: published.Revision}, StaticCredentialHandle: pointer(string(published.Handle))}}
		candidate := runtimes.Candidate{Server: repository.server, Authority: repository.authority, RuntimeID: "01ARZ3NDEKTSV4RRFFQ69G5FAX", Generation: 1}
		observer := &observingCoordinator{delegate: coordinator}
		resolver, resolverErr := credentialauthority.New(repository, observer, installationID, func() time.Time { return now })
		require.NoError(t, resolverErr)
		beforeReads := backend.readCount()

		outcome := resolver.Resolve(ctx, candidate)

		assert.Equal(t, contract.ServerCredentialReady, outcome.CredentialState)
		require.NotNil(t, outcome.Lease)
		assert.Equal(t, 2, backend.readCount()-beforeReads)
		assert.Equal(t, make([]byte, len(observer.observed)), observer.observed)
		assert.NotContains(t, fmt.Sprintf("%+v", outcome), staticCanary)
		owner := runtimes.NewRuntimeOwner()
		key, admitErr := owner.Admit(candidate, outcome.Lease, nil)
		require.NoError(t, admitErr)
		secret, ok := owner.Material(key, contract.ServerCredentialStatic)
		require.True(t, ok)
		decoded, decodeErr := servercredentials.DecodeStaticGeneration(secret)
		require.NoError(t, decodeErr)
		assert.Equal(t, staticCanary, decoded.Values["api"])
		assert.True(t, owner.Release(key, true))
		assert.Equal(t, make([]byte, len(secret)), secret)

		staleObserver := &observingCoordinator{delegate: coordinator, afterRead: func() { repository.server.DesiredRevision = "2" }}
		staleResolver, staleErr := credentialauthority.New(repository, staleObserver, installationID, func() time.Time { return now })
		require.NoError(t, staleErr)
		stale := staleResolver.Resolve(ctx, candidate)
		require.NotNil(t, stale.Reason)
		assert.Equal(t, contract.ReasonSuperseded, *stale.Reason)
		assert.Nil(t, stale.Lease)
		assert.Equal(t, make([]byte, len(staleObserver.observed)), staleObserver.observed)
		require.NoError(t, provider.DeleteGeneration(ctx, namespace, published.Handle))
		_, readErr := provider.ReadGeneration(ctx, namespace, published.Handle)
		require.ErrorIs(t, readErr, keyring.ErrNotFound)
	})

	t.Run("oauth", func(t *testing.T) {
		registration := servers.OAuthRegistrationAuthority{Revision: "1", Mode: contract.RegistrationStatic, Issuer: "https://issuer.example", ClientID: "public-client", CallbackURL: "http://127.0.0.1:8210/oauth/callback", ResourceURL: "https://resource.example/mcp", TokenEndpointAuthMethod: contract.TokenEndpointAuthNone, CreatedAt: now.Format(time.RFC3339Nano)}
		desired := contract.StaticOAuthRegistration{Mode: contract.RegistrationStatic, Issuer: &registration.Issuer, ClientID: registration.ClientID, TokenEndpointAuthMethod: contract.TokenEndpointAuthNone}
		transport := contract.StreamableHTTPTransport{Kind: contract.TransportStreamableHTTP, URL: registration.ResourceURL, ProtocolMode: contract.ProtocolModern, Authentication: contract.OAuthAuthentication{Mode: contract.AuthenticationOAuth, Registration: desired, TrustedOrigins: []string{}}}
		expires := now.Add(time.Hour).Format(time.RFC3339Nano)
		refresh := refreshCanary
		material, marshalErr := json.Marshal(oauth.TokenGeneration{Version: 1, ServerID: serverID, Issuer: registration.Issuer, RegistrationRevision: registration.Revision, Resource: registration.ResourceURL, AccessToken: accessCanary, RefreshToken: &refresh, Scopes: []string{"read"}, ScopeSpecified: true, IssuedAt: now.Format(time.RFC3339Nano), ExpiresAt: &expires})
		require.NoError(t, marshalErr)
		_, decodeErr := oauth.DecodeTokenGeneration(material)
		require.NoError(t, decodeErr)
		namespace, namespaceErr := keyring.NewNamespace(installationID, serverID, keyring.RecordOAuthTokens)
		require.NoError(t, namespaceErr)
		published, replaceErr := coordinator.Replace(ctx, namespace, material)
		require.NoError(t, replaceErr)
		repository := &repository{server: candidateServer(t, transport), registration: registration, authority: servers.AuthorityMetadata{RegistrationRevision: "1", CredentialRevisions: contract.CredentialRevisions{OAuthTokens: published.Revision}, OAuthTokensHandle: pointer(string(published.Handle))}}
		candidate := runtimes.Candidate{Server: repository.server, Authority: repository.authority, RuntimeID: "01ARZ3NDEKTSV4RRFFQ69G5FAY", Generation: 1}
		observer := &observingCoordinator{delegate: coordinator}
		resolver, resolverErr := credentialauthority.New(repository, observer, installationID, func() time.Time { return now })
		require.NoError(t, resolverErr)
		beforeReads := backend.readCount()

		outcome := resolver.Resolve(ctx, candidate)

		assert.Equal(t, contract.ServerCredentialReady, outcome.CredentialState)
		require.NotNil(t, outcome.Lease)
		assert.Equal(t, 2, backend.readCount()-beforeReads)
		assert.Equal(t, make([]byte, len(observer.observed)), observer.observed)
		assert.NotContains(t, fmt.Sprintf("%+v", outcome), accessCanary)
		assert.NotContains(t, fmt.Sprintf("%+v", outcome), refreshCanary)
		owner := runtimes.NewRuntimeOwner()
		key, admitErr := owner.Admit(candidate, outcome.Lease, nil)
		require.NoError(t, admitErr)
		secret, ok := owner.Material(key, contract.ServerCredentialOAuthTokens)
		require.True(t, ok)
		assert.Equal(t, accessCanary, string(secret))
		assert.NotContains(t, string(secret), refreshCanary)
		assert.True(t, owner.Release(key, true))
		assert.Equal(t, make([]byte, len(secret)), secret)
		require.NoError(t, provider.DeleteGeneration(ctx, namespace, published.Handle))
	})

	backupPath := filepath.Join(t.TempDir(), "gateway.db")
	require.NoError(t, store.BackupTo(ctx, backupPath))
	backup, err := os.ReadFile(backupPath)
	require.NoError(t, err)
	assert.False(t, bytes.Contains(backup, []byte(staticCanary)))
	assert.False(t, bytes.Contains(backup, []byte(accessCanary)))
	assert.False(t, bytes.Contains(backup, []byte(refreshCanary)))
}

func candidateServer(t *testing.T, transport contract.Transport) servers.Server {
	t.Helper()
	contents, err := json.Marshal(transport)
	require.NoError(t, err)
	return servers.Server{ID: serverID, Namespace: "material", DesiredState: contract.DesiredServerEnabled, DesiredRevision: "1", Transport: contents}
}

func pointer[T any](value T) *T { return &value }
