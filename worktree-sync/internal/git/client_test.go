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
	response := f.responses[0]
	f.responses = f.responses[1:]
	return response.output, response.err
}
func (*fakeRunner) Interactive(context.Context, string, string, ...string) error { return nil }

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
