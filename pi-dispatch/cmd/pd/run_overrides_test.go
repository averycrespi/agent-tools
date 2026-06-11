package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	pdconfig "github.com/averycrespi/agent-tools/pi-dispatch/internal/config"
	pdexec "github.com/averycrespi/agent-tools/pi-dispatch/internal/exec"
	"github.com/averycrespi/agent-tools/pi-dispatch/internal/store"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestRunEnvMetadataPersistsKeysOnly(t *testing.T) {
	env, err := parseRunEnv([]string{"OPENAI_API_KEY=secret", "EMPTY=", "OPENAI_API_KEY=new secret"})
	require.NoError(t, err)

	namesJSON, err := runEnvNamesMetadata(env)

	require.NoError(t, err)
	require.JSONEq(t, `["OPENAI_API_KEY","EMPTY"]`, namesJSON)
	require.NotContains(t, namesJSON, "secret")
}

func TestParseRunEnvRejectsInvalidNames(t *testing.T) {
	_, err := parseRunEnv([]string{"BAD-NAME=value"})
	require.ErrorContains(t, err, "invalid env var name")
}

func TestRunLaunchMetadataRendersEffectiveOptions(t *testing.T) {
	agent := pdconfig.AgentOptions{Provider: "openai", Model: "gpt-5", Thinking: "high", Tools: []string{"bash"}, SystemPrompt: "secret system prompt"}

	agentOptionsJSON, piArgvJSON, err := runLaunchMetadata(agent)

	require.NoError(t, err)
	require.JSONEq(t, `{"provider":"openai","model":"gpt-5","thinking":"high","tools":["bash"],"system_prompt":"secret system prompt"}`, agentOptionsJSON)
	require.JSONEq(t, `["pi","--mode","rpc","--provider","openai","--model","gpt-5","--thinking","high","--tools","bash","--system-prompt","secret system prompt"]`, piArgvJSON)
}

func TestEffectiveWorktreeCleanupPolicyUsesOverrideOverConfig(t *testing.T) {
	got, err := effectiveWorktreeCleanupPolicy("on-success", "on-terminal")

	require.NoError(t, err)
	require.Equal(t, store.CleanupPolicyOnTerminal, got)
}

func TestEffectiveWorktreeCleanupPolicyDefaultsToNever(t *testing.T) {
	got, err := effectiveWorktreeCleanupPolicy("", "")

	require.NoError(t, err)
	require.Equal(t, store.CleanupPolicyNever, got)
}

func TestRunTaskPersistsCleanupPolicyAndOwnership(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pd.db")
	stateDir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateDir)
	oldCfg := cfg
	cfg = pdconfig.Config{DatabasePath: dbPath, DefaultWorktreeCleanupPolicy: "on-success"}
	t.Cleanup(func() { cfg = oldCfg })
	oldRunCleanupPolicy := runCleanupPolicy
	runCleanupPolicy = "on-terminal"
	t.Cleanup(func() { runCleanupPolicy = oldRunCleanupPolicy })
	oldRunRepo := runRepo
	runRepo = "/repo"
	t.Cleanup(func() { runRepo = oldRunRepo })
	oldRunBranch := runBranch
	runBranch = "pd/test"
	t.Cleanup(func() { runBranch = oldRunBranch })

	runner := &fakeRunRunner{repoRoot: "/repo", branch: "main", pid: 4242}
	oldNewRunner := newRunner
	newRunner = func() pdexec.Runner { return runner }
	t.Cleanup(func() { newRunner = oldNewRunner })
	withWorktreeClient(t, &fakeRunWorktree{path: "/worktrees/pd-test", created: true})
	oldNewSandboxClient := newSandboxClient
	newSandboxClient = func() (sandboxClient, error) { return &fakeRunSandbox{}, nil }
	t.Cleanup(func() { newSandboxClient = oldNewSandboxClient })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	require.NoError(t, runTask(cmd, []string{"hello"}))

	st, err := store.Open(dbPath)
	require.NoError(t, err)
	defer st.Close() //nolint:errcheck
	tasks, err := st.ListTasks(context.Background())
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, store.CleanupPolicyOnTerminal, tasks[0].WorktreeCleanupPolicy)
	require.True(t, tasks[0].WorktreeCreatedByPD)
	require.Equal(t, store.CleanupStatusPending, tasks[0].WorktreeCleanupStatus)
}

func TestRunTaskMarksFailedWhenSupervisorLaunchFails(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pd.db")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	oldCfg := cfg
	cfg = pdconfig.Config{DatabasePath: dbPath, DefaultWorktreeCleanupPolicy: "never"}
	t.Cleanup(func() { cfg = oldCfg })
	oldRunRepo := runRepo
	runRepo = "/repo"
	t.Cleanup(func() { runRepo = oldRunRepo })
	oldRunBranch := runBranch
	runBranch = "pd/test"
	t.Cleanup(func() { runBranch = oldRunBranch })

	runner := &fakeRunRunner{repoRoot: "/repo", branch: "main", startErr: errReleaseFailed}
	oldNewRunner := newRunner
	newRunner = func() pdexec.Runner { return runner }
	t.Cleanup(func() { newRunner = oldNewRunner })
	withWorktreeClient(t, &fakeRunWorktree{path: "/worktrees/pd-test", created: true})
	oldNewSandboxClient := newSandboxClient
	newSandboxClient = func() (sandboxClient, error) { return &fakeRunSandbox{}, nil }
	t.Cleanup(func() { newSandboxClient = oldNewSandboxClient })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	err := runTask(cmd, []string{"hello"})

	require.ErrorIs(t, err, errReleaseFailed)
	st, openErr := store.Open(dbPath)
	require.NoError(t, openErr)
	defer st.Close() //nolint:errcheck
	tasks, listErr := st.ListTasks(context.Background())
	require.NoError(t, listErr)
	require.Len(t, tasks, 1)
	require.Equal(t, store.StatusFailed, tasks[0].Status)
	run, latestErr := st.LatestRun(context.Background(), tasks[0].ID)
	require.NoError(t, latestErr)
	require.Equal(t, store.StatusFailed, run.Status)
	require.Equal(t, errReleaseFailed.Error(), run.ErrorMessage)
}

