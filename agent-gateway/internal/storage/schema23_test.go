package storage

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEncryptedCustodyMigrationMarksOnlyHistoricalGenerations(t *testing.T) {
	owner := newOwnership(t)
	writePopulatedSchemaEightFixture(t, t.Context(), owner)
	prior, err := openConfigured(t.Context(), owner.Layout(), testOptions{})
	require.NoError(t, err)
	require.NoError(t, prior.migrateThrough(t.Context(), 8, 22))
	_, err = prior.database.ExecContext(t.Context(), `INSERT INTO keyring_authorities(owner,kind,handle,revision) VALUES ('01J60000000000000000000020','oauth_tokens','historical-authority',1); INSERT INTO keyring_candidates(owner,kind,handle,created_at) VALUES ('01J60000000000000000000020','oauth_tokens','historical-candidate','2026-10-06T00:00:00Z')`)
	require.NoError(t, err)
	require.NoError(t, prior.Checkpoint(t.Context()))
	require.NoError(t, prior.Close())
	_, err = Open(t.Context(), owner)
	require.ErrorIs(t, err, ErrLegacyCustody)
	// Inspect the retained historical DDL directly; production opening refuses
	// before migrating installations with unresolved native dependencies.
	current, err := openConfigured(t.Context(), owner.Layout(), testOptions{})
	require.NoError(t, err)
	defer func() { require.NoError(t, current.Close()) }()
	require.NoError(t, current.migrateThrough(t.Context(), 22, 23))
	var count int
	require.NoError(t, current.database.QueryRowContext(t.Context(), `SELECT count(*) FROM secret_generations WHERE custody='legacy' AND ciphertext IS NULL AND version IS NULL AND key_id IS NULL`).Scan(&count))
	require.Equal(t, 2, count)
	require.NoError(t, current.database.QueryRowContext(t.Context(), `SELECT count(*) FROM secret_custody`).Scan(&count))
	require.Zero(t, count)
	_, err = current.database.ExecContext(t.Context(), `INSERT INTO secret_generations(handle,owner,kind,custody) VALUES ('incomplete','owner','oauth_tokens','encrypted')`)
	require.Error(t, err)
	require.NoError(t, current.verifyMigrationStructure(t.Context(), "023_encrypted_custody.sql"))
	_, err = current.database.ExecContext(t.Context(), `ALTER TABLE secret_generations ADD COLUMN plaintext TEXT`)
	require.NoError(t, err)
	require.Error(t, current.verifyMigrationStructure(t.Context(), "023_encrypted_custody.sql"))
}
