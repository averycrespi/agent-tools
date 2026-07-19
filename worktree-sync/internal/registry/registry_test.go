package registry_test

import (
	"context"
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
