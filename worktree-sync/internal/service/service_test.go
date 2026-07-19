package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/app"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/config"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/service"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/state"
)

type editorRunner struct {
	replacement []byte
	seen        []byte
	err         error
}

func (*editorRunner) Run(context.Context, string, string, ...string) ([]byte, error) { return nil, nil }
func (*editorRunner) RunEnv(context.Context, string, []string, string, ...string) ([]byte, error) {
	return nil, nil
}
func (r *editorRunner) Interactive(_ context.Context, _ string, _ string, args ...string) error {
	path := args[len(args)-1]
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	r.seen = data
	if r.err != nil {
		return r.err
	}
	if r.replacement == nil {
		return nil
	}
	return os.WriteFile(path, r.replacement, 0o600)
}

type doctorRunner struct{ launchdLoaded bool }

func (r doctorRunner) Run(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
	switch name {
	case "git":
		return []byte("git version test\n"), nil
	case "tmux":
		if len(args) == 1 && args[0] == "-V" {
			return []byte("tmux test\n"), nil
		}
		return []byte("no server running"), errors.New("missing")
	case "launchctl":
		if r.launchdLoaded {
			return []byte("state = running"), nil
		}
		return []byte("Could not find service"), errors.New("missing")
	default:
		return nil, fmt.Errorf("unexpected command %s", name)
	}
}
func (doctorRunner) RunEnv(context.Context, string, []string, string, ...string) ([]byte, error) {
	return nil, errors.New("unexpected RunEnv")
}
func (doctorRunner) Interactive(context.Context, string, string, ...string) error {
	return errors.New("unexpected interactive command")
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

func TestDoctorFreshInstallHasFixedOrderedChecksAndNoMutation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := t.TempDir()
	paths := config.Paths{ConfigHome: filepath.Join(base, "cfg"), DataHome: filepath.Join(base, "data"), StateHome: filepath.Join(base, "state-home"), Config: filepath.Join(base, "cfg", "worktree-sync", "config.json"), State: filepath.Join(base, "state-home", "worktree-sync"), Worktrees: filepath.Join(base, "data", "worktree-sync", "worktrees")}
	output, err := service.New(doctorRunner{}, paths).Execute(context.Background(), app.Request{Action: "doctor", Options: map[string]any{"json": true}})
	require.NoError(t, err)
	var report struct {
		Version int `json:"version"`
		Checks  []struct {
			ID       string   `json:"id"`
			Status   string   `json:"status"`
			Details  []string `json:"details"`
			Recovery string   `json:"recovery"`
		} `json:"checks"`
	}
	require.NoError(t, json.Unmarshal([]byte(output), &report))
	require.Equal(t, 1, report.Version)
	ids := make([]string, 0, len(report.Checks))
	for _, check := range report.Checks {
		ids = append(ids, check.ID)
		require.NotNil(t, check.Details)
	}
	expected := []string{"tools.git", "tools.tmux", "paths.resolved", "config.file", "config.syntax", "config.version", "config.runtime", "state.directory", "state.action_ledger", "state.provenance", "tmux.snapshot"}
	if runtime.GOOS == "darwin" {
		expected = append(expected, "launchd.plist", "launchd.lifecycle", "launchd.environment")
	} else {
		expected = append(expected, "launchd.support")
	}
	require.Equal(t, expected, ids)
	require.NoFileExists(t, paths.Config)
	require.NoDirExists(t, paths.State)
}

