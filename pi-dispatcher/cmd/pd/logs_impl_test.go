package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	pdconfig "github.com/averycrespi/agent-tools/pi-dispatcher/internal/config"
	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/control"
	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/store"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestControlAllowedAllowsForceStopWhileStopping(t *testing.T) {
	require.NoError(t, controlAllowed(store.StatusStopping, control.Request{Operation: control.OpStop, Force: true}))
}

func TestForceStopTaskWaitsForSupervisorToTransition(t *testing.T) {
	db, task, run := setupRemoveTask(t, store.StatusRunning)
	require.NoError(t, db.UpdateRunSupervisorPID(context.Background(), task.ID, os.Getpid()))
	require.NoError(t, db.Close())
	withProcessExists(t, func(int) bool { return true })
	withControlSender(t, func(path string, req control.Request) (control.Response, error) {
		if req.Operation == control.OpStop {
			require.Equal(t, run.ControlSocketPath, path)
			db, err := store.Open(cfg.DBPath())
			require.NoError(t, err)
			defer db.Close() //nolint:errcheck
			require.NoError(t, db.CompleteRun(context.Background(), task.ID, store.StatusStopped, 0, "", ""))
		}
		return control.Response{OK: true}, nil
	})
	killed := false
	withKillProcessGroup(t, func(int) error { killed = true; return nil })
	withForceStopTiming(t, time.Second, 10*time.Millisecond)

	require.NoError(t, sendStop(stopTestCommand(t, true), []string{task.ID}))

	require.False(t, killed, "supervisor should not be killed when it transitions cleanly")
	checkDB, err := store.Open(cfg.DBPath())
	require.NoError(t, err)
	defer checkDB.Close() //nolint:errcheck
	got, err := checkDB.GetTask(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, store.StatusStopped, got.Status)
}

func TestForceStopTaskEscalatesWhenSupervisorIsStuck(t *testing.T) {
	db, task, run := setupRemoveTask(t, store.StatusStopping)
	require.NoError(t, db.UpdateRunSupervisorPID(context.Background(), task.ID, 4242))
	require.NoError(t, db.Close())
	withProcessExists(t, func(int) bool { return true })
	withControlSender(t, func(path string, req control.Request) (control.Response, error) {
		if req.Operation == control.OpStop {
			require.Equal(t, run.ControlSocketPath, path)
			require.True(t, req.Force)
		}
		return control.Response{OK: true}, nil
	})
	killedPID := 0
	withKillProcessGroup(t, func(pid int) error { killedPID = pid; return nil })
	withForceStopTiming(t, 50*time.Millisecond, 10*time.Millisecond)

	require.NoError(t, sendStop(stopTestCommand(t, true), []string{task.ID}))

	require.Equal(t, 4242, killedPID, "supervisor process group should be killed when status is stuck")
	checkDB, err := store.Open(cfg.DBPath())
	require.NoError(t, err)
	defer checkDB.Close() //nolint:errcheck
	got, err := checkDB.GetTask(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, store.StatusStopped, got.Status)
	gotRun, err := checkDB.LatestRun(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, "force-killed by pd stop --force", gotRun.ErrorMessage)
}

func TestForceStopTaskSkipsCleanupWhenKilledSupervisorStillExists(t *testing.T) {
	db, task, _ := setupRemoveTask(t, store.StatusStopping, struct {
		policy store.WorktreeCleanupPolicy
		owned  bool
	}{policy: store.CleanupPolicyOnTerminal, owned: true})
	require.NoError(t, db.UpdateRunSupervisorPID(context.Background(), task.ID, 4242))
	require.NoError(t, db.Close())
	withProcessExists(t, func(int) bool { return true })
	withControlSender(t, func(string, control.Request) (control.Response, error) { return control.Response{OK: true}, nil })
	withKillProcessGroup(t, func(int) error { return nil })
	withForceStopTiming(t, 20*time.Millisecond, 5*time.Millisecond)
	fakeWT := &fakeRemoveWorktree{}
	withWorktreeClient(t, fakeWT)

	require.NoError(t, sendStop(stopTestCommand(t, true), []string{task.ID}))

	require.Empty(t, fakeWT.repoRoot)
	checkDB, err := store.Open(cfg.DBPath())
	require.NoError(t, err)
	defer checkDB.Close() //nolint:errcheck
	got, err := checkDB.GetTask(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, store.CleanupStatusSkipped, got.WorktreeCleanupStatus)
	require.Contains(t, got.WorktreeCleanupError, "still running")
}

