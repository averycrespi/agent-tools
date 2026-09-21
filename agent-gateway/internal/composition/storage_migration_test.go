//go:build integration

package composition

import (
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/invocation"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestStoppedMigrationAcceptsSchema17(t *testing.T) {
	ctx := t.Context()
	root := filepath.Join(t.TempDir(), "gateway")
	const installation = "01J60000000000000000000001"
	const generation = "01J60000000000000000000002"
	_, err := storage.WriteAcceptedSchemaFixtureForIntegration(ctx, root, installation, 17)
	require.NoError(t, err)
	before, err := storage.VerifyBackup(ctx, filepath.Join(root, "gateway.db"))
	require.NoError(t, err)
	require.Equal(t, 17, before.SchemaVersion)

	identity, err := MigrateStorage(ctx, root, installation, generation, 8<<20)
	require.NoError(t, err)
	require.Equal(t, installation, identity.InstallationID)
	verified, err := VerifyStorageBudget(ctx, root, 8<<20)
	require.NoError(t, err)
	require.Equal(t, storage.CurrentSchema, verified.SchemaVersion)
	previous, err := filepath.Glob(filepath.Join(root, "gateway.db.previous-*"))
	require.NoError(t, err)
	require.Len(t, previous, 1)
	retained, err := storage.VerifyBackup(ctx, previous[0])
	require.NoError(t, err)
	require.Equal(t, 17, retained.SchemaVersion)
	require.Equal(t, installation, retained.InstallationID)

	_, err = MigrateStorage(ctx, root, installation, "01J60000000000000000000003", 8<<20)
	require.ErrorIs(t, err, invocation.ErrInvalidState, "an already-selected pair must not be migrated again")
	_, err = VerifyStorageBudget(ctx, root, 8<<20)
	require.NoError(t, err)
}
