package service_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/app"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/config"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/service"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/state"
)

type editorRunner struct{ replacement []byte }

func (*editorRunner) Run(context.Context, string, string, ...string) ([]byte, error) { return nil, nil }
func (*editorRunner) RunEnv(context.Context, string, []string, string, ...string) ([]byte, error) {
	return nil, nil
}
func (r *editorRunner) Interactive(_ context.Context, _ string, _ string, args ...string) error {
	return os.WriteFile(args[len(args)-1], r.replacement, 0o600)
}

type safetyRunner struct {
	calls   [][]string
	primary string
}

func (r *safetyRunner) Run(_ context.Context, dir, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{dir, name}, args...))
	if name == "tmux" {
		return []byte("no server running"), errors.New("missing")
	}
	if name == "git" && strings.Join(args, " ") == "worktree list --porcelain -z" {
		return []byte("worktree " + r.primary + "\x00HEAD abc\x00branch refs/heads/main\x00\x00"), nil
	}
	return nil, fmt.Errorf("unexpected command %s %v", name, args)
}
func (*safetyRunner) RunEnv(context.Context, string, []string, string, ...string) ([]byte, error) {
	return nil, nil
}
func (*safetyRunner) Interactive(context.Context, string, string, ...string) error { return nil }

func TestCleanupDryRunNeverInvokesGitPrune(t *testing.T) {
	base := t.TempDir()
	primary, common, allowed := filepath.Join(base, "repo"), filepath.Join(base, "repo", ".git"), filepath.Join(base, "allowed")
	require.NoError(t, os.MkdirAll(common, 0o700))
	require.NoError(t, os.Mkdir(allowed, 0o700))
	paths := config.Paths{Config: filepath.Join(base, "config.json"), State: filepath.Join(base, "state"), Worktrees: filepath.Join(base, "worktrees")}
	cfg := config.Default()
	cfg.Repositories = []config.Repository{{ID: "repo", PrimaryRoot: primary, CommonGitDir: common, WorktreeCreationRoot: allowed, AllowedRoots: []string{allowed}}}
	require.NoError(t, config.Save(paths.Config, cfg))
	runner := &safetyRunner{primary: primary}
	output, err := service.New(runner, paths).Execute(context.Background(), app.Request{Action: "cleanup", Options: map[string]any{}})
	require.NoError(t, err)
	require.Contains(t, output, "dry run")
	for _, call := range runner.calls {
		require.NotContains(t, call, "prune")
	}
}

func TestReconcileWaitsForSharedOperationLock(t *testing.T) {
	base := t.TempDir()
	paths := config.Paths{Config: filepath.Join(base, "config.json"), State: filepath.Join(base, "state"), Worktrees: filepath.Join(base, "worktrees")}
	require.NoError(t, config.Save(paths.Config, config.Default()))
	lock, err := state.Acquire(context.Background(), filepath.Join(paths.State, "operation.lock"))
	require.NoError(t, err)
	defer func() { require.NoError(t, lock.Unlock()) }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = service.New(&editorRunner{}, paths).Execute(ctx, app.Request{Action: "reconcile"})
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestRepositoryRootCommandsPersistFocusedChanges(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "repo")
	common := filepath.Join(primary, ".git")
	creation := filepath.Join(base, "creation")
	newCreation := filepath.Join(base, "new-creation")
	extra := filepath.Join(base, "extra")
	for _, path := range []string{common, creation, newCreation, extra} {
		require.NoError(t, os.MkdirAll(path, 0o700))
	}
	paths := config.Paths{Config: filepath.Join(base, "config.json"), State: filepath.Join(base, "state"), Worktrees: filepath.Join(base, "worktrees")}
	cfg := config.Default()
	cfg.Repositories = []config.Repository{{ID: "repo", PrimaryRoot: primary, CommonGitDir: common, WorktreeCreationRoot: creation, AllowedRoots: []string{creation}, SetupPolicy: config.ActionManual, LaunchPolicy: config.ActionManual}}
	require.NoError(t, config.Save(paths.Config, cfg))
	controller := service.New(&safetyRunner{primary: primary}, paths)

	output, err := controller.Execute(context.Background(), app.Request{Action: "repo.roots.set-creation", Args: []string{newCreation}, Options: map[string]any{"repo_id": "repo"}})
	require.NoError(t, err)
	require.Contains(t, output, "daemon will reconcile")
	_, err = controller.Execute(context.Background(), app.Request{Action: "repo.roots.add-allowed", Args: []string{extra}, Options: map[string]any{"repo_id": "repo"}})
	require.NoError(t, err)
	_, err = controller.Execute(context.Background(), app.Request{Action: "repo.roots.remove-allowed", Args: []string{creation}, Options: map[string]any{"repo_id": "repo"}})
	require.NoError(t, err)

	updated, err := config.Load(paths.Config)
	require.NoError(t, err)
	require.Equal(t, newCreation, updated.Repositories[0].WorktreeCreationRoot)
	require.Equal(t, []string{newCreation, extra}, updated.Repositories[0].AllowedRoots)
	output, err = controller.Execute(context.Background(), app.Request{Action: "repo.roots.show", Options: map[string]any{"repo_id": "repo"}})
	require.NoError(t, err)
	require.Contains(t, output, "creation\t"+newCreation)
	require.Contains(t, output, "allowed\t"+extra)
}