func TestForceStopTaskRejectsTerminalStatus(t *testing.T) {
	db, task, _ := setupRemoveTask(t, store.StatusSucceeded)
	require.NoError(t, db.Close())
	withProcessExists(t, func(int) bool { return false })

	err := sendStop(stopTestCommand(t, true), []string{task.ID})

	require.ErrorContains(t, err, "not running")
}

func TestSendStopWithoutForceUsesGracefulPath(t *testing.T) {
	db, task, run := setupRemoveTask(t, store.StatusRunning)
	require.NoError(t, db.UpdateRunSupervisorPID(context.Background(), task.ID, os.Getpid()))
	require.NoError(t, db.Close())
	withProcessExists(t, func(int) bool { return true })
	sentForce := false
	withControlSender(t, func(path string, req control.Request) (control.Response, error) {
		if req.Operation == control.OpStop {
			require.Equal(t, run.ControlSocketPath, path)
			sentForce = req.Force
		}
		return control.Response{OK: true}, nil
	})
	killed := false
	withKillProcessGroup(t, func(int) error { killed = true; return nil })

	require.NoError(t, sendStop(stopTestCommand(t, false), []string{task.ID}))

	require.False(t, sentForce, "non-force stop should not set Force=true")
	require.False(t, killed, "non-force stop should never escalate to KillProcessGroup")
}

func withProcessExists(t *testing.T, fn func(int) bool) {
	t.Helper()
	old := processExists
	processExists = fn
	t.Cleanup(func() { processExists = old })
}

func withKillProcessGroup(t *testing.T, fn func(int) error) {
	t.Helper()
	old := killProcessGroup
	killProcessGroup = fn
	t.Cleanup(func() { killProcessGroup = old })
}

func withForceStopTiming(t *testing.T, grace, poll time.Duration) {
	t.Helper()
	oldGrace := forceStopEscalationGrace
	oldKillWait := forceStopKillWait
	oldPoll := forceStopPollInterval
	forceStopEscalationGrace = grace
	forceStopKillWait = grace
	forceStopPollInterval = poll
	t.Cleanup(func() {
		forceStopEscalationGrace = oldGrace
		forceStopKillWait = oldKillWait
		forceStopPollInterval = oldPoll
	})
}

func stopTestCommand(t *testing.T, force bool) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.Flags().Bool("force", false, "")
	require.NoError(t, cmd.Flags().Set("force", fmt.Sprintf("%t", force)))
	return cmd
}

func TestControlAllowedRejectsNonForceStopWhileStopping(t *testing.T) {
	err := controlAllowed(store.StatusStopping, control.Request{Operation: control.OpStop})
	require.Error(t, err)
}

