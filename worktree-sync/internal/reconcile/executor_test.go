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

type sequenceSnapshotter struct{ snapshots []gitclient.Snapshot }

func (s *sequenceSnapshotter) Snapshot(context.Context, gitclient.Repository) (gitclient.Snapshot, error) {
	result := s.snapshots[0]
	if len(s.snapshots) > 1 {
		s.snapshots = s.snapshots[1:]
	}
	return result, nil
}

type tmuxFake struct {
	snapshotCalls    int
	snapshot         tmux.Snapshot
	snapshotErr      error
	createdSession   bool
	createTargets    []string
	created          []tmux.Window
	repaired         []string
	killed           []string
	launched         []string
	createErr        error
	repairResult     tmux.RepairResult
	repairErr        error
	ownershipChanged map[string]bool
}

func (f *tmuxFake) Snapshot(context.Context) (tmux.Snapshot, error) {
	f.snapshotCalls++
	return f.snapshot, f.snapshotErr
}
func (f *tmuxFake) CreateSession(_ context.Context, _ string, _ tmux.Window) (string, error) {
	f.createdSession = true
	return "$new", nil
}
func (f *tmuxFake) RenameSession(context.Context, string, string) error { return nil }
func (f *tmuxFake) OwnsSession(_ context.Context, id string, _ tmux.Metadata) (bool, error) {
	return !f.ownershipChanged[id], nil
}
func (f *tmuxFake) OwnsWindow(_ context.Context, id string, _ tmux.Metadata) (bool, error) {
	return !f.ownershipChanged[id], nil
}
func (f *tmuxFake) CreateWindow(_ context.Context, target string, w tmux.Window) (string, error) {
	f.createTargets = append(f.createTargets, target)
	f.created = append(f.created, w)
	if f.createErr != nil {
		return "", f.createErr
	}
	return "@new", nil
}
func (f *tmuxFake) RepairWindow(_ context.Context, id string, _, _ tmux.Window) (tmux.RepairResult, error) {
	f.repaired = append(f.repaired, id)
	return f.repairResult, f.repairErr
}
func (f *tmuxFake) KillWindow(_ context.Context, id string) error {
	f.killed = append(f.killed, id)
	return nil
}
func (f *tmuxFake) Launch(_ context.Context, id string, _ tmux.Metadata, _ string) error {
	f.launched = append(f.launched, id)
	return nil
}

type actionFake struct {
	runs      int
	launches  int
	prunes    int
	fail      bool
	runResult *actions.Result
}

func (a *actionFake) Run(context.Context, config.Repository, actions.Worktree, actions.Trigger, bool) actions.Result {
	a.runs++
	if a.runResult != nil {
		return *a.runResult
	}
	if a.fail {
		return actions.Result{Error: errors.New("setup failed")}
	}
	return actions.Result{}
}
func (a *actionFake) Launch(_ context.Context, _ config.Repository, _ actions.Worktree, _ actions.Trigger, _ bool, launch func() error) actions.Result {
	a.launches++
	return actions.Result{Error: launch()}
}
func (a *actionFake) Prune(context.Context, config.Repository, map[string]bool) error {
	a.prunes++
	return nil
}

func TestExecutorUsesProvidedSocketSnapshotWithoutResnapshotting(t *testing.T) {
	repo, snapshot := fixture(t)
	tmuxClient := &tmuxFake{}
	executor := reconcile.NewExecutor(gitSnapshotter{snapshot: snapshot}, tmuxClient, &actionFake{})
	executor.ReconcileRepoWithSnapshot(context.Background(), repo, tmux.Snapshot{Complete: true}, func(string, string) actions.Trigger { return actions.Passive })
	require.Zero(t, tmuxClient.snapshotCalls)
}

