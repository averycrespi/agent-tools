package storagefixture

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"

	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const installationID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

func fixtureOwner(t *testing.T) *gatewaypaths.Ownership {
	t.Helper()
	owner, err := gatewaypaths.Acquire(testutil.NewOwnerOnlyDataRoot(t))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Close()) })
	return owner
}

func schema(t *testing.T, store *storage.Store) []string {
	t.Helper()
	var result []string
	require.NoError(t, store.View(t.Context(), func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(t.Context(), `SELECT type || ':' || name || ':' || tbl_name || ':' || coalesce(sql, '') FROM sqlite_schema ORDER BY type, name`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var row string
			if err := rows.Scan(&row); err != nil {
				return err
			}
			result = append(result, row)
		}
		return rows.Err()
	}))
	return result
}

func TestTemplateMatchesRealInitialization(t *testing.T) {
	direct, err := storage.Initialize(t.Context(), fixtureOwner(t), installationID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, direct.Close()) })
	owner := fixtureOwner(t)
	copied, err := New(installationID).Open(t.Context(), owner)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, copied.Close()) })
	wantIdentity, err := direct.Identity(t.Context())
	require.NoError(t, err)
	identity, err := copied.Identity(t.Context())
	require.NoError(t, err)
	assert.Equal(t, wantIdentity, identity)
	assert.Equal(t, storage.CurrentSchema, identity.SchemaVersion)
	wantSettings, err := direct.Settings(t.Context())
	require.NoError(t, err)
	settings, err := copied.Settings(t.Context())
	require.NoError(t, err)
	assert.Equal(t, wantSettings, settings)
	wantVersions, err := direct.MigrationVersions(t.Context())
	require.NoError(t, err)
	versions, err := copied.MigrationVersions(t.Context())
	require.NoError(t, err)
	assert.Equal(t, wantVersions, versions)
	assert.Equal(t, schema(t, direct), schema(t, copied))
	layout, err := owner.ActiveLayout()
	require.NoError(t, err)
	require.NoError(t, gatewaypaths.ValidateOwnerOnlyFile(layout.Database))
	_, err = New(installationID).Open(t.Context(), owner)
	assert.ErrorIs(t, err, os.ErrExist, "opening a fixture must never overwrite an existing database")
	require.NoError(t, gatewaypaths.ValidateOwnerOnlyFile(layout.Database))
}

func TestTemplateConcurrentCopiesHaveIndependentDurableState(t *testing.T) {
	template := New(installationID)
	for range 4 {
		t.Run("private-generation", func(t *testing.T) {
			t.Parallel()
			owner := fixtureOwner(t)
			store, err := template.Open(t.Context(), owner)
			require.NoError(t, err)
			identity, err := store.Identity(t.Context())
			require.NoError(t, err)
			assert.Equal(t, uint64(0), identity.Revision)
			require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
				_, err := tx.ExecContext(t.Context(), `UPDATE gateway_meta SET revision = 41 WHERE singleton = 1`)
				return err
			}))
			require.NoError(t, store.Close())
			reopened, err := storage.Open(t.Context(), owner)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, reopened.Close()) })
			identity, err = reopened.Identity(t.Context())
			require.NoError(t, err)
			assert.Equal(t, uint64(41), identity.Revision)
		})
	}
}

func TestTemplateFailureAndNormalOpenValidation(t *testing.T) {
	invalid := New("invalid")
	for range 2 {
		owner := fixtureOwner(t)
		_, err := invalid.Open(t.Context(), owner)
		assert.ErrorIs(t, err, storage.ErrInvalidInstallationID)
		layout, err := owner.ActiveLayout()
		require.NoError(t, err)
		_, err = os.Stat(layout.Database)
		assert.ErrorIs(t, err, os.ErrNotExist)
	}
	valid := New(installationID)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		name     string
		template *Template
		ctx      context.Context
		wantErr  error
	}{
		{name: "corrupt", template: &Template{image: sync.OnceValues(func() (string, error) { return "not sqlite", nil })}, ctx: t.Context(), wantErr: storage.ErrInvalidDatabase},
		{name: "canceled", template: valid, ctx: canceled, wantErr: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			owner := fixtureOwner(t)
			_, err := test.template.Open(test.ctx, owner)
			require.ErrorIs(t, err, test.wantErr)
			layout, err := owner.ActiveLayout()
			require.NoError(t, err)
			for _, suffix := range []string{"", "-wal", "-shm"} {
				_, err := os.Lstat(layout.Database + suffix)
				assert.ErrorIs(t, err, os.ErrNotExist)
			}
			store, err := valid.Open(t.Context(), owner)
			require.NoError(t, err, "failed copies must not block a new fixture for the same owner")
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			identity, err := store.Identity(t.Context())
			require.NoError(t, err)
			assert.Equal(t, installationID, identity.InstallationID)
		})
	}
}

func TestTemplateRemovesGenerationRootsAfterSuccessAndFailure(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	for _, id := range []string{installationID, "invalid"} {
		template := New(id)
		_, err := template.image()
		if id == installationID {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, storage.ErrInvalidInstallationID)
		}
		entries, err := os.ReadDir(root)
		require.NoError(t, err)
		assert.Empty(t, entries)
	}
}

func BenchmarkCurrentSchemaFixture(b *testing.B) {
	template := New(installationID)
	for _, mode := range []string{"initialize", "copy-open"} {
		b.Run(mode, func(b *testing.B) {
			root := b.TempDir()
			for index := 0; index < b.N; index++ {
				owner, err := gatewaypaths.Acquire(filepath.Join(root, "gateway"))
				require.NoError(b, err)
				var store *storage.Store
				if mode == "initialize" {
					store, err = storage.Initialize(context.Background(), owner, installationID)
				} else {
					store, err = template.Open(context.Background(), owner)
				}
				require.NoError(b, err)
				require.NoError(b, store.Close())
				require.NoError(b, owner.Close())
				require.NoError(b, os.RemoveAll(filepath.Join(root, "gateway")))
			}
		})
	}
}
