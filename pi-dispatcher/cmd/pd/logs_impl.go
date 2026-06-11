package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	pdconfig "github.com/averycrespi/agent-tools/pi-dispatcher/internal/config"
	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/control"
	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/output"
	pdprocess "github.com/averycrespi/agent-tools/pi-dispatcher/internal/process"
	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	logsCmd.RunE = showLogs
	rmCmd.RunE = removeTask
	stopCmd.RunE = sendStop
}

func showLogs(cmd *cobra.Command, args []string) error {
	follow, _ := cmd.Flags().GetBool("follow")
	taskID := args[0]
	var task store.Task
	var run store.Run
	var err error
	if follow {
		task, run, err = taskAndRunReconciled(cmd, taskID, processExists)
	} else {
		run, err = latestRun(cmd, taskID)
	}
	if err != nil {
		return err
	}
	if follow {
		if _, err := fmt.Fprintf(os.Stdout, "Task %s [%s]\nLogs: %s\nRaw Pi events: %s\n", task.ID, task.Status, run.StdoutLogPath, run.PiEventsPath); err != nil {
			return err
		}
	}
	if err := printFile(run.StdoutLogPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := printFile(run.StderrLogPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	if follow {
		return followLogFiles(logFollowTarget{label: "stdout", path: run.StdoutLogPath}, logFollowTarget{label: "stderr", path: run.StderrLogPath})
	}
	return nil
}

type removeResult struct {
	TaskID  string `json:"task_id"`
	Removed bool   `json:"removed"`
}

func removeTask(cmd *cobra.Command, args []string) error {
	task, run, err := taskAndRunReconciled(cmd, args[0], processExists)
	if err != nil {
		return err
	}
	if task.Status == store.StatusRunning || task.Status == store.StatusStopping || task.Status == store.StatusStarting {
		return fmt.Errorf("refusing to remove %s task; stop it first", task.Status)
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
		return output.JSON(os.Stdout, removeResult{TaskID: task.ID, Removed: true})
	}
	_, err = fmt.Fprintf(os.Stdout, "Removed task %s\n", task.ID)
	return err
}

func sendStop(cmd *cobra.Command, args []string) error {
	force, _ := cmd.Flags().GetBool("force")
	if force {
		return forceStopTask(cmd, args[0])
	}
	return sendControl(cmd, args[0], control.Request{Operation: control.OpStop})
}

type controlResult struct {
	TaskID    string `json:"task_id"`
	Operation string `json:"operation"`
	OK        bool   `json:"ok"`
}

var (
	sendControlRequest       = control.Send
	processExists            = pdprocess.Exists
	killProcessGroup         = pdprocess.KillGroup
	followLogFiles           = followFiles
	forceStopEscalationGrace = 3 * time.Second
	forceStopKillWait        = 3 * time.Second
	forceStopPollInterval    = 100 * time.Millisecond
)

func forceStopTask(cmd *cobra.Command, taskID string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	task, run, err := taskAndRunReconciled(cmd, taskID, processExists)
	if err != nil {
		return err
	}
	req := control.Request{Operation: control.OpStop, Force: true}
	if err := controlAllowed(task.Status, req); err != nil {
		return fmt.Errorf("cannot %s task %s: %w", req.Operation, task.ID, err)
	}
	if run.ControlSocketPath != "" {
		_, _ = sendControlRequest(run.ControlSocketPath, req)
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck
	if waitForTerminalStatus(ctx, db, taskID, forceStopEscalationGrace) {
		return printForceStopResult(taskID)
	}
	processExited := true
	if run.SupervisorPID > 0 {
		_ = killProcessGroup(run.SupervisorPID)
		processExited = waitForProcessExit(ctx, run.SupervisorPID, forceStopKillWait)
	}
	if err := db.CompleteRun(ctx, taskID, store.StatusStopped, 0, "force-killed by pd stop --force", ""); err != nil {
		return err
	}
	if processExited {
		runPostTerminalCleanup(ctx, db, taskID, store.StatusStopped)
	} else {
		recordSkippedPostTerminalCleanup(ctx, db, taskID, "supervisor process still running after force kill")
	}
	return printForceStopResult(taskID)
}

func waitForProcessExit(ctx context.Context, pid int, grace time.Duration) bool {
	deadline := time.Now().Add(grace)
	for {
		if !processExists(pid) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(forceStopPollInterval):
		}
	}
}

func waitForTerminalStatus(ctx context.Context, db *store.Store, taskID string, grace time.Duration) bool {
	deadline := time.Now().Add(grace)
	for {
		task, err := db.GetTask(ctx, taskID)
		if err == nil && isTerminalStatus(task.Status) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(forceStopPollInterval)
	}
}

func printForceStopResult(taskID string) error {
	if jsonOut {
		return output.JSON(os.Stdout, controlResult{TaskID: taskID, Operation: string(control.OpStop), OK: true})
	}
	return nil
}

func sendControl(cmd *cobra.Command, taskID string, req control.Request) error {
	task, run, err := taskAndRunReconciled(cmd, taskID, processExists)
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
