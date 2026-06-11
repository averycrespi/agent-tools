package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/store"
	"github.com/stretchr/testify/require"
)

func TestAutomaticCleanupOnSuccessRemovesOwnedWorktree(t *testing.T) {
	db, task := setupCleanupTask(t, store.StatusSucceeded, store.CleanupPolicyOnSuccess, true)
	fakeWT := &fakeRemoveWorktree{}
	withWorktreeClient(t, fakeWT)

	result, err := performWorktreeCleanup(context.Background(), db, task, store.StatusSucceeded, false)

	require.NoError(t, err)
	require.Equal(t, string(store.CleanupStatusRemoved), result.Status)
	require.Equal(t, task.RepoPath, fakeWT.repoRoot)
	require.Equal(t, task.Branch, fakeWT.branch)
	got, err := db.GetTask(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, store.StatusSucceeded, got.Status)
	require.Equal(t, store.CleanupStatusRemoved, got.WorktreeCleanupStatus)
	require.True(t, got.WorktreeRemovedAt.Valid)
}

func TestAutomaticCleanupOnTerminalRemovesFailedOwnedWorktree(t *testing.T) {
	db, task := setupCleanupTask(t, store.StatusFailed, store.CleanupPolicyOnTerminal, true)
	fakeWT := &fakeRemoveWorktree{}
	withWorktreeClient(t, fakeWT)

	result, err := performWorktreeCleanup(context.Background(), db, task, store.StatusFailed, false)

	require.NoError(t, err)
	require.Equal(t, string(store.CleanupStatusRemoved), result.Status)
	require.Equal(t, task.RepoPath, fakeWT.repoRoot)
	require.Equal(t, task.Branch, fakeWT.branch)
}

func TestAutomaticCleanupNeverDoesNotRemoveOrMutate(t *testing.T) {
	db, task := setupCleanupTask(t, store.StatusSucceeded, store.CleanupPolicyNever, true)
	fakeWT := &fakeRemoveWorktree{}
	withWorktreeClient(t, fakeWT)

	result, err := performWorktreeCleanup(context.Background(), db, task, store.StatusSucceeded, false)

	require.NoError(t, err)
	require.Equal(t, string(store.CleanupStatusNotRequested), result.Status)
	require.Empty(t, fakeWT.repoRoot)
	got, err := db.GetTask(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, store.CleanupStatusNotRequested, got.WorktreeCleanupStatus)
	require.False(t, got.WorktreeCleanupAttemptedAt.Valid)
}

func TestAutomaticCleanupSkipsNonOwnedWorktree(t *testing.T) {
	db, task := setupCleanupTask(t, store.StatusSucceeded, store.CleanupPolicyOnSuccess, false)
	fakeWT := &fakeRemoveWorktree{}
	withWorktreeClient(t, fakeWT)

	result, err := performWorktreeCleanup(context.Background(), db, task, store.StatusSucceeded, false)

	require.NoError(t, err)
	require.Equal(t, string(store.CleanupStatusSkipped), result.Status)
	require.Empty(t, fakeWT.repoRoot)
	got, err := db.GetTask(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, store.CleanupStatusSkipped, got.WorktreeCleanupStatus)
	require.Contains(t, got.WorktreeCleanupError, "not created")
}

func TestAutomaticCleanupFailureDoesNotChangeTerminalStatus(t *testing.T) {
	db, task := setupCleanupTask(t, store.StatusSucceeded, store.CleanupPolicyOnSuccess, true)
	withWorktreeClient(t, &fakeRemoveWorktree{err: errors.New("dirty worktree")})

	result, err := performWorktreeCleanup(context.Background(), db, task, store.StatusSucceeded, false)

	require.NoError(t, err)
	require.Equal(t, string(store.CleanupStatusFailed), result.Status)
	got, err := db.GetTask(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, store.StatusSucceeded, got.Status)
	require.Equal(t, store.CleanupStatusFailed, got.WorktreeCleanupStatus)
	require.Contains(t, got.WorktreeCleanupError, "dirty worktree")
}

func setupCleanupTask(t *testing.T, status store.TaskStatus, policy store.WorktreeCleanupPolicy, owned bool) (*store.Store, store.Task) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "pd.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	now := time.Now()
	cleanupStatus := store.CleanupStatusNotRequested
	if policy != store.CleanupPolicyNever {
		cleanupStatus = store.CleanupStatusPending
	}
	task := store.Task{ID: "pd-test", RepoPath: "/repo", RepoName: "repo", Branch: "pd/test", WorktreePath: "/wt", PromptSource: "arg", Prompt: "hello", PromptPreview: "hello", Status: status, WorktreeCleanupPolicy: policy, WorktreeCreatedByPD: owned, WorktreeCleanupStatus: cleanupStatus, CreatedAt: now, UpdatedAt: now}
	run := store.Run{ID: "run-test", TaskID: task.ID, Attempt: 1, Status: status, StartedAt: now, ControlSocketPath: "/sock", StdoutLogPath: "/stdout", StderrLogPath: "/stderr", PiEventsPath: "/events"}
	require.NoError(t, db.CreateTaskWithRun(context.Background(), task, run))
	return db, task
}

func withWorktreeClient(t *testing.T, fake worktreeClient) {
	t.Helper()
	oldNewWorktreeClient := newWorktreeClient
	newWorktreeClient = func() (worktreeClient, error) { return fake, nil }
	t.Cleanup(func() { newWorktreeClient = oldNewWorktreeClient })
}
