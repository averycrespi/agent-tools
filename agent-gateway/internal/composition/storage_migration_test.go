package composition

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/invocation"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestStoppedMigrationAcceptsSchema17(t *testing.T) {
	ctx := t.Context()
	root := filepath.Join(t.TempDir(), "gateway")
	owner, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	const installation = "01J60000000000000000000001"
	const generation = "01J60000000000000000000002"
	control, err := storage.Initialize(ctx, owner, installation)
	require.NoError(t, err)
	require.NoError(t, control.Close())
	// Schema 18 adds only selection metadata. Remove that addition to construct
	// a validated released schema-17 source, not an already-upgraded fixture.
	database, err := sql.Open("sqlite3", owner.Layout().Database)
	require.NoError(t, err)
	_, err = database.ExecContext(ctx, `DROP TABLE traffic_selection; DELETE FROM schema_migrations WHERE version=18; PRAGMA user_version=17`)
	require.NoError(t, err)
	require.NoError(t, database.Close())
	require.NoError(t, owner.MarkClean())
	require.NoError(t, owner.Close())
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
