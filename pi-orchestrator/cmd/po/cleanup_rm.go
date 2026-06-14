package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/store"
	wtworktree "github.com/averycrespi/agent-tools/worktree-manager/pkg/worktree"
	"github.com/spf13/cobra"
)

var (
	cleanupDryRun bool
	cleanupAll    bool
	rmAll         bool
)

type worktreeRemover interface {
	Remove(repoRoot, branch string) error
}

var newWorktreeRemover = func() (worktreeRemover, error) {
	return wtworktree.New()
}

var cleanupCmd = &cobra.Command{
	Use:   "cleanup [--all|<run-id>...]",
	Short: "Remove terminal workflow worktree and artifacts",
	Args:  requireRunArgsOrAll("all", "po cleanup [--all|<run-id>...]"),
	RunE:  cleanupWorkflowRun,
}

var rmCmd = &cobra.Command{
	Use:   "rm [--all|<run-id>...]",
	Short: "Forget terminal workflow metadata",
	Args:  requireRunArgsOrAll("all", "po rm [--all|<run-id>...]"),
	RunE:  removeWorkflowRunMetadata,
}

func init() {
	cleanupCmd.Flags().BoolVar(&cleanupDryRun, "dry-run", false, "show cleanup targets without removing them")
	cleanupCmd.Flags().BoolVar(&cleanupAll, "all", false, "cleanup all terminal workflow runs")
	rmCmd.Flags().BoolVar(&rmAll, "all", false, "remove metadata for all terminal workflow runs")
}

func cleanupWorkflowRun(cmd *cobra.Command, args []string) error {
	if cleanupAll {
		runIDs, err := terminalWorkflowRunIDs(cmd.Context())
		if err != nil {
			return err
		}
		args = runIDs
	}
	var errs []error
	for _, runID := range args {
		if err := cleanupOneWorkflowRun(cmd, runID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func cleanupOneWorkflowRun(cmd *cobra.Command, runID string) error {
	run, err := getWorkflowRun(cmd.Context(), runID)
	if err != nil {
		return err
	}
	if !isTerminalState(run.State) {
		return fmt.Errorf("workflow run %s is not terminal", run.ID)
	}
	if cleanupDryRun {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\nWorktree:\t%s\nArtifacts:\t%s\n", run.ID, run.WorktreePath, run.ArtifactRoot)
		return err
	}
	if run.ArtifactRoot != "" {
		if err := validateArtifactRootForCleanup(run.ArtifactRoot); err != nil {
			_ = recordWorkflowCleanup(cmd.Context(), run.ID, "failed", err.Error())
			return err
		}
	}
	if run.WorktreePath != "" {
		remover, err := newWorktreeRemover()
		if err != nil {
			_ = recordWorkflowCleanup(cmd.Context(), run.ID, "failed", err.Error())
			return err
		}
		if err := remover.Remove(run.Repo, run.Branch); err != nil {
			wrapped := fmt.Errorf("remove workflow worktree %s: %w", run.WorktreePath, err)
			_ = recordWorkflowCleanup(cmd.Context(), run.ID, "failed", wrapped.Error())
			return wrapped
		}
	}
	if run.ArtifactRoot != "" {
		if err := os.RemoveAll(run.ArtifactRoot); err != nil {
			wrapped := fmt.Errorf("remove artifacts %s: %w", run.ArtifactRoot, err)
			_ = recordWorkflowCleanup(cmd.Context(), run.ID, "failed", wrapped.Error())
			return wrapped
		}
		if err := markRunArtifactsRemoved(cmd.Context(), run.ID); err != nil {
			_ = recordWorkflowCleanup(cmd.Context(), run.ID, "failed", err.Error())
			return err
		}
	}
	if err := recordWorkflowCleanup(cmd.Context(), run.ID, "removed", ""); err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s cleaned up\n", run.ID)
	return err
}

func validateArtifactRootForCleanup(artifactRoot string) error {
	absParent, err := filepath.Abs(cfg.ArtifactParentDir)
	if err != nil {
		return fmt.Errorf("resolve artifact parent: %w", err)
	}
	absRoot, err := filepath.Abs(artifactRoot)
	if err != nil {
		return fmt.Errorf("resolve artifact root: %w", err)
	}
	rel, err := filepath.Rel(absParent, absRoot)
	if err != nil {
		return fmt.Errorf("compare artifact root and parent: %w", err)
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return fmt.Errorf("artifact root %s is outside configured artifact parent %s", artifactRoot, cfg.ArtifactParentDir)
	}
	return nil
}

func recordWorkflowCleanup(ctx context.Context, runID string, status string, cleanupErr string) error {
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck
	return db.RecordWorkflowCleanup(ctx, runID, status, cleanupErr, nowFunc().UTC())
}

func markRunArtifactsRemoved(ctx context.Context, runID string) error {
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck
	detail, err := db.GetWorkflowRunDetail(ctx, runID)
	if err != nil {
		return err
	}
	for i := range detail.Artifacts {
		detail.Artifacts[i].Exists = false
		detail.Artifacts[i].UpdatedAt = nowFunc().UTC()
	}
	return db.UpdateArtifactExistence(ctx, detail.Artifacts)
}

func removeWorkflowRunMetadata(cmd *cobra.Command, args []string) error {
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck
	if rmAll {
		runIDs, err := terminalWorkflowRunIDsFromStore(cmd.Context(), db)
		if err != nil {
			return err
		}
		args = runIDs
	}
	var errs []error
	for _, runID := range args {
		if err := removeOneWorkflowRunMetadata(cmd, db, runID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func terminalWorkflowRunIDs(ctx context.Context) ([]string, error) {
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return nil, err
	}
	defer db.Close() //nolint:errcheck
	return terminalWorkflowRunIDsFromStore(ctx, db)
}

func terminalWorkflowRunIDsFromStore(ctx context.Context, db *store.Store) ([]string, error) {
	runs, err := db.ListWorkflowRuns(ctx)
	if err != nil {
		return nil, err
	}
	runIDs := make([]string, 0, len(runs))
	for _, run := range runs {
		if isTerminalState(run.State) {
			runIDs = append(runIDs, run.ID)
		}
	}
	return runIDs, nil
}

func removeOneWorkflowRunMetadata(cmd *cobra.Command, db *store.Store, runID string) error {
	run, err := db.GetWorkflowRun(cmd.Context(), runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("workflow run %s not found", runID)
		}
		return err
	}
	if !isTerminalState(run.State) {
		return fmt.Errorf("workflow run %s is not terminal", run.ID)
	}
	if err := db.DeleteWorkflowRun(cmd.Context(), run.ID); err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s removed\n", run.ID)
	return err
}
