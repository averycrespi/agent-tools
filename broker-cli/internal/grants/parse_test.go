package grants

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSplitByToolBoundaries(t *testing.T) {
	args := []string{
		"--ttl", "1h",
		"--description", "push feat/foo",
		"--tool", "git.git_push",
		"--arg-equal", "branch=feat/foo",
		"--arg-equal", "force=false",
		"--tool", "git.git_fetch",
	}
	global, groups, err := splitByTool(args)
	require.NoError(t, err)
	require.Equal(t, []string{"--ttl", "1h", "--description", "push feat/foo"}, global)
	require.Len(t, groups, 2)
	require.Equal(t, "git.git_push", groups[0].tool)
	require.Equal(t, []string{"--arg-equal", "branch=feat/foo", "--arg-equal", "force=false"}, groups[0].flags)
	require.Equal(t, "git.git_fetch", groups[1].tool)
	require.Empty(t, groups[1].flags)
}

func TestSplitByToolNoTool(t *testing.T) {
	_, _, err := splitByTool([]string{"--ttl", "1h"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "at least one --tool")
}

func TestSplitByToolMissingName(t *testing.T) {
	_, _, err := splitByTool([]string{"--tool"})
	require.Error(t, err)
}
