package composition

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/configimport"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

func importFixture(t *testing.T) (*gatewaypaths.Ownership, gatewaypaths.InitializationPlan) {
	t.Helper()
	value := contract.ConfigExport{Format: contract.ConfigExportFormat, Version: 1, Agents: []contract.ConfigAgent{{Ref: "agent/1", DisplayName: "Imported", State: contract.PrincipalActive, Visibility: contract.VisibilityAll, HTTPDefault: contract.HTTPDefaultBlock}}, MCP: contract.ConfigMCP{Servers: []contract.ConfigServer{}, Grants: []contract.ConfigMCPGrant{{Agent: "agent/1", Server: "mcp/mcp_gateway", Effect: contract.GrantAllow, Constraint: json.RawMessage("null")}}}, HTTP: contract.ConfigHTTP{Credentials: []contract.ConfigHTTPCredential{}, Grants: []contract.ConfigHTTPGrant{}}, Git: contract.ConfigGit{Credentials: []contract.ConfigGitCredential{}, Repositories: []contract.ConfigGitRepository{}, Grants: []contract.ConfigGitGrant{}, RoutingOrigins: []string{}}}
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return importBytesFixture(t, data)
}

func importBytesFixture(t *testing.T, data []byte) (*gatewaypaths.Ownership, gatewaypaths.InitializationPlan) {
	t.Helper()
	input, err := configimport.Decode(data)
	require.NoError(t, err)
	owner, err := gatewaypaths.AcquireInitialization(filepath.Join(t.TempDir(), "gateway"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Close()) })
	installation, err := admin.NewID(compositionTime, rand.Reader)
	require.NoError(t, err)
	traffic, err := admin.NewID(compositionTime, rand.Reader)
	require.NoError(t, err)
	plan := gatewaypaths.InitializationPlan{Version: 1, Root: owner.Layout().Root, Bearer: owner.Layout().AdminBearer, Installation: installation, Traffic: traffic, ImportDigest: input.Digest}
	require.NoError(t, gatewaypaths.BeginInitialization(owner, plan))
	require.NoError(t, gatewaypaths.RetainInitializationInput(owner, plan, input.Bytes))
	return owner, plan
}

func TestConfigurationImportReconcilesActualTransactionAndCompletionBoundaries(t *testing.T) {
	points := []string{"control-effect", "custody-effect", "traffic-effect", "administrator-effect", "ca-effect", "certificate-effect", "import-transaction-staged", "import-transaction-committed", "import-effect", "import-sealed", "complete-effect", "complete-sealed"}
	for _, point := range points {
		t.Run(point, func(t *testing.T) {
			owner, plan := importFixture(t)
			injected := errors.New("interrupted import")
			clock := testutil.NewFakeClock(compositionTime)
			_, err := initializeOwned(t.Context(), owner, plan, clock, rand.Reader, func(actual string) error {
				if actual == point {
					return injected
				}
				return nil
			})
			require.ErrorIs(t, err, injected)
			retained := map[string][]byte{}
			for _, name := range []string{gatewaypaths.MasterKeyName, gatewaypaths.AdminBearerName, gatewaypaths.PublicCertificateName} {
				data, err := os.ReadFile(filepath.Join(plan.Root, name))
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				require.NoError(t, err)
				retained[name] = data
			}
			require.NoError(t, owner.Close())
			reopened, err := gatewaypaths.AcquireStoppedExisting(plan.Root)
			require.NoError(t, err)
			defer func() { require.NoError(t, reopened.Close()) }()
			_, complete, err := gatewaypaths.ReadInitialization(reopened.Layout())
			require.NoError(t, err)
			if !complete {
				_, err = InitializeOwned(t.Context(), reopened, plan, clock, rand.Reader)
				require.NoError(t, err)
			} else {
				require.NoError(t, gatewaypaths.RemoveInitializationInput(reopened))
			}
			for name, data := range retained {
				actual, err := os.ReadFile(filepath.Join(plan.Root, name))
				require.NoError(t, err)
				require.Equal(t, data, actual)
			}
			_, err = os.Stat(filepath.Join(plan.Root, gatewaypaths.InitializationName, "input"))
			require.ErrorIs(t, err, os.ErrNotExist)
			_, err = storage.InspectMaintenance(t.Context(), reopened, func(tx *sql.Tx) error {
				for table, count := range map[string]int{"principals": 1, "grants": 1, "configuration_import": 1, "admin_credentials": 1, "secret_custody": 1, "keyring_authorities": 1, "server_operations": 0, "server_auth_flows": 0, "grant_requests": 0} {
					var actual int
					require.NoError(t, tx.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&actual))
					require.Equal(t, count, actual, table)
				}
				return nil
			})
			require.NoError(t, err)
			before := initializationTree(t, plan.Root)
			_, err = InitializeOwned(t.Context(), reopened, plan, clock, rand.Reader)
			require.Error(t, err)
			require.Equal(t, before, initializationTree(t, plan.Root))
		})
	}
}

