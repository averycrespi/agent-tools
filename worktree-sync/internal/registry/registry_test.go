package registry_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/config"
	gitclient "github.com/averycrespi/agent-tools/worktree-sync/internal/git"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/registry"
)

type inspector struct {
	info gitclient.Repository
	err  error
}

func (i inspector) InspectPrimary(context.Context, string) (gitclient.Repository, error) {
	return i.info, i.err
}

func TestAddUsesConfiguredDefaultWorktreeRoots(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "repo")
	common := filepath.Join(primary, ".git")
	creation := filepath.Join(base, "managed")
	first := filepath.Join(base, "worktrees-one")
	second := filepath.Join(base, "worktrees-two")
	for _, path := range []string{common, creation, first, second} {
		require.NoError(t, os.MkdirAll(path, 0o700))
	}
	configPath := filepath.Join(base, "config.json")
	data := fmt.Sprintf(`{"version":3,"global":{"reconcile_interval":"30s","debounce":"250ms","command_timeout":"20s","default_worktree_creation_root":%q,"default_allowed_worktree_roots":[%q,%q]},"repositories":[]}`, creation, first, second)
	require.NoError(t, os.WriteFile(configPath, []byte(data), 0o600))
	cfg, err := config.Load(configPath)
	require.NoError(t, err)

	fallback := filepath.Join(base, "fallback")
	service := registry.New(inspector{info: gitclient.Repository{PrimaryRoot: primary, CommonGitDir: common}}, config.Paths{Worktrees: fallback})
	_, repo, err := service.Add(context.Background(), cfg, registry.AddOptions{Path: primary})
	require.NoError(t, err)
	require.Equal(t, creation, repo.WorktreeCreationRoot)
	require.Equal(t, []string{creation, first, second}, repo.AllowedRoots)
	_, err = os.Stat(fallback)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestAddCanExcludeConfiguredDefaultAllowedRoots(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "repo")
	common := filepath.Join(primary, ".git")
	creation := filepath.Join(base, "creation")
	additional := filepath.Join(base, "additional")
	for _, path := range []string{common, creation, additional} {
		require.NoError(t, os.MkdirAll(path, 0o700))
	}
	cfg := config.Default()
	cfg.Global.DefaultCreationRoot = creation
	cfg.Global.DefaultAllowedRoots = []string{additional}
	service := registry.New(inspector{info: gitclient.Repository{PrimaryRoot: primary, CommonGitDir: common}}, config.Paths{})
	_, repo, err := service.Add(context.Background(), cfg, registry.AddOptions{Path: primary, NoDefaultAllowedRoots: true})
	require.NoError(t, err)
	require.Equal(t, []string{creation}, repo.AllowedRoots)
}

func TestAddExplicitRootsOverrideConfiguredDefaultsAndDeduplicate(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "repo")
	common := filepath.Join(primary, ".git")
	configuredCreation := filepath.Join(base, "configured-creation")
	configuredAllowed := filepath.Join(base, "configured-allowed")
	explicitCreation := filepath.Join(base, "explicit-creation")
	explicitAllowed := filepath.Join(base, "explicit-allowed")
	for _, path := range []string{common, configuredCreation, configuredAllowed, explicitCreation, explicitAllowed} {
		require.NoError(t, os.MkdirAll(path, 0o700))
	}
	alias := filepath.Join(base, "explicit-alias")
	require.NoError(t, os.Symlink(explicitAllowed, alias))
	cfg := config.Default()
	cfg.Global.DefaultCreationRoot = configuredCreation
	cfg.Global.DefaultAllowedRoots = []string{configuredAllowed}
	service := registry.New(inspector{info: gitclient.Repository{PrimaryRoot: primary, CommonGitDir: common}}, config.Paths{Worktrees: filepath.Join(base, "fallback")})

	_, repo, err := service.Add(context.Background(), cfg, registry.AddOptions{Path: primary, CreationRoot: explicitCreation, AllowedRoots: []string{explicitAllowed, alias}})
	require.NoError(t, err)
	require.Equal(t, explicitCreation, repo.WorktreeCreationRoot)
	require.Equal(t, []string{explicitCreation, explicitAllowed}, repo.AllowedRoots)
}

func TestAddCreatesPrivateDefaultRootAndRequiresExplicitIDCollision(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "same")
	common := filepath.Join(primary, ".git")
	require.NoError(t, os.MkdirAll(common, 0o700))
	paths := config.Paths{Config: filepath.Join(base, "cfg", "config.json"), Worktrees: filepath.Join(base, "data", "worktrees"), State: filepath.Join(base, "state")}
	cfg := config.Default()
	service := registry.New(inspector{info: gitclient.Repository{PrimaryRoot: primary, CommonGitDir: common}}, paths)

	updated, repo, err := service.Add(context.Background(), cfg, registry.AddOptions{Path: primary})
	require.NoError(t, err)
	require.Equal(t, "same", repo.ID)
	require.Equal(t, paths.Worktrees, repo.WorktreeCreationRoot)
	require.Equal(t, []string{paths.Worktrees}, repo.AllowedRoots)
	info, err := os.Stat(paths.Worktrees)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())

	other := filepath.Join(base, "other", "same")
	otherGit := filepath.Join(other, ".git")
	require.NoError(t, os.MkdirAll(otherGit, 0o700))
	service = registry.New(inspector{info: gitclient.Repository{PrimaryRoot: other, CommonGitDir: otherGit}}, paths)
	_, _, err = service.Add(context.Background(), updated, registry.AddOptions{Path: other})
	require.ErrorContains(t, err, "--id")
}

func TestAddRejectsDuplicateIdentityAndMissingExplicitRoot(t *testing.T) {
	base := t.TempDir()
	common := filepath.Join(base, ".git")
	require.NoError(t, os.Mkdir(common, 0o700))
	info := gitclient.Repository{PrimaryRoot: base, CommonGitDir: common}
	service := registry.New(inspector{info: info}, config.Paths{Worktrees: filepath.Join(base, "default")})
	cfg := config.Default()
	cfg.Repositories = []config.Repository{{ID: "existing", PrimaryRoot: base, CommonGitDir: common, AllowedRoots: []string{base}}}
	_, _, err := service.Add(context.Background(), cfg, registry.AddOptions{Path: base, ID: "other", AllowedRoots: []string{filepath.Join(base, "missing")}})
	require.ErrorContains(t, err, "already registered")

	cfg.Repositories = nil
	_, _, err = service.Add(context.Background(), cfg, registry.AddOptions{Path: base, ID: "other", AllowedRoots: []string{filepath.Join(base, "missing")}})
	require.Error(t, err)
	_, statErr := os.Stat(filepath.Join(base, "missing"))
	require.ErrorIs(t, statErr, os.ErrNotExist)
}
