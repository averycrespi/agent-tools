package main

import (
	"io"
	"testing"

	pdexec "github.com/averycrespi/agent-tools/pi-dispatch/internal/exec"
	"github.com/averycrespi/agent-tools/pi-dispatch/internal/gitmeta"
	"github.com/stretchr/testify/require"
)

type repoInfoRunner struct {
	calls []repoInfoCall
	outs  [][]byte
}

type repoInfoCall struct {
	dir  string
	name string
	args []string
}

func (r *repoInfoRunner) Run(name string, args ...string) ([]byte, error) { return nil, nil }

func (r *repoInfoRunner) RunDir(dir, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, repoInfoCall{dir: dir, name: name, args: append([]string(nil), args...)})
	idx := len(r.calls) - 1
	if idx < len(r.outs) {
		return r.outs[idx], nil
	}
	return nil, nil
}

func (r *repoInfoRunner) Start(name string, args ...string) (int, error) { return 0, nil }
func (r *repoInfoRunner) StartPiped(name string, args ...string) (pdexec.Process, error) {
	return repoInfoProcess{}, nil
}

type repoInfoProcess struct{}

func (repoInfoProcess) Stdin() io.WriteCloser { return repoInfoWriteCloser{} }
func (repoInfoProcess) Stdout() io.ReadCloser { return io.NopCloser(&repoInfoReader{}) }
func (repoInfoProcess) Stderr() io.ReadCloser { return io.NopCloser(&repoInfoReader{}) }
func (repoInfoProcess) Wait() error           { return nil }
func (repoInfoProcess) Kill() error           { return nil }

type repoInfoWriteCloser struct{}

func (repoInfoWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (repoInfoWriteCloser) Close() error                { return nil }

type repoInfoReader struct{}

func (*repoInfoReader) Read(_ []byte) (int, error) { return 0, io.EOF }

func TestResolveRunRepoInfoUsesGitMetadataFromRepoArgument(t *testing.T) {
	runner := &repoInfoRunner{outs: [][]byte{[]byte("/repo\n"), []byte("feature\n")}}

	info, err := resolveRunRepoInfo(runner, "/repo/subdir")

	require.NoError(t, err)
	require.Equal(t, gitmeta.Info{Root: "/repo", Name: "repo", CurrentBranch: "feature"}, info)
	require.Equal(t, []repoInfoCall{
		{dir: "/repo/subdir", name: "git", args: []string{"rev-parse", "--show-toplevel"}},
		{dir: "/repo/subdir", name: "git", args: []string{"branch", "--show-current"}},
	}, runner.calls)
}
