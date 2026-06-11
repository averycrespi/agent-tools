package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/output"
	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/store"
	"github.com/spf13/cobra"
)

var cleanupDryRun bool

func init() {
	cleanupCmd.Flags().BoolVar(&cleanupDryRun, "dry-run", false, "show what would be cleaned without mutating state")
	cleanupCmd.RunE = cleanupTask
}

type cleanupResult struct {
	TaskID              string `json:"task_id"`
	Policy              string `json:"policy"`
	Status              string `json:"status"`
	Error               string `json:"error,omitempty"`
	WorktreePath        string `json:"worktree_path"`
	BranchPreserved     bool   `json:"branch_preserved"`
	NonForced           bool   `json:"non_forced"`
	WouldRemoveWorktree bool   `json:"would_remove_worktree,omitempty"`
	RemovedWorktree     bool   `json:"removed_worktree,omitempty"`
}

func effectiveWorktreeCleanupPolicy(configDefault, override string) (store.WorktreeCleanupPolicy, error) {
	policy := configDefault
	if policy == "" {
		policy = string(store.CleanupPolicyNever)
	}
	if override != "" {
		policy = override
	}
	return parseWorktreeCleanupPolicy(policy)
}

func parseWorktreeCleanupPolicy(value string) (store.WorktreeCleanupPolicy, error) {
	switch value {
	case string(store.CleanupPolicyNever):
		return store.CleanupPolicyNever, nil
	case string(store.CleanupPolicyOnSuccess):
		return store.CleanupPolicyOnSuccess, nil
	case string(store.CleanupPolicyOnTerminal):
		return store.CleanupPolicyOnTerminal, nil
	default:
		return "", fmt.Errorf("cleanup policy must be one of never, on-success, or on-terminal")
	}
}

func cleanupTask(cmd *cobra.Command, args []string) error {
	results := make([]cleanupResult, 0, len(args))
	var errs []error
	for _, taskID := range args {
		res, err := cleanupOneTask(cmd, taskID)
		if err != nil {
			errs = append(errs, err)
			results = append(results, cleanupResult{TaskID: taskID, Status: "error", Error: err.Error()})
			continue
		}
		results = append(results, res)
	}
	if jsonOut {
		if err := output.JSON(os.Stdout, results); err != nil {
			return err
		}
	}
	return errors.Join(errs...)
}

func cleanupOneTask(cmd *cobra.Command, taskID string) (cleanupResult, error) {
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	task, _, err := taskAndRunReconciled(cmd, taskID, processExists)
	if err != nil {
		return cleanupResult{}, err
	}
	if !isTerminalStatus(task.Status) {
		return cleanupResult{}, fmt.Errorf("refusing to cleanup %s task; wait for it to finish or stop it first", task.Status)
	}
	if dryRun {
		result := cleanupDryRunResult(task)
		if !jsonOut {
			if _, err := fmt.Fprintf(os.Stdout, "Would remove worktree %s for task %s; branch %s would be preserved\n", task.WorktreePath, task.ID, task.Branch); err != nil {
				return cleanupResult{}, err
			}
		}
		return result, nil
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return cleanupResult{}, err
	}
	defer db.Close() //nolint:errcheck
	result, err := performWorktreeCleanup(cmd.Context(), db, task, task.Status, true)
	if err != nil {
		return cleanupResult{}, err
	}
	if !jsonOut {
		if result.Status == string(store.CleanupStatusRemoved) {
			if _, err := fmt.Fprintf(os.Stdout, "Removed worktree %s for task %s; branch %s preserved\n", task.WorktreePath, task.ID, task.Branch); err != nil {
				return cleanupResult{}, err
			}
		} else {
			if _, err := fmt.Fprintf(os.Stdout, "Worktree cleanup %s for task %s: %s\n", result.Status, task.ID, result.Error); err != nil {
				return cleanupResult{}, err
			}
		}
	}
	return result, nil
}

