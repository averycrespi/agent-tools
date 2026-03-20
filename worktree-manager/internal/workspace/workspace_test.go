package workspace

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/worktree-manager/internal/config"
	"github.com/averycrespi/agent-tools/worktree-manager/internal/exec"
	"github.com/averycrespi/agent-tools/worktree-manager/internal/git"
)

var nopLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

// mockGitClient implements gitClient for tests.
type mockGitClient struct {
	mock.Mock
}

func (m *mockGitClient) RepoInfo(path string) (git.Info, error) {
	args := m.Called(path)
	return args.Get(0).(git.Info), args.Error(1)
}

func (m *mockGitClient) AddWorktree(repoRoot, worktreeDir, branch string) error {
	args := m.Called(repoRoot, worktreeDir, branch)
	return args.Error(0)
}

func (m *mockGitClient) RemoveWorktree(repoRoot, worktreeDir string) error {
	args := m.Called(repoRoot, worktreeDir)
	return args.Error(0)
}

func (m *mockGitClient) DeleteBranch(repoRoot, branch string, force bool) error {
	args := m.Called(repoRoot, branch, force)
	return args.Error(0)
}

func (m *mockGitClient) CommonDir(path string) (string, error) {
	args := m.Called(path)
	return args.String(0), args.Error(1)
}

// mockTmuxClient implements tmuxClient for tests.
type mockTmuxClient struct {
	mock.Mock
}

func (m *mockTmuxClient) SessionExists(session string) bool {
	args := m.Called(session)
	return args.Bool(0)
}

func (m *mockTmuxClient) CreateSession(session, window string) error {
	args := m.Called(session, window)
	return args.Error(0)
}

func (m *mockTmuxClient) CreateWindow(session, window, dir string) error {
	args := m.Called(session, window, dir)
	return args.Error(0)
}

func (m *mockTmuxClient) KillWindow(session, window string) error {
	args := m.Called(session, window)
	return args.Error(0)
}

func (m *mockTmuxClient) WindowExists(session, window string) bool {
	args := m.Called(session, window)
	return args.Bool(0)
}

func (m *mockTmuxClient) SendKeys(session, window, keys string) error {
	args := m.Called(session, window, keys)
	return args.Error(0)
}

func (m *mockTmuxClient) Attach(session string) error {
	args := m.Called(session)
	return args.Error(0)
}

func (m *mockTmuxClient) AttachToWindow(session, window string) error {
	args := m.Called(session, window)
	return args.Error(0)
}

// mockRunner implements exec.Runner for setup script tests.
type mockRunner struct {
	mock.Mock
}

func (m *mockRunner) Run(name string, args ...string) ([]byte, error) {
	callArgs := m.Called(name, args)
	return callArgs.Get(0).([]byte), callArgs.Error(1)
}

func (m *mockRunner) RunDir(dir, name string, args ...string) ([]byte, error) {
	callArgs := m.Called(dir, name, args)
	return callArgs.Get(0).([]byte), callArgs.Error(1)
}

func (m *mockRunner) RunInteractive(name string, args ...string) error {
	callArgs := m.Called(name, args)
	return callArgs.Error(0)
}

var _ exec.Runner = (*mockRunner)(nil)

func TestService_Init_CreatesSession(t *testing.T) {
	g := new(mockGitClient)
	g.On("RepoInfo", "/repo").Return(git.Info{Name: "myrepo", Root: "/repo"}, nil)

	tm := new(mockTmuxClient)
	tm.On("SessionExists", "wt-myrepo").Return(false)
	tm.On("CreateSession", "wt-myrepo", "main").Return(nil)

	svc := NewService(g, tm, config.Default(), nopLogger, nil)
	err := svc.Init("/repo")

	require.NoError(t, err)
	tm.AssertCalled(t, "CreateSession", "wt-myrepo", "main")
}

func TestService_Init_SessionAlreadyExists(t *testing.T) {
	g := new(mockGitClient)
	g.On("RepoInfo", "/repo").Return(git.Info{Name: "myrepo", Root: "/repo"}, nil)

	tm := new(mockTmuxClient)
	tm.On("SessionExists", "wt-myrepo").Return(true)

	svc := NewService(g, tm, config.Default(), nopLogger, nil)
	err := svc.Init("/repo")

	require.NoError(t, err)
	tm.AssertNotCalled(t, "CreateSession", mock.Anything, mock.Anything)
}

