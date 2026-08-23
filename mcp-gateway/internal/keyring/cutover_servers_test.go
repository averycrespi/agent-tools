package keyring_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	serverTestInstallationID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	serverTestOwnerID        = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
)

var serverTestTime = time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)

type serverTestClock struct{}

func (serverTestClock) Now() time.Time { return serverTestTime }

type serverTestEntropy struct {
	mu    sync.Mutex
	value byte
}

func (entropy *serverTestEntropy) Read(buffer []byte) (int, error) {
	entropy.mu.Lock()
	defer entropy.mu.Unlock()
	entropy.value++
	for index := range buffer {
		buffer[index] = entropy.value
	}
	return len(buffer), nil
}

func TestPostAuthorizationCutoverFencesRealServerCallbackAcrossDriftAndRead(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		drift func(context.Context, *servers.Repository, *storage.Store, servers.Server) error
	}{
		{
			name: "desired revision",
			drift: func(ctx context.Context, repository *servers.Repository, _ *storage.Store, server servers.Server) error {
				name := "Drifted"
				_, err := repository.Patch(ctx, server.ID, "1", servers.Patch{DisplayName: &name})
				return err
			},
		},
		{
			name: "registration revision",
			drift: func(ctx context.Context, _ *servers.Repository, store *storage.Store, server servers.Server) error {
				return store.Mutate(ctx, func(transaction *sql.Tx) error {
					_, err := transaction.ExecContext(ctx, `
						UPDATE server_oauth_registrations SET revision = 1, mode = 'static',
							issuer = 'https://issuer.example', client_id = 'client',
							callback_url = 'http://127.0.0.1:8210/oauth/callback',
							resource_url = 'https://resource.example/mcp',
							token_endpoint_auth_method = 'none', created_at = ?,
							client_secret_expires_at = NULL
						WHERE server_id = ?`, serverTestTime.Format(time.RFC3339Nano), server.ID)
					return err
				})
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			repository, store, coordinator, adapter, namespace, server, closeHarness := newServerCutoverHarness(t, nil)
			defer closeHarness()
			baselineCallback := credentialCallback(t, repository, server, "0")
			_, err := coordinator.ReplaceFenced(ctx, namespace, []byte("old tokens"), baselineCallback)
			require.NoError(t, err)

			started := make(chan struct{})
			release := make(chan struct{})
			adapter.BlockNextSetForTest(started, release)
			callback := credentialCallback(t, repository, server, "1")
			result := make(chan error, 1)
			go func() {
				_, replaceErr := coordinator.ReplaceFencedAfterAuthorizationSuccess(ctx, namespace, []byte("new tokens"), callback)
				result <- replaceErr
			}()
			<-started

			_, _, err = coordinator.ReadActive(ctx, namespace)
			assert.ErrorIs(t, err, keyring.ErrWorkLimit)
			require.NoError(t, testCase.drift(ctx, repository, store, server))
			close(release)
			assert.ErrorIs(t, <-result, servers.ErrStaleRevision)
			_, _, err = coordinator.ReadActive(ctx, namespace)
			assert.ErrorIs(t, err, keyring.ErrNoAuthority)
			authority, err := repository.Authority(ctx, server.ID)
			require.NoError(t, err)
			assert.Equal(t, "2", authority.CredentialRevisions.OAuthTokens)
			assert.Nil(t, authority.OAuthTokensHandle)
		})
	}
}