func TestRemoveTaskDeletesInactiveMetadataLogsAndSocketWithoutRemovingWorktree(t *testing.T) {
	db, task, run := setupRemoveTask(t, store.StatusFailed)
	logPath := filepath.Join(pdconfig.TaskDir(task.ID), "stdout.log")
	require.NoError(t, os.MkdirAll(filepath.Dir(logPath), 0o750))
	require.NoError(t, os.WriteFile(logPath, []byte("log"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Dir(run.ControlSocketPath), 0o750))
	require.NoError(t, os.WriteFile(run.ControlSocketPath, nil, 0o600))
	require.NoError(t, db.Close())
	fakeWT := &fakeRemoveWorktree{}
	withWorktreeClient(t, fakeWT)

	cmd := removeTestCommand(t, false)
	require.NoError(t, removeTask(cmd, []string{task.ID}))

	checkDB, err := store.Open(cfg.DBPath())
	require.NoError(t, err)
	defer checkDB.Close() //nolint:errcheck
	_, err = checkDB.GetTask(context.Background(), task.ID)
	require.Error(t, err)
	require.NoFileExists(t, logPath)
	require.NoFileExists(t, run.ControlSocketPath)
	require.Empty(t, fakeWT.repoRoot)
}

func TestRemoveTaskRemovesMultipleTasks(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)
	dbPath := filepath.Join(t.TempDir(), "pd.db")
	oldCfg := cfg
	cfg = pdconfig.Config{DatabasePath: dbPath}
	t.Cleanup(func() { cfg = oldCfg })
	db, err := store.Open(dbPath)
	require.NoError(t, err)
	now := time.Now().Add(-time.Minute)
	ids := []string{"pd-a", "pd-b"}
	for _, id := range ids {
		task := store.Task{ID: id, RepoPath: "/repo", RepoName: "repo", Branch: "pd/" + id, WorktreePath: "/wt", PromptSource: "arg", Prompt: "hi", PromptPreview: "hi", Status: store.StatusSucceeded, CreatedAt: now, UpdatedAt: now}
		run := store.Run{ID: "run-" + id, TaskID: id, Attempt: 1, Status: store.StatusSucceeded, StartedAt: now, StdoutLogPath: filepath.Join(pdconfig.TaskDir(id), "stdout.log"), StderrLogPath: filepath.Join(pdconfig.TaskDir(id), "stderr.log"), PiEventsPath: filepath.Join(pdconfig.TaskDir(id), "pi-events.jsonl")}
		require.NoError(t, db.CreateTaskWithRun(context.Background(), task, run))
	}
	require.NoError(t, db.Close())
	withProcessExists(t, func(int) bool { return false })

	require.NoError(t, removeTask(removeTestCommand(t, false), ids))

	checkDB, err := store.Open(cfg.DBPath())
	require.NoError(t, err)
	defer checkDB.Close() //nolint:errcheck
	for _, id := range ids {
		_, err := checkDB.GetTask(context.Background(), id)
		require.Error(t, err, "task %s should be removed", id)
	}
}

func TestRemoveTaskAllRemovesInactiveTasksOnly(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)
	dbPath := filepath.Join(t.TempDir(), "pd.db")
	oldCfg := cfg
	cfg = pdconfig.Config{DatabasePath: dbPath}
	t.Cleanup(func() { cfg = oldCfg })
	db, err := store.Open(dbPath)
	require.NoError(t, err)
	now := time.Now().Add(-time.Minute)
	seedRemoveTaskRecord(t, db, "pd-done", store.StatusSucceeded, now)
	seedRemoveTaskRecord(t, db, "pd-failed", store.StatusFailed, now)
	seedRemoveTaskRecord(t, db, "pd-unknown", store.StatusUnknown, now)
	seedRemoveTaskRecord(t, db, "pd-running", store.StatusRunning, now)
	require.NoError(t, db.Close())
	withProcessExists(t, func(int) bool { return true })

	require.NoError(t, removeTask(removeTestCommand(t, true), nil))

	checkDB, err := store.Open(cfg.DBPath())
	require.NoError(t, err)
	defer checkDB.Close() //nolint:errcheck
	for _, id := range []string{"pd-done", "pd-failed"} {
		_, err := checkDB.GetTask(context.Background(), id)
		require.Error(t, err, "task %s should be removed", id)
	}
	for _, id := range []string{"pd-unknown", "pd-running"} {
		_, err := checkDB.GetTask(context.Background(), id)
		require.NoError(t, err, "task %s should be preserved", id)
	}
}

func TestRemoveTaskAllIncludesUnknownWithFlag(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)
	dbPath := filepath.Join(t.TempDir(), "pd.db")
	oldCfg := cfg
	cfg = pdconfig.Config{DatabasePath: dbPath}
	t.Cleanup(func() { cfg = oldCfg })
	db, err := store.Open(dbPath)
	require.NoError(t, err)
	now := time.Now().Add(-time.Minute)
	seedRemoveTaskRecord(t, db, "pd-unknown", store.StatusUnknown, now)
	require.NoError(t, db.Close())
	withProcessExists(t, func(int) bool { return true })

	require.NoError(t, removeTask(removeTestCommand(t, true, true), nil))

	checkDB, err := store.Open(cfg.DBPath())
	require.NoError(t, err)
	defer checkDB.Close() //nolint:errcheck
	_, err = checkDB.GetTask(context.Background(), "pd-unknown")
	require.Error(t, err, "unknown task should be removed when included")
}

