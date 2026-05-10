package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	adconfig "github.com/averycrespi/agent-tools/agent-dispatch/internal/config"
	"github.com/averycrespi/agent-tools/agent-dispatch/internal/control"
	"github.com/averycrespi/agent-tools/agent-dispatch/internal/store"
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

func TestTaskAndRunReconciledMarksAttachStaleTaskUnknown(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ad.db")
	oldCfg := cfg
	cfg = adconfig.Config{DatabasePath: dbPath}
	defer func() { cfg = oldCfg }()

	db, err := store.Open(dbPath)
	require.NoError(t, err)
	now := time.Now()
	task := store.Task{ID: "ad-test", RepoPath: "/repo", RepoName: "repo", Branch: "ad/test", WorktreePath: "/wt", PromptSource: "arg", Prompt: "hello", PromptPreview: "hello", Status: store.StatusRunning, CreatedAt: now, UpdatedAt: now}
	run := store.Run{ID: "run-test", TaskID: task.ID, Attempt: 1, SupervisorPID: 4242, Status: store.StatusRunning, StartedAt: now, ControlSocketPath: filepath.Join(t.TempDir(), "missing.sock"), StdoutLogPath: "/stdout", StderrLogPath: "/stderr", SupervisorLogPath: "/supervisor", PiEventsPath: "/events"}
	require.NoError(t, db.CreateTaskWithRun(context.Background(), task, run))
	require.NoError(t, db.Close())

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	got, _, err := taskAndRunReconciled(cmd, task.ID, func(int) bool { return false })
	require.NoError(t, err)
	require.Equal(t, store.StatusUnknown, got.Status)
}
