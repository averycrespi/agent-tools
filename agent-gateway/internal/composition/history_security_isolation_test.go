package composition

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/backup"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/invocation"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestSecurityFaultsRemainClosedWhileHistoryOpeningStalls(t *testing.T) {
	for _, fault := range []string{"audit", string(storage.FaultArmCreate), string(storage.FaultAfterCommit)} {
		t.Run(fault, func(t *testing.T) {
			var armed atomic.Bool
			options, cleanup := newCompositionOptionsWithFault(t, func(point storage.FaultPoint) error {
				if armed.Load() && string(point) == fault {
					return errors.New("injected control uncertainty")
				}
				return nil
			})
			defer cleanup()
			entered, release := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			built, err := newWithHooks(options, constructorHooks{openTraffic: func(context.Context, *gatewaypaths.Ownership, string, string, invocation.TrafficConfig) (*invocation.TrafficStore, error) {
				close(entered)
				<-release
				return nil, errors.New("injected history failure")
			}})
			require.NoError(t, err)
			defer func() { unblock(); built.shutdownConstructed() }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("opener did not enter")
			}
			require.NoError(t, built.Start(t.Context()))
			ctx := audit.WithSystem(t.Context())
			if fault == "audit" {
				require.NoError(t, options.Store.Mutate(ctx, func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, `CREATE TRIGGER refuse_isolation_audit BEFORE INSERT ON control_audit_events WHEN NEW.category='principal' BEGIN SELECT RAISE(ABORT,'refused'); END`)
					return err
				}))
			}
			armed.Store(true)
			_, err = built.authorization.CreatePrincipal(ctx, authorization.CreatePrincipalRequest{DisplayName: "must be audited", Visibility: contract.VisibilityAll})
			require.Error(t, err, "optional history loss must not permit successful unaudited security mutation")
			require.Equal(t, "opening", built.traffic.Status(t.Context()).State)
			// Read-only artifact observation records known committed facts even when
			// authority is latched; it cannot reopen or qualify that authority.
			db, err := sql.Open("sqlite3", "file:"+options.Ownership.Layout().Database+"?mode=ro")
			require.NoError(t, err)
			defer func() { require.NoError(t, db.Close()) }()
			var principals, audits int
			require.NoError(t, db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM principals),(SELECT count(*) FROM control_audit_events WHERE category='principal')`).Scan(&principals, &audits))
			if fault == string(storage.FaultAfterCommit) {
				require.Equal(t, 1, principals)
				require.Positive(t, audits, "a committed-but-unacknowledged mutation still has atomic audit")
			} else {
				require.Zero(t, principals)
				require.Zero(t, audits)
			}
			if fault != "audit" {
				require.True(t, options.Store.Latched())
			}
		})
	}
}

func TestSecurityBackupCompletesBeforeHistoryOpenerSettles(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	built, err := newWithHooks(options, constructorHooks{openTraffic: func(ctx context.Context, owner *gatewaypaths.Ownership, installation, generation string, config invocation.TrafficConfig) (*invocation.TrafficStore, error) {
		close(entered)
		<-release
		return invocation.OpenTraffic(ctx, owner, installation, generation, config)
	}})
	require.NoError(t, err)
	defer func() { unblock(); built.shutdownConstructed() }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("opener did not enter")
	}
	manager, err := backup.New(backup.Options{Store: options.Store, Layout: options.Ownership.Layout(), Clock: options.Clock, Entropy: options.Entropy})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(audit.WithSystem(t.Context()), 5*time.Second)
	defer cancel()
	artifact, _, err := manager.Create(ctx, "isolation-owner", "opening-history")
	require.NoError(t, err)
	require.Equal(t, "omitted", artifact.History)
	require.Equal(t, "opening", built.traffic.Status(ctx).State)
	identity, err := storage.VerifyBackup(ctx, filepath.Join(options.Ownership.Layout().Backups, artifact.ID, "gateway.db"))
	require.NoError(t, err)
	selected, err := options.Store.SelectedTraffic(ctx)
	require.NoError(t, err)
	require.Equal(t, selected, identity.TrafficGeneration, "backup copies control selection; restore disables omitted history")
	_, err = os.Stat(filepath.Join(options.Ownership.Layout().Backups, artifact.ID, "traffic.db"))
	require.ErrorIs(t, err, os.ErrNotExist)
	require.False(t, options.Store.Latched())
	// Finishing the backup did not transfer installation ownership from the opener.
	_, err = gatewaypaths.AcquireStoppedExisting(options.Ownership.Layout().Root)
	require.ErrorIs(t, err, gatewaypaths.ErrInUse)
	unblock()
}
