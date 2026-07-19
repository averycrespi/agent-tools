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
	CreateSession(context.Context, string, tmux.Window) (string, error)
	RenameSession(context.Context, string, string) error
	OwnsSession(context.Context, string, tmux.Metadata) (bool, error)
	OwnsWindow(context.Context, string, tmux.Metadata) (bool, error)
	CreateWindow(context.Context, string, tmux.Window) (string, error)
	RepairWindow(context.Context, string, tmux.Window, tmux.Window) error
	KillWindow(context.Context, string) error
	Launch(context.Context, string, tmux.Metadata, string) error
}
type actionManager interface {
	Run(context.Context, config.Repository, actions.Worktree, actions.Trigger, bool) actions.Result
	Launch(context.Context, config.Repository, actions.Worktree, actions.Trigger, bool, func() error) actions.Result
	Prune(context.Context, config.Repository, map[string]bool) error
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

func (e *Executor) ReconcileRepo(ctx context.Context, repo config.Repository, trigger func(string, string) actions.Trigger) Report {
	actual, tmuxErr := e.tmux.Snapshot(ctx)
	return e.reconcileRepo(ctx, repo, actual, tmuxErr, trigger)
}

func (e *Executor) ReconcileRepoWithSnapshot(ctx context.Context, repo config.Repository, actual tmux.Snapshot, trigger func(string, string) actions.Trigger) Report {
	return e.reconcileRepo(ctx, repo, actual, nil, trigger)
}

func (e *Executor) reconcileRepo(ctx context.Context, repo config.Repository, actual tmux.Snapshot, tmuxErr error, trigger func(string, string) actions.Trigger) Report {
	report := Report{}
	if tmuxErr != nil || !actual.Complete {
		if tmuxErr != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("tmux snapshot: %v", tmuxErr))
		} else {
			report.Errors = append(report.Errors, "tmux snapshot incomplete")
		}
		return report
	}
	gitSnapshot, gitErr := e.git.Snapshot(ctx, gitclient.Repository{PrimaryRoot: repo.PrimaryRoot, CommonGitDir: repo.CommonGitDir})
	if gitErr != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("git snapshot: %v", gitErr))
		return report
	}
	if !gitSnapshot.Complete {
		report.Errors = append(report.Errors, "git snapshot incomplete")
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
	actualByID := make(map[string]tmux.Window)
	for _, session := range actual.Sessions {
		for _, window := range session.Windows {
			actualByID[window.ID] = window
		}
	}
	created := make(map[string]string)
	projected := make(map[string]bool)
	for _, session := range actual.Sessions {
		if session.Metadata.Repository == repo.Identity() {
			for _, window := range session.Windows {
				if window.Metadata.Schema == tmux.MetadataSchema && window.Metadata.Repository == repo.Identity() && window.Metadata.Role == "worktree" {
					projected[window.Metadata.Identity] = true
				}
			}
		}
	}
	sessionTarget := report.Plan.Desired.SessionName
	sessionReady := false
	for _, session := range actual.Sessions {
		if session.Metadata.Schema == tmux.MetadataSchema && session.Metadata.Repository == repo.Identity() && session.Metadata.Role == "session" && session.Metadata.Identity == repo.Identity() {
			sessionTarget = session.ID
			sessionReady = true
			break
		}
	}
	for _, operation := range report.Plan.Operations {
		window := windowByIdentity[operation.Identity]
		var err error
		switch operation.Type {
		case CreateSession:
			var id string
			id, err = e.tmux.CreateSession(ctx, report.Plan.Desired.SessionName, window)
			if err == nil {
				sessionTarget, sessionReady = id, true
			}
		case RepairSession:
			expected := tmux.Metadata{Schema: tmux.MetadataSchema, Repository: repo.Identity(), Role: "session", Identity: repo.Identity()}
			owned, ownershipErr := e.tmux.OwnsSession(ctx, operation.TargetID, expected)
			switch {
			case ownershipErr != nil:
				err = ownershipErr
			case !owned:
				err = fmt.Errorf("ownership changed; refusing session mutation")
			default:
				err = e.tmux.RenameSession(ctx, operation.TargetID, operation.Name)
			}
		case CreateWindow:
			if !sessionReady {
				err = fmt.Errorf("managed session is unavailable")
				break
			}
			expectedSession := tmux.Metadata{Schema: tmux.MetadataSchema, Repository: repo.Identity(), Role: "session", Identity: repo.Identity()}
			owned, ownershipErr := e.tmux.OwnsSession(ctx, sessionTarget, expectedSession)
			if ownershipErr != nil {
				err = ownershipErr
				break
			}
			if !owned {
				err = fmt.Errorf("ownership changed; refusing window creation")
				break
			}
			var id string
			id, err = e.tmux.CreateWindow(ctx, sessionTarget, window)
			if err == nil {
				created[operation.Identity] = id
				projected[operation.Identity] = true
			}
		case RepairWindow:
			owned, ownershipErr := e.tmux.OwnsWindow(ctx, operation.TargetID, window.Metadata)
			switch {
			case ownershipErr != nil:
				err = ownershipErr
			case !owned:
				err = fmt.Errorf("ownership changed; refusing window repair")
			default:
				err = e.tmux.RepairWindow(ctx, operation.TargetID, actualByID[operation.TargetID], window)
			}
		case KillWindow:
			expected := tmux.Metadata{Schema: tmux.MetadataSchema, Repository: repo.Identity(), Role: operation.Role, Identity: operation.Identity}
			owned, ownershipErr := e.tmux.OwnsWindow(ctx, operation.TargetID, expected)
			switch {
			case ownershipErr != nil:
				err = ownershipErr
			case !owned:
				err = fmt.Errorf("ownership changed; refusing window removal")
			default:
				err = e.tmux.KillWindow(ctx, operation.TargetID)
			}
		}
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("%s %s: %v", operation.Type, operation.Identity, err))
		}
	}
	actionSnapshot, actionSnapshotErr := e.git.Snapshot(ctx, gitclient.Repository{PrimaryRoot: repo.PrimaryRoot, CommonGitDir: repo.CommonGitDir})
	if actionSnapshotErr != nil || !actionSnapshot.Complete {
		if actionSnapshotErr != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("action revalidation: %v", actionSnapshotErr))
		} else {
			report.Errors = append(report.Errors, "action revalidation incomplete")
		}
		return report
	}
	liveIdentities := make(map[string]bool)
	for _, worktree := range actionSnapshot.Worktrees {
		if worktree.Exclusion == "" && worktree.Identity != "" {
			liveIdentities[worktree.Identity] = true
		}
	}
	if err := e.actions.Prune(ctx, repo, liveIdentities); err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("action ledger prune: %v", err))
	}
	for _, worktree := range actionSnapshot.Worktrees {
		desiredWindow, desired := windowByIdentity[worktree.Identity]
		if !desired || worktree.Identity == repo.Identity() || !projected[worktree.Identity] {
			continue
		}
		if desiredWindow.Path != worktree.Path {
			report.Errors = append(report.Errors, fmt.Sprintf("action revalidation path changed for %s", worktree.Identity))
			continue
		}
		worktreeTrigger := trigger(worktree.Path, worktree.Identity)
		actionWorktree := actions.Worktree{Path: worktree.Path, Identity: worktree.Identity}
		result := e.actions.Run(ctx, repo, actionWorktree, worktreeTrigger, false)
		if result.Error != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("setup %s: %v", worktree.Path, result.Error))
		}
		if windowID, wasCreated := created[worktree.Identity]; wasCreated {
			launchResult := e.actions.Launch(ctx, repo, actionWorktree, worktreeTrigger, false, func() error {
				owned, ownershipErr := e.tmux.OwnsWindow(ctx, windowID, desiredWindow.Metadata)
				if ownershipErr != nil {
					return ownershipErr
				}
				if !owned {
					return fmt.Errorf("ownership changed; refusing launch")
				}
				return e.tmux.Launch(ctx, windowID, desiredWindow.Metadata, repo.LaunchCommand)
			})
			if launchResult.Error != nil {
				report.Errors = append(report.Errors, fmt.Sprintf("launch %s: %v", worktree.Path, launchResult.Error))
			}
		}
	}
	return report
}
