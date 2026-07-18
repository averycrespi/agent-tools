package reconcile_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/config"
	gitclient "github.com/averycrespi/agent-tools/worktree-sync/internal/git"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/reconcile"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/tmux"
)

func fixture(t *testing.T) (config.Repository, gitclient.Snapshot) {
	t.Helper()
	root := t.TempDir()
	allowed := filepath.Join(root, "worktrees")
	require.NoError(t, mkdir(allowed))
	one := filepath.Join(allowed, "one")
	require.NoError(t, mkdir(one))
	return config.Repository{ID: "repo", PrimaryRoot: root, CommonGitDir: filepath.Join(root, ".git"), RepositoryIdentity: "repo-identity", AllowedRoots: []string{allowed}}, gitclient.Snapshot{Complete: true, Worktrees: []gitclient.Worktree{
		{Path: root, Branch: "main", Identity: "repo-identity"},
		{Path: one, Branch: "feature/a", Identity: "worktree-one"},
	}}
}

func mkdir(path string) error { return os.MkdirAll(path, 0o700) }

func TestPlanCreatesSessionBaseAndEligibleWorktree(t *testing.T) {
	repo, gitSnapshot := fixture(t)
	plan := reconcile.Build(repo, gitSnapshot, tmux.Snapshot{Complete: true})
	require.Empty(t, plan.Conflicts)
	require.Equal(t, []reconcile.Operation{{Type: reconcile.CreateSession, Identity: "repo-identity", Name: "base", Path: repo.PrimaryRoot, Role: "base"}, {Type: reconcile.CreateWindow, Identity: "worktree-one", Name: "feature-a", Path: gitSnapshot.Worktrees[1].Path, Role: "worktree"}}, plan.Operations)
}

func TestPlanRepairsRenamedOwnedSession(t *testing.T) {
	repo, gitSnapshot := fixture(t)
	meta := func(role, identity string) tmux.Metadata {
		return tmux.Metadata{Schema: 1, Repository: repo.RepositoryIdentity, Role: role, Identity: identity}
	}
	actual := tmux.Snapshot{Complete: true, Sessions: []tmux.Session{{ID: "$1", Name: "renamed", Metadata: meta("session", repo.RepositoryIdentity), Windows: []tmux.Window{
		{ID: "@1", Name: "base", Path: repo.PrimaryRoot, Metadata: meta("base", repo.RepositoryIdentity)},
		{ID: "@2", Name: "feature-a", Path: gitSnapshot.Worktrees[1].Path, Metadata: meta("worktree", "worktree-one")},
	}}}}
	plan := reconcile.Build(repo, gitSnapshot, actual)
	require.Equal(t, []reconcile.Operation{{Type: reconcile.RepairSession, TargetID: "$1", Identity: repo.RepositoryIdentity, Name: "wts-repo"}}, plan.Operations)
}

func TestPlanPreservesManualAndRejectsForeignSessionCollision(t *testing.T) {
	repo, gitSnapshot := fixture(t)
	foreign := tmux.Session{ID: "$1", Name: "wts-repo", Metadata: tmux.Metadata{Schema: 1, Repository: "other", Role: "session", Identity: "other"}}
	plan := reconcile.Build(repo, gitSnapshot, tmux.Snapshot{Complete: true, Sessions: []tmux.Session{foreign}})
	require.Empty(t, plan.Operations)
	require.Contains(t, plan.Conflicts[0], "foreign")

	owned := tmux.Session{ID: "$1", Name: "wts-repo", Metadata: tmux.Metadata{Schema: 1, Repository: "repo-identity", Role: "session", Identity: "repo-identity"}, Windows: []tmux.Window{{ID: "@manual", Name: "feature-a", Path: "/scratch"}}}
	plan = reconcile.Build(repo, gitSnapshot, tmux.Snapshot{Complete: true, Sessions: []tmux.Session{owned}})
	require.Len(t, plan.Operations, 2)
	require.Equal(t, reconcile.CreateWindow, plan.Operations[0].Type)
	require.Contains(t, plan.Operations[1].Name, "feature-a-")
}

