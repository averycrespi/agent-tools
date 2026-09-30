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
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/invocation"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestIntegrationGitPairedRestorePreservesConfigurationNotRetiredMaterial(t *testing.T) {
	manager, control, owner := newBackupManager(t, nil)
	ctx := audit.WithSystem(t.Context())
	_, err := admin.NewService(control, manager.clock, rand.Reader).Initialize(ctx, new(captureSink))
	require.NoError(t, err)
	backend := &retainedHTTPKeyring{values: map[string]string{}}
	provider, err := keyring.NewProviderWithBackend(backupTestInstallationID, backend)
	require.NoError(t, err)
	serviceFor := func(store *storage.Store) (*gitcredentials.Service, *authorization.Repository) {
		policies, err := authorization.New(store, manager.clock, rand.Reader)
		require.NoError(t, err)
		service, err := gitcredentials.NewService(store, keyring.NewCoordinator(provider, store, manager.clock, rand.Reader), policies, manager.clock, rand.Reader, backupTestInstallationID)
		require.NoError(t, err)
		return service, policies
	}
	service, policies := serviceFor(control)
	created, err := service.Create(ctx, contract.GitCredentialDefinition{Name: "Backup Git access", Origin: "https://example.com", Recipe: contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: "Bearer "}}, []byte("paired-git-private-canary"))
	require.NoError(t, err)
	principal, err := policies.CreatePrincipal(ctx, authorization.CreatePrincipalRequest{DisplayName: "Git restore", Visibility: contract.VisibilityAll})
	require.NoError(t, err)
	repository, err := policies.PutGitRepository(ctx, "", "", contract.GitRepositoryDefinition{Name: "Repository", URL: "https://example.com/team/repo", Aliases: []string{"https://example.com/team/repo.git"}, CredentialID: &created.ID})
	require.NoError(t, err)
	grant, err := policies.PutGitGrant(ctx, "", "", authorization.GitGrantInput{PrincipalID: principal.Principal.ID, RepositoryID: repository.ID, Policy: json.RawMessage(`{"version":1,"read":true,"refs":[]}`)})
	require.NoError(t, err)
	profile, err := policies.GetGitRoutingProfile(ctx)
	require.NoError(t, err)
	profile, err = policies.PutGitRoutingProfile(ctx, profile.Revision, []string{"https://example.com"})
	require.NoError(t, err)
	generation := "01ARZ3NDEKTSV4RRFFQ69G5FA0"
	traffic, err := invocation.CreateTraffic(ctx, owner, backupTestInstallationID, generation, invocation.DefaultTrafficConfig())
	require.NoError(t, err)
	defer func() { require.NoError(t, traffic.Close()) }()
	require.NoError(t, control.SelectTraffic(ctx, "", generation))
	manager.traffic = traffic
	artifact, _, err := manager.Create(ctx, "authority", "git-paired-restore")
	require.NoError(t, err)
	_, err = manager.Get(ctx, artifact.ID)
	require.NoError(t, err)
	for _, name := range []string{databaseFile, metadataFile, "traffic.db"} {
		contents, err := os.ReadFile(filepath.Join(owner.Layout().Backups, artifact.ID, name))
		require.NoError(t, err)
		require.NotContains(t, string(contents), "paired-git-private-canary")
	}
	_, err = service.Rotate(ctx, created.ID, created.Revision, []byte("retired-git-private-canary"))
	require.NoError(t, err)
	require.NotEmpty(t, backend.values)
	root := owner.Layout().Root
	require.NoError(t, traffic.Close())
	require.NoError(t, control.Close())
	require.NoError(t, owner.Close())
	_, err = Restore(ctx, RestoreOptions{Root: root, BackupID: artifact.ID, Sink: new(captureSink), Clock: manager.clock, Entropy: rand.Reader})
	require.NoError(t, err)
	restoredOwner, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	defer func() { require.NoError(t, restoredOwner.Close()) }()
	restored, err := storage.Open(ctx, restoredOwner)
	require.NoError(t, err)
	defer func() { require.NoError(t, restored.Close()) }()
	require.NoError(t, gitcredentials.ValidateStartup(ctx, restored))
	restoredService, restoredPolicies := serviceFor(restored)
	retained, err := restoredService.Get(ctx, created.ID)
	require.NoError(t, err)
	require.False(t, retained.Available)
	require.Equal(t, created.GitCredentialDefinition, retained.GitCredentialDefinition)
	material, err := restoredService.Acquire(ctx, contract.GitRevisionRef{ID: retained.ID, Revision: retained.Revision})
	require.Error(t, err)
	require.Nil(t, material)
	retainedRepository, err := restoredPolicies.GetGitRepository(ctx, repository.ID)
	require.NoError(t, err)
	require.Equal(t, repository, retainedRepository)
	retainedGrant, err := restoredPolicies.GetGitGrant(ctx, grant.ID)
	require.NoError(t, err)
	require.Equal(t, grant, retainedGrant)
	retainedProfile, err := restoredPolicies.GetGitRoutingProfile(ctx)
	require.NoError(t, err)
	require.Equal(t, profile, retainedProfile)
	require.False(t, retainedProfile.Active)
	restoredGeneration, err := restored.SelectedTraffic(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, restoredGeneration)
	require.NotEqual(t, generation, restoredGeneration)
	restoredTraffic, err := invocation.OpenTraffic(ctx, restoredOwner, backupTestInstallationID, restoredGeneration, invocation.DefaultTrafficConfig())
	require.NoError(t, err)
	require.NoError(t, restoredTraffic.Close())
}
