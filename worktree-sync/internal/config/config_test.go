package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/config"
)

func TestPathsUseWorktreeSyncXDGNamespaces(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/cfg")
	t.Setenv("XDG_DATA_HOME", "/data")
	t.Setenv("XDG_STATE_HOME", "/state")

	paths, err := config.PathsFromEnv()
	require.NoError(t, err)
	require.Equal(t, "/cfg/worktree-sync/config.json", paths.Config)
	require.Equal(t, "/data/worktree-sync/worktrees", paths.Worktrees)
	require.Equal(t, "/state/worktree-sync", paths.State)
}

func TestCanonicalContainmentIsComponentAware(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "worktrees", "one")
	outside := filepath.Join(root, "worktrees-other", "one")
	require.NoError(t, os.MkdirAll(inside, 0o700))
	require.NoError(t, os.MkdirAll(outside, 0o700))

	canonicalRoot, err := config.CanonicalExisting(filepath.Join(root, "worktrees"))
	require.NoError(t, err)
	canonicalInside, err := config.CanonicalExisting(inside)
	require.NoError(t, err)
	canonicalOutside, err := config.CanonicalExisting(outside)
	require.NoError(t, err)
	require.True(t, config.Contains(canonicalRoot, canonicalInside))
	require.False(t, config.Contains(canonicalRoot, canonicalOutside))
}

func TestCanonicalContainmentRejectsSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	require.NoError(t, os.Mkdir(root, 0o700))
	require.NoError(t, os.Mkdir(outside, 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(outside, "child"), 0o700))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape")))
	canonicalRoot, err := config.CanonicalExisting(root)
	require.NoError(t, err)
	escaped, err := config.CanonicalExisting(filepath.Join(root, "escape", "child"))
	require.NoError(t, err)
	require.False(t, config.Contains(canonicalRoot, escaped))
}

func TestValidateRejectsDuplicateIdentityAndUnsafeID(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.Repositories = []config.Repository{
		{ID: "safe", PrimaryRoot: root, CommonGitDir: filepath.Join(root, ".git"), AllowedRoots: []string{root}},
		{ID: "unsafe:name", PrimaryRoot: root, CommonGitDir: filepath.Join(root, ".git"), AllowedRoots: []string{root}},
	}

	err := cfg.Validate()
	require.ErrorContains(t, err, "repository ID")

	cfg.Repositories[1].ID = "other"
	err = cfg.Validate()
	require.ErrorContains(t, err, "duplicate repository identity")
}

func TestRepositoryIdentityIsDerivedFromCommonGitDirectory(t *testing.T) {
	root := t.TempDir()
	common := filepath.Join(root, ".git")
	require.NoError(t, os.Mkdir(common, 0o700))
	cfg := config.Default()
	cfg.Repositories = []config.Repository{{ID: "repo", PrimaryRoot: root, CommonGitDir: common, AllowedRoots: []string{root}}}
	path := filepath.Join(root, "config.json")
	require.NoError(t, config.Save(path, cfg))
	loaded, err := config.Load(path)
	require.NoError(t, err)
	require.Equal(t, common, loaded.Repositories[0].Identity())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(data), "repository_identity")
}

func TestLoadRequiresExplicitRefreshForVersionOne(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"version":1}`), 0o600))
	_, err := config.Load(path)
	require.ErrorContains(t, err, "wts config refresh")
}

func TestLoadForRefreshMigratesVersionOnePolicies(t *testing.T) {
	base := t.TempDir()
	common := filepath.Join(base, ".git")
	require.NoError(t, os.Mkdir(common, 0o700))
	path := filepath.Join(base, "config.json")
	data := fmt.Sprintf(`{
  "version": 1,
  "global": {"reconcile_interval":"30s","debounce":"250ms","command_timeout":"20s"},
  "repositories": [
    {"id":"none","primary_root":%q,"common_git_dir":%q,"repository_identity":%q,"allowed_worktree_roots":[%q],"policy":{}},
    {"id":"created","primary_root":%q,"common_git_dir":%q,"repository_identity":%q,"allowed_worktree_roots":[%q],"policy":{"setup_explicit":true,"launch_explicit":true}},
    {"id":"all","primary_root":%q,"common_git_dir":%q,"repository_identity":%q,"allowed_worktree_roots":[%q],"policy":{"setup_explicit":true,"setup_passive":true,"launch_explicit":true,"launch_passive":true}}
  ]
}`, base, common, common, base, base, common, common, base, base, common, common, base)
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
	cfg, err := config.LoadForRefresh(path)
	require.NoError(t, err)
	require.Equal(t, config.Version, cfg.Version)
	require.Equal(t, config.ActionNone, cfg.Repositories[0].SetupPolicy)
	require.Equal(t, config.ActionWTSCreated, cfg.Repositories[1].SetupPolicy)
	require.Equal(t, config.ActionWTSCreated, cfg.Repositories[1].LaunchPolicy)
	require.Equal(t, config.ActionAll, cfg.Repositories[2].SetupPolicy)
	require.Equal(t, config.ActionAll, cfg.Repositories[2].LaunchPolicy)
}

func TestLoadForRefreshRejectsPassiveOnlyVersionOnePolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"version":1,"repositories":[{"id":"repo","policy":{"setup_passive":true}}]}`), 0o600))
	_, err := config.LoadForRefresh(path)
	require.ErrorContains(t, err, "cannot migrate")
}

func TestVersionTwoRejectsLegacyPolicyField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"version":2,"global":{"reconcile_interval":"30s","debounce":"250ms","command_timeout":"20s"},"repositories":[{"policy":{}}]}`), 0o600))
	_, err := config.Load(path)
	require.ErrorContains(t, err, "unknown field")
}

func TestActionPolicyValidationAndDefaults(t *testing.T) {
	root := t.TempDir()
	common := filepath.Join(root, ".git")
	require.NoError(t, os.Mkdir(common, 0o700))
	cfg := config.Default()
	cfg.Repositories = []config.Repository{{ID: "repo", PrimaryRoot: root, CommonGitDir: common, AllowedRoots: []string{root}}}
	path := filepath.Join(root, "config.json")
	require.NoError(t, config.Save(path, cfg))
	loaded, err := config.Load(path)
	require.NoError(t, err)
	require.Equal(t, config.ActionManual, loaded.Repositories[0].SetupPolicy)
	require.Equal(t, config.ActionManual, loaded.Repositories[0].LaunchPolicy)
	loaded.Repositories[0].SetupPolicy = "sometimes"
	require.ErrorContains(t, loaded.Validate(), "setup_policy")
}

func TestSaveAtomicPreservesValidConfigOnValidationFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := config.Default()
	require.NoError(t, config.Save(path, cfg))
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	cfg.Global.CommandTimeout = "0s"
	require.Error(t, config.Save(path, cfg))
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}
