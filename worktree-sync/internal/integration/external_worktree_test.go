//go:build integration

package integration_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/app"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/config"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/daemon"
	execclient "github.com/averycrespi/agent-tools/worktree-sync/internal/exec"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/service"
)

func command(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	return string(output)
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatal("condition not satisfied before deadline")
		case <-ticker.C:
			if condition() {
				return
			}
		}
	}
}

func TestDaemonDetectsOrdinaryGitWorktreeAndPreservesScratch(t *testing.T) {
	if os.Getenv("WTS_INTEGRATION") != "1" {
		t.Skip("set WTS_INTEGRATION=1")
	}
	base := t.TempDir()
	primary := filepath.Join(base, "repo")
	allowed := filepath.Join(base, "worktrees")
	require.NoError(t, os.Mkdir(primary, 0o700))
	require.NoError(t, os.Mkdir(allowed, 0o700))
	command(t, primary, "git", "init", "-q")
	command(t, primary, "git", "config", "user.email", "test@example.com")
	command(t, primary, "git", "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(filepath.Join(primary, "file"), []byte("test"), 0o600))
	command(t, primary, "git", "add", "file")
	command(t, primary, "git", "commit", "-qm", "initial")
	paths := config.Paths{Config: filepath.Join(base, "config", "config.json"), State: filepath.Join(base, "state"), Worktrees: filepath.Join(base, "data", "worktrees")}
	socket := "wts-test-" + filepath.Base(base)
	controller := service.NewWithSocket(execclient.OSRunner{}, paths, socket)
	_, err := controller.Execute(context.Background(), app.Request{Action: "repo.add", Args: []string{primary}, Options: map[string]any{"roots": []string{allowed}}})
	require.NoError(t, err)
	cfg, err := config.Load(paths.Config)
	require.NoError(t, err)
	cfg.Global.ReconcileInterval = "100ms"
	cfg.Global.Debounce = "20ms"
	require.NoError(t, config.Save(paths.Config, cfg))
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go func() { done <- daemon.Run(ctx, paths, controller, logger) }()
	windows := func() string {
		cmd := exec.Command("tmux", "-L", socket, "list-windows", "-t", "wts-repo", "-F", "#{window_name}|#{@wts-role}|#{pane_current_path}")
		output, _ := cmd.CombinedOutput()
		return string(output)
	}
	waitFor(t, func() bool { return strings.Contains(windows(), "base|base|"+primary) })
	linked := filepath.Join(allowed, "external")
	command(t, primary, "git", "worktree", "add", "-q", "-b", "external", linked)
	waitFor(t, func() bool { return strings.Contains(windows(), "|worktree|"+linked) })
	command(t, "", "tmux", "-L", socket, "new-window", "-d", "-t", "wts-repo", "-n", "scratch", "-c", primary)
	command(t, primary, "git", "worktree", "remove", linked)
	waitFor(t, func() bool {
		output := windows()
		return !strings.Contains(output, "|worktree|"+linked) && strings.Contains(output, "scratch")
	})
	finalWindows := windows()
	require.Contains(t, finalWindows, "scratch|session|"+primary, finalWindows)
	windowRole := command(t, "", "tmux", "-L", socket, "show-options", "-w", "-qv", "-t", "wts-repo:scratch", "@wts-role")
	require.Empty(t, strings.TrimSpace(windowRole))
	cancel()
	select {
	case daemonErr := <-done:
		require.True(t, daemonErr == nil || errors.Is(daemonErr, context.Canceled))
	case <-time.After(time.Second):
		t.Fatal("daemon did not stop")
	}
}
