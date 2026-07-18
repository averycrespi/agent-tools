package state_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/worktree-sync/internal/state"
)

func TestProvenanceRequiresRepositoryPathAndWorktreeIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "provenance.json")
	provenance, err := state.LoadProvenance(path)
	require.NoError(t, err)
	provenance.RecordExplicit("repo-one", "/worktree", "identity-one")
	require.True(t, provenance.Explicit("repo-one", "/worktree", "identity-one"))
	require.False(t, provenance.Explicit("repo-two", "/worktree", "identity-one"))
	require.False(t, provenance.Explicit("repo-one", "/worktree", "identity-two"))
	provenance.Remove("repo-one", "/worktree", "identity-one")
	require.False(t, provenance.Explicit("repo-one", "/worktree", "identity-one"))
	require.NoError(t, provenance.Save(path))
}