func TestPreviewUsesApplyPlanWithoutMutationsActionsOrLedgerPrune(t *testing.T) {
	repo, snapshot := fixture(t)
	actual := tmux.Snapshot{Complete: true}
	previewTmux, previewActions := &tmuxFake{}, &actionFake{}
	preview := reconcile.NewExecutor(gitSnapshotter{snapshot: snapshot}, previewTmux, previewActions).PreviewRepoWithSnapshot(context.Background(), repo, actual)
	require.NotEmpty(t, preview.Plan.Operations)
	require.Empty(t, preview.Outcomes)
	require.False(t, previewTmux.createdSession)
	require.Empty(t, previewTmux.created)
	require.Zero(t, previewActions.runs)
	require.Zero(t, previewActions.launches)
	require.Zero(t, previewActions.prunes)

	apply := reconcile.NewExecutor(gitSnapshotter{snapshot: snapshot}, &tmuxFake{snapshot: actual}, &actionFake{}).ReconcileRepoWithSnapshot(context.Background(), repo, actual, func(string, string) actions.Trigger { return actions.Passive })
	require.Equal(t, preview.Plan, apply.Plan)
	require.Len(t, apply.Outcomes, len(apply.Plan.Operations))
}

func TestExecutorCreatesInspectionWindowDespiteSetupFailureAndLaunchesOnlyCreated(t *testing.T) {
	repo, snapshot := fixture(t)
	repo.LaunchCommand = "agent"
	repo.SetupPolicy, repo.LaunchPolicy = config.ActionAll, config.ActionAll
	tmuxClient := &tmuxFake{snapshot: tmux.Snapshot{Complete: true}}
	actionManager := &actionFake{fail: true}
	executor := reconcile.NewExecutor(gitSnapshotter{snapshot: snapshot}, tmuxClient, actionManager)
	report := executor.ReconcileRepo(context.Background(), repo, func(string, string) actions.Trigger { return actions.Passive })
	require.True(t, tmuxClient.createdSession)
	require.Len(t, tmuxClient.created, 1)
	require.Equal(t, []string{"$new"}, tmuxClient.createTargets)
	require.Equal(t, 1, actionManager.runs)
	require.Equal(t, 1, actionManager.launches)
	require.Equal(t, []string{"@new"}, tmuxClient.launched)
	require.Contains(t, report.Errors[0], "setup failed")
}

func TestExecutorReportsCompletedSetupWithUnrecordedAttempt(t *testing.T) {
	repo, snapshot := fixture(t)
	desired := reconcile.Build(repo, snapshot, tmux.Snapshot{Complete: true}).Desired
	desired.Windows[0].ID = "@base"
	desired.Windows[1].ID = "@one"
	actual := tmux.Snapshot{Complete: true, Sessions: []tmux.Session{{ID: "$1", Name: desired.SessionName, Metadata: tmux.Metadata{Schema: 1, Repository: repo.Identity(), Role: "session", Identity: repo.Identity()}, Windows: desired.Windows}}}
	failedSave := actions.Result{Code: actions.ResultFailed, OperationCompleted: true, Error: errors.New("ledger save failed")}
	actionManager := &actionFake{runResult: &failedSave}
	report := reconcile.NewExecutor(gitSnapshotter{snapshot: snapshot}, &tmuxFake{snapshot: actual}, actionManager).ReconcileRepo(context.Background(), repo, func(string, string) actions.Trigger { return actions.Passive })
	require.Len(t, report.ActionOutcomes, 1)
	require.Equal(t, reconcile.OutcomePartial, report.ActionOutcomes[0].Status)
	require.Equal(t, "yes", report.ActionOutcomes[0].OperationCompleted)
	require.Equal(t, "no", report.ActionOutcomes[0].AttemptRecorded)
	require.Contains(t, report.ActionOutcomes[0].Recovery, "do not rerun")
}

func TestExecutorRevalidatesWorktreeIdentityBeforeActions(t *testing.T) {
	repo, initial := fixture(t)
	desired := reconcile.Build(repo, initial, tmux.Snapshot{Complete: true}).Desired
	actual := tmux.Snapshot{Complete: true, Sessions: []tmux.Session{{ID: "$1", Name: desired.SessionName, Metadata: tmux.Metadata{Schema: 1, Repository: repo.Identity(), Role: "session", Identity: repo.Identity()}, Windows: desired.Windows}}}
	replacement := initial
	replacement.Worktrees = append([]gitclient.Worktree(nil), initial.Worktrees...)
	replacement.Worktrees[1].Identity = "replacement"
	actionManager := &actionFake{}
	executor := reconcile.NewExecutor(&sequenceSnapshotter{snapshots: []gitclient.Snapshot{initial, replacement}}, &tmuxFake{snapshot: actual}, actionManager)
	executor.ReconcileRepo(context.Background(), repo, func(string, string) actions.Trigger { return actions.Passive })
	require.Zero(t, actionManager.runs)
}