func TestRunTaskDoesNotCleanupWhenSupervisorMayBeLive(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "pd.db")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	oldCfg := cfg
	cfg = pdconfig.Config{DatabasePath: dbPath, DefaultWorktreeCleanupPolicy: "on-terminal"}
	t.Cleanup(func() { cfg = oldCfg })
	oldRunCleanupPolicy := runCleanupPolicy
	runCleanupPolicy = ""
	t.Cleanup(func() { runCleanupPolicy = oldRunCleanupPolicy })
	oldRunRepo := runRepo
	runRepo = "/repo"
	t.Cleanup(func() { runRepo = oldRunRepo })
	oldRunBranch := runBranch
	runBranch = "pd/test"
	t.Cleanup(func() { runBranch = oldRunBranch })

	runner := &fakeRunRunner{repoRoot: "/repo", branch: "main", pid: 4242, startErr: errReleaseFailed}
	oldNewRunner := newRunner
	newRunner = func() pdexec.Runner { return runner }
	t.Cleanup(func() { newRunner = oldNewRunner })
	fakeWT := &fakeRunWorktree{path: "/worktrees/pd-test", created: true}
	withWorktreeClient(t, fakeWT)
	oldNewSandboxClient := newSandboxClient
	newSandboxClient = func() (sandboxClient, error) { return &fakeRunSandbox{}, nil }
	t.Cleanup(func() { newSandboxClient = oldNewSandboxClient })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	err := runTask(cmd, []string{"hello"})

	require.ErrorIs(t, err, errReleaseFailed)
	require.Empty(t, fakeWT.removedRepo)
	st, openErr := store.Open(dbPath)
	require.NoError(t, openErr)
	defer st.Close() //nolint:errcheck
	tasks, listErr := st.ListTasks(context.Background())
	require.NoError(t, listErr)
	require.Len(t, tasks, 1)
	require.Equal(t, store.CleanupStatusPending, tasks[0].WorktreeCleanupStatus)
}

func TestApplyRunOverrides(t *testing.T) {
	old := runAgentOverrides
	runAgentOverrides = pdconfig.AgentOptions{Model: "gpt-5", Thinking: "high", Tools: []string{"bash"}, Extensions: []string{"x.ts"}, DisableAllTools: true, SessionDir: "/tmp/pi"}
	defer func() { runAgentOverrides = old }()

	got := applyRunOverrides(pdconfig.AgentOptions{Provider: "anthropic", Model: "claude", Thinking: "medium"})

	require.Equal(t, "anthropic", got.Provider)
	require.Equal(t, "gpt-5", got.Model)
	require.Equal(t, "high", got.Thinking)
	require.Equal(t, []string{"bash"}, got.Tools)
	require.Equal(t, []string{"x.ts"}, got.Extensions)
	require.True(t, got.DisableAllTools)
	require.Equal(t, "/tmp/pi", got.SessionDir)
}

var errReleaseFailed = errors.New("release failed")

type fakeRunRunner struct {
	repoRoot string
	branch   string
	pid      int
	startErr error
}

func (r *fakeRunRunner) Run(string, ...string) ([]byte, error) { return nil, nil }
func (r *fakeRunRunner) RunDir(_ string, _ string, args ...string) ([]byte, error) {
	if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--show-toplevel" {
		return []byte(r.repoRoot + "\n"), nil
	}
	if len(args) >= 2 && args[0] == "branch" && args[1] == "--show-current" {
		return []byte(r.branch + "\n"), nil
	}
	return nil, nil
}
func (r *fakeRunRunner) Start(string, ...string) (int, error) { return r.pid, r.startErr }
func (r *fakeRunRunner) StartEnv([]string, string, ...string) (int, error) {
	return r.pid, r.startErr
}
func (r *fakeRunRunner) StartPiped(string, ...string) (pdexec.Process, error) { return nil, nil }

type fakeRunSandbox struct{}

func (s *fakeRunSandbox) Create() error                          { return nil }
func (s *fakeRunSandbox) Exec(string, ...string) ([]byte, error) { return nil, nil }

type fakeRunWorktree struct {
	path        string
	created     bool
	removedRepo string
}

func (w *fakeRunWorktree) AddHeadless(string, string) (string, error) { return w.path, nil }
func (w *fakeRunWorktree) AddHeadlessWithOwnership(string, string) (string, bool, error) {
	return w.path, w.created, nil
}
func (w *fakeRunWorktree) Path(string, string) (string, error) { return w.path, nil }
func (w *fakeRunWorktree) Remove(repoRoot, _ string) error {
	w.removedRepo = repoRoot
	return nil
}
