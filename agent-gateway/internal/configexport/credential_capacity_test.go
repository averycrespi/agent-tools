package configexport

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/gitcredentials"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httpcredentials"
	"github.com/stretchr/testify/require"
)

func TestRetainedCredentialProjectionsRejectRowCapacityWithoutTruncation(t *testing.T) {
	for _, kind := range []string{"git", "http"} {
		t.Run(kind, func(t *testing.T) {
			store := fixture(t)
			insert := func(tx *sql.Tx, n int) {
				if kind == "git" {
					exec(t, tx, `INSERT INTO git_credentials(id,name,origin,header,prefix,revision,material_revision,deleted,created_at,updated_at) VALUES(?,'Git','https://github.com:443','Authorization','Bearer ',1,0,1,?,?)`, id(n+1000), stamp, stamp)
				} else {
					exec(t, tx, `INSERT INTO http_credentials(id,name,host,port,allow_wildcard,header,prefix,revision,material_revision,deleted,created_at,updated_at) VALUES(?,'HTTP','example.com',443,0,'Authorization','Bearer ',1,0,1,?,?)`, id(n+1000), stamp, stamp)
				}
			}
			require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
				for i := 0; i < 1024; i++ {
					insert(tx, i)
				}
				return nil
			}))
			projection := func(tx *sql.Tx, want int, wantErr error) {
				if kind == "git" {
					values, err := gitcredentials.RetainedConfigurationTx(t.Context(), tx)
					require.ErrorIs(t, err, wantErr)
					require.Len(t, values, want)
				} else {
					values, err := httpcredentials.RetainedConfigurationTx(t.Context(), tx)
					require.ErrorIs(t, err, wantErr)
					require.Len(t, values, want)
				}
			}
			require.NoError(t, store.View(t.Context(), func(tx *sql.Tx) error { projection(tx, 1024, nil); return nil }))
			setActive := func(count int) {
				require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
					_, err := tx.ExecContext(t.Context(), fmt.Sprintf("UPDATE %s_credentials SET deleted=CASE WHEN id IN (SELECT id FROM %s_credentials ORDER BY id LIMIT ?) THEN 0 ELSE 1 END", kind, kind), count)
					return err
				}))
			}
			setActive(256)
			require.NoError(t, store.View(t.Context(), func(tx *sql.Tx) error { projection(tx, 1024, nil); return nil }))
			setActive(257)
			require.NoError(t, store.View(t.Context(), func(tx *sql.Tx) error {
				if kind == "git" {
					projection(tx, 0, gitcredentials.ErrLimit)
				} else {
					projection(tx, 0, httpcredentials.ErrLimit)
				}
				return nil
			}))
			// The intentionally corrupt over-capacity inventory exercises the
			// owner's fail-closed projection, never a reopened valid installation.
			setActive(0)
			require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error { insert(tx, 1024); return nil }))
			require.NoError(t, store.View(t.Context(), func(tx *sql.Tx) error {
				if kind == "git" {
					projection(tx, 0, gitcredentials.ErrLimit)
				} else {
					projection(tx, 0, httpcredentials.ErrLimit)
				}
				return nil
			}))
		})
	}
}