func TestPlanRepairsManagedStateRemovesDuplicatesAndStaleOnlyWhenComplete(t *testing.T) {
	repo, gitSnapshot := fixture(t)
	meta := func(role, identity string) tmux.Metadata {
		return tmux.Metadata{Schema: 1, Repository: "repo-identity", Role: role, Identity: identity}
	}
	actual := tmux.Snapshot{Complete: true, Sessions: []tmux.Session{{ID: "$1", Name: "wts-repo", Metadata: meta("session", "repo-identity"), Windows: []tmux.Window{
		{ID: "@1", Name: "renamed-base", Path: "/wrong", Metadata: meta("base", "repo-identity")},
		{ID: "@2", Name: "old", Path: gitSnapshot.Worktrees[1].Path, Metadata: meta("worktree", "worktree-one")},
		{ID: "@3", Name: "duplicate", Path: gitSnapshot.Worktrees[1].Path, Metadata: meta("worktree", "worktree-one")},
		{ID: "@4", Name: "stale", Path: "/gone", Metadata: meta("worktree", "gone")},
	}}}}
	plan := reconcile.Build(repo, gitSnapshot, actual)
	require.Equal(t, []reconcile.Operation{
		{Type: reconcile.RepairWindow, TargetID: "@1", Identity: "repo-identity", Name: "base", Path: repo.PrimaryRoot, Role: "base"},
		{Type: reconcile.RepairWindow, TargetID: "@2", Identity: "worktree-one", Name: "feature-a", Path: gitSnapshot.Worktrees[1].Path, Role: "worktree"},
		{Type: reconcile.KillWindow, TargetID: "@3", Identity: "worktree-one", Role: "worktree"},
		{Type: reconcile.KillWindow, TargetID: "@4", Identity: "gone", Role: "worktree"},
	}, plan.Operations)

	gitSnapshot.Complete = false
	plan = reconcile.Build(repo, gitSnapshot, actual)
	for _, operation := range plan.Operations {
		require.False(t, operation.Type == reconcile.KillWindow && operation.TargetID == "@4")
	}
}

func TestPlanIgnoresMalformedMetadataAndRemovesOwnedWindowOutsideSession(t *testing.T) {
	repo, snapshot := fixture(t)
	meta := func(role, identity string) tmux.Metadata {
		return tmux.Metadata{Schema: 1, Repository: repo.RepositoryIdentity, Role: role, Identity: identity}
	}
	actual := tmux.Snapshot{Complete: true, Sessions: []tmux.Session{
		{ID: "$owned", Name: "wts-repo", Metadata: meta("session", repo.RepositoryIdentity), Windows: []tmux.Window{
			{ID: "@base", Name: "base", Path: repo.PrimaryRoot, Metadata: meta("base", repo.RepositoryIdentity)},
			{ID: "@one", Name: "feature-a", Path: snapshot.Worktrees[1].Path, Metadata: meta("worktree", "worktree-one")},
			{ID: "@malformed", Name: "manual", Path: "/manual", Metadata: meta("base", "not-repository")},
		}},
		{ID: "$scratch", Name: "scratch", Windows: []tmux.Window{{ID: "@moved", Name: "moved", Path: "/gone", Metadata: meta("worktree", "moved")}}},
	}}
	plan := reconcile.Build(repo, snapshot, actual)
	require.Contains(t, plan.Operations, reconcile.Operation{Type: reconcile.KillWindow, TargetID: "@moved", Identity: "moved", Role: "worktree"})
	for _, operation := range plan.Operations {
		require.NotEqual(t, "@malformed", operation.TargetID)
	}
}

func TestDesiredSupportsDetachedLockedAndExcludesOutsideAndPrunable(t *testing.T) {
	repo, snapshot := fixture(t)
	outside := filepath.Join(t.TempDir(), "outside")
	snapshot.Worktrees = append(snapshot.Worktrees,
		gitclient.Worktree{Path: filepath.Join(repo.AllowedRoots[0], "locked"), HEAD: "abcdef012345", Detached: true, Locked: "busy", Identity: "locked"},
		gitclient.Worktree{Path: outside, Branch: "outside", Identity: "outside"},
		gitclient.Worktree{Path: "/gone", Branch: "gone", Prunable: "stale", Exclusion: "prunable"},
	)
	plan := reconcile.Build(repo, snapshot, tmux.Snapshot{Complete: true})
	require.Len(t, plan.Desired.Windows, 3)
	var detached tmux.Window
	for _, window := range plan.Desired.Windows {
		if window.Metadata.Identity == "locked" {
			detached = window
		}
	}
	require.Equal(t, "detached-abcdef01", detached.Name)
	require.Contains(t, strings.Join(plan.Report, "\n"), "outside allowed roots")
	require.Contains(t, strings.Join(plan.Report, "\n"), "prunable")
}
