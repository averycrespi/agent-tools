package git_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	gitclient "github.com/averycrespi/agent-tools/worktree-sync/internal/git"
)

type response struct {
	output []byte
	err    error
}
type fakeRunner struct {
	responses []response
	calls     [][]string
}

func (f *fakeRunner) Run(_ context.Context, dir, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{dir, name}, args...))
	if len(f.responses) == 0 {
		return nil, nil
	}
	response := f.responses[0]
	f.responses = f.responses[1:]
	return response.output, response.err
}
func (*fakeRunner) Interactive(context.Context, string, string, ...string) error { return nil }

func TestInspectContextIdentifiesPrimaryFromLinkedWorktree(t *testing.T) {
	root := t.TempDir()
	primary := filepath.Join(root, "primary")
	linked := filepath.Join(root, "linked")
	common := filepath.Join(primary, ".git")
	require.NoError(t, os.MkdirAll(common, 0o700))
	require.NoError(t, os.Mkdir(linked, 0o700))
	porcelain := "worktree " + primary + "\x00HEAD abcdef\x00branch refs/heads/main\x00\x00" +
		"worktree " + linked + "\x00HEAD 123456\x00branch refs/heads/feature\x00\x00"
	runner := &fakeRunner{responses: []response{
		{[]byte("true\n"), nil},
		{[]byte(linked + "\n"), nil},
		{[]byte(common + "\n"), nil},
		{[]byte(porcelain), nil},
	}}
	client := gitclient.New(runner, time.Second)
	contextInfo, err := client.InspectContext(context.Background(), linked)
	require.NoError(t, err)
	require.Equal(t, linked, contextInfo.WorktreeRoot)
	require.Equal(t, primary, contextInfo.PrimaryRoot)
	require.Equal(t, common, contextInfo.CommonGitDir)
}

func TestInspectRejectsLinkedAndBareWorktrees(t *testing.T) {
	root := t.TempDir()
	common := filepath.Join(root, ".git")
	require.NoError(t, os.Mkdir(common, 0o700))
	runner := &fakeRunner{responses: []response{{[]byte(root + "\n"), nil}, {[]byte("true\n"), nil}}}
	client := gitclient.New(runner, time.Second)
	_, err := client.InspectPrimary(context.Background(), root)
	require.ErrorContains(t, err, "bare")

	linkedGit := filepath.Join(common, "worktrees", "linked")
	require.NoError(t, os.MkdirAll(linkedGit, 0o700))
	runner = &fakeRunner{responses: []response{{[]byte(root + "\n"), nil}, {[]byte("false\n"), nil}, {[]byte(linkedGit + "\n"), nil}, {[]byte(common + "\n"), nil}}}
	client = gitclient.New(runner, time.Second)
	_, err = client.InspectPrimary(context.Background(), root)
	require.ErrorContains(t, err, "linked worktree")
}

func TestSnapshotReportsPrunableAndFailsIncompleteCanonicalization(t *testing.T) {
	root := t.TempDir()
	common := filepath.Join(root, ".git")
	require.NoError(t, os.Mkdir(common, 0o700))
	missing := filepath.Join(root, "missing")
	output := "worktree " + root + "\x00HEAD abc\x00branch refs/heads/main\x00\x00" +
		"worktree " + missing + "\x00HEAD def\x00branch refs/heads/missing\x00\x00" +
		"worktree /gone\x00HEAD 000\x00prunable stale\x00\x00"
	runner := &fakeRunner{responses: []response{{[]byte(output), nil}, {[]byte(common + "\n"), nil}}}
	client := gitclient.New(runner, time.Second)
	snapshot, err := client.Snapshot(context.Background(), gitclient.Repository{PrimaryRoot: root, CommonGitDir: common})
	require.Error(t, err)
	require.False(t, snapshot.Complete)
	require.Len(t, snapshot.Worktrees, 3)
	require.Equal(t, "missing", snapshot.Worktrees[1].Exclusion)
	require.Equal(t, "prunable", snapshot.Worktrees[2].Exclusion)
	require.Len(t, runner.calls, 1, "primary identity should come from registration without another Git subprocess")
}

