package daemon_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/app"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/config"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/daemon"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/state"
)

type controller struct{ calls chan string }

func (c controller) Execute(_ context.Context, request app.Request) (string, error) {
	c.calls <- request.Action
	return "ok", nil
}

func TestConfigEventResetsReconcileIntervalImmediately(t *testing.T) {
	base := t.TempDir()
	paths := config.Paths{Config: filepath.Join(base, "config", "config.json"), State: filepath.Join(base, "state"), Worktrees: filepath.Join(base, "data", "worktrees")}
	cfg := config.Default()
	cfg.Global.ReconcileInterval = "1h"
	cfg.Global.Debounce = "5ms"
	require.NoError(t, config.Save(paths.Config, cfg))
	calls := make(chan string, 8)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- daemon.Run(ctx, paths, controller{calls: calls}, nil) }()
	require.Equal(t, "reconcile", receive(t, calls))
	cfg.Global.ReconcileInterval = "20ms"
	require.NoError(t, config.Save(paths.Config, cfg))
	require.Equal(t, "reconcile", receive(t, calls))
	require.Equal(t, "reconcile", receive(t, calls))
	cancel()
	require.NoError(t, <-done)
}

func receive(t *testing.T, calls <-chan string) string {
	t.Helper()
	select {
	case value := <-calls:
		return value
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for reconcile")
		return ""
	}
}

func TestConfigReloadRebuildsAllowedRootWatch(t *testing.T) {
	base := t.TempDir()
	paths := config.Paths{Config: filepath.Join(base, "config", "config.json"), State: filepath.Join(base, "state"), Worktrees: filepath.Join(base, "data", "worktrees")}
	cfg := config.Default()
	cfg.Global.ReconcileInterval, cfg.Global.Debounce = "1h", "5ms"
	require.NoError(t, config.Save(paths.Config, cfg))
	calls := make(chan string, 16)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- daemon.Run(ctx, paths, controller{calls: calls}, nil) }()
	require.Equal(t, "reconcile", receive(t, calls))
	primary, common, allowed := filepath.Join(base, "repo"), filepath.Join(base, "repo", ".git"), filepath.Join(base, "allowed")
	require.NoError(t, os.MkdirAll(common, 0o700))
	require.NoError(t, os.Mkdir(allowed, 0o700))
	nested := filepath.Join(allowed, "existing-parent")
	require.NoError(t, os.Mkdir(nested, 0o700))
	cfg.Repositories = []config.Repository{{ID: "repo", PrimaryRoot: primary, CommonGitDir: common, AllowedRoots: []string{allowed}}}
	require.NoError(t, config.Save(paths.Config, cfg))
	require.Equal(t, "reconcile", receive(t, calls))
drain:
	for {
		select {
		case <-calls:
		case <-time.After(20 * time.Millisecond):
			break drain
		}
	}
	require.NoError(t, os.WriteFile(filepath.Join(nested, "event"), []byte("event"), 0o600))
	require.Equal(t, "reconcile", receive(t, calls))
	cancel()
	require.NoError(t, <-done)
}

func TestRunRejectsSecondDaemonForStateDirectory(t *testing.T) {
	base := t.TempDir()
	paths := config.Paths{Config: filepath.Join(base, "config", "config.json"), State: filepath.Join(base, "state"), Worktrees: filepath.Join(base, "data", "worktrees")}
	require.NoError(t, config.Save(paths.Config, config.Default()))
	lock, err := state.Acquire(context.Background(), filepath.Join(paths.State, "daemon.lock"))
	require.NoError(t, err)
	defer func() { require.NoError(t, lock.Unlock()) }()
	err = daemon.Run(context.Background(), paths, controller{calls: make(chan string, 1)}, nil)
	require.ErrorContains(t, err, "another wtsd")
}

func TestRunPerformsStartupReconcileAndStopsWithContext(t *testing.T) {
	base := t.TempDir()
	paths := config.Paths{Config: filepath.Join(base, "config", "config.json"), State: filepath.Join(base, "state"), Worktrees: filepath.Join(base, "data", "worktrees")}
	require.NoError(t, config.Save(paths.Config, config.Default()))
	calls := make(chan string, 2)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- daemon.Run(ctx, paths, controller{calls: calls}, nil) }()
	select {
	case action := <-calls:
		require.Equal(t, "reconcile", action)
	case <-time.After(time.Second):
		t.Fatal("startup reconcile did not run")
	}
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("daemon did not stop")
	}
}
