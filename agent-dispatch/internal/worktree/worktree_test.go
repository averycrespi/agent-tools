package worktree

import (
	"errors"
	"io"
	"testing"

	adexec "github.com/averycrespi/agent-tools/agent-dispatch/internal/exec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockRunner struct {
	name string
	args []string
	out  []byte
	err  error
}

func (m *mockRunner) Run(name string, args ...string) ([]byte, error) {
	m.name = name
	m.args = args
	return m.out, m.err
}

func (m *mockRunner) RunDir(dir, name string, args ...string) ([]byte, error) {
	m.name = name
	m.args = args
	return m.out, m.err
}

func (m *mockRunner) Start(name string, args ...string) (int, error) { return 0, nil }
func (m *mockRunner) StartPiped(name string, args ...string) (adexec.Process, error) {
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

func TestClientAddHeadlessArgv(t *testing.T) {
	r := &mockRunner{}
	c := NewClient(r)
	require.NoError(t, c.AddHeadless("/repo", "feat"))
	assert.Equal(t, "wt", r.name)
	assert.Equal(t, []string{"add", "--no-window", "feat"}, r.args)
}

func TestClientPathArgv(t *testing.T) {
	r := &mockRunner{out: []byte("/tmp/wt\n")}
	c := NewClient(r)
	path, err := c.Path("/repo", "feat")
	require.NoError(t, err)
	assert.Equal(t, "/tmp/wt", path)
	assert.Equal(t, []string{"path", "feat"}, r.args)
}

func TestClientPathErrorIncludesOutput(t *testing.T) {
	r := &mockRunner{out: []byte("not main repo"), err: errors.New("exit 1")}
	c := NewClient(r)
	_, err := c.Path("/repo", "feat")
	assert.ErrorContains(t, err, "not main repo")
}
