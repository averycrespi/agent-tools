//go:build integration

package backup

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestIntegrationEncryptedRestoreRecoversHTTPMaterialAndPolicy(t *testing.T) {
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
			require.NoError(t, keyring.SetupCustody(ctx, owner, store, clock))
			provider, err := keyring.NewProvider(backupTestInstallationID)
			require.NoError(t, err)
			serviceFor := func(store *storage.Store) (*httpcredentials.Service, *authorization.Repository) {
				require.NoError(t, provider.UseDatabaseCustody(ctx, owner, store))
				policies, err := authorization.New(store, clock, rand.Reader)
				require.NoError(t, err)
				repo, err := httpcredentials.NewRepository(store, clock, rand.Reader, policies)
				require.NoError(t, err)
				service, err := httpcredentials.NewService(repo, keyring.NewCoordinator(provider, store, clock, rand.Reader), backupTestInstallationID)
				require.NoError(t, err)
				return service, policies
			}
			service, policies := serviceFor(store)
			created, err := service.Create(ctx, httpcredentials.Definition{Name: "Backup scope", Boundary: httpcredentials.Boundary{Host: "api.example.com", Port: 443}, Recipe: contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: "Bearer "}}, []byte("backup-http-private-canary"))
			require.NoError(t, err)
			principal, err := policies.CreatePrincipal(ctx, authorization.CreatePrincipalRequest{DisplayName: "HTTP restore", Visibility: contract.VisibilityAll})
			require.NoError(t, err)
			grant, err := policies.PutHTTPGrant(ctx, "", "", authorization.HTTPGrantInput{PrincipalID: principal.Principal.ID, Policy: json.RawMessage(fmt.Sprintf(`{"version":1,"type":"allow_requests","request":{"origin":{"scheme":"https","host":"api.example.com","port":443},"methods":{"any":true},"path":{"kind":"any"}},"credential_id":%q}`, created.ID))})
			require.NoError(t, err)
			allow, block := contract.HTTPDefaultAllow, contract.HTTPDefaultBlock
			savedPrincipal, err := policies.PatchPrincipal(ctx, principal.Principal.ID, authorization.PatchPrincipalRequest{ExpectedRevision: principal.Principal.Revision, HTTPDefault: &allow})
			require.NoError(t, err)
			manager, err := New(Options{Ownership: owner, Store: store, Layout: owner.Layout(), Clock: clock, Entropy: rand.Reader})
			require.NoError(t, err)
			artifact, _, err := manager.Create(ctx, "authority", "http-restore")
			require.NoError(t, err)
			for _, name := range []string{databaseFile, metadataFile} {
				contents, err := os.ReadFile(filepath.Join(owner.Layout().Backups, artifact.ID, name))
				require.NoError(t, err)
				require.NotContains(t, string(contents), "backup-http-private-canary")
			}
			_, err = policies.PatchPrincipal(ctx, principal.Principal.ID, authorization.PatchPrincipalRequest{ExpectedRevision: savedPrincipal.Revision, HTTPDefault: &block})
			require.NoError(t, err)
			if action == "rotate" {
				_, err = service.Rotate(ctx, created.ID, created.Revision, []byte("new-http-private-canary"))
			} else {
				require.NoError(t, policies.DeleteHTTPGrant(ctx, grant.ID, grant.Revision))
				err = service.Delete(ctx, created.ID, created.Revision)
			}
			require.NoError(t, err)
			require.NoError(t, store.Close())
			require.NoError(t, owner.Close())
			_, err = Restore(ctx, RestoreOptions{Root: root, BackupID: artifact.ID, Sink: new(captureSink), Clock: clock, Entropy: rand.Reader})
			require.NoError(t, err)
			owner, err = gatewaypaths.Acquire(root)
			require.NoError(t, err)
			store, err = storage.Open(ctx, owner)
			require.NoError(t, err)
			require.NoError(t, httpcredentials.ValidateStartup(ctx, store))
			restoredService, restoredPolicies := serviceFor(store)
			restoredPrincipal, err := restoredPolicies.GetPrincipal(ctx, principal.Principal.ID)
			require.NoError(t, err)
			require.Equal(t, contract.HTTPDefaultAllow, restoredPrincipal.HTTPDefault)
			restoredGrant, err := restoredPolicies.GetHTTPGrant(ctx, grant.ID)
			require.NoError(t, err)
			require.Equal(t, grant, restoredGrant)
			preview, err := restoredPolicies.PreviewHTTPAccess(ctx, authorization.HTTPAccessInput{PrincipalID: principal.Principal.ID, URL: "https://api.example.com/", Method: "GET"})
			require.NoError(t, err)
			require.Equal(t, contract.HTTPDefaultAllow, preview.Default)
			require.EqualValues(t, 2, preview.Decision.DefaultRevision)
			require.True(t, preview.Decision.Allowed)
			restored, err := restoredService.Get(ctx, created.ID)
			require.NoError(t, err)
			require.True(t, restored.Available)
			revision, err := strconv.ParseUint(restored.Revision, 10, 64)
			require.NoError(t, err)
			material, err := restoredService.Acquire(ctx, contract.HTTPRevisionRef{ID: created.ID, Revision: revision})
			require.NoError(t, err)
			require.NotNil(t, material)
			defer material.Clear()
		})
	}
}
