package worktree

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

func (m *mockRunner) RunDir(dir, name string, args ...string) ([]byte, error) {
	m.name = name
	m.args = args
	return m.out, m.err
}

func (m *mockRunner) Start(name string, args ...string) error { return nil }

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
