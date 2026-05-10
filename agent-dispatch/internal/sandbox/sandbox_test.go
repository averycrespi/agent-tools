package sandbox

import (
	"errors"
	"testing"

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

func (m *mockRunner) Start(name string, args ...string) error { return nil }

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
