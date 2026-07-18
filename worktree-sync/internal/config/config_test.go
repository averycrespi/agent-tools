package config_test

import (
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
