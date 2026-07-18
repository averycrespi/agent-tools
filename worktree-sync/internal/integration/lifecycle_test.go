//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/app"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/config"
	execclient "github.com/averycrespi/agent-tools/worktree-sync/internal/exec"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/service"
)

func TestExplicitLifecycleStatusBranchSafetyCleanupAndUnregister(t *testing.T) {
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
	socket := "wts-lifecycle-" + filepath.Base(base)
	controller := service.NewWithSocket(execclient.OSRunner{}, paths, socket)
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
	_, err := controller.Execute(context.Background(), app.Request{Action: "repo.add", Args: []string{primary}, Options: map[string]any{"roots": []string{allowed}}})
	require.NoError(t, err)
	expected := filepath.Join(allowed, "repo", "feature")
	pathOutput, err := controller.Execute(context.Background(), app.Request{Action: "worktree.path", Args: []string{"feature", "repo"}})
	require.NoError(t, err)
	require.Equal(t, expected, pathOutput)
	created, err := controller.Execute(context.Background(), app.Request{Action: "worktree.create", Args: []string{"feature", "repo"}, Options: map[string]any{"start": ""}})
	require.NoError(t, err)
	require.Equal(t, expected, created)
	again, err := controller.Execute(context.Background(), app.Request{Action: "worktree.path", Args: []string{"feature", "repo"}})
	require.NoError(t, err)
	require.Equal(t, created, again)
	statusOne, err := controller.Execute(context.Background(), app.Request{Action: "status", Args: []string{"repo"}, Options: map[string]any{"json": true}})
	require.NoError(t, err)
	statusTwo, err := controller.Execute(context.Background(), app.Request{Action: "status", Args: []string{"repo"}, Options: map[string]any{"json": true}})
	require.NoError(t, err)
	require.Equal(t, statusOne, statusTwo)
	var status map[string]any
	require.NoError(t, json.Unmarshal([]byte(statusOne), &status))
	require.Equal(t, float64(1), status["version"])
	_, err = controller.Execute(context.Background(), app.Request{Action: "worktree.remove", Args: []string{created, "repo"}, Options: map[string]any{}})
	require.NoError(t, err)
	require.NoError(t, exec.Command("git", "-C", primary, "show-ref", "--verify", "--quiet", "refs/heads/feature").Run())
	created, err = controller.Execute(context.Background(), app.Request{Action: "worktree.create", Args: []string{"feature", "repo"}, Options: map[string]any{}})
	require.NoError(t, err)
	_, err = controller.Execute(context.Background(), app.Request{Action: "worktree.remove", Args: []string{created, "repo"}, Options: map[string]any{"delete_branch": true}})
	require.NoError(t, err)
	branchErr := exec.Command("git", "-C", primary, "show-ref", "--verify", "--quiet", "refs/heads/feature").Run()
	require.Error(t, branchErr)
	dryRun, err := controller.Execute(context.Background(), app.Request{Action: "cleanup", Options: map[string]any{}})
	require.NoError(t, err)
	require.Contains(t, dryRun, "dry run; no changes applied")
	_, err = controller.Execute(context.Background(), app.Request{Action: "reconcile", Args: []string{"repo"}})
	require.NoError(t, err)
	command(t, "", "tmux", "-L", socket, "new-window", "-d", "-t", "wts-repo", "-n", "scratch", "-c", primary)
	_, err = controller.Execute(context.Background(), app.Request{Action: "repo.remove", Args: []string{"repo"}})
	require.NoError(t, err)
	cfg, err := config.Load(paths.Config)
	require.NoError(t, err)
	require.Empty(t, cfg.Repositories)
	windows := command(t, "", "tmux", "-L", socket, "list-windows", "-t", "wts-repo", "-F", "#{window_name}|#{@wts-role}")
	require.Contains(t, windows, "scratch|session")
	require.NotContains(t, windows, "base|base")
	require.False(t, strings.Contains(windows, "|worktree"))
}