func TestConfigurationImportActualMarkerPublicationFailuresNeverReplay(t *testing.T) {
	for _, point := range []storage.FaultPoint{storage.FaultArmCreate, storage.FaultArmWrite, storage.FaultArmFileSync, storage.FaultArmRename, storage.FaultArmDirectorySync, storage.FaultAfterCommit, storage.FaultDisarmRename, storage.FaultDisarmDirectorySync, storage.FaultDisarmDelete, storage.FaultDisarmFinalDirectorySync} {
		t.Run(string(point), func(t *testing.T) {
			owner, plan := importFixture(t)
			injected := errors.New("marker publication failed")
			clock := testutil.NewFakeClock(compositionTime)
			_, err := initializeOwned(t.Context(), owner, plan, clock, rand.Reader, func(actual string) error {
				if actual == "import-marker:"+string(point) {
					return injected
				}
				return nil
			})
			require.ErrorIs(t, err, injected)
			require.NoError(t, owner.Close())
			reopened, err := gatewaypaths.AcquireStoppedExisting(plan.Root)
			require.NoError(t, err)
			defer func() { require.NoError(t, reopened.Close()) }()
			provenUnstarted := point == storage.FaultArmCreate || point == storage.FaultArmWrite || point == storage.FaultArmFileSync || point == storage.FaultArmRename
			snapshot, inspectErr := storage.InspectMaintenance(t.Context(), reopened, func(tx *sql.Tx) error {
				var principals, grants, receipts int
				require.NoError(t, tx.QueryRowContext(t.Context(), `SELECT (SELECT count(*) FROM principals),(SELECT count(*) FROM grants),(SELECT count(*) FROM configuration_import)`).Scan(&principals, &grants, &receipts))
				if provenUnstarted || point == storage.FaultArmDirectorySync {
					require.Zero(t, receipts)
				} else {
					require.Equal(t, 1, receipts)
				}
				require.Equal(t, receipts, principals)
				require.Equal(t, receipts, grants)
				return nil
			})
			require.NoError(t, inspectErr)
			resumable := provenUnstarted || point == storage.FaultDisarmFinalDirectorySync
			require.Equal(t, !resumable, snapshot.Marked)
			before := initializationTree(t, plan.Root)
			_, err = InitializeOwned(t.Context(), reopened, plan, clock, rand.Reader)
			if resumable {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.Equal(t, before, initializationTree(t, plan.Root))
			}
			_, err = storage.InspectMaintenance(t.Context(), reopened, func(tx *sql.Tx) error {
				var count int
				require.NoError(t, tx.QueryRowContext(t.Context(), `SELECT count(*) FROM configuration_import`).Scan(&count))
				if resumable {
					require.Equal(t, 1, count)
				}
				return nil
			})
			require.NoError(t, err)
		})
	}
}

func TestConfigurationImportRefusesChangedRetainedInputAndCommittedGraph(t *testing.T) {
	for _, scenario := range []string{"missing-input", "changed-input", "changed-graph", "missing-receipt"} {
		t.Run(scenario, func(t *testing.T) {
			owner, plan := importFixture(t)
			clock := testutil.NewFakeClock(compositionTime)
			_, err := initializeOwned(t.Context(), owner, plan, clock, rand.Reader, func(point string) error {
				if point == "import-transaction-committed" {
					return errors.New("stop")
				}
				return nil
			})
			require.Error(t, err)
			switch scenario {
			case "missing-input":
				require.NoError(t, os.Remove(filepath.Join(plan.Root, gatewaypaths.InitializationName, "input")))
			case "changed-input":
				require.NoError(t, os.WriteFile(filepath.Join(plan.Root, gatewaypaths.InitializationName, "input"), []byte(`{}`), 0600))
			default:
				store, err := storage.Open(t.Context(), owner)
				require.NoError(t, err)
				statement := `UPDATE principals SET display_name='changed'`
				if scenario == "missing-receipt" {
					statement = `DELETE FROM configuration_import`
				}
				require.NoError(t, store.Mutate(audit.WithOffline(context.Background()), func(tx *sql.Tx) error { _, err := tx.Exec(statement); return err }))
				require.NoError(t, store.Checkpoint(t.Context()))
				require.NoError(t, store.Close())
			}
			before := initializationTree(t, plan.Root)
			_, err = InitializeOwned(t.Context(), owner, plan, clock, rand.Reader)
			require.Error(t, err)
			require.Equal(t, before, initializationTree(t, plan.Root))
		})
	}
}
