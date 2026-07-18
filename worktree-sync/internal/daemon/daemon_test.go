package daemon_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/app"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/config"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/daemon"
)

type controller struct{ calls chan string }

func (c controller) Execute(_ context.Context, request app.Request) (string, error) {
	c.calls <- request.Action
	return "ok", nil
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
