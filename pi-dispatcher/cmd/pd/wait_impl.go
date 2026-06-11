package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/output"
	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/store"
	"github.com/spf13/cobra"
)

var waitPollInterval = time.Second

func init() {
	waitCmd.RunE = waitForTask
}

func waitForTask(cmd *cobra.Command, args []string) error {
	taskID := args[0]
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
		view, err := waitStatusView(ctx, taskID)
		if err != nil {
			if timeout > 0 && (errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded)) {
				return waitTimeoutError(taskID, timeout)
			}
			return err
		}
		status := store.TaskStatus(view.Task.Status)
		if isTerminalStatus(status) {
			if err := printWaitResult(view); err != nil {
				return err
			}
			if status != store.StatusSucceeded {
				return fmt.Errorf("task %s %s", taskID, status)
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
				return waitTimeoutError(taskID, timeout)
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func waitTimeoutError(taskID string, timeout time.Duration) error {
	return fmt.Errorf("timed out waiting for task %s after %s", taskID, timeout)
}

func waitStatusView(ctx context.Context, taskID string) (statusView, error) {
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return statusView{}, err
	}
	defer db.Close() //nolint:errcheck
	task, err := db.GetTask(ctx, taskID)
	if err != nil {
		return statusView{}, taskLookupError(taskID, err)
	}
	run, err := db.LatestRun(ctx, taskID)
	if err != nil {
		return statusView{}, runLookupError(taskID, err)
	}
	task, err = reconcileTask(ctx, db, task, run, processExists)
	if err != nil {
		return statusView{}, err
	}
	if store.TaskStatus(run.Status) != task.Status {
		run.Status = task.Status
	}
	rv := viewRun(run)
	return statusView{Task: viewTask(task), Run: &rv}, nil
}

func printWaitResult(view statusView) error {
	if jsonOut {
		return output.JSON(os.Stdout, view)
	}
	_, err := fmt.Fprintf(os.Stdout, "%s %s\n", view.Task.ID, view.Task.Status)
	return err
}

func isTerminalStatus(status store.TaskStatus) bool {
	switch status {
	case store.StatusSucceeded, store.StatusFailed, store.StatusStopped, store.StatusUnknown:
		return true
	default:
		return false
	}
}