func TestRemoveTaskAllRejectsTaskIDs(t *testing.T) {
	cmd := rmCmd
	err := cmd.Args(cmd, []string{"pd-test"})
	require.NoError(t, err)
	require.NoError(t, cmd.Flags().Set("all", "true"))
	t.Cleanup(func() { require.NoError(t, cmd.Flags().Set("all", "false")) })

	err = cmd.Args(cmd, []string{"pd-test"})
	require.ErrorContains(t, err, "--all cannot be used with task-ids")
}

func TestRemoveTaskIsIdempotentForMissingTask(t *testing.T) {
	db, _, _ := setupRemoveTask(t, store.StatusFailed)
	require.NoError(t, db.Close())
	withProcessExists(t, func(int) bool { return false })

	require.NoError(t, removeTask(removeTestCommand(t, false), []string{"pd-missing"}))
}

func TestRemoveTaskIsBestEffortAcrossTaskIDs(t *testing.T) {
	db, task, _ := setupRemoveTask(t, store.StatusFailed)
	require.NoError(t, db.Close())
	withProcessExists(t, func(int) bool { return false })

	require.NoError(t, removeTask(removeTestCommand(t, false), []string{task.ID, "pd-missing"}))
	checkDB, openErr := store.Open(cfg.DBPath())
	require.NoError(t, openErr)
	defer checkDB.Close() //nolint:errcheck
	_, getErr := checkDB.GetTask(context.Background(), task.ID)
	require.Error(t, getErr, "existing task should be removed despite a missing sibling")
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

func TestCleanupTaskAllCleansTerminalTasksOnly(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)
	dbPath := filepath.Join(t.TempDir(), "pd.db")
	oldCfg := cfg
	cfg = pdconfig.Config{DatabasePath: dbPath}
	t.Cleanup(func() { cfg = oldCfg })
	db, err := store.Open(dbPath)
	require.NoError(t, err)
	now := time.Now().Add(-time.Minute)
	seedRemoveTaskRecord(t, db, "pd-done", store.StatusSucceeded, now)
	seedRemoveTaskRecord(t, db, "pd-failed", store.StatusFailed, now)
	seedRemoveTaskRecord(t, db, "pd-unknown", store.StatusUnknown, now)
	seedRemoveTaskRecord(t, db, "pd-running", store.StatusRunning, now)
	require.NoError(t, db.Close())
	fakeWT := &fakeRemoveWorktree{}
	withWorktreeClient(t, fakeWT)

	require.NoError(t, cleanupTask(cleanupTestCommand(t, false, true), nil))

	require.ElementsMatch(t, []string{"pd/pd-done", "pd/pd-failed"}, fakeWT.branches)
	checkDB, err := store.Open(cfg.DBPath())
	require.NoError(t, err)
	defer checkDB.Close() //nolint:errcheck
	for _, id := range []string{"pd-done", "pd-failed"} {
		got, err := checkDB.GetTask(context.Background(), id)
		require.NoError(t, err)
		require.Equal(t, store.CleanupStatusRemoved, got.WorktreeCleanupStatus)
	}
	for _, id := range []string{"pd-unknown", "pd-running"} {
		got, err := checkDB.GetTask(context.Background(), id)
		require.NoError(t, err)
		require.Equal(t, store.CleanupStatusNotRequested, got.WorktreeCleanupStatus)
	}
}

