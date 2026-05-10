package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/averycrespi/agent-tools/pi-dispatch/internal/control"
	"github.com/averycrespi/agent-tools/pi-dispatch/internal/output"
	pdprocess "github.com/averycrespi/agent-tools/pi-dispatch/internal/process"
	"github.com/averycrespi/agent-tools/pi-dispatch/internal/store"
	"github.com/spf13/cobra"
)

type taskView struct {
	ID           string    `json:"id"`
	Status       string    `json:"status"`
	RepoName     string    `json:"repo_name"`
	Branch       string    `json:"branch"`
	WorktreePath string    `json:"worktree_path"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type statusView struct {
	Task taskView `json:"task"`
	Run  *runView `json:"run,omitempty"`
}

type runView struct {
	ID                string     `json:"id"`
	Status            string     `json:"status"`
	Attempt           int        `json:"attempt"`
	EndedAt           *time.Time `json:"ended_at,omitempty"`
	ExitCode          *int       `json:"exit_code,omitempty"`
	ErrorMessage      string     `json:"error_message,omitempty"`
	PiSessionFile     string     `json:"pi_session_file,omitempty"`
	ControlSocketPath string     `json:"control_socket_path"`
	StdoutLogPath     string     `json:"stdout_log_path"`
	StderrLogPath     string     `json:"stderr_log_path"`
	PiEventsPath      string     `json:"pi_events_path"`
}

func listTasks(cmd *cobra.Command, _ []string) error {
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck
	tasks, err := db.ListTasks(cmd.Context())
	if err != nil {
		return err
	}
	views := make([]taskView, 0, len(tasks))
	for _, task := range tasks {
		if run, err := db.LatestRun(cmd.Context(), task.ID); err == nil {
			if reconciled, err := reconcileTask(cmd.Context(), db, task, run, pdprocess.Exists); err == nil {
				task = reconciled
			}
		}
		views = append(views, viewTask(task))
	}
	if jsonOut {
		return output.JSON(os.Stdout, views)
	}
	for _, task := range views {
		if _, err := fmt.Fprintf(os.Stdout, "%s\t%s\t%s\t%s\n", task.ID, task.Status, task.RepoName, task.Branch); err != nil {
			return err
		}
	}
	return nil
}

func showStatus(cmd *cobra.Command, args []string) error {
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck
	task, err := db.GetTask(cmd.Context(), args[0])
	if err != nil {
		return err
	}
	run, err := db.LatestRun(cmd.Context(), args[0])
	var rv *runView
	if err == nil {
		if reconciled, err := reconcileTask(cmd.Context(), db, task, run, pdprocess.Exists); err == nil {
			task = reconciled
		}
		view := viewRun(run)
		rv = &view
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	view := statusView{Task: viewTask(task), Run: rv}
	if jsonOut {
		return output.JSON(os.Stdout, view)
	}
	if _, err := fmt.Fprintf(os.Stdout, "Task:    %s\nStatus:  %s\nRepo:    %s\nBranch:  %s\nWorktree:%s\n", view.Task.ID, view.Task.Status, view.Task.RepoName, view.Task.Branch, view.Task.WorktreePath); err != nil {
		return err
	}
	if view.Run != nil {
		if _, err := fmt.Fprintf(os.Stdout, "Logs:    %s\nEvents:  %s\n", view.Run.StdoutLogPath, view.Run.PiEventsPath); err != nil {
			return err
		}
		if view.Run.EndedAt != nil {
			if _, err := fmt.Fprintf(os.Stdout, "Ended:   %s\n", view.Run.EndedAt.Format(time.RFC3339)); err != nil {
				return err
			}
		}
		if view.Run.ExitCode != nil {
			if _, err := fmt.Fprintf(os.Stdout, "Exit:    %d\n", *view.Run.ExitCode); err != nil {
				return err
			}
		}
		if view.Run.ErrorMessage != "" {
			if _, err := fmt.Fprintf(os.Stdout, "Error:   %s\n", view.Run.ErrorMessage); err != nil {
				return err
			}
		}
		if view.Run.PiSessionFile != "" {
			if _, err := fmt.Fprintf(os.Stdout, "Session: %s\n", view.Run.PiSessionFile); err != nil {
				return err
			}
		}
	}
	return nil
}

func showEvents(cmd *cobra.Command, args []string) error {
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck
	events, err := db.ListEvents(cmd.Context(), args[0])
	if err != nil {
		return err
	}
	if jsonOut {
		return output.JSON(os.Stdout, events)
	}
	for _, event := range events {
		if _, err := fmt.Fprintf(os.Stdout, "%s\t%s\t%s\n", event.Timestamp.Format(time.RFC3339), event.Type, event.Message); err != nil {
			return err
		}
	}
	return nil
}

const startingPingGrace = 3 * time.Second

func reconcileTask(ctx context.Context, db *store.Store, task store.Task, run store.Run, pidExists func(int) bool) (store.Task, error) {
	if task.Status != store.StatusRunning && task.Status != store.StatusStarting && task.Status != store.StatusStopping {
		return task, nil
	}
	if run.SupervisorPID > 0 && pidExists(run.SupervisorPID) {
		if task.Status == store.StatusStarting && time.Since(run.StartedAt) < startingPingGrace {
			return task, nil
		}
		if _, err := sendControlRequest(run.ControlSocketPath, control.Request{Operation: control.OpPing}); err == nil {
			return task, nil
		}
	}
	if err := db.MarkUnknown(ctx, task.ID); err != nil {
		return task, err
	}
	task.Status = store.StatusUnknown
	return task, nil
}

func viewTask(task store.Task) taskView {
	return taskView{ID: task.ID, Status: string(task.Status), RepoName: task.RepoName, Branch: task.Branch, WorktreePath: task.WorktreePath, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt}
}

func viewRun(run store.Run) runView {
	view := runView{ID: run.ID, Status: string(run.Status), Attempt: run.Attempt, ErrorMessage: run.ErrorMessage, PiSessionFile: run.PiSessionFile, ControlSocketPath: run.ControlSocketPath, StdoutLogPath: run.StdoutLogPath, StderrLogPath: run.StderrLogPath, PiEventsPath: run.PiEventsPath}
	if run.EndedAt.Valid {
		endedAt := run.EndedAt.Time
		view.EndedAt = &endedAt
	}
	if run.ExitCode.Valid {
		exitCode := int(run.ExitCode.Int64)
		view.ExitCode = &exitCode
	}
	return view
}
