package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/actions"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/config"
	gitclient "github.com/averycrespi/agent-tools/worktree-sync/internal/git"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/state"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/tmux"
)

type repositoryRunner struct {
	primary string
	linked  string
	common  string
	outside bool
}

type gitExitError struct{ code int }

func (e gitExitError) Error() string { return "git failed" }
func (e gitExitError) ExitCode() int { return e.code }

func (r repositoryRunner) Run(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
	if name != "git" {
		return nil, errors.New("unexpected executable")
	}
	command := strings.Join(args, " ")
	switch command {
	case "rev-parse --is-inside-work-tree":
		if r.outside {
			return nil, gitExitError{code: 128}
		}
		return []byte("true\n"), nil
	case "rev-parse --show-toplevel":
		return []byte(r.linked + "\n"), nil
	case "rev-parse --git-common-dir":
		return []byte(r.common + "\n"), nil
	case "worktree list --porcelain -z":
		return []byte("worktree " + r.primary + "\x00HEAD abcdef\x00branch refs/heads/main\x00\x00worktree " + r.linked + "\x00HEAD 123456\x00branch refs/heads/feature\x00\x00"), nil
	default:
		return nil, errors.New("unexpected git command: " + command)
	}
}
func (repositoryRunner) RunEnv(context.Context, string, []string, string, ...string) ([]byte, error) {
	return nil, errors.New("unexpected RunEnv")
}
func (repositoryRunner) Interactive(context.Context, string, string, ...string) error {
	return errors.New("unexpected interactive command")
}

func repositoryFixture(t *testing.T) (repositoryRunner, config.Repository) {
	t.Helper()
	root := t.TempDir()
	primary := filepath.Join(root, "primary")
	linked := filepath.Join(root, "linked")
	common := filepath.Join(primary, ".git")
	allowed := filepath.Join(root, "allowed")
	require.NoError(t, os.MkdirAll(common, 0o700))
	require.NoError(t, os.Mkdir(linked, 0o700))
	require.NoError(t, os.Mkdir(allowed, 0o700))
	t.Chdir(linked)
	runner := repositoryRunner{primary: primary, linked: linked, common: common}
	repo := config.Repository{ID: "repo", PrimaryRoot: primary, CommonGitDir: common, WorktreeCreationRoot: allowed, AllowedRoots: []string{allowed}, SetupPolicy: config.ActionManual, LaunchPolicy: config.ActionManual}
	return runner, repo
}

func TestResolveRepoUsesCurrentCommonGitIdentity(t *testing.T) {
	runner, repo := repositoryFixture(t)
	service := New(runner, config.Paths{})
	cfg := config.Default()
	cfg.Repositories = []config.Repository{repo}
	resolved, err := service.resolveRepo(context.Background(), cfg, "")
	require.NoError(t, err)
	require.Equal(t, repo, resolved)
}

func TestResolveRepoExplainsEmptyAndUnregisteredContexts(t *testing.T) {
	runner, repo := repositoryFixture(t)
	service := New(runner, config.Paths{})
	_, err := service.resolveRepo(context.Background(), config.Default(), "")
	require.ErrorContains(t, err, "no repositories are registered")
	require.ErrorContains(t, err, "wts repo add")
	require.ErrorContains(t, err, repo.PrimaryRoot)

	cfg := config.Default()
	other := repo
	other.ID = "other"
	other.CommonGitDir = filepath.Join(t.TempDir(), ".git")
	require.NoError(t, os.Mkdir(other.CommonGitDir, 0o700))
	cfg.Repositories = []config.Repository{other}
	_, err = service.resolveRepo(context.Background(), cfg, "")
	require.ErrorContains(t, err, "current Git repository is not registered")
	require.ErrorContains(t, err, repo.PrimaryRoot)
}

func TestPostCommitFailureReportsEffectAndRecovery(t *testing.T) {
	cause := errors.New("directory sync failed")
	output, err := postCommitFailure(&state.PostCommitError{Err: cause}, "registration was written", "run wts repo list")
	require.ErrorIs(t, err, cause)
	require.Contains(t, output, "registration was written")
	require.Contains(t, output, "durability is uncertain")
	require.Contains(t, output, "run wts repo list")

	output, err = postCommitFailure(cause, "not written", "retry")
	require.ErrorIs(t, err, cause)
	require.Empty(t, output)
}

func TestSetupResultReportsCompletedSkippedAndFailed(t *testing.T) {
	output, err := formatSetupResult("/worktree", "repo", actions.Result{Code: actions.ResultCompleted})
	require.NoError(t, err)
	require.Equal(t, "setup completed in /worktree", output)
	output, err = formatSetupResult("/worktree", "repo", actions.Result{Code: actions.ResultSkippedPolicy, Skipped: true, Reason: "disabled by policy"})
	require.NoError(t, err)
	require.Equal(t, "setup skipped for /worktree: disabled by policy", output)
	output, err = formatSetupResult("/worktree", "repo", actions.Result{Code: actions.ResultSkippedAttempted, Skipped: true, Reason: "already attempted"})
	require.NoError(t, err)
	require.Contains(t, output, `wts worktree setup "/worktree" --repo-id repo --rerun`)
	failure := errors.New("failed")
	output, err = formatSetupResult("/worktree", "repo", actions.Result{Code: actions.ResultFailed, AttemptRecorded: true, Error: failure})
	require.ErrorIs(t, err, failure)
	require.Contains(t, output, "setup failed in /worktree")
	require.Contains(t, output, "earlier setup side effects may remain")
	require.Contains(t, output, `wts worktree setup "/worktree" --repo-id repo --rerun`)

	output, err = formatSetupResult("/worktree", "repo", actions.Result{Code: actions.ResultFailed, OperationCompleted: true, AttemptRecorded: false, Error: failure})
	require.ErrorIs(t, err, failure)
	require.Contains(t, output, "operation_completed=yes attempt_recorded=no")
	require.Contains(t, output, "do not rerun")

	output, err = formatSetupResult("/worktree", "repo", actions.Result{Code: actions.ResultFailed, AttemptRecordUncertain: true, Error: failure})
	require.ErrorIs(t, err, failure)
	require.Contains(t, output, "attempt_recorded=unknown")
	require.Contains(t, output, "inspect the action ledger")
}

