package reconcile_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/actions"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/config"
	gitclient "github.com/averycrespi/agent-tools/worktree-sync/internal/git"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/reconcile"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/tmux"
)

type gitSnapshotter struct {
	snapshot gitclient.Snapshot
	err      error
}

func (g gitSnapshotter) Snapshot(context.Context, gitclient.Repository) (gitclient.Snapshot, error) {
	return g.snapshot, g.err
}

type tmuxFake struct {
	snapshot       tmux.Snapshot
	createdSession bool
	created        []tmux.Window
	repaired       []string
	killed         []string
	launched       []string
}

func (f *tmuxFake) Snapshot(context.Context) (tmux.Snapshot, error) { return f.snapshot, nil }
func (f *tmuxFake) CreateSession(_ context.Context, _ string, _ tmux.Window) error {
	f.createdSession = true
	return nil
}
func (f *tmuxFake) CreateWindow(_ context.Context, _ string, w tmux.Window) (string, error) {
	f.created = append(f.created, w)
	return "@new", nil
}
func (f *tmuxFake) RepairWindow(_ context.Context, id string, _ tmux.Window) error {
	f.repaired = append(f.repaired, id)
	return nil
}
func (f *tmuxFake) KillWindow(_ context.Context, id string) error {
	f.killed = append(f.killed, id)
	return nil
}
func (f *tmuxFake) Launch(_ context.Context, id, _ string) error {
	f.launched = append(f.launched, id)
	return nil
}

type actionFake struct {
	runs     int
	launches int
	fail     bool
}

func (a *actionFake) Run(context.Context, config.Repository, actions.Worktree, actions.Trigger, bool) actions.Result {
	a.runs++
	if a.fail {
		return actions.Result{Error: errors.New("setup failed")}
	}
	return actions.Result{}
}
func (a *actionFake) Launch(_ config.Repository, _ actions.Worktree, _ actions.Trigger, _ bool, launch func() error) actions.Result {
	a.launches++
	return actions.Result{Error: launch()}
}

func TestExecutorCreatesInspectionWindowDespiteSetupFailureAndLaunchesOnlyCreated(t *testing.T) {
	repo, snapshot := fixture(t)
	repo.LaunchCommand = "agent"
	repo.Policy = config.Policy{SetupPassive: true, LaunchPassive: true}
	tmuxClient := &tmuxFake{snapshot: tmux.Snapshot{Complete: true}}
	actionManager := &actionFake{fail: true}
	executor := reconcile.NewExecutor(gitSnapshotter{snapshot: snapshot}, tmuxClient, actionManager)
	report := executor.ReconcileRepo(context.Background(), repo, func(string) actions.Trigger { return actions.Passive })
	require.True(t, tmuxClient.createdSession)
	require.Len(t, tmuxClient.created, 1)
	require.Equal(t, 1, actionManager.runs)
	require.Equal(t, 1, actionManager.launches)
	require.Equal(t, []string{"@new"}, tmuxClient.launched)
	require.Contains(t, report.Errors[0], "setup failed")
}

func TestIncompleteGitSnapshotNeverRepairsOrDeletes(t *testing.T) {
	repo, snapshot := fixture(t)
	snapshot.Complete = false
	meta := tmux.Metadata{Schema: 1, Repository: repo.RepositoryIdentity, Role: "session", Identity: repo.RepositoryIdentity}
	actual := tmux.Snapshot{Complete: true, Sessions: []tmux.Session{
		{ID: "$1", Name: "wts-" + repo.ID, Metadata: meta, Windows: []tmux.Window{
			{ID: "@stale", Name: "stale", Path: filepath.Join(repo.PrimaryRoot, "gone"), Metadata: tmux.Metadata{Schema: 1, Repository: repo.RepositoryIdentity, Role: "worktree", Identity: "stale"}},
		}},
	}}
	tmuxClient := &tmuxFake{snapshot: actual}
	executor := reconcile.NewExecutor(gitSnapshotter{snapshot: snapshot, err: errors.New("partial")}, tmuxClient, &actionFake{})
	report := executor.ReconcileRepo(context.Background(), repo, func(string) actions.Trigger { return actions.Passive })
	require.Empty(t, tmuxClient.repaired)
	require.Empty(t, tmuxClient.killed)
	require.NotEmpty(t, report.Errors)
}
