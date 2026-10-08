package composition

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/invocation"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

// Keep the seeding connection open so its committed frames cannot be folded by
// last-connection close. The installation owner is still the sole process owner.
func TestOptionalTrafficOpensCommittedWAL(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	generation, err := options.Store.SelectedTraffic(t.Context())
	require.NoError(t, err)
	path := filepath.Join(options.Ownership.Layout().Root, "traffic-"+generation+".db")
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=rw")
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	_, err = db.ExecContext(t.Context(), `UPDATE traffic_meta SET generation='01ARZ3NDEKTSV4RRFFQ69G5FA0'`)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `PRAGMA wal_checkpoint(TRUNCATE)`)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `UPDATE traffic_meta SET generation=?`, generation)
	require.NoError(t, err)
	info, err := os.Stat(path + "-wal")
	require.NoError(t, err)
	require.Positive(t, info.Size())
	traffic, err := openOptionalTraffic(t.Context(), options.Ownership, options.InstallationID, generation, invocation.DefaultTrafficConfig())
	require.NoError(t, err)
	require.True(t, traffic.Healthy())
	require.NoError(t, traffic.Close())
}

func TestOptionalTrafficPreservesRejectedCrashWAL(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	generation, err := options.Store.SelectedTraffic(t.Context())
	require.NoError(t, err)
	path := filepath.Join(options.Ownership.Layout().Root, "traffic-"+generation+".db")
	t.Setenv("AGENT_GATEWAY_WAL_REJECT_PATH", path)
	executable, err := os.Executable()
	require.NoError(t, err)
	runner, err := testutil.NewBinaryRunner(15*time.Second, 64<<10)
	require.NoError(t, err)
	process, err := runner.Start(t.Context(), executable, "-test.run=^TestOptionalTrafficRejectedWALFixture$", "-test.timeout=12s")
	require.NoError(t, err)
	defer func() { _ = process.Stop() }()
	select {
	case <-process.StdoutReady():
	case <-time.After(10 * time.Second):
		t.Fatal("WAL seeding process did not commit")
	}
	require.NoError(t, process.Signal(syscall.SIGKILL))
	result, waitErr := process.Wait()
	require.Error(t, waitErr)
	require.Contains(t, string(result.Stdout), "foreign binding committed in WAL")
	require.True(t, result.Cleanup.Reaped)
	require.False(t, result.Cleanup.Survived)
	beforeDB, err := os.ReadFile(path)
	require.NoError(t, err)
	beforeWAL, err := os.ReadFile(path + "-wal")
	require.NoError(t, err)
	require.NotEmpty(t, beforeWAL)
	traffic, err := openOptionalTraffic(t.Context(), options.Ownership, options.InstallationID, generation, invocation.DefaultTrafficConfig())
	require.Error(t, err)
	require.Nil(t, traffic)
	afterDB, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, sha256.Sum256(beforeDB), sha256.Sum256(afterDB), "rejected generation must not be checkpointed")
	afterWAL, err := os.ReadFile(path + "-wal")
	require.NoError(t, err)
	require.Equal(t, sha256.Sum256(beforeWAL), sha256.Sum256(afterWAL), "rejected WAL must be retained byte-for-byte")
}

func TestOptionalTrafficRejectedWALFixture(t *testing.T) {
	path := os.Getenv("AGENT_GATEWAY_WAL_REJECT_PATH")
	if path == "" {
		t.Skip("owned subprocess fixture")
	}
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=rw")
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	_, err = db.ExecContext(t.Context(), `UPDATE traffic_meta SET generation='01ARZ3NDEKTSV4RRFFQ69G5FA0'`)
	require.NoError(t, err)
	fmt.Println("foreign binding committed in WAL")
	<-time.After(10 * time.Second)
	t.Fatal("fixture was not killed")
}

func TestOptionalTrafficAuthenticatesWALState(t *testing.T) {
	for _, statement := range []string{
		`UPDATE traffic_meta SET installation='01ARZ3NDEKTSV4RRFFQ69G5FA0'`,
		`UPDATE traffic_meta SET generation='01ARZ3NDEKTSV4RRFFQ69G5FA0'`,
		`PRAGMA user_version=99`,
		`DROP TRIGGER traffic_size_insert`,
	} {
		t.Run(statement, func(t *testing.T) {
			options, cleanup := newCompositionOptions(t)
			defer cleanup()
			generation, err := options.Store.SelectedTraffic(t.Context())
			require.NoError(t, err)
			path := filepath.Join(options.Ownership.Layout().Root, "traffic-"+generation+".db")
			db, err := sql.Open("sqlite3", "file:"+path+"?mode=rw")
			require.NoError(t, err)
			defer func() { require.NoError(t, db.Close()) }()
			_, err = db.ExecContext(t.Context(), statement)
			require.NoError(t, err)
			info, err := os.Stat(path + "-wal")
			require.NoError(t, err)
			require.Positive(t, info.Size())
			traffic, err := openOptionalTraffic(t.Context(), options.Ownership, options.InstallationID, generation, invocation.DefaultTrafficConfig())
			require.Error(t, err)
			require.Nil(t, traffic)
		})
	}
}