func TestCleanupTaskAllIncludesUnknownWithFlag(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)
	dbPath := filepath.Join(t.TempDir(), "pd.db")
	oldCfg := cfg
	cfg = pdconfig.Config{DatabasePath: dbPath}
	t.Cleanup(func() { cfg = oldCfg })
	db, err := store.Open(dbPath)
	require.NoError(t, err)
	now := time.Now().Add(-time.Minute)
	seedRemoveTaskRecord(t, db, "pd-unknown", store.StatusUnknown, now)
	require.NoError(t, db.Close())
	fakeWT := &fakeRemoveWorktree{}
	withWorktreeClient(t, fakeWT)

	require.NoError(t, cleanupTask(cleanupTestCommand(t, false, true, true), nil))

	require.ElementsMatch(t, []string{"pd/pd-unknown"}, fakeWT.branches)
}

func TestCleanupTaskAllRejectsTaskIDs(t *testing.T) {
	cmd := cleanupCmd
	err := cmd.Args(cmd, []string{"pd-test"})
	require.NoError(t, err)
	require.NoError(t, cmd.Flags().Set("all", "true"))
	t.Cleanup(func() { require.NoError(t, cmd.Flags().Set("all", "false")) })

	err = cmd.Args(cmd, []string{"pd-test"})
	require.ErrorContains(t, err, "--all cannot be used with task-ids")
}

func TestCleanupTaskDryRunDoesNotRemoveOrMutate(t *testing.T) {
	db, task, _ := setupRemoveTask(t, store.StatusFailed)
	require.NoError(t, db.Close())
	fakeWT := &fakeRemoveWorktree{}
	withWorktreeClient(t, fakeWT)

	require.NoError(t, cleanupTask(cleanupTestCommand(t, true), []string{task.ID}))

	require.Empty(t, fakeWT.repoRoot)
	checkDB, err := store.Open(cfg.DBPath())
	require.NoError(t, err)
	defer checkDB.Close() //nolint:errcheck
	got, err := checkDB.GetTask(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, store.CleanupStatusNotRequested, got.WorktreeCleanupStatus)
	require.False(t, got.WorktreeCleanupAttemptedAt.Valid)
}

func TestCleanupTaskRefusesActiveTask(t *testing.T) {
	db, task, run := setupRemoveTask(t, store.StatusRunning)
	require.NoError(t, db.UpdateRunSupervisorPID(context.Background(), task.ID, os.Getpid()))
	require.NoError(t, db.Close())
	withControlSender(t, func(path string, req control.Request) (control.Response, error) {
		require.Equal(t, run.ControlSocketPath, path)
		return control.Response{OK: true}, nil
	})

	err := cleanupTask(cleanupTestCommand(t, false), []string{task.ID})

	require.ErrorContains(t, err, "refusing to cleanup running task")
}

func TestCleanupTaskRemovesWorktreeAndPreservesDBAndLogs(t *testing.T) {
	db, task, _ := setupRemoveTask(t, store.StatusFailed)
	logPath := filepath.Join(pdconfig.TaskDir(task.ID), "stdout.log")
	require.NoError(t, os.MkdirAll(filepath.Dir(logPath), 0o750))
	require.NoError(t, os.WriteFile(logPath, []byte("log"), 0o600))
	require.NoError(t, db.Close())
	fakeWT := &fakeRemoveWorktree{}
	oldNewWorktreeClient := newWorktreeClient
	newWorktreeClient = func() (worktreeClient, error) { return fakeWT, nil }
	defer func() { newWorktreeClient = oldNewWorktreeClient }()

	require.NoError(t, cleanupTask(cleanupTestCommand(t, false), []string{task.ID}))

	require.Equal(t, task.RepoPath, fakeWT.repoRoot)
	require.Equal(t, task.Branch, fakeWT.branch)
	require.FileExists(t, logPath)
	checkDB, err := store.Open(cfg.DBPath())
	require.NoError(t, err)
	defer checkDB.Close() //nolint:errcheck
	got, err := checkDB.GetTask(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, store.CleanupStatusRemoved, got.WorktreeCleanupStatus)
}

