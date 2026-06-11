package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	pdconfig "github.com/averycrespi/agent-tools/pi-dispatcher/internal/config"
	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/control"
	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/output"
	pdprocess "github.com/averycrespi/agent-tools/pi-dispatcher/internal/process"
	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/store"
	"github.com/spf13/cobra"
)

type taskView struct {
	ID                         string     `json:"id"`
	Status                     string     `json:"status"`
	RepoName                   string     `json:"repo_name"`
	Branch                     string     `json:"branch"`
	WorktreePath               string     `json:"worktree_path"`
	WorktreeCleanupPolicy      string     `json:"worktree_cleanup_policy"`
	WorktreeCreatedByPD        bool       `json:"worktree_created_by_pd"`
	WorktreeCleanupStatus      string     `json:"worktree_cleanup_status"`
	WorktreeCleanupError       string     `json:"worktree_cleanup_error,omitempty"`
	WorktreeCleanupAttemptedAt *time.Time `json:"worktree_cleanup_attempted_at,omitempty"`
	WorktreeRemovedAt          *time.Time `json:"worktree_removed_at,omitempty"`
	CreatedAt                  time.Time  `json:"created_at"`
	UpdatedAt                  time.Time  `json:"updated_at"`
}

type statusView struct {
	Task taskView `json:"task"`
	Run  *runView `json:"run,omitempty"`
}

type runView struct {
	ID                string            `json:"id"`
	Status            string            `json:"status"`
	Attempt           int               `json:"attempt"`
	EndedAt           *time.Time        `json:"ended_at,omitempty"`
	ExitCode          *int              `json:"exit_code,omitempty"`
	ErrorMessage      string            `json:"error_message,omitempty"`
	PiSessionFile     string            `json:"pi_session_file,omitempty"`
	AgentOptions      *agentOptionsView `json:"agent_options,omitempty"`
	EnvVarNames       []string          `json:"env_var_names,omitempty"`
	ControlSocketPath string            `json:"control_socket_path"`
	StdoutLogPath     string            `json:"stdout_log_path"`
	StderrLogPath     string            `json:"stderr_log_path"`
	PiEventsPath      string            `json:"pi_events_path"`
}

