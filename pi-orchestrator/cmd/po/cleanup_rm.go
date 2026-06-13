package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/store"
	wtworktree "github.com/averycrespi/agent-tools/worktree-manager/pkg/worktree"
	"github.com/spf13/cobra"
)

var cleanupDryRun bool

type worktreeRemover interface {
	Remove(repoRoot, branch string) error
}

var newWorktreeRemover = func() (worktreeRemover, error) {
	return wtworktree.New()
}

var cleanupCmd = &cobra.Command{
	Use:   "cleanup <run>",
	Short: "Remove terminal workflow run worktree and artifacts",
	Args:  requireRunArg("po cleanup <run>"),
	RunE:  cleanupWorkflowRun,
}

var rmCmd = &cobra.Command{
	Use:   "rm <run>",
	Short: "Remove terminal workflow run metadata",
	Args:  requireRunArg("po rm <run>"),
	RunE:  removeWorkflowRunMetadata,
}

func init() {
	cleanupCmd.Flags().BoolVar(&cleanupDryRun, "dry-run", false, "show cleanup targets without removing them")
}

func cleanupWorkflowRun(cmd *cobra.Command, args []string) error {
	run, err := getWorkflowRun(cmd.Context(), args[0])
	if err != nil {
		return err
	}
	if !isTerminalState(run.State) {
		return fmt.Errorf("workflow run %s is not terminal", run.ID)
	}
	if cleanupDryRun {
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "Worktree:\t%s\nArtifacts:\t%s\n", run.WorktreePath, run.ArtifactRoot)
		return err
	}
	if run.WorktreePath != "" {
		remover, err := newWorktreeRemover()
		if err != nil {
			return err
		}
		if err := remover.Remove(run.Repo, run.Branch); err != nil {
			return fmt.Errorf("remove workflow worktree %s: %w", run.WorktreePath, err)
		}
	}
	if run.ArtifactRoot != "" {
		if err := os.RemoveAll(run.ArtifactRoot); err != nil {
			return fmt.Errorf("remove artifacts %s: %w", run.ArtifactRoot, err)
		}
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s cleaned up\n", run.ID)
	return err
}

func removeWorkflowRunMetadata(cmd *cobra.Command, args []string) error {
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck
	run, err := db.GetWorkflowRun(cmd.Context(), args[0])
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("workflow run %s not found", args[0])
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
