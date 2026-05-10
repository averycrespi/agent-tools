package sandbox

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

func (m *mockRunner) Start(name string, args ...string) error { return nil }
func (m *mockRunner) StartPiped(name string, args ...string) (adexec.Process, error) {
	m.name = name
	m.args = args
	return fakeProcess{}, m.err
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

func TestClientCreateArgv(t *testing.T) {
	r := &mockRunner{}
	c := NewClient(r)
	require.NoError(t, c.Create())
	assert.Equal(t, "sb", r.name)
	assert.Equal(t, []string{"create"}, r.args)
}

func TestClientExecArgv(t *testing.T) {
	r := &mockRunner{out: []byte("ok")}
	c := NewClient(r)
	out, err := c.Exec("/work", "pwd")
	require.NoError(t, err)
	assert.Equal(t, []byte("ok"), out)
	assert.Equal(t, []string{"exec", "--workdir", "/work", "--", "pwd"}, r.args)
}

func TestCheckWorktreeVisibleErrorHasMountGuidance(t *testing.T) {
	r := &mockRunner{err: errors.New("exit 1")}
	c := NewClient(r)
	err := c.CheckWorktreeVisible("/host/wt")
	assert.ErrorContains(t, err, "worktree is not visible")
	assert.ErrorContains(t, err, "writable sb mount")
	assert.Equal(t, []string{"exec", "--workdir", "/", "--", "test", "-d", "/host/wt"}, r.args)
}