type agentOptionsView struct {
	Provider                  string   `json:"provider,omitempty"`
	Model                     string   `json:"model,omitempty"`
	Thinking                  string   `json:"thinking,omitempty"`
	Tools                     []string `json:"tools,omitempty"`
	DisableBuiltinTools       bool     `json:"disable_builtin_tools,omitempty"`
	DisableAllTools           bool     `json:"disable_all_tools,omitempty"`
	Extensions                []string `json:"extensions,omitempty"`
	DisableExtensionDiscovery bool     `json:"disable_extension_discovery,omitempty"`
	Skills                    []string `json:"skills,omitempty"`
	DisableSkillDiscovery     bool     `json:"disable_skill_discovery,omitempty"`
	DisableContextFiles       bool     `json:"disable_context_files,omitempty"`
	SessionDir                string   `json:"session_dir,omitempty"`
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
		return taskLookupError(args[0], err)
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
	if view.Task.WorktreeCleanupPolicy != string(store.CleanupPolicyNever) || view.Task.WorktreeCleanupStatus != string(store.CleanupStatusNotRequested) || view.Task.WorktreeCleanupError != "" {
		if _, err := fmt.Fprintf(os.Stdout, "Cleanup: policy=%s status=%s\n", view.Task.WorktreeCleanupPolicy, view.Task.WorktreeCleanupStatus); err != nil {
			return err
		}
		if view.Task.WorktreeCleanupError != "" {
			if _, err := fmt.Fprintf(os.Stdout, "Cleanup error: %s\n", view.Task.WorktreeCleanupError); err != nil {
				return err
			}
		}
		if view.Task.WorktreeRemovedAt != nil {
			if _, err := fmt.Fprintf(os.Stdout, "Worktree removed: %s (branch preserved)\n", view.Task.WorktreeRemovedAt.Format(time.RFC3339)); err != nil {
				return err
			}
		}
	}
	if view.Run != nil {
		if _, err := fmt.Fprintf(os.Stdout, "Logs:          %s\nRaw Pi events: %s\n", view.Run.StdoutLogPath, view.Run.PiEventsPath); err != nil {
			return err
		}
		if view.Run.AgentOptions != nil {
			if err := printAgentOptions(view.Run.AgentOptions); err != nil {
				return err
			}
		}
		if len(view.Run.EnvVarNames) > 0 {
			if _, err := fmt.Fprintf(os.Stdout, "Env:      %s\n", strings.Join(view.Run.EnvVarNames, ", ")); err != nil {
				return err
			}
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
	view := taskView{ID: task.ID, Status: string(task.Status), RepoName: task.RepoName, Branch: task.Branch, WorktreePath: task.WorktreePath, WorktreeCleanupPolicy: string(task.WorktreeCleanupPolicy), WorktreeCreatedByPD: task.WorktreeCreatedByPD, WorktreeCleanupStatus: string(task.WorktreeCleanupStatus), WorktreeCleanupError: task.WorktreeCleanupError, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt}
	if task.WorktreeCleanupAttemptedAt.Valid {
		attemptedAt := task.WorktreeCleanupAttemptedAt.Time
		view.WorktreeCleanupAttemptedAt = &attemptedAt
	}
	if task.WorktreeRemovedAt.Valid {
		removedAt := task.WorktreeRemovedAt.Time
		view.WorktreeRemovedAt = &removedAt
	}
	return view
}

func viewRun(run store.Run) runView {
	view := runView{ID: run.ID, Status: string(run.Status), Attempt: run.Attempt, ErrorMessage: run.ErrorMessage, PiSessionFile: run.PiSessionFile, AgentOptions: decodeAgentOptionsView(run.AgentOptionsJSON), EnvVarNames: decodeEnvVarNames(run.EnvVarNamesJSON), ControlSocketPath: run.ControlSocketPath, StdoutLogPath: run.StdoutLogPath, StderrLogPath: run.StderrLogPath, PiEventsPath: run.PiEventsPath}
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

func decodeEnvVarNames(data string) []string {
	if data == "" {
		return nil
	}
	var names []string
	if err := json.Unmarshal([]byte(data), &names); err != nil {
		return nil
	}
	return names
}

func decodeAgentOptionsView(data string) *agentOptionsView {
	if data == "" {
		return nil
	}
	var agent pdconfig.AgentOptions
	if err := json.Unmarshal([]byte(data), &agent); err != nil {
		return nil
	}
	view := agentOptionsView{Provider: agent.Provider, Model: agent.Model, Thinking: agent.Thinking, Tools: agent.Tools, DisableBuiltinTools: agent.DisableBuiltinTools, DisableAllTools: agent.DisableAllTools, Extensions: agent.Extensions, DisableExtensionDiscovery: agent.DisableExtensionDiscovery, Skills: agent.Skills, DisableSkillDiscovery: agent.DisableSkillDiscovery, DisableContextFiles: agent.DisableContextFiles, SessionDir: agent.SessionDir}
	if !view.hasValues() {
		return nil
	}
	return &view
}

func (a agentOptionsView) hasValues() bool {
	return a.Provider != "" || a.Model != "" || a.Thinking != "" || len(a.Tools) > 0 || a.DisableBuiltinTools || a.DisableAllTools || len(a.Extensions) > 0 || a.DisableExtensionDiscovery || len(a.Skills) > 0 || a.DisableSkillDiscovery || a.DisableContextFiles || a.SessionDir != ""
}

func printAgentOptions(agent *agentOptionsView) error {
	printValue := func(label, value string) error {
		if value == "" {
			return nil
		}
		_, err := fmt.Fprintf(os.Stdout, "%s %s\n", label, value)
		return err
	}
	printBool := func(label string, value bool) error {
		if !value {
			return nil
		}
		_, err := fmt.Fprintf(os.Stdout, "%s true\n", label)
		return err
	}
	if err := printValue("Provider:", agent.Provider); err != nil {
		return err
	}
	if err := printValue("Model:   ", agent.Model); err != nil {
		return err
	}
	if err := printValue("Thinking:", agent.Thinking); err != nil {
		return err
	}
	if err := printValue("Tools:   ", strings.Join(agent.Tools, ", ")); err != nil {
		return err
	}
	if err := printBool("No builtin tools:", agent.DisableBuiltinTools); err != nil {
		return err
	}
	if err := printBool("No tools:", agent.DisableAllTools); err != nil {
		return err
	}
	if err := printValue("Extensions:", strings.Join(agent.Extensions, ", ")); err != nil {
		return err
	}
	if err := printBool("No extensions:", agent.DisableExtensionDiscovery); err != nil {
		return err
	}
	if err := printValue("Skills:  ", strings.Join(agent.Skills, ", ")); err != nil {
		return err
	}
	if err := printBool("No skills:", agent.DisableSkillDiscovery); err != nil {
		return err
	}
	if err := printBool("No context files:", agent.DisableContextFiles); err != nil {
		return err
	}
	return printValue("Session dir:", agent.SessionDir)
}