func TestRepositoryRootMutationsPreserveSafetyBoundaries(t *testing.T) {
	base := t.TempDir()
	creation := filepath.Join(base, "creation")
	oldAllowed := filepath.Join(base, "old")
	nestedAllowed := filepath.Join(oldAllowed, "nested")
	newCreation := filepath.Join(base, "new-creation")
	for _, path := range []string{creation, nestedAllowed, newCreation} {
		require.NoError(t, os.MkdirAll(path, 0o700))
	}
	repo := config.Repository{ID: "repo", WorktreeCreationRoot: creation, AllowedRoots: []string{creation, oldAllowed, nestedAllowed}}

	updated := setCreationRoot(repo, newCreation)
	require.Equal(t, newCreation, updated.WorktreeCreationRoot)
	require.Equal(t, []string{creation, oldAllowed, nestedAllowed, newCreation}, updated.AllowedRoots)
	updated = addAllowedRoot(updated, nestedAllowed)
	require.Equal(t, []string{creation, oldAllowed, nestedAllowed, newCreation}, updated.AllowedRoots)

	_, err := removeAllowedRoot(updated, gitclient.Snapshot{Complete: true}, newCreation)
	require.ErrorContains(t, err, "creation root")
	dependent := filepath.Join(oldAllowed, "feature")
	snapshot := gitclient.Snapshot{Complete: true, Worktrees: []gitclient.Worktree{{Path: dependent, Identity: "feature"}}}
	_, err = removeAllowedRoot(updated, snapshot, oldAllowed)
	require.ErrorContains(t, err, dependent)

	nestedWorktree := filepath.Join(nestedAllowed, "feature")
	snapshot.Worktrees[0].Path = nestedWorktree
	updated, err = removeAllowedRoot(updated, snapshot, oldAllowed)
	require.NoError(t, err)
	require.Equal(t, []string{creation, nestedAllowed, newCreation}, updated.AllowedRoots)
	outside := filepath.Join(base, "outside", "feature")
	_, err = removeAllowedRoot(updated, gitclient.Snapshot{Complete: true, Worktrees: []gitclient.Worktree{{Path: outside, Identity: "outside"}}}, nestedAllowed)
	require.ErrorContains(t, err, "not covered")
	_, err = removeAllowedRoot(updated, gitclient.Snapshot{Complete: false}, nestedAllowed)
	require.ErrorContains(t, err, "incomplete")
}

func TestAttachSessionRequiresOwnedMetadataAndRejectsNameCollision(t *testing.T) {
	_, repo := repositoryFixture(t)
	expected := tmux.Metadata{Schema: tmux.MetadataSchema, Repository: repo.Identity(), Role: "session", Identity: repo.Identity()}
	owned := tmux.Session{ID: "$2", Name: "renamed", Metadata: expected}
	foreign := tmux.Session{ID: "$1", Name: "wts-" + repo.ID}
	_, err := attachSession(tmux.Snapshot{Complete: true, Sessions: []tmux.Session{foreign, owned}}, repo)
	require.ErrorContains(t, err, "conflicts")

	session, err := attachSession(tmux.Snapshot{Complete: true, Sessions: []tmux.Session{owned}}, repo)
	require.NoError(t, err)
	require.Equal(t, "$2", session.ID)
	_, err = attachSession(tmux.Snapshot{Complete: true}, repo)
	require.ErrorContains(t, err, "reconcile --repo-id repo")
}

func TestFindWorktreePrefersBranchThenCanonicalizesPath(t *testing.T) {
	root := t.TempDir()
	branchWorktree := filepath.Join(root, "branch-worktree")
	pathWorktree := filepath.Join(root, "path-worktree")
	require.NoError(t, os.Mkdir(branchWorktree, 0o700))
	require.NoError(t, os.Mkdir(pathWorktree, 0o700))
	require.NoError(t, os.Symlink(pathWorktree, filepath.Join(root, "linked-path")))
	require.NoError(t, os.Mkdir(filepath.Join(root, "feature"), 0o700))
	t.Chdir(root)
	snapshot := gitclient.Snapshot{Complete: true, Worktrees: []gitclient.Worktree{
		{Path: pathWorktree, Branch: "other"},
		{Path: branchWorktree, Branch: "feature"},
	}}

	matched, err := findWorktree(snapshot, "feature")
	require.NoError(t, err)
	require.Equal(t, branchWorktree, matched.Path)
	matched, err = findWorktree(snapshot, "linked-path")
	require.NoError(t, err)
	require.Equal(t, pathWorktree, matched.Path)
}

func TestResolveRepoExplainsOutsideGitContext(t *testing.T) {
	runner, repo := repositoryFixture(t)
	runner.outside = true
	service := New(runner, config.Paths{})
	cfg := config.Default()
	cfg.Repositories = []config.Repository{repo}
	_, err := service.resolveRepo(context.Background(), cfg, "")
	require.ErrorContains(t, err, "not inside a registered Git worktree")
	require.ErrorContains(t, err, "--repo-id")
}
