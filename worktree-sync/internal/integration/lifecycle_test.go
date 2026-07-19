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
	firstIdentity := statusWorktreeIdentity(t, statusOne, created)
	_, err = controller.Execute(context.Background(), app.Request{Action: "worktree.remove", Args: []string{created, "repo"}, Options: map[string]any{}})
	require.NoError(t, err)
	require.NoError(t, exec.Command("git", "-C", primary, "show-ref", "--verify", "--quiet", "refs/heads/feature").Run())
	created, err = controller.Execute(context.Background(), app.Request{Action: "worktree.create", Args: []string{"feature", "repo"}, Options: map[string]any{}})
	require.NoError(t, err)
	recreatedStatus, err := controller.Execute(context.Background(), app.Request{Action: "status", Args: []string{"repo"}, Options: map[string]any{"json": true}})
	require.NoError(t, err)
	require.NotEqual(t, firstIdentity, statusWorktreeIdentity(t, recreatedStatus, created))
	_, err = controller.Execute(context.Background(), app.Request{Action: "worktree.remove", Args: []string{created, "repo"}, Options: map[string]any{"delete_branch": true}})
	require.NoError(t, err)
	branchErr := exec.Command("git", "-C", primary, "show-ref", "--verify", "--quiet", "refs/heads/feature").Run()
	require.Error(t, branchErr)
	dirty, err := controller.Execute(context.Background(), app.Request{Action: "worktree.create", Args: []string{"dirty", "repo"}, Options: map[string]any{}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dirty, "dirty"), []byte("dirty"), 0o600))
	_, err = controller.Execute(context.Background(), app.Request{Action: "worktree.remove", Args: []string{dirty, "repo"}, Options: map[string]any{}})
	require.Error(t, err)
	_, err = controller.Execute(context.Background(), app.Request{Action: "worktree.remove", Args: []string{dirty, "repo"}, Options: map[string]any{"force": true}})
	require.NoError(t, err)
	created, err = controller.Execute(context.Background(), app.Request{Action: "worktree.create", Args: []string{"feature", "repo"}, Options: map[string]any{}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(created, "unmerged"), []byte("commit"), 0o600))
	command(t, created, "git", "add", "unmerged")
	command(t, created, "git", "commit", "-qm", "unmerged")
	_, err = controller.Execute(context.Background(), app.Request{Action: "worktree.remove", Args: []string{created, "repo"}, Options: map[string]any{"delete_branch": true}})
	require.ErrorContains(t, err, "branch deletion failed")
	created, err = controller.Execute(context.Background(), app.Request{Action: "worktree.create", Args: []string{"feature", "repo"}, Options: map[string]any{}})
	require.NoError(t, err)
	_, err = controller.Execute(context.Background(), app.Request{Action: "worktree.remove", Args: []string{created, "repo"}, Options: map[string]any{"force_delete_branch": true}})
	require.NoError(t, err)
	require.Error(t, exec.Command("git", "-C", primary, "show-ref", "--verify", "--quiet", "refs/heads/feature").Run())
	prunable := filepath.Join(allowed, "prunable")
	command(t, primary, "git", "worktree", "add", "-q", "-b", "prunable", prunable)
	require.NoError(t, os.RemoveAll(prunable))
	dryRun, err := controller.Execute(context.Background(), app.Request{Action: "cleanup", Options: map[string]any{}})
	require.NoError(t, err)
	require.Contains(t, dryRun, "dry run; no changes applied")
	require.Contains(t, command(t, primary, "git", "worktree", "list", "--porcelain"), prunable)
	pruneOutput, err := controller.Execute(context.Background(), app.Request{Action: "cleanup", Options: map[string]any{"prune_git": "repo"}})
	require.NoError(t, err)
	require.Contains(t, pruneOutput, "before=1 after=0")
	oldwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(allowed))
	_, inferErr := controller.Execute(context.Background(), app.Request{Action: "worktree.path", Args: []string{"unregistered-location"}})
	require.NoError(t, os.Chdir(oldwd))
	require.ErrorContains(t, inferErr, "does not identify")
	_, err = controller.Execute(context.Background(), app.Request{Action: "reconcile", Args: []string{"repo"}})
	require.NoError(t, err)
	command(t, "", "tmux", "-L", socket, "new-window", "-d", "-t", "wts-repo", "-n", "scratch", "-c", primary)
	cfg, err := config.Load(paths.Config)
	require.NoError(t, err)
	repo := cfg.Repositories[0]
	duplicateID := strings.TrimSpace(command(t, "", "tmux", "-L", socket, "new-session", "-d", "-P", "-F", "#{window_id}", "-s", "duplicate", "-n", "duplicate", "-c", primary))
	for key, value := range map[string]string{"@wts-schema": "1", "@wts-repository": repo.RepositoryIdentity, "@wts-role": "base", "@wts-identity": repo.RepositoryIdentity} {
		command(t, "", "tmux", "-L", socket, "set-option", "-w", "-t", duplicateID, key, value)
	}
	staleID := strings.TrimSpace(command(t, "", "tmux", "-L", socket, "new-window", "-d", "-P", "-F", "#{window_id}", "-t", "duplicate:1", "-n", "stale", "-c", primary))
	for key, value := range map[string]string{"@wts-schema": "1", "@wts-repository": repo.RepositoryIdentity, "@wts-role": "worktree", "@wts-identity": "stale"} {
		command(t, "", "tmux", "-L", socket, "set-option", "-w", "-t", staleID, key, value)
	}
	_, err = controller.Execute(context.Background(), app.Request{Action: "cleanup", Options: map[string]any{"remove_orphaned_tmux": "repo"}})
	require.NoError(t, err)
	duplicateWindows := command(t, "", "tmux", "-L", socket, "list-windows", "-t", "duplicate", "-F", "#{window_id}")
	require.Contains(t, duplicateWindows, duplicateID)
	require.NotContains(t, duplicateWindows, staleID)
	_, err = controller.Execute(context.Background(), app.Request{Action: "repo.remove", Args: []string{"repo"}})
	require.NoError(t, err)
	cfg, err = config.Load(paths.Config)
	require.NoError(t, err)
	require.Empty(t, cfg.Repositories)
	windows := command(t, "", "tmux", "-L", socket, "list-windows", "-t", "wts-repo", "-F", "#{window_name}|#{@wts-role}")
	require.Contains(t, windows, "scratch|session")
	require.NotContains(t, windows, "base|base")
	require.False(t, strings.Contains(windows, "|worktree"))
}

func statusWorktreeIdentity(t *testing.T, document, path string) string {
	t.Helper()
	var status struct {
		Repositories []struct {
			Desired []struct {
				Path     string `json:"path"`
				Identity string `json:"identity"`
			} `json:"desired_worktrees"`
		} `json:"repositories"`
	}
	require.NoError(t, json.Unmarshal([]byte(document), &status))
	for _, repo := range status.Repositories {
		for _, worktree := range repo.Desired {
			if worktree.Path == path {
				return worktree.Identity
			}
		}
	}
	t.Fatalf("status did not contain %s", path)
	return ""
}
