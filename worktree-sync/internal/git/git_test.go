package git_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	gitclient "github.com/averycrespi/agent-tools/worktree-sync/internal/git"
)

func TestParsePorcelainZSupportsSpecialPathsAndStates(t *testing.T) {
	input := "worktree /repo\x00HEAD abc\x00branch refs/heads/main\x00\x00" +
		"worktree /tmp/space and\nnewline-λ\x00HEAD def\x00detached\x00locked reason with spaces\x00\x00" +
		"worktree /gone\x00HEAD 000\x00prunable gitdir file points to non-existent location\x00\x00"

	got, err := gitclient.ParsePorcelainZ([]byte(input))
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.Equal(t, "main", got[0].Branch)
	require.Equal(t, "/tmp/space and\nnewline-λ", got[1].Path)
	require.True(t, got[1].Detached)
	require.Equal(t, "reason with spaces", got[1].Locked)
	require.NotEmpty(t, got[2].Prunable)
}

func TestParsePorcelainZFailsClosedOnMalformedRecords(t *testing.T) {
	_, err := gitclient.ParsePorcelainZ([]byte("HEAD abc\x00\x00"))
	require.ErrorContains(t, err, "worktree")

	_, err = gitclient.ParsePorcelainZ([]byte("worktree /repo\x00HEAD abc"))
	require.ErrorContains(t, err, "terminated")
}