func TestCleanupTaskFailurePreservesDBAndLogsAndRecordsFailure(t *testing.T) {
	db, task, _ := setupRemoveTask(t, store.StatusFailed)
	logPath := filepath.Join(pdconfig.TaskDir(task.ID), "stdout.log")
	require.NoError(t, os.MkdirAll(filepath.Dir(logPath), 0o750))
	require.NoError(t, os.WriteFile(logPath, []byte("log"), 0o600))
	require.NoError(t, db.Close())
	fakeWT := &fakeRemoveWorktree{err: errors.New("remove failed")}
	oldNewWorktreeClient := newWorktreeClient
	newWorktreeClient = func() (worktreeClient, error) { return fakeWT, nil }
	defer func() { newWorktreeClient = oldNewWorktreeClient }()

	require.NoError(t, cleanupTask(cleanupTestCommand(t, false), []string{task.ID}))

	require.FileExists(t, logPath)
	checkDB, err := store.Open(cfg.DBPath())
	require.NoError(t, err)
	defer checkDB.Close() //nolint:errcheck
	got, err := checkDB.GetTask(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, store.CleanupStatusFailed, got.WorktreeCleanupStatus)
	require.Contains(t, got.WorktreeCleanupError, "remove failed")
}

func seedRemoveTaskRecord(t *testing.T, db *store.Store, id string, status store.TaskStatus, now time.Time) {
	t.Helper()
	task := store.Task{ID: id, RepoPath: "/repo", RepoName: "repo", Branch: "pd/" + id, WorktreePath: "/wt/" + id, PromptSource: "arg", Prompt: "hi", PromptPreview: "hi", Status: status, WorktreeCleanupPolicy: store.CleanupPolicyNever, WorktreeCleanupStatus: store.CleanupStatusNotRequested, CreatedAt: now, UpdatedAt: now}
	run := store.Run{ID: "run-" + id, TaskID: id, Attempt: 1, Status: status, StartedAt: now, StdoutLogPath: filepath.Join(pdconfig.TaskDir(id), "stdout.log"), StderrLogPath: filepath.Join(pdconfig.TaskDir(id), "stderr.log"), PiEventsPath: filepath.Join(pdconfig.TaskDir(id), "pi-events.jsonl")}
	require.NoError(t, db.CreateTaskWithRun(context.Background(), task, run))
}

func setupRemoveTask(t *testing.T, status store.TaskStatus, cleanup ...struct {
	policy store.WorktreeCleanupPolicy
	owned  bool
}) (*store.Store, store.Task, store.Run) {
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
	policy := store.CleanupPolicyNever
	owned := false
	cleanupStatus := store.CleanupStatusNotRequested
	if len(cleanup) > 0 {
		policy = cleanup[0].policy
		owned = cleanup[0].owned
		if policy != store.CleanupPolicyNever {
			cleanupStatus = store.CleanupStatusPending
		}
	}
	task := store.Task{ID: "pd-test", RepoPath: "/repo", RepoName: "repo", Branch: "pd/test", WorktreePath: "/wt", PromptSource: "arg", Prompt: "hello", PromptPreview: "hello", Status: status, WorktreeCleanupPolicy: policy, WorktreeCreatedByPD: owned, WorktreeCleanupStatus: cleanupStatus, CreatedAt: now, UpdatedAt: now}
	run := store.Run{ID: "run-test", TaskID: task.ID, Attempt: 1, SupervisorPID: 0, Status: status, StartedAt: now, ControlSocketPath: filepath.Join(stateDir, "run", "pd-test.sock"), StdoutLogPath: filepath.Join(pdconfig.TaskDir(task.ID), "stdout.log"), StderrLogPath: filepath.Join(pdconfig.TaskDir(task.ID), "stderr.log"), PiEventsPath: filepath.Join(pdconfig.TaskDir(task.ID), "pi-events.jsonl")}
	require.NoError(t, db.CreateTaskWithRun(context.Background(), task, run))
	return db, task, run
}

func removeTestCommand(t *testing.T, all bool, includeUnknown ...bool) *cobra.Command {
	t.Helper()
	oldRMAll := rmAll
	oldRMIncludeUnknown := rmIncludeUnknown
	rmAll = all
	rmIncludeUnknown = len(includeUnknown) > 0 && includeUnknown[0]
	t.Cleanup(func() {
		rmAll = oldRMAll
		rmIncludeUnknown = oldRMIncludeUnknown
	})
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	return cmd
}