func TestRemoveRejectsConflictingBranchDeletionFlags(t *testing.T) {
	controller := service.New(&editorRunner{}, config.Paths{})
	_, err := controller.Execute(context.Background(), app.Request{Action: "worktree.remove", Args: []string{"feature"}, Options: map[string]any{"delete_branch": true, "force_delete_branch": true}})
	require.ErrorContains(t, err, "choose only one")
}

func TestConfigRefreshCreatesValidatedDefaults(t *testing.T) {
	base := t.TempDir()
	paths := config.Paths{Config: filepath.Join(base, "config", "config.json"), State: filepath.Join(base, "state"), Worktrees: filepath.Join(base, "data", "worktrees")}
	controller := service.New(&editorRunner{}, paths)
	_, err := controller.Execute(context.Background(), app.Request{Action: "config.refresh"})
	require.NoError(t, err)
	cfg, err := config.Load(paths.Config)
	require.NoError(t, err)
	require.Equal(t, config.Default().Global, cfg.Global)
}

func TestConfigRefreshMigratesVersionTwoCreationRoots(t *testing.T) {
	base := t.TempDir()
	primary, common, allowed := filepath.Join(base, "repo"), filepath.Join(base, "repo", ".git"), filepath.Join(base, "worktrees")
	require.NoError(t, os.MkdirAll(common, 0o700))
	require.NoError(t, os.Mkdir(allowed, 0o700))
	paths := config.Paths{Config: filepath.Join(base, "config", "config.json"), State: filepath.Join(base, "state"), Worktrees: filepath.Join(base, "data", "worktrees")}
	require.NoError(t, os.MkdirAll(filepath.Dir(paths.Config), 0o700))
	versionTwo := fmt.Sprintf(`{"version":2,"global":{"reconcile_interval":"30s","debounce":"250ms","command_timeout":"20s","default_allowed_worktree_roots":[%q]},"repositories":[{"id":"repo","primary_root":%q,"common_git_dir":%q,"allowed_worktree_roots":[%q],"setup_policy":"manual","launch_policy":"manual"}]}`, allowed, primary, common, allowed)
	require.NoError(t, os.WriteFile(paths.Config, []byte(versionTwo), 0o600))

	_, err := service.New(&editorRunner{}, paths).Execute(context.Background(), app.Request{Action: "config.refresh"})
	require.NoError(t, err)
	cfg, err := config.Load(paths.Config)
	require.NoError(t, err)
	require.Equal(t, config.Version, cfg.Version)
	require.Equal(t, allowed, cfg.Global.DefaultCreationRoot)
	require.Equal(t, allowed, cfg.Repositories[0].WorktreeCreationRoot)
}

func TestConfigRefreshMigratesPoliciesWithoutReplacingRepositories(t *testing.T) {
	base := t.TempDir()
	primary, common, allowed := filepath.Join(base, "repo"), filepath.Join(base, "repo", ".git"), filepath.Join(base, "worktrees")
	require.NoError(t, os.MkdirAll(common, 0o700))
	require.NoError(t, os.Mkdir(allowed, 0o700))
	paths := config.Paths{Config: filepath.Join(base, "config", "config.json"), State: filepath.Join(base, "state"), Worktrees: filepath.Join(base, "data", "worktrees")}
	require.NoError(t, os.MkdirAll(filepath.Dir(paths.Config), 0o700))
	legacy := fmt.Sprintf(`{"version":1,"global":{},"repositories":[{"id":"repo","primary_root":%q,"common_git_dir":%q,"repository_identity":%q,"allowed_worktree_roots":[%q],"policy":{"setup_explicit":true,"launch_explicit":true}}]}`, primary, common, common, allowed)
	require.NoError(t, os.WriteFile(paths.Config, []byte(legacy), 0o600))
	controller := service.New(&editorRunner{}, paths)
	_, err := controller.Execute(context.Background(), app.Request{Action: "config.refresh"})
	require.NoError(t, err)
	cfg, err := config.Load(paths.Config)
	require.NoError(t, err)
	require.Equal(t, config.Default().Global, cfg.Global)
	require.Equal(t, "repo", cfg.Repositories[0].ID)
	require.Equal(t, config.ActionWTSCreated, cfg.Repositories[0].SetupPolicy)
	require.Equal(t, config.ActionWTSCreated, cfg.Repositories[0].LaunchPolicy)
	require.Equal(t, allowed, cfg.Repositories[0].WorktreeCreationRoot)
	data, err := os.ReadFile(paths.Config)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"policy"`)
	require.NotContains(t, string(data), "repository_identity")
}

func TestConfigEditLeavesLiveFileUnchangedWhenEditedCopyIsInvalid(t *testing.T) {
	t.Setenv("EDITOR", "editor")
	base := t.TempDir()
	paths := config.Paths{Config: filepath.Join(base, "config", "config.json"), State: filepath.Join(base, "state"), Worktrees: filepath.Join(base, "data", "worktrees")}
	require.NoError(t, config.Save(paths.Config, config.Default()))
	before, err := os.ReadFile(paths.Config)
	require.NoError(t, err)
	controller := service.New(&editorRunner{replacement: []byte(`{"version":999}`)}, paths)
	_, err = controller.Execute(context.Background(), app.Request{Action: "config.edit"})
	require.ErrorContains(t, err, "invalid")
	after, readErr := os.ReadFile(paths.Config)
	require.NoError(t, readErr)
	require.Equal(t, before, after)
}
