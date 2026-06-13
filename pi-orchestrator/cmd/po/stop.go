package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	pddispatcher "github.com/averycrespi/agent-tools/pi-dispatcher/pkg/dispatcher"
	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/store"
	"github.com/spf13/cobra"
)

var stopCmd = &cobra.Command{
	Use:   "stop <run>",
	Short: "Stop a workflow run",
	Args:  requireRunArg("po stop <run>"),
	RunE:  stopWorkflowRun,
}

var defaultStopPDRun = func(ctx context.Context, taskID string, runID string) error {
	return pddispatcher.NewClient(pddispatcher.Config{}).StopTaskRun(ctx, pddispatcher.StopTaskRunRequest{TaskID: taskID, RunID: runID})
}
var stopPDRun = defaultStopPDRun

func stopWorkflowRun(cmd *cobra.Command, args []string) error {
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
	if isTerminalState(run.State) {
		return fmt.Errorf("workflow run %s is already terminal", run.ID)
	}
	now := time.Now().UTC()
	if err := db.UpdateWorkflowRunState(cmd.Context(), run.ID, store.StateStopping, "stop requested", now); err != nil {
		return err
	}
	step, err := db.RunningStepRun(cmd.Context(), run.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		if step.PDTaskID != "" || step.PDRunID != "" {
			if err := stopPDRun(cmd.Context(), step.PDTaskID, step.PDRunID); err != nil {
				return err
			}
		}
		if err := db.UpdateStepState(cmd.Context(), run.ID, step.StepID, store.StateStopped, "stop requested", now); err != nil {
			return err
		}
	}
	if err := killSupervisor(run.SupervisorPID); err != nil {
		return err
	}
	if err := db.UpdateWorkflowRunState(cmd.Context(), run.ID, store.StateStopped, "stop requested", now); err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s stopped\n", run.ID)
	return err
}
