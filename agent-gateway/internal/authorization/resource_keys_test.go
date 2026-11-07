package authorization

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/accesstarget"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestResourceKeysPreserveIdentityAcrossMetadataEditsAndDeletion(t *testing.T) {
	r, store := newRepository(t, nil)
	ctx := t.Context()
	a, err := r.CreatePrincipal(ctx, CreatePrincipalRequest{Key: "builder", DisplayName: "Build", Visibility: contract.VisibilityAll})
	require.NoError(t, err)
	require.Equal(t, "builder", a.Principal.Key)
	require.True(t, contract.ValidResourceKey(a.DefaultGrant.Key))
	renamed := "Renamed"
	updated, err := r.PatchPrincipal(ctx, a.Principal.ID, PatchPrincipalRequest{ExpectedRevision: a.Principal.Revision, DisplayName: &renamed})
	require.NoError(t, err)
	require.Equal(t, a.Principal.Key, updated.Key)
	require.Equal(t, a.Principal.ID, updated.ID)
	_, err = r.CreatePrincipal(ctx, CreatePrincipalRequest{Key: "builder", DisplayName: "Other", Visibility: contract.VisibilityAll})
	require.ErrorIs(t, err, ErrConflict)
	syntheticGrantValidator := func(context.Context, *sql.Tx, string) (bool, error) { return true, nil }
	request := CreateGrantRequest{Key: "builder", PrincipalID: a.Principal.ID, Effect: contract.GrantAllow, Target: accesstarget.MCP{ServerID: contract.SyntheticServerID}}
	g, err := r.CreateGrant(ctx, request, syntheticGrantValidator)
	require.NoError(t, err, "same key in another type is valid")
	request.Key = ""
	duplicate, err := r.CreateGrant(ctx, request, syntheticGrantValidator)
	require.NoError(t, err)
	require.NotEqual(t, g.Key, duplicate.Key, "identical policies retain distinct grant keys")
	for _, grant := range []contract.Grant{g, duplicate, a.DefaultGrant} {
		require.True(t, contract.ValidResourceKey(grant.Key))
	}
	require.NoError(t, r.DeleteGrant(ctx, g.ID))
	request.Key = "builder"
	_, err = r.CreateGrant(ctx, request, syntheticGrantValidator)
	require.ErrorIs(t, err, ErrConflict, "deleted keys cannot resurrect grants")
	_, err = r.GetGrant(ctx, g.ID)
	require.ErrorIs(t, err, ErrNotFound)

	repoInput := gitRepositoryInput()
	repoInput.Key = "builder"
	repo, err := r.PutGitRepository(ctx, "", "", repoInput)
	require.NoError(t, err)
	require.Equal(t, "builder", repo.Key)
	repoInput.Key = "changed"
	_, err = r.PutGitRepository(ctx, repo.ID, repo.Revision, repoInput)
	require.ErrorIs(t, err, ErrInvalidInput)
	gitInput := GitGrantInput{Key: "builder", PrincipalID: a.Principal.ID, RepositoryID: repo.ID, Policy: json.RawMessage(gitReadPolicy)}
	gitGrant, err := r.PutGitGrant(ctx, "", "", gitInput)
	require.NoError(t, err)
	require.Equal(t, "builder", gitGrant.Key)
	require.NoError(t, r.DeleteGitGrant(ctx, gitGrant.ID, gitGrant.Revision))
	_, err = r.PutGitGrant(ctx, "", "", gitInput)
	require.ErrorIs(t, err, ErrConflict)
	httpInput := HTTPGrantInput{Key: "builder", PrincipalID: a.Principal.ID, Policy: json.RawMessage(`{"version":1,"type":"block_destination","destination":{"host":"example.com","port":443}}`)}
	httpGrant, err := r.PutHTTPGrant(ctx, "", "", httpInput)
	require.NoError(t, err)
	require.Equal(t, "builder", httpGrant.Key)
	require.NoError(t, r.DeleteHTTPGrant(ctx, httpGrant.ID, httpGrant.Revision))
	_, err = r.PutHTTPGrant(ctx, "", "", httpInput)
	require.ErrorIs(t, err, ErrConflict)
	require.NoError(t, r.DeleteGitRepository(ctx, repo.ID, repo.Revision))
	repoInput.URL = "https://example.com/different/repository"
	repoInput.Key = "builder"
	_, err = r.PutGitRepository(ctx, "", "", repoInput)
	require.ErrorIs(t, err, ErrConflict)
	// All authority remains bound to installation-local IDs.
	loaded, err := r.GetPrincipal(ctx, a.Principal.ID)
	require.NoError(t, err)
	require.Equal(t, a.Principal.ID, loaded.ID)
	require.NoError(t, store.View(ctx, func(tx *sql.Tx) error {
		var id string
		err := tx.QueryRowContext(ctx, `SELECT resource_id FROM resource_keys WHERE kind='mcp_grant' AND key='builder'`).Scan(&id)
		require.Equal(t, g.ID, id)
		return err
	}))
}