func TestExecutorSkipsActionsWhenWorktreeMovesWithSameIdentity(t *testing.T) {
	repo, initial := fixture(t)
	desired := reconcile.Build(repo, initial, tmux.Snapshot{Complete: true}).Desired
	actual := tmux.Snapshot{Complete: true, Sessions: []tmux.Session{{ID: "$1", Name: desired.SessionName, Metadata: tmux.Metadata{Schema: 1, Repository: repo.Identity(), Role: "session", Identity: repo.Identity()}, Windows: desired.Windows}}}
	moved := initial
	moved.Worktrees = append([]gitclient.Worktree(nil), initial.Worktrees...)
	moved.Worktrees[1].Path = filepath.Join(t.TempDir(), "moved")
	actionManager := &actionFake{}
	report := reconcile.NewExecutor(&sequenceSnapshotter{snapshots: []gitclient.Snapshot{initial, moved}}, &tmuxFake{snapshot: actual}, actionManager).ReconcileRepo(context.Background(), repo, func(string, string) actions.Trigger { return actions.Passive })
	require.Zero(t, actionManager.runs)
	require.Contains(t, report.Errors[0], "path changed")
}

func TestExecutorReportsPartialRepairEffects(t *testing.T) {
	repo, snapshot := fixture(t)
	desired := reconcile.Build(repo, snapshot, tmux.Snapshot{Complete: true}).Desired
	worktree := desired.Windows[1]
	worktree.Name = "old-worktree"
	worktree.Path = "/wrong"
	actual := tmux.Snapshot{Complete: true, Sessions: []tmux.Session{{ID: "$1", Name: desired.SessionName, Metadata: tmux.Metadata{Schema: 1, Repository: repo.Identity(), Role: "session", Identity: repo.Identity()}, Windows: []tmux.Window{desired.Windows[0], worktree}}}}
	actual.Sessions[0].Windows[0].ID = "@base"
	actual.Sessions[0].Windows[1].ID = "@one"
	tmuxClient := &tmuxFake{snapshot: actual, repairResult: tmux.RepairResult{Renamed: true}, repairErr: errors.New("respawn failed")}
	actionManager := &actionFake{}
	report := reconcile.NewExecutor(gitSnapshotter{snapshot: snapshot}, tmuxClient, actionManager).ReconcileRepo(context.Background(), repo, func(string, string) actions.Trigger { return actions.Passive })
	require.Len(t, report.Outcomes, 1)
	require.Equal(t, reconcile.OutcomePartial, report.Outcomes[0].Status)
	require.Contains(t, report.Outcomes[0].Effects, "renamed window")
	require.True(t, report.Outcomes[0].Operation.RespawnsPane)
	require.Contains(t, report.Outcomes[0].Error, "respawn failed")
	require.Zero(t, actionManager.runs)
}

func TestExecutorRechecksSessionBeforeCreatingWindow(t *testing.T) {
	repo, snapshot := fixture(t)
	desired := reconcile.Build(repo, snapshot, tmux.Snapshot{Complete: true}).Desired
	actual := tmux.Snapshot{Complete: true, Sessions: []tmux.Session{{ID: "$1", Name: desired.SessionName, Metadata: tmux.Metadata{Schema: 1, Repository: repo.Identity(), Role: "session", Identity: repo.Identity()}, Windows: desired.Windows[:1]}}}
	tmuxClient := &tmuxFake{snapshot: actual, ownershipChanged: map[string]bool{"$1": true}}
	reconcile.NewExecutor(gitSnapshotter{snapshot: snapshot}, tmuxClient, &actionFake{}).ReconcileRepo(context.Background(), repo, func(string, string) actions.Trigger { return actions.Passive })
	require.Empty(t, tmuxClient.created)
}

func TestExecutorRechecksCreatedWindowBeforeLaunch(t *testing.T) {
	repo, snapshot := fixture(t)
	repo.LaunchCommand = "agent"
	repo.LaunchPolicy = config.ActionAll
	tmuxClient := &tmuxFake{snapshot: tmux.Snapshot{Complete: true}, ownershipChanged: map[string]bool{"@new": true}}
	report := reconcile.NewExecutor(gitSnapshotter{snapshot: snapshot}, tmuxClient, &actionFake{}).ReconcileRepo(context.Background(), repo, func(string, string) actions.Trigger { return actions.Passive })
	require.Empty(t, tmuxClient.launched)
	require.Contains(t, report.Errors[len(report.Errors)-1], "ownership changed")
}

