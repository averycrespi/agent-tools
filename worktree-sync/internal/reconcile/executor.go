package reconcile

import (
	"context"
	"errors"
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
	RepairWindow(context.Context, string, tmux.Window, tmux.Window) (tmux.RepairResult, error)
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

type OutcomeStatus string

const (
	OutcomeApplied OutcomeStatus = "applied"
	OutcomePartial OutcomeStatus = "partial"
	OutcomeFailed  OutcomeStatus = "failed"
)

type Outcome struct {
	Operation Operation     `json:"operation"`
	Status    OutcomeStatus `json:"status"`
	Effects   []string      `json:"effects"`
	Error     string        `json:"error,omitempty"`
}

type ActionOutcome struct {
	Action             string        `json:"action"`
	Path               string        `json:"path"`
	Status             OutcomeStatus `json:"status"`
	OperationCompleted string        `json:"operation_completed"`
	AttemptRecorded    string        `json:"attempt_recorded"`
	TextSent           string        `json:"text_sent,omitempty"`
	EnterSent          string        `json:"enter_sent,omitempty"`
	Error              string        `json:"error,omitempty"`
	Recovery           string        `json:"recovery,omitempty"`
}

type Report struct {
	Plan           Plan            `json:"plan"`
	Outcomes       []Outcome       `json:"outcomes"`
	ActionOutcomes []ActionOutcome `json:"action_outcomes"`
	Errors         []string        `json:"errors"`
}

func attemptState(result actions.Result) string {
	if result.AttemptRecorded {
		return "yes"
	}
	if result.AttemptRecordUncertain {
		return "unknown"
	}
	return "no"
}

func completedState(result actions.Result) string {
	if result.OperationCompleted {
		return "yes"
	}
	return "no"
}

func actionOutcome(action string, repo config.Repository, worktree actions.Worktree, result actions.Result) ActionOutcome {
	outcome := ActionOutcome{Action: action, Path: worktree.Path, Status: OutcomeApplied, OperationCompleted: completedState(result), AttemptRecorded: attemptState(result)}
	if result.Error == nil {
		return outcome
	}
	outcome.Status = OutcomeFailed
	outcome.Error = result.Error.Error()
	if action == "setup" {
		outcome.Status = OutcomePartial
		switch {
		case result.OperationCompleted && !result.AttemptRecorded:
			outcome.Recovery = "setup completed but ledger persistence failed or is uncertain; do not rerun until state and side effects are inspected"
		case result.AttemptRecordUncertain:
			outcome.Recovery = "inspect the action ledger and side effects before deciding whether --rerun is safe"
		case result.AttemptRecorded:
			outcome.Recovery = fmt.Sprintf("inspect earlier side effects, correct the cause, then run wts worktree setup %q --repo-id %s --rerun if safe", worktree.Path, repo.ID)
		default:
			outcome.Recovery = fmt.Sprintf("earlier side effects may remain; inspect them before retrying wts worktree setup %q --repo-id %s", worktree.Path, repo.ID)
		}
		return outcome
	}
	outcome.TextSent, outcome.EnterSent = "no", "no"
	if result.OperationCompleted {
		outcome.TextSent, outcome.EnterSent = "yes", "yes"
	} else {
		var delivery *tmux.LaunchError
		if errors.As(result.Error, &delivery) {
			outcome.TextSent, outcome.EnterSent = delivery.TextSent, delivery.EnterSent
		}
	}
	if outcome.TextSent != "no" || outcome.EnterSent != "no" || result.AttemptRecorded || result.AttemptRecordUncertain {
		outcome.Status = OutcomePartial
	}
	outcome.Recovery = fmt.Sprintf("inspect the managed window and action ledger, then run wts worktree launch %q --repo-id %s --rerun if safe", worktree.Path, repo.ID)
	return outcome
}

func (e *Executor) ReconcileRepo(ctx context.Context, repo config.Repository, trigger func(string, string) actions.Trigger) Report {
	actual, tmuxErr := e.tmux.Snapshot(ctx)
	return e.reconcileRepo(ctx, repo, actual, tmuxErr, trigger)
}

func (e *Executor) ReconcileRepoWithSnapshot(ctx context.Context, repo config.Repository, actual tmux.Snapshot, trigger func(string, string) actions.Trigger) Report {
	return e.reconcileRepo(ctx, repo, actual, nil, trigger)
}

func (e *Executor) PreviewRepoWithSnapshot(ctx context.Context, repo config.Repository, actual tmux.Snapshot) Report {
	return e.planRepo(ctx, repo, actual, nil)
}

func (e *Executor) planRepo(ctx context.Context, repo config.Repository, actual tmux.Snapshot, tmuxErr error) Report {
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
	return report
}

func (e *Executor) reconcileRepo(ctx context.Context, repo config.Repository, actual tmux.Snapshot, tmuxErr error, trigger func(string, string) actions.Trigger) Report {
	report := e.planRepo(ctx, repo, actual, tmuxErr)
	if len(report.Errors) > 0 {
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
	projected := make(map[string]string)
	for _, session := range actual.Sessions {
		if session.Metadata.Repository == repo.Identity() {
			for _, window := range session.Windows {
				if window.Metadata.Schema == tmux.MetadataSchema && window.Metadata.Repository == repo.Identity() && window.Metadata.Role == "worktree" {
					currentID := projected[window.Metadata.Identity]
					if currentID == "" || window.ID < currentID {
						projected[window.Metadata.Identity] = window.ID
					}
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
		effects := make([]string, 0, 3)
		switch operation.Type {
		case CreateSession:
			var id string
			id, err = e.tmux.CreateSession(ctx, report.Plan.Desired.SessionName, window)
			if err == nil {
				sessionTarget, sessionReady = id, true
				effects = append(effects, "created session "+id)
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
				if err == nil {
					effects = append(effects, "renamed session")
				}
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
				projected[operation.Identity] = id
				effects = append(effects, "created window "+id)
			}
		case RepairWindow:
			current, exists := actualByID[operation.TargetID]
			if !exists {
				err = fmt.Errorf("target window disappeared")
				break
			}
			owned, ownershipErr := e.tmux.OwnsWindow(ctx, operation.TargetID, current.Metadata)
			switch {
			case ownershipErr != nil:
				err = ownershipErr
			case !owned:
				err = fmt.Errorf("ownership changed; refusing window repair")
			default:
				var repaired tmux.RepairResult
				repaired, err = e.tmux.RepairWindow(ctx, operation.TargetID, current, window)
				if repaired.Renamed {
					effects = append(effects, "renamed window")
				}
				if repaired.Respawned {
					effects = append(effects, "respawned pane")
				}
				if repaired.MetadataOptions > 0 {
					effects = append(effects, fmt.Sprintf("wrote %d metadata options", repaired.MetadataOptions))
				}
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
				if err == nil {
					effects = append(effects, "removed window")
				}
			}
		}
		outcome := Outcome{Operation: operation, Status: OutcomeApplied, Effects: effects}
		if err != nil {
			if operation.Type == RepairWindow {
				delete(projected, operation.Identity)
			}
			outcome.Status = OutcomeFailed
			if len(effects) > 0 {
				outcome.Status = OutcomePartial
			}
			outcome.Error = err.Error()
			report.Errors = append(report.Errors, fmt.Sprintf("%s %s: %v", operation.Type, operation.Identity, err))
		}
		report.Outcomes = append(report.Outcomes, outcome)
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
		windowID, isProjected := projected[worktree.Identity]
		if !desired || worktree.Identity == repo.Identity() || !isProjected {
			continue
		}
		owned, ownershipErr := e.tmux.OwnsWindow(ctx, windowID, desiredWindow.Metadata)
		if ownershipErr != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("action window revalidation %s: %v", worktree.Path, ownershipErr))
			continue
		}
		if !owned {
			report.Errors = append(report.Errors, fmt.Sprintf("action window ownership changed for %s", worktree.Path))
			continue
		}
		if desiredWindow.Path != worktree.Path {
			report.Errors = append(report.Errors, fmt.Sprintf("action revalidation path changed for %s", worktree.Identity))
			continue
		}
		worktreeTrigger := trigger(worktree.Path, worktree.Identity)
		actionWorktree := actions.Worktree{Path: worktree.Path, Identity: worktree.Identity}
		result := e.actions.Run(ctx, repo, actionWorktree, worktreeTrigger, false)
		if !result.Skipped {
			report.ActionOutcomes = append(report.ActionOutcomes, actionOutcome("setup", repo, actionWorktree, result))
		}
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
			if !launchResult.Skipped {
				report.ActionOutcomes = append(report.ActionOutcomes, actionOutcome("launch", repo, actionWorktree, launchResult))
			}
			if launchResult.Error != nil {
				report.Errors = append(report.Errors, fmt.Sprintf("launch %s: %v", worktree.Path, launchResult.Error))
			}
		}
	}
	return report
}
