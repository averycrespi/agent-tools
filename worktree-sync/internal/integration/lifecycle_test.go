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

func forRepo(action string, args []string, options map[string]any) app.Request {
	if options == nil {
		options = make(map[string]any)
	}
	options["repo_id"] = "repo"
	return app.Request{Action: action, Args: args, Options: options}
}

func TestExplicitLifecycleStatusBranchSafetyCleanupAndUnregister(t *testing.T) {
	if os.Getenv("WTS_INTEGRATION") != "1" {
		t.Skip("set WTS_INTEGRATION=1")
	}
	base := t.TempDir()
	primary := filepath.Join(base, "repo")
	creationRoot := filepath.Join(base, "managed-worktrees")
	allowed := filepath.Join(base, "external-worktrees")
	require.NoError(t, os.Mkdir(primary, 0o700))
	require.NoError(t, os.Mkdir(creationRoot, 0o700))
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
	_, err := controller.Execute(context.Background(), app.Request{Action: "repo.add", Args: []string{primary}, Options: map[string]any{"root": creationRoot, "roots": []string{allowed}}})
	require.NoError(t, err)
	registered, err := config.Load(paths.Config)
	require.NoError(t, err)
	require.Equal(t, creationRoot, registered.Repositories[0].WorktreeCreationRoot)
	registered.Repositories[0].AllowedRoots = []string{allowed, creationRoot}
	require.NoError(t, config.Save(paths.Config, registered))
	expected := filepath.Join(creationRoot, "repo", "feature")
	pathOutput, err := controller.Execute(context.Background(), forRepo("worktree.path", []string{"feature"}, nil))
	require.NoError(t, err)
	require.Equal(t, expected, pathOutput)
	created, err := controller.Execute(context.Background(), forRepo("worktree.create", []string{"feature"}, map[string]any{"from": ""}))
	require.NoError(t, err)
	require.Equal(t, expected, created)
	{
		oldwd, cwdErr := os.Getwd()
		require.NoError(t, cwdErr)
		require.NoError(t, os.Chdir(created))
		inferred, inferErr := controller.Execute(context.Background(), app.Request{Action: "worktree.path", Args: []string{"feature"}, Options: map[string]any{}})
		require.NoError(t, os.Chdir(oldwd))
		require.NoError(t, inferErr)
		require.Equal(t, created, inferred)
	}
	alias := filepath.Join(base, "feature-alias")
	require.NoError(t, os.Symlink(created, alias))
	launchOutput, err := controller.Execute(context.Background(), forRepo("worktree.launch", []string{alias}, nil))
	require.NoError(t, err)
	require.Equal(t, "launch skipped: no launch command is configured", launchOutput)
	again, err := controller.Execute(context.Background(), forRepo("worktree.path", []string{"feature"}, nil))
	require.NoError(t, err)
	require.Equal(t, created, again)
	statusOne, err := controller.Execute(context.Background(), forRepo("status", nil, map[string]any{"json": true}))
	require.NoError(t, err)
	statusTwo, err := controller.Execute(context.Background(), forRepo("status", nil, map[string]any{"json": true}))
	require.NoError(t, err)
	require.Equal(t, statusOne, statusTwo)
	var status map[string]any
	require.NoError(t, json.Unmarshal([]byte(statusOne), &status))
	require.Equal(t, float64(1), status["version"])
	firstIdentity := statusWorktreeIdentity(t, statusOne, created)
	_, err = controller.Execute(context.Background(), forRepo("worktree.remove", []string{created}, nil))
	require.NoError(t, err)
	require.NoError(t, exec.Command("git", "-C", primary, "show-ref", "--verify", "--quiet", "refs/heads/feature").Run())
	created, err = controller.Execute(context.Background(), forRepo("worktree.create", []string{"feature"}, nil))
	require.NoError(t, err)
	recreatedStatus, err := controller.Execute(context.Background(), forRepo("status", nil, map[string]any{"json": true}))
	require.NoError(t, err)
	require.NotEqual(t, firstIdentity, statusWorktreeIdentity(t, recreatedStatus, created))
	_, err = controller.Execute(context.Background(), forRepo("worktree.remove", []string{created}, map[string]any{"delete_branch": true}))
	require.NoError(t, err)
	branchErr := exec.Command("git", "-C", primary, "show-ref", "--verify", "--quiet", "refs/heads/feature").Run()
	require.Error(t, branchErr)
	dirty, err := controller.Execute(context.Background(), forRepo("worktree.create", []string{"dirty"}, nil))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dirty, "dirty"), []byte("dirty"), 0o600))
	_, err = controller.Execute(context.Background(), forRepo("worktree.remove", []string{dirty}, nil))
	require.Error(t, err)
	_, err = controller.Execute(context.Background(), forRepo("worktree.remove", []string{dirty}, map[string]any{"force": true}))
	require.NoError(t, err)
	created, err = controller.Execute(context.Background(), forRepo("worktree.create", []string{"feature"}, nil))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(created, "unmerged"), []byte("commit"), 0o600))
	command(t, created, "git", "add", "unmerged")
	command(t, created, "git", "commit", "-qm", "unmerged")
	_, err = controller.Execute(context.Background(), forRepo("worktree.remove", []string{created}, map[string]any{"delete_branch": true}))
	require.ErrorContains(t, err, "branch deletion failed")
	created, err = controller.Execute(context.Background(), forRepo("worktree.create", []string{"feature"}, nil))
	require.NoError(t, err)
	_, err = controller.Execute(context.Background(), forRepo("worktree.remove", []string{created}, map[string]any{"force_delete_branch": true}))
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
	require.ErrorContains(t, inferErr, "not inside a registered Git worktree")
	require.ErrorContains(t, inferErr, "--repo-id")
	_, err = controller.Execute(context.Background(), forRepo("reconcile", nil, nil))
	require.NoError(t, err)
	command(t, "", "tmux", "-L", socket, "new-window", "-d", "-t", "wts-repo", "-n", "scratch", "-c", primary)
	cfg, err := config.Load(paths.Config)
	require.NoError(t, err)
	repo := cfg.Repositories[0]
	duplicateID := strings.TrimSpace(command(t, "", "tmux", "-L", socket, "new-session", "-d", "-P", "-F", "#{window_id}", "-s", "duplicate", "-n", "duplicate", "-c", primary))
	for key, value := range map[string]string{"@wts-schema": "1", "@wts-repository": repo.Identity(), "@wts-role": "base", "@wts-identity": repo.Identity()} {
		command(t, "", "tmux", "-L", socket, "set-option", "-w", "-t", duplicateID, key, value)
	}
	staleID := strings.TrimSpace(command(t, "", "tmux", "-L", socket, "new-window", "-d", "-P", "-F", "#{window_id}", "-t", "duplicate:1", "-n", "stale", "-c", primary))
	for key, value := range map[string]string{"@wts-schema": "1", "@wts-repository": repo.Identity(), "@wts-role": "worktree", "@wts-identity": "stale"} {
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