func TestExecutorDoesNotRunSetupWithoutInspectionWindow(t *testing.T) {
	repo, snapshot := fixture(t)
	tmuxClient := &tmuxFake{snapshot: tmux.Snapshot{Complete: true}, createErr: errors.New("create failed")}
	actionManager := &actionFake{}
	executor := reconcile.NewExecutor(gitSnapshotter{snapshot: snapshot}, tmuxClient, actionManager)
	report := executor.ReconcileRepo(context.Background(), repo, func(string, string) actions.Trigger { return actions.Passive })
	require.Zero(t, actionManager.runs)
	require.NotEmpty(t, report.Errors)
}

func TestExecutorRechecksOwnershipBeforeRemoval(t *testing.T) {
	repo, snapshot := fixture(t)
	meta := func(role, identity string) tmux.Metadata {
		return tmux.Metadata{Schema: 1, Repository: repo.Identity(), Role: role, Identity: identity}
	}
	actual := tmux.Snapshot{Complete: true, Sessions: []tmux.Session{{ID: "$1", Name: "wts-repo", Metadata: meta("session", repo.Identity()), Windows: []tmux.Window{
		{ID: "@base", Name: "base", Path: repo.PrimaryRoot, Metadata: meta("base", repo.Identity())},
		{ID: "@one", Name: "feature-a", Path: snapshot.Worktrees[1].Path, Metadata: meta("worktree", "worktree-one")},
		{ID: "@stale", Name: "stale", Path: "/gone", Metadata: meta("worktree", "stale")},
	}}}}
	tmuxClient := &tmuxFake{snapshot: actual, ownershipChanged: map[string]bool{"@stale": true}}
	report := reconcile.NewExecutor(gitSnapshotter{snapshot: snapshot}, tmuxClient, &actionFake{}).ReconcileRepo(context.Background(), repo, func(string, string) actions.Trigger { return actions.Passive })
	require.Empty(t, tmuxClient.killed)
	require.NotEmpty(t, report.Errors)
}

func TestIncompleteTmuxSnapshotPerformsNoMutation(t *testing.T) {
	repo, snapshot := fixture(t)
	tmuxClient := &tmuxFake{snapshot: tmux.Snapshot{Complete: false}, snapshotErr: errors.New("partial tmux")}
	executor := reconcile.NewExecutor(gitSnapshotter{snapshot: snapshot}, tmuxClient, &actionFake{})
	report := executor.ReconcileRepo(context.Background(), repo, func(string, string) actions.Trigger { return actions.Passive })
	require.False(t, tmuxClient.createdSession)
	require.Empty(t, tmuxClient.created)
	require.Empty(t, tmuxClient.repaired)
	require.Empty(t, tmuxClient.killed)
	require.NotEmpty(t, report.Errors)
}

func TestIncompleteGitSnapshotNeverRepairsOrDeletes(t *testing.T) {
	repo, snapshot := fixture(t)
	snapshot.Complete = false
	meta := tmux.Metadata{Schema: 1, Repository: repo.Identity(), Role: "session", Identity: repo.Identity()}
	actual := tmux.Snapshot{Complete: true, Sessions: []tmux.Session{
		{ID: "$1", Name: "wts-" + repo.ID, Metadata: meta, Windows: []tmux.Window{
			{ID: "@stale", Name: "stale", Path: filepath.Join(repo.PrimaryRoot, "gone"), Metadata: tmux.Metadata{Schema: 1, Repository: repo.Identity(), Role: "worktree", Identity: "stale"}},
		}},
	}}
	tmuxClient := &tmuxFake{snapshot: actual}
	executor := reconcile.NewExecutor(gitSnapshotter{snapshot: snapshot, err: errors.New("partial")}, tmuxClient, &actionFake{})
	report := executor.ReconcileRepo(context.Background(), repo, func(string, string) actions.Trigger { return actions.Passive })
	require.False(t, tmuxClient.createdSession)
	require.Empty(t, tmuxClient.created)
	require.Empty(t, tmuxClient.repaired)
	require.Empty(t, tmuxClient.killed)
	require.NotEmpty(t, report.Errors)
}
