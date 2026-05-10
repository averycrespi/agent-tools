package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	pdconfig "github.com/averycrespi/agent-tools/pi-dispatch/internal/config"
	"github.com/averycrespi/agent-tools/pi-dispatch/internal/control"
	"github.com/averycrespi/agent-tools/pi-dispatch/internal/store"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestControlAllowedAllowsForceStopWhileStopping(t *testing.T) {
	require.NoError(t, controlAllowed(store.StatusStopping, control.Request{Operation: control.OpStop, Force: true}))
}

func TestControlAllowedRejectsSteerWhileStopping(t *testing.T) {
	err := controlAllowed(store.StatusStopping, control.Request{Operation: control.OpSteer})
	require.Error(t, err)
}

func TestRemoveTaskDeletesInactiveMetadataLogsAndSocket(t *testing.T) {
	db, task, run := setupRemoveTask(t, store.StatusFailed)
	logPath := filepath.Join(pdconfig.TaskDir(task.ID), "stdout.log")
	require.NoError(t, os.MkdirAll(filepath.Dir(logPath), 0o750))
	require.NoError(t, os.WriteFile(logPath, []byte("log"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Dir(run.ControlSocketPath), 0o750))
	require.NoError(t, os.WriteFile(run.ControlSocketPath, nil, 0o600))
	require.NoError(t, db.Close())

	cmd := removeTestCommand(t, false)
	require.NoError(t, removeTask(cmd, []string{task.ID}))

	checkDB, err := store.Open(cfg.DBPath())
	require.NoError(t, err)
	defer checkDB.Close() //nolint:errcheck
	_, err = checkDB.GetTask(context.Background(), task.ID)
	require.Error(t, err)
	require.NoFileExists(t, logPath)
	require.NoFileExists(t, run.ControlSocketPath)
}

func TestRemoveTaskRefusesActiveTask(t *testing.T) {
	db, task, run := setupRemoveTask(t, store.StatusStarting)
	require.NoError(t, db.UpdateRunSupervisorPID(context.Background(), task.ID, os.Getpid()))
	require.NoError(t, db.Close())
	withControlSender(t, func(path string, req control.Request) (control.Response, error) {
		require.Equal(t, run.ControlSocketPath, path)
		require.Equal(t, control.OpPing, req.Operation)
		return control.Response{OK: true}, nil
	})

	err := removeTask(removeTestCommand(t, false), []string{task.ID})

	require.ErrorContains(t, err, "refusing to remove starting task")
	checkDB, err := store.Open(cfg.DBPath())
	require.NoError(t, err)
	defer checkDB.Close() //nolint:errcheck
	_, err = checkDB.GetTask(context.Background(), task.ID)
	require.NoError(t, err)
}

func TestRemoveTaskWithWorktreeRemovesWorktreeBeforeDB(t *testing.T) {
	db, task, _ := setupRemoveTask(t, store.StatusFailed)
	require.NoError(t, db.Close())
	fakeWT := &fakeRemoveWorktree{}
	oldNewWorktreeClient := newWorktreeClient
	newWorktreeClient = func() (worktreeClient, error) { return fakeWT, nil }
	defer func() { newWorktreeClient = oldNewWorktreeClient }()

	require.NoError(t, removeTask(removeTestCommand(t, true), []string{task.ID}))

	require.Equal(t, task.RepoPath, fakeWT.repoRoot)
	require.Equal(t, task.Branch, fakeWT.branch)
	checkDB, err := store.Open(cfg.DBPath())
	require.NoError(t, err)
	defer checkDB.Close() //nolint:errcheck
	_, err = checkDB.GetTask(context.Background(), task.ID)
	require.Error(t, err)
}

func TestRemoveTaskWithWorktreeFailurePreservesDBAndLogs(t *testing.T) {
	db, task, _ := setupRemoveTask(t, store.StatusFailed)
	logPath := filepath.Join(pdconfig.TaskDir(task.ID), "stdout.log")
	require.NoError(t, os.MkdirAll(filepath.Dir(logPath), 0o750))
	require.NoError(t, os.WriteFile(logPath, []byte("log"), 0o600))
	require.NoError(t, db.Close())
	fakeWT := &fakeRemoveWorktree{err: errors.New("remove failed")}
	oldNewWorktreeClient := newWorktreeClient
	newWorktreeClient = func() (worktreeClient, error) { return fakeWT, nil }
	defer func() { newWorktreeClient = oldNewWorktreeClient }()

	err := removeTask(removeTestCommand(t, true), []string{task.ID})

	require.ErrorContains(t, err, "remove failed")
	require.FileExists(t, logPath)
	checkDB, err := store.Open(cfg.DBPath())
	require.NoError(t, err)
	defer checkDB.Close() //nolint:errcheck
	_, err = checkDB.GetTask(context.Background(), task.ID)
	require.NoError(t, err)
}

func setupRemoveTask(t *testing.T, status store.TaskStatus) (*store.Store, store.Task, store.Run) {
	t.Helper()
	stateDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)
	dbPath := filepath.Join(t.TempDir(), "pd.db")
	oldCfg := cfg
	cfg = pdconfig.Config{DatabasePath: dbPath}
	t.Cleanup(func() { cfg = oldCfg })
	db, err := store.Open(dbPath)
	require.NoError(t, err)
	now := time.Now().Add(-time.Minute)
	task := store.Task{ID: "pd-test", RepoPath: "/repo", RepoName: "repo", Branch: "pd/test", WorktreePath: "/wt", PromptSource: "arg", Prompt: "hello", PromptPreview: "hello", Status: status, CreatedAt: now, UpdatedAt: now}
	run := store.Run{ID: "run-test", TaskID: task.ID, Attempt: 1, SupervisorPID: 0, Status: status, StartedAt: now, ControlSocketPath: filepath.Join(stateDir, "run", "pd-test.sock"), StdoutLogPath: filepath.Join(pdconfig.TaskDir(task.ID), "stdout.log"), StderrLogPath: filepath.Join(pdconfig.TaskDir(task.ID), "stderr.log"), PiEventsPath: filepath.Join(pdconfig.TaskDir(task.ID), "pi-events.jsonl")}
	require.NoError(t, db.CreateTaskWithRun(context.Background(), task, run))
	return db, task, run
}