func TestService_Init_RejectsWorktree(t *testing.T) {
	g := new(mockGitClient)
	g.On("RepoInfo", "/wt").Return(git.Info{Name: "wt", Root: "/wt", IsWorktree: true}, nil)

	svc := NewService(g, new(mockTmuxClient), config.Default(), nopLogger, nil)
	err := svc.Init("/wt")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "main git repository")
}

func TestService_Add_CreatesWorktreeAndWindow(t *testing.T) {
	g := new(mockGitClient)
	g.On("RepoInfo", "/repo").Return(git.Info{Name: "myrepo", Root: "/repo"}, nil)
	g.On("AddWorktree", "/repo", mock.Anything, "feat").Return(nil)

	tm := new(mockTmuxClient)
	tm.On("SessionExists", "wt-myrepo").Return(true)
	tm.On("WindowExists", "wt-myrepo", "feat").Return(false)
	tm.On("CreateWindow", "wt-myrepo", "feat", mock.Anything).Return(nil)
	tm.On("SendKeys", "wt-myrepo", "feat", "claude").Return(nil)

	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	cfg := config.Config{
		LaunchCommand: "claude",
		SetupScripts:  []string{},
		CopyFiles:     []string{},
	}
	svc := NewService(g, tm, cfg, nopLogger, nil)
	err := svc.Add("/repo", "feat")

	require.NoError(t, err)
	tm.AssertCalled(t, "CreateWindow", "wt-myrepo", "feat", mock.Anything)
	tm.AssertCalled(t, "SendKeys", "wt-myrepo", "feat", "claude")
}

func TestService_Add_NoLaunchCommand(t *testing.T) {
	g := new(mockGitClient)
	g.On("RepoInfo", "/repo").Return(git.Info{Name: "myrepo", Root: "/repo"}, nil)
	g.On("AddWorktree", "/repo", mock.Anything, "feat").Return(nil)

	tm := new(mockTmuxClient)
	tm.On("SessionExists", "wt-myrepo").Return(true)
	tm.On("WindowExists", "wt-myrepo", "feat").Return(false)
	tm.On("CreateWindow", "wt-myrepo", "feat", mock.Anything).Return(nil)

	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	svc := NewService(g, tm, config.Default(), nopLogger, nil)
	err := svc.Add("/repo", "feat")

	require.NoError(t, err)
	tm.AssertNotCalled(t, "SendKeys", mock.Anything, mock.Anything, mock.Anything)
}

