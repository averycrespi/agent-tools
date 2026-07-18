package actions_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/actions"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/config"
)

type runner struct {
	calls int
	env   []string
	cwd   string
}

func (r *runner) RunEnv(_ context.Context, dir string, env []string, _ string, _ ...string) ([]byte, error) {
	r.calls++
	r.env = env
	r.cwd = dir
	return nil, nil
}

func TestCopyDoesNotOverwriteAndRejectsDestinationSymlinkEscape(t *testing.T) {
	primary := t.TempDir()
	worktree := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(primary, "source"), []byte("new"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(worktree, "existing"), []byte("old"), 0o600))
	repo := config.Repository{RepositoryIdentity: "r", PrimaryRoot: primary, CopyActions: []config.CopyAction{{Source: "source", Destination: "existing"}}, Policy: config.Policy{SetupExplicit: true}}
	manager := actions.New(&runner{}, filepath.Join(t.TempDir(), "ledger.json"), time.Second)
	result := manager.Run(context.Background(), repo, actions.Worktree{Path: worktree, Identity: "w"}, actions.Explicit, false)
	require.NoError(t, result.Error)
	data, err := os.ReadFile(filepath.Join(worktree, "existing"))
	require.NoError(t, err)
	require.Equal(t, "old", string(data))

	require.NoError(t, os.Symlink(outside, filepath.Join(worktree, "escape")))
	repo.CopyActions = []config.CopyAction{{Source: "source", Destination: "escape/file"}}
	result = manager.Run(context.Background(), repo, actions.Worktree{Path: worktree, Identity: "w2"}, actions.Explicit, false)
	require.ErrorContains(t, result.Error, "escape")
	_, err = os.Stat(filepath.Join(outside, "file"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestFailedSetupIsPersistedAndNotRetriedUntilRerun(t *testing.T) {
	primary := t.TempDir()
	worktree := t.TempDir()
	ledger := filepath.Join(t.TempDir(), "ledger.json")
	r := &failingRunner{}
	repo := config.Repository{RepositoryIdentity: "r", PrimaryRoot: primary, SetupActions: []config.SetupAction{{Argv: []string{"false"}}}, Policy: config.Policy{SetupPassive: true}}
	manager := actions.New(r, ledger, time.Second)
	first := manager.Run(context.Background(), repo, actions.Worktree{Path: worktree, Identity: "w"}, actions.Passive, false)
	require.Error(t, first.Error)
	require.Equal(t, 1, r.calls)
	second := manager.Run(context.Background(), repo, actions.Worktree{Path: worktree, Identity: "w"}, actions.Passive, false)
	require.True(t, second.Skipped)
	require.Equal(t, 1, r.calls)
	third := manager.Run(context.Background(), repo, actions.Worktree{Path: worktree, Identity: "w"}, actions.Passive, true)
	require.Error(t, third.Error)
	require.Equal(t, 2, r.calls)
}

type failingRunner struct{ calls int }

func (r *failingRunner) RunEnv(context.Context, string, []string, string, ...string) ([]byte, error) {
	r.calls++
	return []byte("boom"), context.Canceled
}

func TestPassivePolicyDefaultsDisabledAndEnvironmentOverridesAreExplicit(t *testing.T) {
	r := &runner{}
	repo := config.Repository{RepositoryIdentity: "r", PrimaryRoot: t.TempDir(), SetupActions: []config.SetupAction{{Argv: []string{"tool", "arg"}, Env: map[string]string{"WTS_TEST": "yes"}}}}
	manager := actions.New(r, filepath.Join(t.TempDir(), "ledger.json"), time.Second)
	result := manager.Run(context.Background(), repo, actions.Worktree{Path: t.TempDir(), Identity: "w"}, actions.Passive, false)
	require.True(t, result.Skipped)
	require.Zero(t, r.calls)
	repo.Policy.SetupExplicit = true
	result = manager.Run(context.Background(), repo, actions.Worktree{Path: t.TempDir(), Identity: "w2"}, actions.Explicit, false)
	require.NoError(t, result.Error)
	require.Equal(t, 1, r.calls)
	require.Contains(t, r.env, "WTS_TEST=yes")
}