func cleanupTestCommand(t *testing.T, dryRun bool, all ...bool) *cobra.Command {
	t.Helper()
	oldCleanupAll := cleanupAll
	oldCleanupIncludeUnknown := cleanupIncludeUnknown
	cleanupAll = len(all) > 0 && all[0]
	cleanupIncludeUnknown = len(all) > 1 && all[1]
	t.Cleanup(func() {
		cleanupAll = oldCleanupAll
		cleanupIncludeUnknown = oldCleanupIncludeUnknown
	})
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.Flags().Bool("dry-run", false, "")
	require.NoError(t, cmd.Flags().Set("dry-run", fmt.Sprintf("%t", dryRun)))
	return cmd
}

type fakeRemoveWorktree struct {
	repoRoot string
	branch   string
	branches []string
	err      error
}

func (w *fakeRemoveWorktree) AddHeadless(string, string) (string, error) { return "", nil }
func (w *fakeRemoveWorktree) AddHeadlessWithOwnership(string, string) (string, bool, error) {
	return "", false, nil
}
func (w *fakeRemoveWorktree) Path(string, string) (string, error) { return "", nil }
func (w *fakeRemoveWorktree) Remove(repoRoot, branch string) error {
	w.repoRoot = repoRoot
	w.branch = branch
	w.branches = append(w.branches, branch)
	return w.err
}

func TestShowLogsFollowPrintsMonitorHeaderAndReconcilesStaleTask(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pd.db")
	oldCfg := cfg
	cfg = pdconfig.Config{DatabasePath: dbPath}
	defer func() { cfg = oldCfg }()

	db, err := store.Open(dbPath)
	require.NoError(t, err)
	now := time.Now().Add(-time.Minute)
	logDir := t.TempDir()
	task := store.Task{ID: "pd-test", RepoPath: "/repo", RepoName: "repo", Branch: "pd/test", WorktreePath: "/wt", PromptSource: "arg", Prompt: "hello", PromptPreview: "hello", Status: store.StatusRunning, CreatedAt: now, UpdatedAt: now}
	run := store.Run{ID: "run-test", TaskID: task.ID, Attempt: 1, SupervisorPID: 4242, Status: store.StatusRunning, StartedAt: now, ControlSocketPath: filepath.Join(t.TempDir(), "missing.sock"), StdoutLogPath: filepath.Join(logDir, "stdout.log"), StderrLogPath: filepath.Join(logDir, "stderr.log"), PiEventsPath: filepath.Join(logDir, "pi-events.jsonl")}
	require.NoError(t, os.WriteFile(run.StdoutLogPath, []byte("old stdout\n"), 0o600))
	require.NoError(t, os.WriteFile(run.StderrLogPath, []byte("old stderr\n"), 0o600))
	require.NoError(t, db.CreateTaskWithRun(context.Background(), task, run))
	require.NoError(t, db.Close())

	cmd := logsTestCommand(t, true)
	oldFollowLogFiles := followLogFiles
	followLogFiles = func(targets ...logFollowTarget) error {
		require.Equal(t, []logFollowTarget{{label: "stdout", path: run.StdoutLogPath}, {label: "stderr", path: run.StderrLogPath}}, targets)
		return nil
	}
	defer func() { followLogFiles = oldFollowLogFiles }()
	oldProcessExists := processExists
	processExists = func(int) bool { return false }
	defer func() { processExists = oldProcessExists }()

	out := captureStdout(t, func() {
		require.NoError(t, showLogs(cmd, []string{task.ID}))
	})

	require.Contains(t, out, "Task pd-test [unknown]")
	require.Regexp(t, `Logs:\s+`+regexp.QuoteMeta(run.StdoutLogPath), out)
	require.Regexp(t, `Raw Pi events:\s+`+regexp.QuoteMeta(run.PiEventsPath), out)
	require.Contains(t, out, "old stdout\n")
	require.Contains(t, out, "old stderr\n")
}

func logsTestCommand(t *testing.T, follow bool) *cobra.Command {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.Flags().BoolP("follow", "f", false, "")
	require.NoError(t, cmd.Flags().Set("follow", fmt.Sprintf("%t", follow)))
	return cmd
}
