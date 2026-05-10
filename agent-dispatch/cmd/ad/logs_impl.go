package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/averycrespi/agent-tools/agent-dispatch/internal/store"
	"github.com/spf13/cobra"
)

func init() {
	logsCmd.RunE = showLogs
	attachCmd.RunE = attachTask
	rmCmd.RunE = removeTask
	steerCmd.RunE = controlNotReady("steer")
	followupCmd.RunE = controlNotReady("followup")
	followUpCmd.RunE = controlNotReady("follow-up")
	stopCmd.RunE = controlNotReady("stop")
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
	task, run, err := taskAndRun(cmd, args[0])
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "Task %s [%s]\nLogs: %s\nEvents: %s\n", task.ID, task.Status, run.StdoutLogPath, run.PiEventsPath)
	if task.Status == store.StatusRunning || task.Status == store.StatusStarting {
		return followFile(run.StdoutLogPath)
	}
	fmt.Fprintf(os.Stdout, "Task is not running. Use `ad logs %s` or `ad events %s` for persisted output.\n", task.ID, task.ID)
	return nil
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

func controlNotReady(name string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		task, _, err := taskAndRun(cmd, args[0])
		if err != nil {
			return err
		}
		if task.Status != store.StatusRunning {
			return fmt.Errorf("cannot %s task %s: task is %s, not running", name, task.ID, task.Status)
		}
		return fmt.Errorf("%s control channel is not implemented yet", name)
	}
}

func latestRun(cmd *cobra.Command, taskID string) (store.Run, error) {
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return store.Run{}, err
	}
	defer db.Close() //nolint:errcheck
	return db.LatestRun(cmd.Context(), taskID)
}

func taskAndRun(cmd *cobra.Command, taskID string) (store.Task, store.Run, error) {
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return store.Task{}, store.Run{}, err
	}
	defer db.Close() //nolint:errcheck
	task, err := db.GetTask(cmd.Context(), taskID)
	if err != nil {
		return store.Task{}, store.Run{}, err
	}
	run, err := db.LatestRun(cmd.Context(), taskID)
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
