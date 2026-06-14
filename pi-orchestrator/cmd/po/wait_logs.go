package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/store"
	"github.com/spf13/cobra"
)

var waitPollInterval = time.Second

var waitCmd = &cobra.Command{
	Use:   "wait <run-id>",
	Short: "Wait for a workflow run to finish",
	Args:  requireRunArg("po wait <run-id>"),
	RunE:  waitForWorkflowRun,
}

var logsCmd = &cobra.Command{
	Use:   "logs <run-id>",
	Short: "Show workflow supervisor logs and backing pd log pointers",
	Args:  requireRunArg("po logs <run-id>"),
	RunE:  showWorkflowRunLogs,
}

func init() {
	waitCmd.Flags().Duration("timeout", 0, "maximum time to wait, such as 30s, 5m, or 1h; 0 waits forever")
}

func waitForWorkflowRun(cmd *cobra.Command, args []string) error {
	runID := args[0]
	timeout, _ := cmd.Flags().GetDuration("timeout")
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	for {
		run, err := getWorkflowRun(ctx, runID)
		if err != nil {
			if timeout > 0 && (errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded)) {
				return fmt.Errorf("timed out waiting for workflow run %s after %s", runID, timeout)
			}
			return err
		}
		if isTerminalState(run.State) {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", run.ID, run.State); err != nil {
				return err
			}
			if run.State != store.StateSucceeded {
				return fmt.Errorf("workflow run %s %s", run.ID, run.State)
			}
			return nil
		}
		timer := time.NewTimer(waitPollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("timed out waiting for workflow run %s after %s", runID, timeout)
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func showWorkflowRunLogs(cmd *cobra.Command, args []string) error {
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
	if detail.Run.SupervisorLogPath != "" {
		file, err := os.Open(detail.Run.SupervisorLogPath) //nolint:gosec
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read supervisor log: %w", err)
		}
		if file != nil {
			if _, err := io.Copy(cmd.OutOrStdout(), file); err != nil {
				_ = file.Close()
				return err
			}
			if err := file.Close(); err != nil {
				return err
			}
		}
	}
	if len(detail.Steps) > 0 {
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), "Backing pd logs:"); err != nil {
			return err
		}
		for _, step := range detail.Steps {
			if step.PDTaskID == "" && step.PDRunID == "" {
				continue
			}
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\tpd_task=%s\tpd_run=%s\tstdout=%s\tstderr=%s\tevents=%s\tpointer=\"pd logs %s\"\n", step.StepID, step.PDTaskID, step.PDRunID, step.PDStdoutPath, step.PDStderrPath, step.PDEventsPath, step.PDTaskID); err != nil {
				return err
			}
		}
	}
	return nil
}

func getWorkflowRun(ctx context.Context, runID string) (store.WorkflowRun, error) {
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return store.WorkflowRun{}, err
	}
	defer db.Close() //nolint:errcheck
	run, err := db.GetWorkflowRun(ctx, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.WorkflowRun{}, fmt.Errorf("workflow run %s not found", runID)
		}
		return store.WorkflowRun{}, err
	}
	return reconcileWorkflowRun(ctx, db, run)
}

func isTerminalState(state store.State) bool {
	switch state {
	case store.StateSucceeded, store.StateFailed, store.StateStopped, store.StateUnknown:
		return true
	default:
		return false
	}
}