func TestDoctorEmitsSkippedRepositoryChecksWhenRuntimeConfigIsInvalid(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := t.TempDir()
	paths := config.Paths{ConfigHome: filepath.Join(base, "cfg"), DataHome: filepath.Join(base, "data"), StateHome: filepath.Join(base, "state-home"), Config: filepath.Join(base, "cfg", "worktree-sync", "config.json"), State: filepath.Join(base, "state-home", "worktree-sync"), Worktrees: filepath.Join(base, "data", "worktree-sync", "worktrees")}
	require.NoError(t, os.MkdirAll(filepath.Dir(paths.Config), 0o700))
	data := `{"version":3,"global":{"reconcile_interval":"30s","debounce":"250ms","command_timeout":"20s"},"repositories":[{"id":"repo","primary_root":"/missing","common_git_dir":"/missing/.git","worktree_creation_root":"/missing/worktrees","allowed_worktree_roots":["/missing/worktrees"],"setup_policy":"manual","launch_policy":"manual"}]}`
	require.NoError(t, os.WriteFile(paths.Config, []byte(data), 0o600))
	output, err := service.New(doctorRunner{}, paths).Execute(context.Background(), app.Request{Action: "doctor", Options: map[string]any{"json": true}})
	require.ErrorContains(t, err, "doctor found errors")
	var report struct {
		Checks []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"checks"`
	}
	require.NoError(t, json.Unmarshal([]byte(output), &report))
	statuses := make(map[string]string)
	for _, check := range report.Checks {
		statuses[check.ID] = check.Status
	}
	require.Equal(t, "error", statuses["config.runtime"])
	for _, id := range []string{"repository.repo.primary", "repository.repo.common_git", "repository.repo.creation_root", "repository.repo.allowed_root.0", "repository.repo.git_snapshot", "tmux.ownership.repo"} {
		require.Equal(t, "skipped", statuses[id], id)
	}
}

func TestDoctorDetectsLoadedLaunchAgentWithoutOwnedPlist(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("LaunchAgent checks run only on macOS")
	}
	t.Setenv("HOME", t.TempDir())
	base := t.TempDir()
	paths := config.Paths{Config: filepath.Join(base, "cfg", "worktree-sync", "config.json"), State: filepath.Join(base, "state", "worktree-sync"), Worktrees: filepath.Join(base, "data", "worktree-sync", "worktrees")}
	output, err := service.New(doctorRunner{launchdLoaded: true}, paths).Execute(context.Background(), app.Request{Action: "doctor", Options: map[string]any{"json": true}})
	require.ErrorContains(t, err, "doctor found errors")
	require.Contains(t, output, "owned plist is missing")
}

func TestDoctorReportsInvalidConfigAndContinuesIndependentChecksWithoutMutation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := t.TempDir()
	paths := config.Paths{ConfigHome: filepath.Join(base, "cfg"), DataHome: filepath.Join(base, "data"), StateHome: filepath.Join(base, "state-home"), Config: filepath.Join(base, "cfg", "worktree-sync", "config.json"), State: filepath.Join(base, "state-home", "worktree-sync"), Worktrees: filepath.Join(base, "data", "worktree-sync", "worktrees")}
	require.NoError(t, os.MkdirAll(filepath.Dir(paths.Config), 0o700))
	require.NoError(t, os.WriteFile(paths.Config, []byte(`{"version":`), 0o600))

	output, err := service.New(doctorRunner{}, paths).Execute(context.Background(), app.Request{Action: "doctor", Options: map[string]any{"json": true}})
	require.ErrorContains(t, err, "doctor found errors")
	var report struct {
		Version int `json:"version"`
		Checks  []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"checks"`
	}
	require.NoError(t, json.Unmarshal([]byte(output), &report))
	require.Equal(t, 1, report.Version)
	statuses := make(map[string]string)
	for _, check := range report.Checks {
		statuses[check.ID] = check.Status
	}
	require.Equal(t, "ok", statuses["tools.git"])
	require.Equal(t, "ok", statuses["tools.tmux"])
	require.Equal(t, "error", statuses["config.syntax"])
	require.Equal(t, "skipped", statuses["config.version"])
	require.Equal(t, "skipped", statuses["config.runtime"])
	require.NoDirExists(t, paths.State)
}

func TestStatusCheckReportsCorruptActionLedgerInVersionTwoJSON(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "repo")
	common := filepath.Join(primary, ".git")
	allowed := filepath.Join(base, "allowed")
	require.NoError(t, os.MkdirAll(common, 0o700))
	require.NoError(t, os.Mkdir(allowed, 0o700))
	paths := config.Paths{Config: filepath.Join(base, "config.json"), State: filepath.Join(base, "state"), Worktrees: filepath.Join(base, "worktrees")}
	cfg := config.Default()
	cfg.Repositories = []config.Repository{{ID: "repo", PrimaryRoot: primary, CommonGitDir: common, WorktreeCreationRoot: allowed, AllowedRoots: []string{allowed}, SetupPolicy: config.ActionManual, LaunchPolicy: config.ActionManual}}
	require.NoError(t, config.Save(paths.Config, cfg))
	require.NoError(t, os.MkdirAll(paths.State, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(paths.State, "actions.json"), []byte(`{"version":1,"attempts":{"{}":{"success":false,"attempted":"2026-01-01T00:00:00Z"}}}`), 0o600))

	output, err := service.New(&safetyRunner{primary: primary}, paths).Execute(context.Background(), app.Request{Action: "status", Options: map[string]any{"all": true, "json": true, "check": true}})
	require.ErrorContains(t, err, "status check failed")
	var document map[string]any
	require.NoError(t, json.Unmarshal([]byte(output), &document))
	require.Equal(t, float64(2), document["version"])
	diagnostics := document["diagnostics"].([]any)
	require.Equal(t, "action_ledger_unavailable", diagnostics[0].(map[string]any)["code"])
	daemon := document["daemon"].(map[string]any)
	require.Contains(t, []any{"running", "stopped", "not_installed", "unsupported", "unavailable"}, daemon["state"])
	repositories := document["repositories"].([]any)
	require.Len(t, repositories, 1)
	repo := repositories[0].(map[string]any)
	for _, key := range []string{"id", "health", "diagnostics", "desired_worktrees", "actual_managed_windows", "conflicts", "reported_worktrees", "prunable_worktrees", "action_failures"} {
		require.Contains(t, repo, key)
	}
	desired := repo["desired_worktrees"].([]any)
	require.Len(t, desired, 1)
	require.Equal(t, true, desired[0].(map[string]any)["eligible"])
}

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

