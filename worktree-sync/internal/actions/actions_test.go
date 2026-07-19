package actions_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/actions"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/config"
	"github.com/averycrespi/agent-tools/worktree-sync/internal/state"
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
	repo := config.Repository{RepositoryIdentity: "r", PrimaryRoot: primary, AllowedRoots: []string{worktree}, CopyActions: []config.CopyAction{{Source: "source", Destination: "existing"}}, Policy: config.Policy{SetupExplicit: true}}
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

func TestCopyRejectsReplacedWorktreeRootSymlink(t *testing.T) {
	primary := t.TempDir()
	allowed := t.TempDir()
	worktree := filepath.Join(allowed, "worktree")
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(primary, "source"), []byte("value"), 0o600))
	require.NoError(t, os.Mkdir(worktree, 0o700))
	require.NoError(t, os.Remove(worktree))
	require.NoError(t, os.Symlink(outside, worktree))
	repo := config.Repository{RepositoryIdentity: "r", PrimaryRoot: primary, AllowedRoots: []string{allowed}, CopyActions: []config.CopyAction{{Source: "source", Destination: "copied"}}, Policy: config.Policy{SetupExplicit: true}}
	result := actions.New(&runner{}, filepath.Join(t.TempDir(), "ledger.json"), time.Second).Run(context.Background(), repo, actions.Worktree{Path: worktree, Identity: "w"}, actions.Explicit, false)
	require.Error(t, result.Error)
	_, err := os.Stat(filepath.Join(outside, "copied"))
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

func TestLedgerLockPreventsConcurrentDuplicateExecution(t *testing.T) {
	ledger := filepath.Join(t.TempDir(), "ledger.json")
	r := &blockingActionRunner{started: make(chan struct{}), release: make(chan struct{})}
	repo := config.Repository{RepositoryIdentity: "r", PrimaryRoot: t.TempDir(), SetupActions: []config.SetupAction{{Argv: []string{"tool"}}}, Policy: config.Policy{SetupPassive: true}}
	worktree := actions.Worktree{Path: t.TempDir(), Identity: "w"}
	results := make(chan actions.Result, 2)
	for range 2 {
		go func() {
			results <- actions.New(r, ledger, time.Second).Run(context.Background(), repo, worktree, actions.Passive, false)
		}()
	}
	<-r.started
	close(r.release)
	first, second := <-results, <-results
	require.NoError(t, first.Error)
	require.NoError(t, second.Error)
	require.Equal(t, int32(1), r.calls.Load())
}

type blockingActionRunner struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (r *blockingActionRunner) RunEnv(context.Context, string, []string, string, ...string) ([]byte, error) {
	if r.calls.Add(1) == 1 {
		close(r.started)
		<-r.release
	}
	return nil, nil
}

func TestSetupFailureDoesNotExposeCommandOutput(t *testing.T) {
	manager := actions.New(&failingRunner{}, filepath.Join(t.TempDir(), "ledger.json"), time.Second)
	repo := config.Repository{RepositoryIdentity: "r", SetupActions: []config.SetupAction{{Argv: []string{"tool"}}}, Policy: config.Policy{SetupExplicit: true}}
	result := manager.Run(context.Background(), repo, actions.Worktree{Path: t.TempDir(), Identity: "w"}, actions.Explicit, false)
	require.Error(t, result.Error)
	require.NotContains(t, result.Error.Error(), "boom")
}

func TestLaunchPolicyLedgerAndRerun(t *testing.T) {
	ledger := filepath.Join(t.TempDir(), "ledger.json")
	repo := config.Repository{RepositoryIdentity: "r", LaunchCommand: "launch", Policy: config.Policy{LaunchExplicit: true}}
	manager := actions.New(&runner{}, ledger, time.Second)
	calls := 0
	launch := func() error { calls++; return nil }
	worktree := actions.Worktree{Path: t.TempDir(), Identity: "w"}
	require.NoError(t, manager.Launch(context.Background(), repo, worktree, actions.Passive, false, launch).Error)
	require.Zero(t, calls)
	require.NoError(t, manager.Launch(context.Background(), repo, worktree, actions.Explicit, false, launch).Error)
	require.Equal(t, 1, calls)
	skipped := manager.Launch(context.Background(), repo, worktree, actions.Explicit, false, launch)
	require.True(t, skipped.Skipped)
	require.Equal(t, "already attempted", skipped.Reason)
	require.Equal(t, 1, calls)
	require.NoError(t, manager.Launch(context.Background(), repo, worktree, actions.Explicit, true, launch).Error)
	require.Equal(t, 2, calls)
}

func TestLaunchReportsWhyItWasSkipped(t *testing.T) {
	manager := actions.New(&runner{}, filepath.Join(t.TempDir(), "ledger.json"), time.Second)
	worktree := actions.Worktree{Path: t.TempDir(), Identity: "w"}
	disabled := manager.Launch(context.Background(), config.Repository{LaunchCommand: "launch"}, worktree, actions.Explicit, false, func() error { return nil })
	require.Equal(t, "disabled by policy", disabled.Reason)
	notConfigured := manager.Launch(context.Background(), config.Repository{Policy: config.Policy{LaunchExplicit: true}}, worktree, actions.Explicit, false, func() error { return nil })
	require.Equal(t, "no launch command is configured", notConfigured.Reason)
}

func TestPruneRemovesAttemptsForWorktreesNoLongerLive(t *testing.T) {
	ledger := filepath.Join(t.TempDir(), "ledger.json")
	repo := config.Repository{RepositoryIdentity: "r", SetupActions: []config.SetupAction{{Argv: []string{"tool"}}}, Policy: config.Policy{SetupExplicit: true}}
	manager := actions.New(&runner{}, ledger, time.Second)
	for _, identity := range []string{"removed", "live"} {
		require.NoError(t, manager.Run(context.Background(), repo, actions.Worktree{Path: t.TempDir(), Identity: identity}, actions.Explicit, false).Error)
	}
	repo.SetupActions = []config.SetupAction{{Argv: []string{"changed"}}}
	require.NoError(t, manager.Prune(context.Background(), repo, map[string]bool{"live": true}))
	stored, err := state.LoadLedger(ledger)
	require.NoError(t, err)
	require.Empty(t, stored.Attempts, "removed identities and obsolete action digests should be pruned")
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
