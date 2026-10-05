package authorization

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/accesstarget"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitpolicy"
	"github.com/stretchr/testify/require"
)

const gitReadPolicy = `{"version":1,"read":true,"refs":[]}`
const gitWritePolicy = `{"version":1,"read":true,"refs":[{"ref":{"kind":"prefix","value":"refs/heads/team/"},"actions":["create","update"]}]}`

func gitRepositoryInput() contract.GitRepositoryDefinition {
	return contract.GitRepositoryDefinition{Name: "Repository", URL: "https://example.com/team/repo", Aliases: []string{}}
}

func TestGitRepositoryImmutableDestinationAliasesAndRevisions(t *testing.T) {
	r, _ := newRepository(t, nil)
	ctx := t.Context()
	in := gitRepositoryInput()
	g, err := r.PutGitRepository(ctx, "", "", in)
	require.NoError(t, err)
	require.Equal(t, "https://example.com:443/team/repo", g.URL)
	in.URL = "https://example.com/team/other"
	_, err = r.PutGitRepository(ctx, g.ID, g.Revision, in)
	require.ErrorIs(t, err, ErrConflict)
	in = g.GitRepositoryDefinition
	in.Aliases = []string{"https://example.com/team/repo.git"}
	edited, err := r.PutGitRepository(ctx, g.ID, g.Revision, in)
	require.NoError(t, err)
	require.Equal(t, "2", edited.AliasRevision)
	_, err = r.PutGitRepository(ctx, g.ID, g.Revision, in)
	require.ErrorIs(t, err, ErrStaleRevision)
	in.URL = "https://example.com/team/repo.git"
	in.Aliases = []string{}
	_, err = r.PutGitRepository(ctx, "", "", in)
	require.ErrorIs(t, err, ErrConflict)
	in.URL = "https://example.com/team/repo/child"
	_, err = r.PutGitRepository(ctx, "", "", in)
	require.ErrorIs(t, err, ErrConflict)
	in.URL = "https://example.com/team/repo"
	in.Aliases = []string{"https://evil.example/team/repo"}
	_, err = r.PutGitRepository(ctx, g.ID, edited.Revision, in)
	require.ErrorIs(t, err, ErrInvalidInput)
	in = edited.GitRepositoryDefinition
	in.Name = "Renamed"
	renamed, err := r.PutGitRepository(ctx, g.ID, edited.Revision, in)
	require.NoError(t, err)
	require.Equal(t, edited.AliasRevision, renamed.AliasRevision)
}

func TestGitCrossDomainDefaultDenyAndIndependentRefActions(t *testing.T) {
	r, _ := newRepository(t, nil)
	ctx := t.Context()
	principal, _ := createAdmissionCredential(t, r)
	repo, err := r.PutGitRepository(ctx, "", "", gitRepositoryInput())
	require.NoError(t, err)
	d, err := r.EvaluateGit(ctx, principal.ID, repo.ID, nil)
	require.NoError(t, err)
	require.False(t, d.Allowed)
	_, err = r.PutHTTPGrant(ctx, "", "", HTTPGrantInput{PrincipalID: principal.ID, Policy: json.RawMessage(allowHTTPPolicy)})
	require.NoError(t, err)
	current, err := r.GetPrincipal(ctx, principal.ID)
	require.NoError(t, err)
	allow := contract.HTTPDefaultAllow
	_, err = r.PatchPrincipal(ctx, principal.ID, PatchPrincipalRequest{ExpectedRevision: current.Revision, HTTPDefault: &allow})
	require.NoError(t, err)
	d, err = r.EvaluateGit(ctx, principal.ID, repo.ID, nil)
	require.NoError(t, err)
	require.False(t, d.Allowed)
	input := GitGrantInput{PrincipalID: principal.ID, RepositoryID: repo.ID, Policy: json.RawMessage(gitReadPolicy)}
	grant, err := r.PutGitGrant(ctx, "", "", input)
	require.NoError(t, err)
	d, err = r.EvaluateGit(ctx, principal.ID, repo.ID, nil)
	require.NoError(t, err)
	require.True(t, d.Allowed)
	d, err = r.EvaluateGit(ctx, principal.ID, repo.ID, []gitpolicy.RefAction{{Ref: "refs/heads/team/a", Action: "create"}})
	require.NoError(t, err)
	require.False(t, d.Allowed)
	input.Policy = json.RawMessage(gitWritePolicy)
	edited, err := r.PutGitGrant(ctx, grant.ID, grant.Revision, input)
	require.NoError(t, err)
	d, err = r.EvaluateGit(ctx, principal.ID, repo.ID, []gitpolicy.RefAction{{Ref: "refs/heads/team/a", Action: "create"}, {Ref: "refs/heads/team/b", Action: "delete"}})
	require.NoError(t, err)
	require.False(t, d.Allowed)
	d, err = r.EvaluateGit(ctx, principal.ID, repo.ID, []gitpolicy.RefAction{{Ref: "refs/heads/team/a", Action: "update"}})
	require.NoError(t, err)
	require.True(t, d.Allowed)
	require.ErrorIs(t, r.DeleteGitGrant(ctx, grant.ID, grant.Revision), ErrStaleRevision)
	require.NoError(t, r.DeleteGitGrant(ctx, grant.ID, edited.Revision))
}

func TestGitPolicyMutationFencesPendingConfirmationAndProfileSurvivesDeletion(t *testing.T) {
	r, store := newRepository(t, nil)
	ctx := t.Context()
	principal, credential := createAdmissionCredential(t, r)
	repo, err := r.PutGitRepository(ctx, "", "", gitRepositoryInput())
	require.NoError(t, err)
	profile, err := r.GetGitRoutingProfile(ctx)
	require.NoError(t, err)
	profile, err = r.PutGitRoutingProfile(ctx, profile.Revision, []string{"https://example.com"})
	require.NoError(t, err)
	require.True(t, profile.Active)
	pending := mustAuthenticateLease(t, r, credential.Bearer)
	defer pending.Release()
	evaluation, err := r.EvaluateAdmission(ctx, pending, id(88), &ResolvedVerification{Target: accesstarget.Tool(contract.SyntheticServerID, "tool"), Arguments: mustAdmissionArguments(t, `{}`)})
	require.NoError(t, err)
	require.NotNil(t, evaluation.Candidate)
	_, err = r.PutGitGrant(ctx, "", "", GitGrantInput{PrincipalID: principal.ID, RepositoryID: repo.ID, Policy: json.RawMessage(gitReadPolicy)})
	require.NoError(t, err)
	_, err = r.ConfirmEvaluation(ctx, evaluation.Candidate, id(88))
	require.Error(t, err)
	require.NoError(t, r.DeleteGitRepository(ctx, repo.ID, repo.Revision))
	retained, err := r.GetGitRoutingProfile(ctx)
	require.NoError(t, err)
	require.Equal(t, profile, retained)
	list, err := r.ListGitRepositories(ctx)
	require.NoError(t, err)
	require.Empty(t, list)
	d, err := r.EvaluateGit(ctx, principal.ID, repo.ID, nil)
	require.NoError(t, err)
	require.False(t, d.Allowed)
	require.NoError(t, store.View(ctx, func(tx *sql.Tx) error {
		var n int
		err := tx.QueryRowContext(ctx, `SELECT count(*) FROM control_audit_events WHERE json_extract(event,'$.category') IN ('git_repository','git_grant','git_profile')`).Scan(&n)
		require.GreaterOrEqual(t, n, 4)
		return err
	}))
}