func TestConfigEditInitializesMissingConfigForEditor(t *testing.T) {
	t.Setenv("EDITOR", "editor")
	base := t.TempDir()
	paths := config.Paths{Config: filepath.Join(base, "config", "config.json"), State: filepath.Join(base, "state"), Worktrees: filepath.Join(base, "data", "worktrees")}
	runner := &editorRunner{}
	output, err := service.New(runner, paths).Execute(context.Background(), app.Request{Action: "config.edit"})
	require.NoError(t, err)
	require.Equal(t, "configuration updated and valid", output)
	var edited config.Config
	require.NoError(t, json.Unmarshal(runner.seen, &edited))
	require.Equal(t, config.Version, edited.Version)
	_, err = config.Load(paths.Config)
	require.NoError(t, err)
}

func TestConfigEditAlwaysSavesThenReportsInvalidConfiguration(t *testing.T) {
	t.Setenv("EDITOR", "editor")
	base := t.TempDir()
	paths := config.Paths{Config: filepath.Join(base, "config", "config.json"), State: filepath.Join(base, "state"), Worktrees: filepath.Join(base, "data", "worktrees")}
	require.NoError(t, config.Save(paths.Config, config.Default()))
	replacement := []byte(`{"version":999}`)
	controller := service.New(&editorRunner{replacement: replacement}, paths)
	output, err := controller.Execute(context.Background(), app.Request{Action: "config.edit"})
	require.ErrorContains(t, err, "invalid")
	require.Contains(t, output, "updated but is invalid: unsupported config version 999")
	after, readErr := os.ReadFile(paths.Config)
	require.NoError(t, readErr)
	require.Equal(t, replacement, after)
	info, statErr := os.Stat(paths.Config)
	require.NoError(t, statErr)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestConfigEditOpensInvalidRawBytesAndEditorFailurePreservesThem(t *testing.T) {
	t.Setenv("EDITOR", "editor")
	base := t.TempDir()
	paths := config.Paths{Config: filepath.Join(base, "config", "config.json"), State: filepath.Join(base, "state"), Worktrees: filepath.Join(base, "data", "worktrees")}
	require.NoError(t, os.MkdirAll(filepath.Dir(paths.Config), 0o700))
	invalid := []byte(`{"version":`)
	require.NoError(t, os.WriteFile(paths.Config, invalid, 0o600))
	runner := &editorRunner{err: errors.New("editor canceled")}
	_, err := service.New(runner, paths).Execute(context.Background(), app.Request{Action: "config.edit"})
	require.ErrorContains(t, err, "editor canceled")
	require.Equal(t, invalid, runner.seen)
	live, readErr := os.ReadFile(paths.Config)
	require.NoError(t, readErr)
	require.Equal(t, invalid, live)

	valid, marshalErr := json.Marshal(config.Default())
	require.NoError(t, marshalErr)
	repair := &editorRunner{replacement: valid}
	output, err := service.New(repair, paths).Execute(context.Background(), app.Request{Action: "config.edit"})
	require.NoError(t, err)
	require.Equal(t, "configuration updated and valid", output)
	require.Equal(t, invalid, repair.seen)
	_, err = config.Load(paths.Config)
	require.NoError(t, err)
}
