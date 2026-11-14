//go:build integration

package gitcredentials

import (
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/resourcekeys"
	"github.com/stretchr/testify/require"
)

func TestIntegrationGitCredentialKeysRemainReservedAfterDeletion(t *testing.T) {
	s, _, _ := fixture(t)
	ctx := t.Context()
	def := definition()
	def.Key = "service-access"
	created, err := s.Create(ctx, def, []byte("synthetic-key-test"))
	require.NoError(t, err)
	require.Equal(t, def.Key, created.Key)
	def.Name = "Renamed"
	updated, err := s.Update(ctx, created.ID, created.Revision, def)
	require.NoError(t, err)
	require.Equal(t, created.Key, updated.Key)
	def.Key = "changed"
	_, err = s.Update(ctx, updated.ID, updated.Revision, def)
	require.ErrorIs(t, err, ErrInvalid)
	require.NoError(t, s.Delete(ctx, updated.ID, updated.Revision))
	def.Key = created.Key
	_, err = s.Create(ctx, def, []byte("synthetic-key-test"))
	require.ErrorIs(t, err, resourcekeys.ErrConflict)
	def.Key = ""
	generated, err := s.Create(ctx, def, []byte("synthetic-key-test"))
	require.NoError(t, err)
	require.True(t, contract.ValidResourceKey(generated.Key))
	require.NotEqual(t, created.Key, generated.Key)
}