func TestService_Add_CopyFiles(t *testing.T) {
	g := new(mockGitClient)
	g.On("RepoInfo", "/repo").Return(git.Info{Name: "myrepo", Root: "/repo"}, nil)
	g.On("AddWorktree", "/repo", mock.Anything, "feat").Return(nil)

	tm := new(mockTmuxClient)
	tm.On("SessionExists", "wt-myrepo").Return(true)
	tm.On("WindowExists", "wt-myrepo", "feat").Return(false)
	tm.On("CreateWindow", "wt-myrepo", "feat", mock.Anything).Return(nil)

	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	// Create a source file to be copied
	repoDir := t.TempDir()
	g = new(mockGitClient)
	g.On("RepoInfo", repoDir).Return(git.Info{Name: "myrepo", Root: repoDir}, nil)
	g.On("AddWorktree", repoDir, mock.Anything, "feat").Return(nil)

	srcFile := filepath.Join(repoDir, ".claude", "settings.local.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(srcFile), 0o755))
	require.NoError(t, os.WriteFile(srcFile, []byte(`{"key":"value"}`), 0o644))

	cfg := config.Config{
		CopyFiles:    []string{".claude/settings.local.json"},
		SetupScripts: []string{},
	}
	svc := NewService(g, tm, cfg, nopLogger, nil)
	err := svc.Add(repoDir, "feat")

	require.NoError(t, err)

	// Verify the file was copied to the worktree
	worktreeDir := config.WorktreeDir("myrepo", "feat")
	dstFile := filepath.Join(worktreeDir, ".claude", "settings.local.json")
	data, err := os.ReadFile(dstFile)
	require.NoError(t, err)
	assert.Equal(t, `{"key":"value"}`, string(data))
}

func TestService_Remove_RemovesWorktreeAndWindow(t *testing.T) {
	g := new(mockGitClient)
	g.On("RepoInfo", "/repo").Return(git.Info{Name: "myrepo", Root: "/repo"}, nil)

	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	worktreeDir := filepath.Join(tmpDir, "wt", "worktrees", "myrepo", "myrepo-feat")
	require.NoError(t, os.MkdirAll(worktreeDir, 0o755))

	g.On("RemoveWorktree", "/repo", worktreeDir).Return(nil)

	tm := new(mockTmuxClient)
	tm.On("SessionExists", "wt-myrepo").Return(true)
	tm.On("WindowExists", "wt-myrepo", "feat").Return(true)
	tm.On("KillWindow", "wt-myrepo", "feat").Return(nil)

	svc := NewService(g, tm, config.Default(), nopLogger, nil)
	err := svc.Remove("/repo", "feat", false, false)

	require.NoError(t, err)
	g.AssertCalled(t, "RemoveWorktree", "/repo", worktreeDir)
	tm.AssertCalled(t, "KillWindow", "wt-myrepo", "feat")
}

func TestService_Remove_DeletesBranch(t *testing.T) {
	g := new(mockGitClient)
	g.On("RepoInfo", "/repo").Return(git.Info{Name: "myrepo", Root: "/repo"}, nil)
	g.On("DeleteBranch", "/repo", "feat", false).Return(nil)

	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	tm := new(mockTmuxClient)
	tm.On("SessionExists", "wt-myrepo").Return(false)

	svc := NewService(g, tm, config.Default(), nopLogger, nil)
	err := svc.Remove("/repo", "feat", true, false)

	require.NoError(t, err)
	g.AssertCalled(t, "DeleteBranch", "/repo", "feat", false)
}

func TestService_Remove_ForceDeletesBranch(t *testing.T) {
	g := new(mockGitClient)
	g.On("RepoInfo", "/repo").Return(git.Info{Name: "myrepo", Root: "/repo"}, nil)
	g.On("DeleteBranch", "/repo", "feat", true).Return(nil)

	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	tm := new(mockTmuxClient)
	tm.On("SessionExists", "wt-myrepo").Return(false)

	svc := NewService(g, tm, config.Default(), nopLogger, nil)
	err := svc.Remove("/repo", "feat", false, true)

	require.NoError(t, err)
	g.AssertCalled(t, "DeleteBranch", "/repo", "feat", true)
}

func TestService_Remove_SkipsBranchDeleteByDefault(t *testing.T) {
	g := new(mockGitClient)
	g.On("RepoInfo", "/repo").Return(git.Info{Name: "myrepo", Root: "/repo"}, nil)

	tmpDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmpDir)

	tm := new(mockTmuxClient)
	tm.On("SessionExists", "wt-myrepo").Return(false)

	svc := NewService(g, tm, config.Default(), nopLogger, nil)
	err := svc.Remove("/repo", "feat", false, false)

	require.NoError(t, err)
	g.AssertNotCalled(t, "DeleteBranch", mock.Anything, mock.Anything, mock.Anything)
}

func TestService_Attach_Session(t *testing.T) {
	g := new(mockGitClient)
	g.On("RepoInfo", "/repo").Return(git.Info{Name: "myrepo", Root: "/repo"}, nil)

	tm := new(mockTmuxClient)
	tm.On("SessionExists", "wt-myrepo").Return(true)
	tm.On("Attach", "wt-myrepo").Return(nil)

	svc := NewService(g, tm, config.Default(), nopLogger, nil)
	err := svc.Attach("/repo", "")

	require.NoError(t, err)
	tm.AssertCalled(t, "Attach", "wt-myrepo")
}

func TestService_Attach_Window(t *testing.T) {
	g := new(mockGitClient)
	g.On("RepoInfo", "/repo").Return(git.Info{Name: "myrepo", Root: "/repo"}, nil)

	tm := new(mockTmuxClient)
	tm.On("SessionExists", "wt-myrepo").Return(true)
	tm.On("WindowExists", "wt-myrepo", "feat").Return(true)
	tm.On("AttachToWindow", "wt-myrepo", "feat").Return(nil)

	svc := NewService(g, tm, config.Default(), nopLogger, nil)
	err := svc.Attach("/repo", "feat")

	require.NoError(t, err)
	tm.AssertCalled(t, "AttachToWindow", "wt-myrepo", "feat")
}

func TestService_Attach_FromWorktree(t *testing.T) {
	g := new(mockGitClient)
	g.On("RepoInfo", "/wt").Return(git.Info{Name: "wt", Root: "/wt", IsWorktree: true}, nil)
	g.On("CommonDir", "/wt").Return("/repo/.git", nil)

	tm := new(mockTmuxClient)
	tm.On("SessionExists", "wt-repo").Return(true)
	tm.On("Attach", "wt-repo").Return(nil)

	svc := NewService(g, tm, config.Default(), nopLogger, nil)
	err := svc.Attach("/wt", "")

	require.NoError(t, err)
	tm.AssertCalled(t, "Attach", "wt-repo")
}
