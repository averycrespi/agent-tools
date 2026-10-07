//go:build integration

package backup

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpca"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestIntegrationEncryptedRestoreRefusesSemanticCorruption(t *testing.T) {
	t.Parallel()
	for _, query := range []string{
		`UPDATE secret_generations SET ciphertext=zeroblob(length(ciphertext))`,
		`DELETE FROM secret_generations`,
		`UPDATE http_ca SET certificate=X'00'`,
		`UPDATE secret_custody SET encryptions=0`,
		`DELETE FROM keyring_authorities`,
	} {
		t.Run(query, func(t *testing.T) {
			ctx := audit.WithSystem(t.Context())
			manager, store, owner := newBackupManager(t, nil)
			require.NoError(t, keyring.SetupCustody(ctx, owner, store, manager.clock))
			_, err := admin.NewService(store, manager.clock, rand.Reader).Initialize(ctx, new(captureSink))
			require.NoError(t, err)
			provider, err := keyring.NewProviderWithBackend(backupTestInstallationID, &retainedHTTPKeyring{values: map[string]string{}})
			require.NoError(t, err)
			require.NoError(t, provider.UseDatabaseCustody(ctx, owner, store))
			ca, err := httpca.New(store, keyring.NewCoordinator(provider, store, manager.clock, rand.Reader), backupTestInstallationID, manager.clock, rand.Reader)
			require.NoError(t, err)
			require.NoError(t, ca.Replace(ctx, "0"))
			ca.Close()
			artifact, _, err := manager.Create(ctx, "authority", "corruption")
			require.NoError(t, err)
			directory := filepath.Join(owner.Layout().Backups, artifact.ID)
			path := filepath.Join(directory, databaseFile)
			database, err := sql.Open("sqlite3", "file:"+path+"?mode=rw")
			require.NoError(t, err)
			_, err = database.ExecContext(ctx, query)
			require.NoError(t, err)
			_, err = database.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
			require.NoError(t, err)
			require.NoError(t, database.Close())
			raw, err := os.ReadFile(filepath.Join(directory, metadataFile))
			require.NoError(t, err)
			var metadata artifactMetadata
			require.NoError(t, json.Unmarshal(raw, &metadata))
			metadata.SHA256, err = digestFile(path)
			require.NoError(t, err)
			info, err := os.Stat(path)
			require.NoError(t, err)
			metadata.SizeBytes = info.Size()
			raw, err = json.Marshal(metadata)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(directory, metadataFile), raw, 0600))
			require.NoError(t, store.Close())
			require.NoError(t, owner.Close())
			before, err := digestFile(owner.Layout().Database)
			require.NoError(t, err)
			sink := new(captureSink)
			_, err = Restore(ctx, RestoreOptions{Root: owner.Layout().Root, BackupID: artifact.ID, Sink: sink, Clock: manager.clock, Entropy: rand.Reader})
			require.Error(t, err)
			require.Empty(t, sink.bearer)
			after, err := digestFile(owner.Layout().Database)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestIntegrationEncryptedRestoreRecoversCAHTTPAndGit(t *testing.T) {
	t.Parallel()
	testEncryptedDomainRecovery(t, false)
}

func TestIntegrationRotatedBackupRecoversExactCAAndAllCredentials(t *testing.T) {
	t.Parallel()
	testEncryptedDomainRecovery(t, true)
}

func testEncryptedDomainRecovery(t *testing.T, rotate bool) {
	t.Helper()
	ctx := audit.WithSystem(t.Context())
	manager, store, owner := newBackupManager(t, nil)
	require.NoError(t, keyring.SetupCustody(ctx, owner, store, manager.clock))
	_, err := admin.NewService(store, manager.clock, rand.Reader).Initialize(ctx, new(captureSink))
	require.NoError(t, err)
	backend := &retainedHTTPKeyring{values: map[string]string{}}
	provider, err := keyring.NewProviderWithBackend(backupTestInstallationID, backend)
	require.NoError(t, err)
	require.NoError(t, provider.UseDatabaseCustody(ctx, owner, store))
	services := func(s *storage.Store) (*httpca.Service, *httpcredentials.Service, *gitcredentials.Service) {
		policies, e := authorization.New(s, manager.clock, rand.Reader)
		require.NoError(t, e)
		coordinator := keyring.NewCoordinator(provider, s, manager.clock, rand.Reader)
		ca, e := httpca.New(s, coordinator, backupTestInstallationID, manager.clock, rand.Reader)
		require.NoError(t, e)
		repository, e := httpcredentials.NewRepository(s, manager.clock, rand.Reader, policies)
		require.NoError(t, e)
		h, e := httpcredentials.NewService(repository, coordinator, backupTestInstallationID)
		require.NoError(t, e)
		g, e := gitcredentials.NewService(s, coordinator, policies, manager.clock, rand.Reader, backupTestInstallationID)
		require.NoError(t, e)
		return ca, h, g
	}
	ca, h, g := services(store)
	require.NoError(t, ca.Replace(ctx, "0"))
	certificate, revision, err := ca.PublicCertificate(ctx)
	require.NoError(t, err)
	require.NoError(t, gatewaypaths.PublishCertificate(filepath.Join(owner.Layout().Root, gatewaypaths.PublicCertificateName), certificate, nil))
	httpCredential, err := h.Create(ctx, httpcredentials.Definition{Name: "recover HTTP", Boundary: httpcredentials.Boundary{Host: "api.example.com", Port: 443}, Recipe: contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: "Bearer "}}, []byte("http-backup-secret"))
	require.NoError(t, err)
	gitCredential, err := g.Create(ctx, contract.GitCredentialDefinition{Name: "recover Git", Origin: "https://example.com", Recipe: contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: "Bearer "}}, []byte("git-backup-secret"))
	require.NoError(t, err)
	targets, err := servers.New(store, manager.clock, rand.Reader)
	require.NoError(t, err)
	createdServer, err := targets.Create(ctx, servers.CreateRequest{Idempotency: &servers.IdempotencyRequest{AuthorityID: backupTestInstallationID, Method: "POST", Route: "/api/v2/mcp/servers", Key: "restore-mcp", RequestHash: sha256.Sum256([]byte("restore-mcp"))}, Definition: servers.Definition{Namespace: "recovered", DisplayName: "Recovered MCP", Transport: contract.StdioTransport{Kind: contract.TransportStdio, Executable: "/bin/true", Arguments: []string{}, WorkingDirectory: "/tmp", Environment: map[string]string{}, SecretEnvironment: map[string]string{}}}})
	require.NoError(t, err)
	coordinator := keyring.NewCoordinator(provider, store, manager.clock, rand.Reader)
	kinds := []contract.ServerCredentialKind{contract.ServerCredentialStatic, contract.ServerCredentialOAuthClient, contract.ServerCredentialOAuthTokens}
	for _, kind := range kinds {
		ns, err := keyring.NewNamespace(backupTestInstallationID, createdServer.Server.ID, keyring.RecordKind(kind))
		require.NoError(t, err)
		callback, err := targets.CredentialAuthorityCallback(servers.CredentialFence{ServerID: createdServer.Server.ID, Kind: kind, ExpectedDesiredRevision: "1", ExpectedCredentialRevision: "0"})
		require.NoError(t, err)
		_, err = coordinator.ReplaceFenced(ctx, ns, []byte("mcp-recovery-"+string(kind)), callback)
		require.NoError(t, err)
	}
	artifact, _, err := manager.Create(ctx, "authority", "domain-recovery")
	require.NoError(t, err)
	require.NoError(t, ca.Replace(ctx, revision))
	changed, _, err := ca.PublicCertificate(ctx)
	require.NoError(t, err)
	require.NoError(t, gatewaypaths.PublishCertificate(filepath.Join(owner.Layout().Root, gatewaypaths.PublicCertificateName), changed, certificate))
	_, err = h.Rotate(ctx, httpCredential.ID, httpCredential.Revision, []byte("new-http"))
	require.NoError(t, err)
	_, err = g.Rotate(ctx, gitCredential.ID, gitCredential.Revision, []byte("new-git"))
	require.NoError(t, err)
	ca.Close()
	require.Empty(t, backend.values)
	root := owner.Layout().Root
	require.NoError(t, store.Close())
	recoveryKey := ""
	var rotatedKey []byte
	if rotate {
		rotation, err := keyring.RotateStoppedMasterKey(ctx, owner, manager.clock, false, nil)
		require.NoError(t, err)
		recoveryKey = filepath.Join(root, rotation.Retained, "old-key")
		rotatedKey, err = gatewaypaths.MasterKey(owner, nil)
		require.NoError(t, err)
		defer clear(rotatedKey)
		_, err = storage.InspectMaintenance(ctx, owner, func(tx *sql.Tx) error {
			inspection, err := httpca.InspectTx(ctx, tx)
			require.NoError(t, err)
			require.Equal(t, changed, inspection.Certificate)
			_, err = verifyEncryptedCustodyTx(ctx, tx, owner)
			return err
		})
		require.NoError(t, err)
	}
	require.NoError(t, owner.Close())
	_, err = Restore(ctx, RestoreOptions{Root: root, BackupID: artifact.ID, RecoveryKey: recoveryKey, Sink: new(captureSink), Clock: manager.clock, Entropy: rand.Reader})
	require.NoError(t, err)
	restoredOwner, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	defer func() { require.NoError(t, restoredOwner.Close()) }()
	restored, err := storage.Open(ctx, restoredOwner)
	require.NoError(t, err)
	defer func() { require.NoError(t, restored.Close()) }()
	require.NoError(t, provider.UseDatabaseCustody(ctx, restoredOwner, restored))
	if rotate {
		active, err := gatewaypaths.MasterKey(restoredOwner, nil)
		require.NoError(t, err)
		require.Equal(t, rotatedKey, active)
		clear(active)
	}
	ca, h, g = services(restored)
	defer ca.Close()
	actual, _, err := ca.PublicCertificate(ctx)
	require.NoError(t, err)
	require.Equal(t, certificate, actual)
	pem, err := os.ReadFile(filepath.Join(root, gatewaypaths.PublicCertificateName))
	require.NoError(t, err)
	require.Equal(t, certificate, pem)
	signer, err := ca.Load(ctx)
	require.NoError(t, err)
	leaf, err := signer.Certificate("example.com")
	require.NoError(t, err)
	require.NotNil(t, leaf.PrivateKey)
	httpRecovered, err := h.Get(ctx, httpCredential.ID)
	require.NoError(t, err)
	require.True(t, httpRecovered.Available)
	rev, err := strconv.ParseUint(httpRecovered.Revision, 10, 64)
	require.NoError(t, err)
	httpMaterial, err := h.Acquire(ctx, contract.HTTPRevisionRef{ID: httpRecovered.ID, Revision: rev})
	require.NoError(t, err)
	target, err := httppolicy.ParseRequest("https://api.example.com/", "GET", "api.example.com", "", nil)
	require.NoError(t, err)
	httpHeaders, err := httpMaterial.Headers(target, make(http.Header))
	require.NoError(t, err)
	require.Equal(t, "Bearer http-backup-secret", httpHeaders.Get("Authorization"))
	httpMaterial.Clear()
	gitRecovered, err := g.Get(ctx, gitCredential.ID)
	require.NoError(t, err)
	require.True(t, gitRecovered.Available)
	material, err := g.Acquire(ctx, contract.GitRevisionRef{ID: gitRecovered.ID, Revision: gitRecovered.Revision})
	require.NoError(t, err)
	defer material.Clear()
	headers, err := material.Apply("https://example.com/team/repo", make(http.Header))
	require.NoError(t, err)
	require.Equal(t, "Bearer git-backup-secret", headers.Get("Authorization"))
	reader := keyring.NewCoordinator(provider, restored, manager.clock, rand.Reader)
	restoredTargets, err := servers.New(restored, manager.clock, rand.Reader)
	require.NoError(t, err)
	authority, err := restoredTargets.Authority(ctx, createdServer.Server.ID)
	require.NoError(t, err)
	require.NotNil(t, authority.StaticCredentialHandle)
	require.NotNil(t, authority.OAuthClientHandle)
	require.NotNil(t, authority.OAuthTokensHandle)
	for _, kind := range kinds {
		ns, err := keyring.NewNamespace(backupTestInstallationID, createdServer.Server.ID, keyring.RecordKind(kind))
		require.NoError(t, err)
		payload, _, err := reader.ReadActive(ctx, ns)
		require.NoError(t, err)
		require.Equal(t, "mcp-recovery-"+string(kind), string(payload))
		clear(payload)
	}
	require.Empty(t, backend.values, "encrypted restore must not use native custody")
}
