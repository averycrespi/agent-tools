package naming_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/naming"
)

func TestWindowNamesAddStableSuffixOnlyForCollisions(t *testing.T) {
	items := []naming.Item{
		{Identity: "id-1", Label: "feature/a"},
		{Identity: "id-2", Label: "feature:a"},
		{Identity: "id-3", Label: "plain"},
	}
	got := naming.Windows(items)
	require.Regexp(t, `^feature-a-[a-f0-9]{8}$`, got["id-1"])
	require.Regexp(t, `^feature-a-[a-f0-9]{8}$`, got["id-2"])
	require.NotEqual(t, got["id-1"], got["id-2"])
	require.Equal(t, "plain", got["id-3"])
	require.Equal(t, got, naming.Windows([]naming.Item{items[2], items[1], items[0]}))
}

func TestDetachedNameIsReadableAndDeterministic(t *testing.T) {
	name := naming.Detached("0123456789abcdef", "/tmp/worktree")
	require.Equal(t, "detached-01234567", name)
	require.Equal(t, name, naming.Detached("0123456789abcdef", "/other"))
}
