package reconcile

import (
	"context"
	"fmt"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/actions"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/config"
	gitclient "github.com/averycrespi/agent-tools/worktree-sync/internal/git"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/tmux"
)

type snapshotter interface {
	Snapshot(context.Context, gitclient.Repository) (gitclient.Snapshot, error)
}
type tmuxClient interface {
	Snapshot(context.Context) (tmux.Snapshot, error)
	CreateSession(context.Context, string, tmux.Window) error
	CreateWindow(context.Context, string, tmux.Window) (string, error)
	RepairWindow(context.Context, string, tmux.Window) error
	KillWindow(context.Context, string) error
	Launch(context.Context, string, string) error
}
type actionManager interface {
	Run(context.Context, config.Repository, actions.Worktree, actions.Trigger, bool) actions.Result
	Launch(config.Repository, actions.Worktree, actions.Trigger, bool, func() error) actions.Result
}

type Executor struct {
	git     snapshotter
	tmux    tmuxClient
	actions actionManager
}

func NewExecutor(git snapshotter, tmux tmuxClient, actions actionManager) *Executor {
	return &Executor{git: git, tmux: tmux, actions: actions}
}

type Report struct {
	Plan   Plan     `json:"plan"`
	Errors []string `json:"errors"`
}

func (e *Executor) ReconcileRepo(ctx context.Context, repo config.Repository, trigger func(string) actions.Trigger) Report {
	gitSnapshot, gitErr := e.git.Snapshot(ctx, gitclient.Repository{PrimaryRoot: repo.PrimaryRoot, CommonGitDir: repo.CommonGitDir, Identity: repo.RepositoryIdentity})
	actual, tmuxErr := e.tmux.Snapshot(ctx)
	report := Report{}
	if gitErr != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("git snapshot: %v", gitErr))
	}
	if tmuxErr != nil || !actual.Complete {
		if tmuxErr != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("tmux snapshot: %v", tmuxErr))
		} else {
			report.Errors = append(report.Errors, "tmux snapshot incomplete")
		}
		return report
	}
	report.Plan = Build(repo, gitSnapshot, actual)
	report.Errors = append(report.Errors, report.Plan.Conflicts...)
	if len(report.Plan.Conflicts) > 0 {
		return report
	}
	windowByIdentity := make(map[string]tmux.Window)
	for _, window := range report.Plan.Desired.Windows {
		windowByIdentity[window.Metadata.Identity] = window
	}
	created := make(map[string]string)
	sessionTarget := report.Plan.Desired.SessionName
	for _, session := range actual.Sessions {
		if session.Name == sessionTarget {
			sessionTarget = session.ID
			break
		}
	}
	for _, operation := range report.Plan.Operations {
		window := windowByIdentity[operation.Identity]
		var err error
		switch operation.Type {
		case CreateSession:
			err = e.tmux.CreateSession(ctx, report.Plan.Desired.SessionName, window)
		case CreateWindow:
			var id string
			id, err = e.tmux.CreateWindow(ctx, sessionTarget, window)
			if err == nil {
				created[operation.Identity] = id
			}
		case RepairWindow:
			err = e.tmux.RepairWindow(ctx, operation.TargetID, window)
		case KillWindow:
			err = e.tmux.KillWindow(ctx, operation.TargetID)
		}
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("%s %s: %v", operation.Type, operation.Identity, err))
		}
	}
	if !gitSnapshot.Complete {
		return report
	}
	for _, worktree := range gitSnapshot.Worktrees {
		if _, desired := windowByIdentity[worktree.Identity]; !desired || worktree.Identity == repo.RepositoryIdentity {
			continue
		}
		worktreeTrigger := trigger(worktree.Path)
		actionWorktree := actions.Worktree{Path: worktree.Path, Identity: worktree.Identity}
		result := e.actions.Run(ctx, repo, actionWorktree, worktreeTrigger, false)
		if result.Error != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("setup %s: %v", worktree.Path, result.Error))
		}
		if windowID, wasCreated := created[worktree.Identity]; wasCreated {
			launchResult := e.actions.Launch(repo, actionWorktree, worktreeTrigger, false, func() error { return e.tmux.Launch(ctx, windowID, repo.LaunchCommand) })
			if launchResult.Error != nil {
				report.Errors = append(report.Errors, fmt.Sprintf("launch %s: %v", worktree.Path, launchResult.Error))
			}
		}
	}
	return report
}
