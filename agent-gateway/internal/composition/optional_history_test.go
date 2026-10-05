package composition

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/invocation"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestOptionalHistoryReadinessBeforeOpenAndLateDrain(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	entered, release, settled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	built, err := newWithHooks(options, constructorHooks{openTraffic: func(ctx context.Context, owner *gatewaypaths.Ownership, installation, generation string, config invocation.TrafficConfig) (*invocation.TrafficStore, error) {
		close(entered)
		<-release
		defer close(settled)
		return invocation.OpenTraffic(ctx, owner, installation, generation, config)
	}})
	require.NoError(t, err)
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer func() { unblock(); built.shutdownConstructed() }()
	<-entered
	require.Equal(t, "opening", built.Traffic().Status(t.Context()).State)
	require.NoError(t, built.Start(t.Context()))
	_, available := built.AgentIngress()
	require.True(t, available)
	_, available = built.ControlAPI()
	require.True(t, available)
	require.True(t, built.accepting.Load(), "security-ready service must not await history")
	deadline, cancel := context.WithCancel(t.Context())
	drain := built.Drain(deadline)
	cancel()
	require.Positive(t, (<-drain).Unconfirmed)
	// Neither the installation lock nor clean marker can be transferred merely
	// because bounded observation timed out while the opener still owns work.
	_, err = gatewaypaths.AcquireStoppedExisting(options.Ownership.Layout().Root)
	require.ErrorIs(t, err, gatewaypaths.ErrInUse)
	_, err = os.Stat(options.Ownership.Layout().RunMarker)
	require.NoError(t, err, "unsettled opener must not mark installation clean")
	require.Equal(t, "disabled", built.Traffic().Status(t.Context()).State)
	unblock()
	<-settled
	result := <-built.Drain(t.Context())
	require.Zero(t, result.Unconfirmed)
	require.False(t, built.Traffic().Healthy())
	// The late result was closed, so explicit stopped ownership may now open it
	// under the still-held installation owner, never as a second live writer.
	generation, err := options.Store.SelectedTraffic(t.Context())
	require.NoError(t, err)
	traffic, err := invocation.OpenTraffic(t.Context(), options.Ownership, options.InstallationID, generation, invocation.DefaultTrafficConfig())
	require.NoError(t, err)
	require.NoError(t, traffic.Close())
}

func TestOptionalHistoryPreservesUnavailableArtifacts(t *testing.T) {
	for _, kind := range []string{"missing", "corrupt", "foreign-binding", "unsafe-link", "permission", "wal"} {
		t.Run(kind, func(t *testing.T) {
			options, cleanup := newCompositionOptions(t)
			defer cleanup()
			generation, err := options.Store.SelectedTraffic(t.Context())
			require.NoError(t, err)
			path := filepath.Join(options.Ownership.Layout().Root, "traffic-"+generation+".db")
			var expected []byte
			switch kind {
			case "missing":
				require.NoError(t, os.Remove(path))
			case "corrupt":
				expected = []byte("not a database")
				require.NoError(t, os.WriteFile(path, expected, 0o600))
			case "foreign-binding":
				db, err := sql.Open("sqlite3", "file:"+path+"?mode=rw")
				require.NoError(t, err)
				_, err = db.ExecContext(t.Context(), `UPDATE traffic_meta SET installation='01ARZ3NDEKTSV4RRFFQ69G5FA0'`)
				require.NoError(t, err)
				_, err = db.ExecContext(t.Context(), `PRAGMA wal_checkpoint(TRUNCATE)`)
				require.NoError(t, err)
				require.NoError(t, db.Close())
				expected, err = os.ReadFile(path)
				require.NoError(t, err)
			case "unsafe-link":
				require.NoError(t, os.Rename(path, path+".retained"))
				require.NoError(t, os.Symlink(path+".retained", path))
			case "permission":
				require.NoError(t, os.Chmod(path, 0o644))
			case "wal":
				expected = []byte("untouched WAL")
				require.NoError(t, os.WriteFile(path+"-wal", expected, 0o600))
			}
			built, err := New(options)
			require.NoError(t, err)
			defer built.shutdownConstructed()
			require.NoError(t, built.Start(t.Context()))
			require.Eventually(t, func() bool { return built.Traffic().Status(t.Context()).State == "unavailable" }, 5*time.Second, time.Millisecond)
			require.True(t, built.accepting.Load())
			switch kind {
			case "missing":
				_, err = os.Lstat(path)
				require.True(t, os.IsNotExist(err))
			case "corrupt", "foreign-binding":
				actual, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, expected, actual)
			case "unsafe-link":
				info, err := os.Lstat(path)
				require.NoError(t, err)
				require.NotZero(t, info.Mode()&os.ModeSymlink)
			case "permission":
				info, err := os.Stat(path)
				require.NoError(t, err)
				require.Equal(t, os.FileMode(0o644), info.Mode().Perm())
			case "wal":
				actual, err := os.ReadFile(path + "-wal")
				require.NoError(t, err)
				require.Equal(t, expected, actual)
			}
		})
	}
}

func TestOptionalHistoryUnselectedAndInvalidSelector(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "unselected", true: "missing-selector"}[invalid], func(t *testing.T) {
			options, cleanup := newCompositionOptions(t)
			defer cleanup()
			require.NoError(t, options.Store.Mutate(t.Context(), func(tx *sql.Tx) error {
				query := `UPDATE traffic_selection SET generation=NULL WHERE singleton=1`
				if invalid {
					query = `DELETE FROM traffic_selection`
				}
				_, err := tx.ExecContext(t.Context(), query)
				return err
			}))
			built, err := New(options)
			if invalid {
				require.Error(t, err)
				require.Nil(t, built)
				return
			}
			require.NoError(t, err)
			defer built.shutdownConstructed()
			require.Equal(t, "disabled", built.Traffic().Status(t.Context()).State)
			require.NoError(t, built.Start(t.Context()))
			identity, err := options.Store.Identity(t.Context())
			require.NoError(t, err)
			require.Equal(t, storage.CurrentSchema, identity.SchemaVersion)
		})
	}
}
