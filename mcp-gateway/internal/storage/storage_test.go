package storage

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	_ "github.com/ncruces/go-sqlite3/driver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testInstallationID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

func TestInitializeCreatesVerifiedGatewayDatabase(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	store, err := Initialize(ctx, ownership, testInstallationID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	identity, err := store.Identity(ctx)
	require.NoError(t, err)
	assert.Equal(t, testInstallationID, identity.InstallationID)
	assert.Equal(t, CurrentSchema, identity.SchemaVersion)
	assert.Equal(t, uint64(0), identity.Revision)

	settings, err := store.Settings(ctx)
	require.NoError(t, err)
	assert.Equal(t, ApplicationID, settings.ApplicationID)
	assert.Equal(t, "wal", settings.JournalMode)
	assert.Equal(t, 2, settings.Synchronous)
	assert.True(t, settings.ForeignKeys)
	assert.Equal(t, BusyTimeoutMilliseconds, settings.BusyTimeoutMilliseconds)
	assert.Equal(t, "ok", settings.Integrity)
	var pageSize, maxPageCount int64
	require.NoError(t, store.database.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize))
	require.NoError(t, store.database.QueryRowContext(ctx, `PRAGMA max_page_count`).Scan(&maxPageCount))
	assert.LessOrEqual(t, maxPageCount*pageSize, compiledDatabaseByteLimit())

	versions, err := store.MigrationVersions(ctx)
	require.NoError(t, err)
	assert.Equal(t, []int{1, 2}, versions)
	assertFileMode(t, ownership.Layout().Database, 0o600)
}

func TestOpenRevalidatesIdentityAndMigrationHistory(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	initialized, err := Initialize(ctx, ownership, testInstallationID)
	require.NoError(t, err)
	require.NoError(t, initialized.Close())

	store, err := Open(ctx, ownership)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	raw := openRaw(t, ownership.Layout().Database)
	_, err = raw.Exec(`DELETE FROM schema_migrations WHERE version = 1`)
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	_, err = Open(ctx, ownership)
	assert.ErrorIs(t, err, ErrInvalidDatabase)
}

func TestOpenRejectsForeignNewerAndMissingIdentity(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *sql.DB)
		want  error
	}{
		{
			name: "foreign application",
			setup: func(t *testing.T, db *sql.DB) {
				t.Helper()
				_, err := db.Exec(`PRAGMA application_id = 7`)
				require.NoError(t, err)
			},
			want: ErrInvalidDatabase,
		},
		{
			name: "newer schema",
			setup: func(t *testing.T, db *sql.DB) {
				t.Helper()
				_, err := db.Exec(`PRAGMA user_version = 999`)
				require.NoError(t, err)
			},
			want: ErrNewerSchema,
		},
		{
			name: "missing installation identity",
			setup: func(t *testing.T, db *sql.DB) {
				t.Helper()
				_, err := db.Exec(`DELETE FROM gateway_meta`)
				require.NoError(t, err)
			},
			want: ErrInvalidDatabase,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			ownership := newOwnership(t)
			store, err := Initialize(ctx, ownership, testInstallationID)
			require.NoError(t, err)
			require.NoError(t, store.Close())
			raw := openRaw(t, ownership.Layout().Database)
			test.setup(t, raw)
			require.NoError(t, raw.Close())

			_, err = Open(ctx, ownership)
			assert.ErrorIs(t, err, test.want)
		})
	}
}

func TestOpenMigratesCommittedPriorSchemaFixture(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	copyFixture(t, "testdata/schema-v0.db", ownership.Layout().Database)

	store, err := Open(ctx, ownership)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	identity, err := store.Identity(ctx)
	require.NoError(t, err)
	assert.Equal(t, testInstallationID, identity.InstallationID)
	assert.Equal(t, CurrentSchema, identity.SchemaVersion)
	assert.Equal(t, []int{1, 2}, mustMigrationVersions(t, store, ctx))
}

func TestOpenMigratesCommittedAdminSchemaFixture(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	copyFixture(t, "testdata/schema-v1.db", ownership.Layout().Database)

	store, err := Open(ctx, ownership)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	identity, err := store.Identity(ctx)
	require.NoError(t, err)
	assert.Equal(t, CurrentSchema, identity.SchemaVersion)
	assert.Equal(t, []int{1, 2}, mustMigrationVersions(t, store, ctx))
	var credentialTable string
	require.NoError(t, store.database.QueryRowContext(ctx, `
		SELECT name FROM sqlite_schema WHERE type = 'table' AND name = 'admin_credentials'`).Scan(&credentialTable))
	assert.Equal(t, "admin_credentials", credentialTable)
}

func TestInitializeRejectsInvalidOrExistingInstallations(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	_, err := Initialize(ctx, ownership, "not-an-installation-id")
	assert.ErrorIs(t, err, ErrInvalidInstallationID)

	store, err := Initialize(ctx, ownership, testInstallationID)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	_, err = Initialize(ctx, ownership, testInstallationID)
	assert.ErrorIs(t, err, ErrAlreadyInitialized)
}

func newOwnership(t *testing.T) *gatewaypaths.Ownership {
	t.Helper()
	ownership, err := gatewaypaths.Acquire(filepath.Join(t.TempDir(), "gateway"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ownership.Close()) })
	return ownership
}

func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite3", "file:"+path+"?_pragma=busy_timeout(2000)")
	require.NoError(t, err)
	require.NoError(t, database.Ping())
	return database
}

func copyFixture(t *testing.T, source, target string) {
	t.Helper()
	input, err := os.Open(source)
	require.NoError(t, err)
	defer func() { require.NoError(t, input.Close()) }()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = io.Copy(output, input)
	require.NoError(t, err)
	require.NoError(t, output.Close())
}

func mustMigrationVersions(t *testing.T, store *Store, ctx context.Context) []int {
	t.Helper()
	versions, err := store.MigrationVersions(ctx)
	require.NoError(t, err)
	return versions
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	require.NoError(t, err)
	assert.Equal(t, want, info.Mode().Perm())
	assert.False(t, info.Mode()&os.ModeSymlink != 0)
}

func TestErrorClassesRemainDistinct(t *testing.T) {
	assert.False(t, errors.Is(ErrInvalidDatabase, ErrNewerSchema))
	assert.False(t, errors.Is(ErrAlreadyInitialized, ErrInvalidInstallationID))
}
