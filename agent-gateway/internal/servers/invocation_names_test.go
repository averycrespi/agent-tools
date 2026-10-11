package servers

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInvocationServerRecognitionExcludesDeletedAndTracksRenames(t *testing.T) {
	repository, store, _ := newRepository(t, new(sequenceReader))
	current := mustCreateServer(t, repository, "recognition", false)
	name, err := repository.InvocationServerName(t.Context(), current.ID)
	require.NoError(t, err)
	require.Equal(t, &current.DisplayName, name)
	require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(t.Context(), "UPDATE servers SET display_name = ? WHERE id = ?", "Renamed", current.ID)
		return err
	}))
	name, err = repository.InvocationServerName(t.Context(), current.ID)
	require.NoError(t, err)
	require.Equal(t, "Renamed", *name)
	_, err = repository.Delete(t.Context(), current.ID, current.DesiredRevision)
	require.NoError(t, err)
	name, err = repository.InvocationServerName(t.Context(), current.ID)
	require.NoError(t, err)
	require.Nil(t, name)
	name, err = repository.InvocationServerName(t.Context(), "01J60000000000000000000999")
	require.NoError(t, err)
	require.Nil(t, name)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	name, err = repository.InvocationServerName(ctx, current.ID)
	require.Error(t, err)
	require.Nil(t, name)
}
