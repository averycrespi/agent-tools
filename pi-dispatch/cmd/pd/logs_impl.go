package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	pdconfig "github.com/averycrespi/agent-tools/pi-dispatch/internal/config"
	"github.com/averycrespi/agent-tools/pi-dispatch/internal/control"
	"github.com/averycrespi/agent-tools/pi-dispatch/internal/output"
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
		return followFiles(logFollowTarget{label: "stdout", path: run.StdoutLogPath}, logFollowTarget{label: "stderr", path: run.StderrLogPath})
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
		return followFiles(logFollowTarget{label: "stdout", path: run.StdoutLogPath}, logFollowTarget{label: "stderr", path: run.StderrLogPath})
	}
	_, err = fmt.Fprintf(os.Stdout, "Task is not running. Use `pd logs %s` or `pd events %s` for persisted output.\n", task.ID, task.ID)
	return err
}

type removeResult struct {
	TaskID          string `json:"task_id"`
	Removed         bool   `json:"removed"`
	WorktreeRemoved bool   `json:"worktree_removed"`
}

func removeTask(cmd *cobra.Command, args []string) error {
	removeWorktree, _ := cmd.Flags().GetBool("worktree")
	task, run, err := taskAndRunReconciled(cmd, args[0], pdprocess.Exists)
	if err != nil {
		return err
	}
	if task.Status == store.StatusRunning || task.Status == store.StatusStopping || task.Status == store.StatusStarting {
		return fmt.Errorf("refusing to remove %s task; stop it first", task.Status)
	}
	if removeWorktree {
		wt, err := newWorktreeClient()
		if err != nil {
			return err
		}
		if err := wt.Remove(task.RepoPath, task.Branch); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(pdconfig.TaskDir(task.ID)); err != nil {
		return err
	}
	if run.ControlSocketPath != "" {
		if err := os.Remove(run.ControlSocketPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck
	if err := db.DeleteTask(cmd.Context(), task.ID); err != nil {
		return err
	}
	if jsonOut {
		return output.JSON(os.Stdout, removeResult{TaskID: task.ID, Removed: true, WorktreeRemoved: removeWorktree})
	}
	_, err = fmt.Fprintf(os.Stdout, "Removed task %s\n", task.ID)
	return err
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

type controlResult struct {
	TaskID    string `json:"task_id"`
	Operation string `json:"operation"`
	OK        bool   `json:"ok"`
}

var sendControlRequest = control.Send

func sendControl(cmd *cobra.Command, taskID string, req control.Request) error {
	task, run, err := taskAndRunReconciled(cmd, taskID, pdprocess.Exists)
	if err != nil {
		return err
	}
	if err := controlAllowed(task.Status, req); err != nil {
		return fmt.Errorf("cannot %s task %s: %w", req.Operation, task.ID, err)
	}
	_, err = sendControlRequest(run.ControlSocketPath, req)
	if err != nil {
		return err
	}
	if jsonOut {
		return output.JSON(os.Stdout, controlResult{TaskID: task.ID, Operation: string(req.Operation), OK: true})
	}
	return nil
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
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := db.GetTask(ctx, taskID); err != nil {
		return store.Run{}, taskLookupError(taskID, err)
	}
	run, err := db.LatestRun(ctx, taskID)
	if err != nil {
		return store.Run{}, runLookupError(taskID, err)
	}
	return run, nil
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
		return store.Task{}, store.Run{}, taskLookupError(taskID, err)
	}
	run, err := db.LatestRun(ctx, taskID)
	if err != nil {
		return store.Task{}, store.Run{}, runLookupError(taskID, err)
	}
	task, err = reconcileTask(ctx, db, task, run, pidExists)
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

type logFollowTarget struct {
	label string
	path  string
}

func followFiles(targets ...logFollowTarget) error {
	errs := make(chan error, len(targets))
	var writerMu sync.Mutex
	for _, target := range targets {
		go func() {
			errs <- followFilePrefixed(target, &writerMu)
		}()
	}
	return <-errs
}

func followFilePrefixed(target logFollowTarget, writerMu *sync.Mutex) error {
	var offset int64
	for {
		f, err := os.Open(target.path) //nolint:gosec
		if err == nil {
			if _, err := f.Seek(offset, io.SeekStart); err == nil {
				scanner := bufio.NewScanner(f)
				for scanner.Scan() {
					line := scanner.Text()
					writerMu.Lock()
					_, writeErr := fmt.Fprintf(os.Stdout, "%s: %s\n", target.label, line)
					writerMu.Unlock()
					if writeErr != nil {
						_ = f.Close()
						return writeErr
					}
				}
				if err := scanner.Err(); err != nil {
					_ = f.Close()
					return err
				}
				pos, err := f.Seek(0, io.SeekCurrent)
				if err == nil {
					offset = pos
				}
			}
			_ = f.Close()
		} else if !os.IsNotExist(err) {
			return err
		}
		time.Sleep(time.Second)
	}
}