func removeTestCommand(t *testing.T, removeWorktree bool) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.Flags().Bool("worktree", false, "")
	require.NoError(t, cmd.Flags().Set("worktree", fmt.Sprintf("%t", removeWorktree)))
	return cmd
}

type fakeRemoveWorktree struct {
	repoRoot string
	branch   string
	err      error
}

func (w *fakeRemoveWorktree) AddHeadless(string, string) (string, error) { return "", nil }
func (w *fakeRemoveWorktree) Remove(repoRoot, branch string) error {
	w.repoRoot = repoRoot
	w.branch = branch
	return w.err
}

func TestTaskAndRunReconciledMarksAttachStaleTaskUnknown(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pd.db")
	oldCfg := cfg
	cfg = pdconfig.Config{DatabasePath: dbPath}
	defer func() { cfg = oldCfg }()

	db, err := store.Open(dbPath)
	require.NoError(t, err)
	now := time.Now()
	task := store.Task{ID: "pd-test", RepoPath: "/repo", RepoName: "repo", Branch: "pd/test", WorktreePath: "/wt", PromptSource: "arg", Prompt: "hello", PromptPreview: "hello", Status: store.StatusRunning, CreatedAt: now, UpdatedAt: now}
	run := store.Run{ID: "run-test", TaskID: task.ID, Attempt: 1, SupervisorPID: 4242, Status: store.StatusRunning, StartedAt: now, ControlSocketPath: filepath.Join(t.TempDir(), "missing.sock"), StdoutLogPath: "/stdout", StderrLogPath: "/stderr", PiEventsPath: "/events"}
	require.NoError(t, db.CreateTaskWithRun(context.Background(), task, run))
	require.NoError(t, db.Close())

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	got, _, err := taskAndRunReconciled(cmd, task.ID, func(int) bool { return false })
	require.NoError(t, err)
	require.Equal(t, store.StatusUnknown, got.Status)
}