func TestPostAuthorizationActivationRevalidatesRealServerCallback(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		drift func(context.Context, *servers.Repository, *storage.Store, servers.Server) error
	}{
		{name: "no drift"},
		{
			name: "desired revision drift",
			drift: func(ctx context.Context, repository *servers.Repository, _ *storage.Store, server servers.Server) error {
				name := "Drifted after publication"
				_, err := repository.Patch(ctx, server.ID, "1", servers.Patch{DisplayName: &name})
				return err
			},
		},
		{
			name: "registration revision drift",
			drift: func(ctx context.Context, _ *servers.Repository, store *storage.Store, server servers.Server) error {
				return store.Mutate(ctx, func(transaction *sql.Tx) error {
					_, err := transaction.ExecContext(ctx, `
						UPDATE server_oauth_registrations SET revision = 1, mode = 'static',
							issuer = 'https://issuer.example', client_id = 'client',
							callback_url = 'http://127.0.0.1:8210/oauth/callback',
							resource_url = 'https://resource.example/mcp',
							token_endpoint_auth_method = 'none', created_at = ?,
							client_secret_expires_at = NULL
						WHERE server_id = ?`, serverTestTime.Format(time.RFC3339Nano), server.ID)
					return err
				})
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			repository, store, baseline, _, namespace, server, closeHarness := newServerCutoverHarness(t, nil)
			defer closeHarness()
			_, err := baseline.ReplaceFenced(ctx, namespace, []byte("old tokens"), credentialCallback(t, repository, server, "0"))
			require.NoError(t, err)

			committed := make(chan struct{})
			release := make(chan struct{})
			candidate := keyring.NewCoordinatorWithAfterCommitForTest(
				baseline.ProviderForTest(), store, serverTestClock{}, new(serverTestEntropy),
				func() error {
					close(committed)
					<-release
					return nil
				},
			)
			result := make(chan error, 1)
			go func() {
				_, replaceErr := candidate.ReplaceFencedAfterAuthorizationSuccess(
					ctx, namespace, []byte("new tokens"), credentialCallback(t, repository, server, "1"),
				)
				result <- replaceErr
			}()
			<-committed
			var driftErr error
			if testCase.drift != nil {
				driftErr = testCase.drift(ctx, repository, store, server)
			}
			close(release)
			require.NoError(t, driftErr)

			replaceErr := <-result
			authority, err := repository.Authority(ctx, server.ID)
			require.NoError(t, err)
			if testCase.drift == nil {
				require.NoError(t, replaceErr)
				secret, active, readErr := candidate.ReadActive(ctx, namespace)
				require.NoError(t, readErr)
				assert.Equal(t, []byte("new tokens"), secret)
				assert.Equal(t, "2", active.Revision)
				assert.Equal(t, "2", authority.CredentialRevisions.OAuthTokens)
				assert.NotNil(t, authority.OAuthTokensHandle)
				return
			}

			assert.ErrorIs(t, replaceErr, servers.ErrStaleRevision)
			_, _, err = candidate.ReadActive(ctx, namespace)
			assert.ErrorIs(t, err, keyring.ErrNoAuthority)
			assert.Equal(t, contract.CredentialRevisions{
				StaticCredential: "0", OAuthClient: "0", OAuthTokens: "3",
			}, authority.CredentialRevisions)
			assert.Nil(t, authority.OAuthTokensHandle)
			require.NoError(t, store.View(ctx, func(transaction *sql.Tx) error {
				var fences int
				if queryErr := transaction.QueryRowContext(ctx, `
					SELECT count(*) FROM keyring_authority_fences
					WHERE owner = ? AND kind = ?`, server.ID, keyring.RecordOAuthTokens).Scan(&fences); queryErr != nil {
					return queryErr
				}
				assert.Equal(t, 1, fences)
				return nil
			}))
		})
	}
}

