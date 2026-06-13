package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"text/tabwriter"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/store"
	"github.com/spf13/cobra"
)

var psCmd = &cobra.Command{
	Use:   "ps",
	Short: "List workflow runs",
	Args:  cobra.NoArgs,
	RunE:  listWorkflowRuns,
}

var statusCmd = &cobra.Command{
	Use:   "status <run>",
	Short: "Show workflow run status",
	Args:  requireRunArg("po status <run>"),
	RunE:  showWorkflowRunStatus,
}

func listWorkflowRuns(cmd *cobra.Command, _ []string) error {
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck
	runs, err := db.ListWorkflowRuns(cmd.Context())
	if err != nil {
		return err
	}
	for i := range runs {
		reconciled, err := reconcileWorkflowRun(cmd.Context(), db, runs[i])
		if err != nil {
			return err
		}
		runs[i] = reconciled
	}
	if jsonOut {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(runs)
	}
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	for _, run := range runs {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", run.ID, run.State, run.Workflow, run.Branch); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func showWorkflowRunStatus(cmd *cobra.Command, args []string) error {
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck
	detail, err := db.GetWorkflowRunDetail(cmd.Context(), args[0])
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("workflow run %s not found", args[0])
		}
		return err
	}
	reconciled, err := reconcileWorkflowRun(cmd.Context(), db, detail.Run)
	if err != nil {
		return err
	}
	detail.Run = reconciled
	if jsonOut {
		return json.NewEncoder(cmd.OutOrStdout()).Encode(detail)
	}
	tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	run := detail.Run
	if _, err := fmt.Fprintf(tw, "Workflow run:\t%s\nState:\t%s\nWorkflow:\t%s\nRepo:\t%s\nBranch:\t%s\nWorktree:\t%s\nArtifacts root:\t%s\nSupervisor log:\t%s\n", run.ID, run.State, run.Workflow, run.Repo, run.Branch, run.WorktreePath, run.ArtifactRoot, run.SupervisorLogPath); err != nil {
		return err
	}
	if len(detail.Steps) > 0 {
		if _, err := fmt.Fprintln(tw, "Steps:"); err != nil {
			return err
		}
		for _, step := range detail.Steps {
			if _, err := fmt.Fprintf(tw, "  %s\t%s\tagent=%s\tpd_task=%s\tpd_run=%s\tstdout=%s\tstderr=%s\tevents=%s\n", step.StepID, step.State, step.Agent, step.PDTaskID, step.PDRunID, step.PDStdoutPath, step.PDStderrPath, step.PDEventsPath); err != nil {
				return err
			}
		}
	}
	if len(detail.Artifacts) > 0 {
		if _, err := fmt.Fprintln(tw, "Artifacts:"); err != nil {
			return err
		}
		for _, artifact := range detail.Artifacts {
			if _, err := fmt.Fprintf(tw, "  %s\tstep=%s\trequired=%t\texists=%t\t%s\n", artifact.Name, artifact.StepID, artifact.Required, artifact.Exists, artifact.AbsolutePath); err != nil {
				return err
			}
		}
	}
	return tw.Flush()
}

func requireRunArg(usage string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("run is required\nUsage: %s", usage)
		}
		if len(args) > 1 {
			return fmt.Errorf("expected one run\nUsage: %s", usage)
		}
		return nil
	}
}
