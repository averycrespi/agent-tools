//go:build integration

package backup

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

type retainedHTTPKeyring struct {
	mu     sync.Mutex
	values map[string]string
}

func (*retainedHTTPKeyring) Probe(context.Context, string) error { return nil }
func (k *retainedHTTPKeyring) Set(service, user, value string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.values[service+user] = value
	return nil
}
func (k *retainedHTTPKeyring) Get(service, user string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	v, ok := k.values[service+user]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return v, nil
}

// Retain orphaned physical items to prove restore does not trust their presence.
func (*retainedHTTPKeyring) Delete(string, string) error { return nil }

func TestIntegrationRestoreNeverReactivatesRetiredHTTPCredential(t *testing.T) {
	for _, action := range []string{"rotate", "delete"} {
		t.Run(action, func(t *testing.T) {
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
			serviceFor := func(store *storage.Store) *httpcredentials.Service {
				repo, err := httpcredentials.NewRepository(store, clock, rand.Reader, httpcredentials.NoHTTPGrants{})
				require.NoError(t, err)
				service, err := httpcredentials.NewService(repo, keyring.NewCoordinator(provider, store, clock, rand.Reader), backupTestInstallationID)
				require.NoError(t, err)
				return service
			}
			service := serviceFor(store)
			created, err := service.Create(ctx, httpcredentials.Definition{Name: "Backup scope", Boundary: httpcredentials.Boundary{Host: "api.example.com", Port: 443}, Recipe: contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: "Bearer "}}, []byte("backup-http-private-canary"))
			require.NoError(t, err)
			manager, err := New(Options{Store: store, Layout: owner.Layout(), Clock: clock, Entropy: rand.Reader})
			require.NoError(t, err)
			artifact, _, err := manager.Create(ctx, "authority", "http-restore")
			require.NoError(t, err)
			for _, name := range []string{databaseFile, metadataFile} {
				contents, err := os.ReadFile(filepath.Join(owner.Layout().Backups, artifact.ID, name))
				require.NoError(t, err)
				require.NotContains(t, string(contents), "backup-http-private-canary")
			}
			if action == "rotate" {
				_, err = service.Rotate(ctx, created.ID, created.Revision, []byte("new-http-private-canary"))
			} else {
				err = service.Delete(ctx, created.ID, created.Revision)
			}
			require.NoError(t, err)
			require.NotEmpty(t, backend.values)
			require.NoError(t, store.Close())
			require.NoError(t, owner.Close())
			_, err = Restore(ctx, RestoreOptions{Root: root, BackupID: artifact.ID, Sink: new(captureSink), Clock: clock, Entropy: rand.Reader})
			require.NoError(t, err)
			owner, err = gatewaypaths.Acquire(root)
			require.NoError(t, err)
			store, err = storage.Open(ctx, owner)
			require.NoError(t, err)
			require.NoError(t, httpcredentials.ValidateStartup(ctx, store))
			restoredService := serviceFor(store)
			restored, err := restoredService.Get(ctx, created.ID)
			require.NoError(t, err)
			require.False(t, restored.Available)
			revision, err := strconv.ParseUint(restored.Revision, 10, 64)
			require.NoError(t, err)
			material, err := restoredService.Acquire(ctx, contract.HTTPRevisionRef{ID: created.ID, Revision: revision})
			require.Error(t, err)
			require.Nil(t, material)
		})
	}
}