func TestPostAuthorizationFailureDoesNotInvalidateNewerCredentialRevision(t *testing.T) {
	ctx := context.Background()
	repository, store, coordinator, adapter, namespace, server, closeHarness := newServerCutoverHarness(t, nil)
	defer closeHarness()
	_, err := coordinator.ReplaceFenced(ctx, namespace, []byte("old tokens"), credentialCallback(t, repository, server, "0"))
	require.NoError(t, err)

	newerHandle, err := keyring.NewHandle(bytes.NewReader(bytes.Repeat([]byte{0x77}, 32)))
	require.NoError(t, err)
	require.NoError(t, coordinator.ProviderForTest().WriteGeneration(ctx, namespace, newerHandle, []byte("newer tokens")))

	started := make(chan struct{})
	release := make(chan struct{})
	adapter.BlockNextSetForTest(started, release)
	result := make(chan error, 1)
	go func() {
		_, replaceErr := coordinator.ReplaceFencedAfterAuthorizationSuccess(
			ctx, namespace, []byte("stale candidate"), credentialCallback(t, repository, server, "1"),
		)
		result <- replaceErr
	}()
	<-started

	newerCallback := credentialCallback(t, repository, server, "1")
	require.NoError(t, store.Mutate(ctx, func(transaction *sql.Tx) error {
		revision, callbackErr := newerCallback(ctx, transaction, keyring.AuthorityUpdate{
			Owner: server.ID, Kind: keyring.RecordOAuthTokens, Handle: &newerHandle,
		})
		if callbackErr != nil {
			return callbackErr
		}
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO keyring_authorities (owner, kind, handle, revision)
			VALUES (?, ?, ?, ?)`, server.ID, keyring.RecordOAuthTokens, string(newerHandle), revision); err != nil {
			return err
		}
		_, err := transaction.ExecContext(ctx, `
			DELETE FROM keyring_authority_fences WHERE owner = ? AND kind = ?`,
			server.ID, keyring.RecordOAuthTokens)
		return err
	}))
	close(release)
	require.Error(t, <-result)
	secret, active, err := coordinator.ReadActive(ctx, namespace)
	require.NoError(t, err)
	assert.Equal(t, []byte("newer tokens"), secret)
	assert.Equal(t, "2", active.Revision)
}

func TestPostAuthorizationActivationAfterCommitFaultRecoversNoAuthority(t *testing.T) {
	ctx := context.Background()
	var faultArmed bool
	fault := func(point storage.FaultPoint) error {
		if faultArmed && point == storage.FaultAfterCommit {
			faultArmed = false
			return errors.New("activation after-commit fault")
		}
		return nil
	}
	repository, store, baseline, _, namespace, server, closeHarness := newServerCutoverHarness(t, fault)
	_, err := baseline.ReplaceFenced(ctx, namespace, []byte("old tokens"), credentialCallback(t, repository, server, "0"))
	require.NoError(t, err)

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	candidate := keyring.NewCoordinatorWithAfterCommitForTest(
		baseline.ProviderForTest(), store, serverTestClock{}, new(serverTestEntropy),
		func() error {
			once.Do(func() { close(entered) })
			<-release
			return nil
		},
	)
	result := make(chan error, 1)
	go func() {
		_, replaceErr := candidate.ReplaceFencedAfterAuthorizationSuccess(
			ctx, namespace, []byte("new tokens"), credentialCallback(t, repository, server, "1"),
		)
		result <- replaceErr
	}()
	<-entered
	faultArmed = true
	close(release)
	assert.ErrorIs(t, <-result, storage.ErrStorageLatched)

	root := closeHarness()
	identity, err := storage.VerifyCurrent(ctx, root)
	require.NoError(t, err)
	assert.Equal(t, serverTestInstallationID, identity.InstallationID)
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	defer func() { require.NoError(t, ownership.Close()) }()
	reopened, err := storage.Open(ctx, ownership)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	restarted := keyring.NewCoordinator(baseline.ProviderForTest(), reopened, serverTestClock{}, new(serverTestEntropy))
	_, _, err = restarted.ReadActive(ctx, namespace)
	assert.ErrorIs(t, err, keyring.ErrNoAuthority)
}

func newServerCutoverHarness(
	t *testing.T,
	fault func(storage.FaultPoint) error,
) (*servers.Repository, *storage.Store, *keyring.Coordinator, *keyring.MemoryAdapterForTest, keyring.Namespace, servers.Server, func() string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	var store *storage.Store
	if fault == nil {
		store, err = storage.Initialize(context.Background(), ownership, serverTestInstallationID)
	} else {
		store, err = storage.InitializeWithFaultInjection(context.Background(), ownership, serverTestInstallationID, fault)
	}
	require.NoError(t, err)
	repository, err := servers.New(store, serverTestClock{}, new(serverTestEntropy))
	require.NoError(t, err)
	digest := sha256.Sum256([]byte("server-cutover"))
	created, err := repository.Create(context.Background(), servers.CreateRequest{
		ID: serverTestOwnerID,
		Definition: servers.Definition{
			Namespace: "server-cutover", DisplayName: "Server Cutover", Enabled: false,
			Transport: contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/bin/true", Arguments: []string{}, WorkingDirectory: "/tmp", Environment: map[string]string{}, SecretEnvironment: map[string]string{}},
		},
		Idempotency: &servers.IdempotencyRequest{
			AuthorityID: serverTestInstallationID, Method: "POST", Route: "/api/v1/servers", Key: "server-cutover", RequestHash: digest,
		},
	})
	require.NoError(t, err)
	adapter := keyring.NewMemoryAdapterForTest()
	provider, err := keyring.NewProviderForTest(serverTestInstallationID, adapter)
	require.NoError(t, err)
	coordinator := keyring.NewCoordinator(provider, store, serverTestClock{}, new(serverTestEntropy))
	namespace, err := keyring.NewNamespace(serverTestInstallationID, created.Server.ID, keyring.RecordOAuthTokens)
	require.NoError(t, err)
	var closeOnce sync.Once
	closeHarness := func() string {
		closeOnce.Do(func() {
			require.NoError(t, store.Close())
			require.NoError(t, ownership.Close())
		})
		return root
	}
	return repository, store, coordinator, adapter, namespace, created.Server, closeHarness
}

func credentialCallback(
	t *testing.T,
	repository *servers.Repository,
	server servers.Server,
	expectedCredentialRevision string,
) keyring.AuthorityCallback {
	t.Helper()
	callback, err := repository.CredentialAuthorityCallback(servers.CredentialFence{
		ServerID: server.ID, Kind: contract.ServerCredentialOAuthTokens,
		ExpectedDesiredRevision: server.DesiredRevision, ExpectedCredentialRevision: expectedCredentialRevision,
		ExpectedRegistrationRevision: "0",
	})
	require.NoError(t, err)
	return callback
}
