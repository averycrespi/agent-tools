//go:build integration

package backup

import (
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpca"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestIntegrationRestoreRequiresNewCADespiteRetainedKeys(t *testing.T) {
	ctx := audit.WithSystem(t.Context())
	root := filepath.Join(t.TempDir(), "gateway")
	owner, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Close()) })
	store, err := storage.Initialize(ctx, owner, backupTestInstallationID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	clock := fixedClock{value: acceptedFixtureTime}
	_, err = admin.NewService(store, clock, rand.Reader).Initialize(ctx, new(captureSink))
	require.NoError(t, err)
	backend := &retainedHTTPKeyring{values: map[string]string{}}
	provider, err := keyring.NewProviderWithBackend(backupTestInstallationID, backend)
	require.NoError(t, err)
	makeService := func() (*httpca.Service, *keyring.Coordinator) {
		coordinator := keyring.NewCoordinator(provider, store, clock, rand.Reader)
		service, err := httpca.New(store, coordinator, backupTestInstallationID, clock, rand.Reader)
		require.NoError(t, err)
		return service, coordinator
	}
	service, coordinator := makeService()
	require.NoError(t, service.Replace(ctx, "0"))
	original, revision, err := service.PublicCertificate(ctx)
	require.NoError(t, err)
	namespace, err := keyring.NewNamespace(backupTestInstallationID, backupTestInstallationID, keyring.RecordHTTPCA)
	require.NoError(t, err)
	var key []byte
	require.NoError(t, coordinator.WithOperation(ctx, func(operation *keyring.Operation) error {
		payload, _, err := operation.ReadActive(ctx, namespace)
		if err != nil {
			return err
		}
		defer clear(payload)
		var envelope struct {
			Key []byte `json:"key"`
		}
		err = json.Unmarshal(payload, &envelope)
		key = envelope.Key
		return err
	}))
	defer clear(key)
	require.NotEmpty(t, key)
	manager, err := New(Options{Store: store, Layout: owner.Layout(), Clock: clock, Entropy: rand.Reader})
	require.NoError(t, err)
	artifact, _, err := manager.Create(ctx, "authority", "ca-restore")
	require.NoError(t, err)
	for _, name := range []string{databaseFile, metadataFile} {
		contents, err := os.ReadFile(filepath.Join(owner.Layout().Backups, artifact.ID, name))
		require.NoError(t, err)
		require.NotContains(t, string(contents), string(key))
	}
	require.NoError(t, service.Replace(ctx, revision))
	second, _, err := service.PublicCertificate(ctx)
	require.NoError(t, err)
	require.NotEqual(t, original, second)
	service.Close()
	count := len(backend.values)
	require.NoError(t, store.Close())
	require.NoError(t, owner.Close())
	_, err = Restore(ctx, RestoreOptions{Root: root, BackupID: artifact.ID, Sink: new(captureSink), Clock: clock, Entropy: rand.Reader})
	require.NoError(t, err)
	require.Len(t, backend.values, count, "restore must not touch keyring")
	owner, err = gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err = storage.Open(ctx, owner)
	require.NoError(t, err)
	service, _ = makeService()
	defer service.Close()
	require.NoError(t, httpca.ValidateStartup(ctx, store))
	_, err = service.Load(ctx)
	require.Error(t, err)
	restored, revision, err := service.PublicCertificate(ctx)
	require.NoError(t, err)
	require.Equal(t, original, restored, "public metadata is history, not signing authority")
	require.NoError(t, service.Replace(ctx, revision))
	replacement, _, err := service.PublicCertificate(ctx)
	require.NoError(t, err)
	require.NotEqual(t, original, replacement)
	require.NotEqual(t, second, replacement)
	_, err = service.Load(ctx)
	require.NoError(t, err)
}