func cleanupDryRunResult(task store.Task) cleanupResult {
	return cleanupResult{TaskID: task.ID, Policy: string(task.WorktreeCleanupPolicy), Status: "dry_run", WorktreePath: task.WorktreePath, BranchPreserved: true, NonForced: true, WouldRemoveWorktree: true}
}

func performWorktreeCleanup(ctx context.Context, db *store.Store, task store.Task, terminalStatus store.TaskStatus, manual bool) (cleanupResult, error) {
	result := cleanupResult{TaskID: task.ID, Policy: string(task.WorktreeCleanupPolicy), WorktreePath: task.WorktreePath, BranchPreserved: true, NonForced: true}
	eligible, status, reason := cleanupEligibility(task, terminalStatus, manual)
	if !eligible {
		result.Status = string(status)
		result.Error = reason
		if status != store.CleanupStatusNotRequested {
			if err := db.RecordWorktreeCleanup(ctx, task.ID, status, reason, false); err != nil {
				return result, err
			}
		}
		return result, nil
	}
	wt, err := newWorktreeClient()
	if err != nil {
		result.Status = string(store.CleanupStatusFailed)
		result.Error = err.Error()
		if recordErr := db.RecordWorktreeCleanup(ctx, task.ID, store.CleanupStatusFailed, err.Error(), false); recordErr != nil {
			return result, recordErr
		}
		return result, nil
	}
	if err := wt.Remove(task.RepoPath, task.Branch); err != nil {
		result.Status = string(store.CleanupStatusFailed)
		result.Error = err.Error()
		if recordErr := db.RecordWorktreeCleanup(ctx, task.ID, store.CleanupStatusFailed, err.Error(), false); recordErr != nil {
			return result, recordErr
		}
		return result, nil
	}
	result.Status = string(store.CleanupStatusRemoved)
	result.RemovedWorktree = true
	return result, db.RecordWorktreeCleanup(ctx, task.ID, store.CleanupStatusRemoved, "", true)
}

func cleanupEligibility(task store.Task, terminalStatus store.TaskStatus, manual bool) (bool, store.WorktreeCleanupStatus, string) {
	if manual {
		return true, "", ""
	}
	switch task.WorktreeCleanupPolicy {
	case store.CleanupPolicyNever, "":
		return false, store.CleanupStatusNotRequested, "cleanup policy is never"
	case store.CleanupPolicyOnSuccess:
		if terminalStatus != store.StatusSucceeded {
			return false, store.CleanupStatusKept, fmt.Sprintf("cleanup policy on-success does not match terminal status %s", terminalStatus)
		}
	case store.CleanupPolicyOnTerminal:
		if !isTerminalStatus(terminalStatus) || terminalStatus == store.StatusUnknown {
			return false, store.CleanupStatusKept, fmt.Sprintf("cleanup policy on-terminal does not match status %s", terminalStatus)
		}
	default:
		return false, store.CleanupStatusFailed, fmt.Sprintf("unknown cleanup policy %s", task.WorktreeCleanupPolicy)
	}
	if !task.WorktreeCreatedByPD {
		return false, store.CleanupStatusSkipped, "worktree was not created by this pd run"
	}
	return true, "", ""
}

func runPostTerminalCleanup(ctx context.Context, db *store.Store, taskID string, terminalStatus store.TaskStatus) {
	task, err := db.GetTask(ctx, taskID)
	if err != nil {
		return
	}
	_, _ = performWorktreeCleanup(ctx, db, task, terminalStatus, false)
}

func recordSkippedPostTerminalCleanup(ctx context.Context, db *store.Store, taskID, reason string) {
	task, err := db.GetTask(ctx, taskID)
	if err != nil || task.WorktreeCleanupPolicy == store.CleanupPolicyNever || task.WorktreeCleanupPolicy == "" {
		return
	}
	_ = db.RecordWorktreeCleanup(ctx, taskID, store.CleanupStatusSkipped, reason, false)
}
