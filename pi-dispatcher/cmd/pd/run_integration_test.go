package main

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeVisibilitySandbox struct {
	workdir string
	args    []string
	err     error
}

func (s *fakeVisibilitySandbox) Exec(workdir string, args ...string) ([]byte, error) {
	s.workdir = workdir
	s.args = append([]string(nil), args...)
	return nil, s.err
}

func TestCheckWorktreeVisibleRunsSandboxTest(t *testing.T) {
	sb := &fakeVisibilitySandbox{}

	err := checkWorktreeVisible(sb, "/host/wt")

	require.NoError(t, err)
	assert.Equal(t, "/", sb.workdir)
	assert.Equal(t, []string{"test", "-d", "/host/wt"}, sb.args)
}

func TestCheckWorktreeVisibleErrorHasMountGuidance(t *testing.T) {
	sb := &fakeVisibilitySandbox{err: errors.New("exit 1")}

	err := checkWorktreeVisible(sb, "/host/wt")

	assert.ErrorContains(t, err, "worktree is not visible")
	assert.ErrorContains(t, err, "writable sb mount")
}

func TestStartedTaskMessageOnlyPrintsTaskID(t *testing.T) {
	message := startedTaskMessage("pd-123")

	require.Equal(t, "pd-123\n", message)
}
