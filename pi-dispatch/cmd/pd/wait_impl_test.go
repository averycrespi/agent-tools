package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	pdconfig "github.com/averycrespi/agent-tools/pi-dispatch/internal/config"
	"github.com/averycrespi/agent-tools/pi-dispatch/internal/store"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestWaitReturnsImmediatelyForSucceededTask(t *testing.T) {
	_, task, _ := setupWaitTask(t, store.StatusSucceeded)
	cmd := waitTestCommand(t, 0)

	out := captureStdout(t, func() {
		require.NoError(t, waitForTask(cmd, []string{task.ID}))
	})

	require.Equal(t, "pd-test succeeded\n", out)
}

func TestWaitPollsUntilTaskCompletes(t *testing.T) {
	db, task, _ := setupWaitTask(t, store.StatusQueued)
	oldWaitPollInterval := waitPollInterval
	waitPollInterval = time.Millisecond
	t.Cleanup(func() { waitPollInterval = oldWaitPollInterval })

	go func() {
		time.Sleep(5 * time.Millisecond)
		_ = db.CompleteRun(context.Background(), task.ID, store.StatusSucceeded, 0, "", "/session.json")
	}()

	out := captureStdout(t, func() {
		require.NoError(t, waitForTask(waitTestCommand(t, 0), []string{task.ID}))
	})

	require.Equal(t, "pd-test succeeded\n", out)
}

func TestWaitTimesOut(t *testing.T) {
	_, task, _ := setupWaitTask(t, store.StatusQueued)
	oldWaitPollInterval := waitPollInterval
	waitPollInterval = time.Millisecond
	t.Cleanup(func() { waitPollInterval = oldWaitPollInterval })

	err := waitForTask(waitTestCommand(t, 3*time.Millisecond), []string{task.ID})

	require.ErrorContains(t, err, "timed out waiting for task pd-test")
}

func TestWaitReturnsErrorForFailedTaskAfterPrintingStatus(t *testing.T) {
	_, task, _ := setupWaitTask(t, store.StatusFailed)
	cmd := waitTestCommand(t, 0)

	out := captureStdout(t, func() {
		err := waitForTask(cmd, []string{task.ID})
		require.ErrorContains(t, err, "task pd-test failed")
	})

	require.Equal(t, "pd-test failed\n", out)
}

func TestWaitJSONPrintsStatusView(t *testing.T) {
	_, task, _ := setupWaitTask(t, store.StatusSucceeded)
	oldJSONOut := jsonOut
	jsonOut = true
	t.Cleanup(func() { jsonOut = oldJSONOut })

	out := captureStdout(t, func() {
		require.NoError(t, waitForTask(waitTestCommand(t, 0), []string{task.ID}))
	})

	require.JSONEq(t, `{"task":{"id":"pd-test","status":"succeeded","repo_name":"repo","branch":"pd/test","worktree_path":"/wt","worktree_cleanup_policy":"never","worktree_created_by_pd":false,"worktree_cleanup_status":"not_requested","created_at":"2024-01-01T00:00:00Z","updated_at":"2024-01-01T00:00:00Z"},"run":{"id":"run-test","status":"succeeded","attempt":1,"agent_options":{"model":"gpt-5"},"env_var_names":["OPENAI_API_KEY"],"control_socket_path":"/sock","stdout_log_path":"/stdout","stderr_log_path":"/stderr","pi_events_path":"/events"}}`, out)
}

func setupWaitTask(t *testing.T, status store.TaskStatus) (*store.Store, store.Task, store.Run) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "pd.db")
	oldCfg := cfg
	cfg = pdconfig.Config{DatabasePath: dbPath}
	t.Cleanup(func() { cfg = oldCfg })
	db, err := store.Open(dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	task := store.Task{ID: "pd-test", RepoPath: "/repo", RepoName: "repo", Branch: "pd/test", WorktreePath: "/wt", PromptSource: "arg", Prompt: "hello", PromptPreview: "hello", Status: status, CreatedAt: now, UpdatedAt: now}
	run := store.Run{ID: "run-test", TaskID: task.ID, Attempt: 1, SupervisorPID: 0, Status: status, StartedAt: now, AgentOptionsJSON: `{"model":"gpt-5"}`, PiArgvJSON: `["pi","--mode","rpc","--model","gpt-5"]`, EnvVarNamesJSON: `["OPENAI_API_KEY"]`, ControlSocketPath: "/sock", StdoutLogPath: "/stdout", StderrLogPath: "/stderr", PiEventsPath: "/events"}
	require.NoError(t, db.CreateTaskWithRun(context.Background(), task, run))
	return db, task, run
}

func waitTestCommand(t *testing.T, timeout time.Duration) *cobra.Command {
	t.Helper()
	cmd := testCommand()
	cmd.Flags().Duration("timeout", 0, "")
	require.NoError(t, cmd.Flags().Set("timeout", timeout.String()))
	return cmd
}
