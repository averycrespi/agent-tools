package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/averycrespi/agent-tools/pi-dispatch/internal/control"
	pdprocess "github.com/averycrespi/agent-tools/pi-dispatch/internal/process"
	"github.com/averycrespi/agent-tools/pi-dispatch/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	logsCmd.RunE = showLogs
	attachCmd.RunE = attachTask
	rmCmd.RunE = removeTask
	steerCmd.RunE = sendSteer
	followupCmd.RunE = sendFollowUp
	followUpCmd.RunE = sendFollowUp
	stopCmd.RunE = sendStop
}

func showLogs(cmd *cobra.Command, args []string) error {
	run, err := latestRun(cmd, args[0])
	if err != nil {
		return err
	}
	follow, _ := cmd.Flags().GetBool("follow")
	if err := printFile(run.StdoutLogPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := printFile(run.StderrLogPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	if follow {
		return followFile(run.StdoutLogPath)
	}
	return nil
}

func attachTask(cmd *cobra.Command, args []string) error {
	task, run, err := taskAndRunReconciled(cmd, args[0], pdprocess.Exists)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(os.Stdout, "Task %s [%s]\nLogs: %s\nEvents: %s\n", task.ID, task.Status, run.StdoutLogPath, run.PiEventsPath); err != nil {
		return err
	}
	if task.Status == store.StatusRunning || task.Status == store.StatusStarting {
		return followFile(run.StdoutLogPath)
	}
	_, err = fmt.Fprintf(os.Stdout, "Task is not running. Use `pd logs %s` or `pd events %s` for persisted output.\n", task.ID, task.ID)
	return err
}

func removeTask(cmd *cobra.Command, args []string) error {
	task, _, err := taskAndRun(cmd, args[0])
	if err != nil {
		return err
	}
	if task.Status == store.StatusRunning || task.Status == store.StatusStopping || task.Status == store.StatusStarting {
		return fmt.Errorf("refusing to remove %s task; stop it first", task.Status)
	}
	return fmt.Errorf("rm storage deletion is not implemented yet")
}

func sendSteer(cmd *cobra.Command, args []string) error {
	return sendControl(cmd, args[0], control.Request{Operation: control.OpSteer, Message: args[1]})
}

func sendFollowUp(cmd *cobra.Command, args []string) error {
	return sendControl(cmd, args[0], control.Request{Operation: control.OpFollowUp, Message: args[1]})
}

func sendStop(cmd *cobra.Command, args []string) error {
	force, _ := cmd.Flags().GetBool("force")
	return sendControl(cmd, args[0], control.Request{Operation: control.OpStop, Force: force})
}

func sendControl(cmd *cobra.Command, taskID string, req control.Request) error {
	task, run, err := taskAndRunReconciled(cmd, taskID, pdprocess.Exists)
	if err != nil {
		return err
	}
	if err := controlAllowed(task.Status, req); err != nil {
		return fmt.Errorf("cannot %s task %s: %w", req.Operation, task.ID, err)
	}
	_, err = control.Send(run.ControlSocketPath, req)
	return err
}

func controlAllowed(status store.TaskStatus, req control.Request) error {
	if status == store.StatusRunning {
		return nil
	}
	if req.Operation == control.OpStop && req.Force && status == store.StatusStopping {
		return nil
	}
	return fmt.Errorf("task is %s, not running", status)
}

func latestRun(cmd *cobra.Command, taskID string) (store.Run, error) {
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return store.Run{}, err
	}
	defer db.Close() //nolint:errcheck
	return db.LatestRun(cmd.Context(), taskID)
}

func taskAndRunReconciled(cmd *cobra.Command, taskID string, pidExists func(int) bool) (store.Task, store.Run, error) {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return store.Task{}, store.Run{}, err
	}
	defer db.Close() //nolint:errcheck
	task, err := db.GetTask(ctx, taskID)
	if err != nil {
		return store.Task{}, store.Run{}, err
	}
	run, err := db.LatestRun(ctx, taskID)
	if err != nil {
		return store.Task{}, store.Run{}, err
	}
	task, err = reconcileTask(ctx, db, task, run, pidExists)
	if err != nil {
		return store.Task{}, store.Run{}, err
	}
	return task, run, nil
}

func taskAndRun(cmd *cobra.Command, taskID string) (store.Task, store.Run, error) {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return store.Task{}, store.Run{}, err
	}
	defer db.Close() //nolint:errcheck
	task, err := db.GetTask(ctx, taskID)
	if err != nil {
		return store.Task{}, store.Run{}, err
	}
	run, err := db.LatestRun(ctx, taskID)
	if err != nil {
		return store.Task{}, store.Run{}, err
	}
	return task, run, nil
}

func printFile(path string) error {
	f, err := os.Open(path) //nolint:gosec
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck
	_, err = io.Copy(os.Stdout, f)
	return err
}

func followFile(path string) error {
	var offset int64
	for {
		f, err := os.Open(path) //nolint:gosec
		if err == nil {
			if _, err := f.Seek(offset, io.SeekStart); err == nil {
				n, copyErr := io.Copy(os.Stdout, f)
				offset += n
				if copyErr != nil {
					_ = f.Close()
					return copyErr
				}
			}
			_ = f.Close()
		} else if !os.IsNotExist(err) {
			return err
		}
		time.Sleep(time.Second)
	}
}