func TestSnapshotReadsLinkedGitFileWithoutPerWorktreeSubprocess(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "repo")
	linked := filepath.Join(base, "linked")
	common := filepath.Join(primary, ".git")
	admin := filepath.Join(common, "worktrees", "linked")
	require.NoError(t, os.MkdirAll(admin, 0o700))
	require.NoError(t, os.Mkdir(linked, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(linked, ".git"), []byte("gitdir: "+admin+"\n"), 0o600))
	output := "worktree " + primary + "\x00HEAD abc\x00branch refs/heads/main\x00\x00" + "worktree " + linked + "\x00HEAD def\x00branch refs/heads/linked\x00\x00"
	runner := &fakeRunner{responses: []response{{output: []byte(output)}}}
	snapshot, err := gitclient.New(runner, time.Second).Snapshot(context.Background(), gitclient.Repository{PrimaryRoot: primary, CommonGitDir: common})
	require.NoError(t, err)
	require.True(t, snapshot.Complete)
	require.Len(t, runner.calls, 1)
	require.Contains(t, snapshot.Worktrees[1].Identity, admin+"#")
}

func TestGitErrorDoesNotExposeCommandOutput(t *testing.T) {
	runner := &fakeRunner{responses: []response{{output: []byte("token=secret"), err: errors.New("failed")}}}
	_, err := gitclient.New(runner, time.Second).ListRaw(context.Background(), "/repo")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "token=secret")
}

func TestAddCreatesMissingBranchFromRevision(t *testing.T) {
	runner := &fakeRunner{}
	err := gitclient.New(runner, time.Second).Add(context.Background(), "/repo", "/worktree", "feature", "origin/main", false)
	require.NoError(t, err)
	require.Equal(t, []string{"/repo", "git", "worktree", "add", "--quiet", "-b", "feature", "/worktree", "origin/main"}, runner.calls[0])
}

func TestAddRejectsBranchOriginWhenBranchAlreadyExists(t *testing.T) {
	runner := &fakeRunner{}
	err := gitclient.New(runner, time.Second).Add(context.Background(), "/repo", "/worktree", "feature", "origin/main", true)
	require.ErrorContains(t, err, "already exists")
	require.Empty(t, runner.calls)
}

func TestBranchExistsDistinguishesMissingBranchFromCommandFailure(t *testing.T) {
	runner := &fakeRunner{responses: []response{{err: exitError{code: 1}}, {err: errors.New("git unavailable")}}}
	client := gitclient.New(runner, time.Second)
	exists, err := client.BranchExists(context.Background(), "/repo", "missing")
	require.NoError(t, err)
	require.False(t, exists)
	_, err = client.BranchExists(context.Background(), "/repo", "unknown")
	require.Error(t, err)
}

type exitError struct{ code int }

func (e exitError) Error() string { return "exit" }
func (e exitError) ExitCode() int { return e.code }

func TestRemoveAndBranchDeletionForwardExplicitSafetyFlags(t *testing.T) {
	runner := &fakeRunner{responses: make([]response, 4)}
	client := gitclient.New(runner, time.Second)
	require.NoError(t, client.Remove(context.Background(), "/repo", "/worktree", false))
	require.NoError(t, client.Remove(context.Background(), "/repo", "/worktree", true))
	require.NoError(t, client.DeleteBranch(context.Background(), "/repo", "feature", false))
	require.NoError(t, client.DeleteBranch(context.Background(), "/repo", "feature", true))
	require.Equal(t, []string{"/repo", "git", "worktree", "remove", "/worktree"}, runner.calls[0])
	require.Equal(t, []string{"/repo", "git", "worktree", "remove", "--force", "/worktree"}, runner.calls[1])
	require.Equal(t, []string{"/repo", "git", "branch", "-d", "feature"}, runner.calls[2])
	require.Equal(t, []string{"/repo", "git", "branch", "-D", "feature"}, runner.calls[3])
}

func TestClientDeadlineCancelsRunner(t *testing.T) {
	runner := &blockingRunner{}
	client := gitclient.New(runner, 5*time.Millisecond)
	_, err := client.ListRaw(context.Background(), "/repo")
	require.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled))
}

type blockingRunner struct{}

func (*blockingRunner) Run(ctx context.Context, _, _ string, _ ...string) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (*blockingRunner) Interactive(context.Context, string, string, ...string) error { return nil }
