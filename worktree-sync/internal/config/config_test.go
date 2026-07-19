package config_test

import (
	"encoding/json"
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
	require.Equal(t, "/cfg", paths.ConfigHome)
	require.Equal(t, "/data", paths.DataHome)
	require.Equal(t, "/state", paths.StateHome)
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

func TestValidateRejectsInvalidDefaultWorktreeRoots(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	require.NoError(t, os.Mkdir(target, 0o700))
	link := filepath.Join(base, "link")
	require.NoError(t, os.Symlink(target, link))
	cfg := config.Default()
	cfg.Global.DefaultCreationRoot = link
	require.ErrorContains(t, cfg.Validate(), "default worktree creation root")

	cfg.Global.DefaultCreationRoot = ""
	cfg.Global.DefaultAllowedRoots = []string{link}
	require.ErrorContains(t, cfg.Validate(), "default allowed worktree root")

	cfg.Global.DefaultAllowedRoots = []string{target, target}
	require.ErrorContains(t, cfg.Validate(), "duplicate default allowed worktree root")
}

func TestValidateRejectsDuplicateIdentityAndUnsafeID(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.Repositories = []config.Repository{
		{ID: "safe", PrimaryRoot: root, CommonGitDir: filepath.Join(root, ".git"), WorktreeCreationRoot: root, AllowedRoots: []string{root}},
		{ID: "unsafe:name", PrimaryRoot: root, CommonGitDir: filepath.Join(root, ".git"), WorktreeCreationRoot: root, AllowedRoots: []string{root}},
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
	cfg.Repositories = []config.Repository{{ID: "repo", PrimaryRoot: root, CommonGitDir: common, WorktreeCreationRoot: root, AllowedRoots: []string{root}}}
	path := filepath.Join(root, "config.json")
	require.NoError(t, config.Save(path, cfg))
	loaded, err := config.Load(path)
	require.NoError(t, err)
	require.Equal(t, common, loaded.Repositories[0].Identity())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(data), "repository_identity")
}

func TestLoadRequiresExplicitRefreshForOlderVersions(t *testing.T) {
	for _, version := range []int{1, 2} {
		path := filepath.Join(t.TempDir(), "config.json")
		require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf(`{"version":%d}`, version)), 0o600))
		_, err := config.Load(path)
		require.ErrorContains(t, err, "wts config refresh")
	}
}

func TestLoadForRefreshMigratesVersionTwoCreationRoots(t *testing.T) {
	base := t.TempDir()
	first := filepath.Join(base, "first")
	second := filepath.Join(base, "second")
	primary := filepath.Join(base, "repo")
	common := filepath.Join(primary, ".git")
	for _, path := range []string{first, second, common} {
		require.NoError(t, os.MkdirAll(path, 0o700))
	}
	path := filepath.Join(base, "config.json")
	data := fmt.Sprintf(`{"version":2,"global":{"reconcile_interval":"30s","debounce":"250ms","command_timeout":"20s","default_allowed_worktree_roots":[%q,%q]},"repositories":[{"id":"repo","primary_root":%q,"common_git_dir":%q,"allowed_worktree_roots":[%q,%q],"setup_policy":"manual","launch_policy":"manual"}]}`, first, second, primary, common, second, first)
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))

	cfg, err := config.LoadForRefresh(path)
	require.NoError(t, err)
	encoded, err := json.Marshal(cfg)
	require.NoError(t, err)
	var migrated struct {
		Version int `json:"version"`
		Global  struct {
			CreationRoot string `json:"default_worktree_creation_root"`
		} `json:"global"`
		Repositories []struct {
			CreationRoot string   `json:"worktree_creation_root"`
			AllowedRoots []string `json:"allowed_worktree_roots"`
		} `json:"repositories"`
	}
	require.NoError(t, json.Unmarshal(encoded, &migrated))
	require.Equal(t, 3, migrated.Version)
	require.Equal(t, first, migrated.Global.CreationRoot)
	require.Equal(t, second, migrated.Repositories[0].CreationRoot)
	require.Equal(t, []string{second, first}, migrated.Repositories[0].AllowedRoots)
}

func TestLoadForRefreshRejectsUnknownVersionOneFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"version":1,"unexpected":true}`), 0o600))
	_, err := config.LoadForRefresh(path)
	require.ErrorContains(t, err, "unknown field")
}

func TestLoadForRefreshRejectsUnknownVersionTwoFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"version":2,"unexpected":true}`), 0o600))
	_, err := config.LoadForRefresh(path)
	require.ErrorContains(t, err, "unknown field")
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
	require.Equal(t, base, cfg.Repositories[0].WorktreeCreationRoot)
}

func TestLoadForRefreshRejectsMismatchedLegacyIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"version":1,"repositories":[{"id":"repo","common_git_dir":"/git/common","repository_identity":"/git/other","policy":{}}]}`), 0o600))
	_, err := config.LoadForRefresh(path)
	require.ErrorContains(t, err, "identity does not match")
}

func TestLoadForRefreshRejectsPassiveOnlyVersionOnePolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"version":1,"repositories":[{"id":"repo","policy":{"setup_passive":true}}]}`), 0o600))
	_, err := config.LoadForRefresh(path)
	require.ErrorContains(t, err, "cannot migrate")
}

func TestDecodeForDiagnosticsReturnsCurrentConfigWithoutRuntimeValidation(t *testing.T) {
	data := []byte(`{"version":3,"global":{"reconcile_interval":"30s","debounce":"250ms","command_timeout":"20s"},"repositories":[{"id":"repo","primary_root":"/missing","common_git_dir":"/missing/.git","worktree_creation_root":"/missing/worktrees","allowed_worktree_roots":["/missing/worktrees"],"setup_policy":"manual","launch_policy":"manual"}]}`)
	cfg, err := config.DecodeForDiagnostics(data)
	require.NoError(t, err)
	require.Equal(t, "repo", cfg.Repositories[0].ID)
	require.Error(t, cfg.Validate())
}

func TestVersionThreeRejectsLegacyPolicyField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"version":3,"global":{"reconcile_interval":"30s","debounce":"250ms","command_timeout":"20s"},"repositories":[{"policy":{}}]}`), 0o600))
	_, err := config.Load(path)
	require.ErrorContains(t, err, "unknown field")
}

func TestActionPolicyValidationAndDefaults(t *testing.T) {
	root := t.TempDir()
	common := filepath.Join(root, ".git")
	require.NoError(t, os.Mkdir(common, 0o700))
	cfg := config.Default()
	cfg.Repositories = []config.Repository{{ID: "repo", PrimaryRoot: root, CommonGitDir: common, WorktreeCreationRoot: root, AllowedRoots: []string{root}}}
	path := filepath.Join(root, "config.json")
	require.NoError(t, config.Save(path, cfg))
	loaded, err := config.Load(path)
	require.NoError(t, err)
	require.Equal(t, config.ActionManual, loaded.Repositories[0].SetupPolicy)
	require.Equal(t, config.ActionManual, loaded.Repositories[0].LaunchPolicy)
	loaded.Repositories[0].AllowedRoots = []string{common}
	require.ErrorContains(t, loaded.Validate(), "worktree_creation_root must be included")
	loaded.Repositories[0].AllowedRoots = []string{root}
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
