package configexport

import (
	"database/sql"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/resourcekeys"
	"github.com/stretchr/testify/require"
)

func TestV2TypedReferencesAreIndependentOfInstallationIdentities(t *testing.T) {
	var outputs [][]byte
	for _, base := range []int{100, 200} {
		store := fixture(t)
		require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error {
			exec(t, tx, `INSERT INTO principals(id,display_name,state,visibility,revision,credential_revision,created_at,updated_at) VALUES(?,'Builder','active','all',1,0,?,?)`, id(base), stamp, stamp)
			_, err := resourcekeys.ReserveTx(t.Context(), tx, resourcekeys.Agent, id(base), "builder")
			require.NoError(t, err)
			exec(t, tx, `INSERT INTO grants(id,principal_id,effect,server_id,upstream_name,constraint_json,created_at,revision,read_only) VALUES(?,?,'allow',?,NULL,NULL,?,1,0)`, id(base+1), id(base), contract.SyntheticServerID, stamp)
			_, err = resourcekeys.ReserveTx(t.Context(), tx, resourcekeys.MCPGrant, id(base+1), "gateway-access")
			return err
		}))
		raw, err := New(store).ExportBundle(t.Context())
		require.NoError(t, err)
		outputs = append(outputs, raw)
	}
	require.Equal(t, outputs[0], outputs[1])
}
