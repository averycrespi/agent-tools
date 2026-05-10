package gitmeta

import (
	"errors"
	"io"
	"testing"

	pdexec "github.com/averycrespi/agent-tools/pi-dispatch/internal/exec"
	"github.com/stretchr/testify/require"
)

type mockRunner struct {
	calls []runCall
	outs  [][]byte
	errs  []error
}

type runCall struct {
	dir  string
	name string
	args []string
}

func (m *mockRunner) Run(name string, args ...string) ([]byte, error) { return nil, nil }

func (m *mockRunner) RunDir(dir, name string, args ...string) ([]byte, error) {
	m.calls = append(m.calls, runCall{dir: dir, name: name, args: append([]string(nil), args...)})
	idx := len(m.calls) - 1
	if idx < len(m.errs) && m.errs[idx] != nil {
		return nil, m.errs[idx]
	}
	if idx < len(m.outs) {
		return m.outs[idx], nil
	}
	return nil, nil
}

func (m *mockRunner) Start(name string, args ...string) (int, error) { return 0, nil }
func (m *mockRunner) StartPiped(name string, args ...string) (pdexec.Process, error) {
	return fakeProcess{}, nil
}

type fakeProcess struct{}

func (fakeProcess) Stdin() io.WriteCloser { return nopWriteCloser{} }
func (fakeProcess) Stdout() io.ReadCloser { return io.NopCloser(&emptyReader{}) }
func (fakeProcess) Stderr() io.ReadCloser { return io.NopCloser(&emptyReader{}) }
func (fakeProcess) Wait() error           { return nil }
func (fakeProcess) Kill() error           { return nil }

type nopWriteCloser struct{}

func (nopWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (nopWriteCloser) Close() error                { return nil }

type emptyReader struct{}

func (*emptyReader) Read(_ []byte) (int, error) { return 0, io.EOF }

func TestInfoUsesGitRootAndCurrentBranch(t *testing.T) {
	runner := &mockRunner{outs: [][]byte{[]byte("/repo\n"), []byte("main\n")}}

	info, err := NewClient(runner).Info("/repo/subdir")

	require.NoError(t, err)
	require.Equal(t, Info{Root: "/repo", Name: "repo", CurrentBranch: "main"}, info)
	require.Equal(t, []runCall{
		{dir: "/repo/subdir", name: "git", args: []string{"rev-parse", "--show-toplevel"}},
		{dir: "/repo/subdir", name: "git", args: []string{"branch", "--show-current"}},
	}, runner.calls)
}

func TestInfoWrapsGitRootErrors(t *testing.T) {
	runner := &mockRunner{errs: []error{errors.New("exit 128")}}

	_, err := NewClient(runner).Info("/not/repo")

	require.ErrorContains(t, err, "failed to determine repo root")
}
