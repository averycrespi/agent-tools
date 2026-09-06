package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/ncruces/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMutateArmsBeforeWorkAndDisarmsAfterDurableCommit(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	store, err := Initialize(ctx, ownership, testInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	err = store.Mutate(ctx, func(transaction *sql.Tx) error {
		_, err := transaction.ExecContext(ctx, `UPDATE gateway_meta SET revision = revision + 1 WHERE singleton = 1`)
		return err
	})
	require.NoError(t, err)
	assert.False(t, store.Latched())
	_, err = os.Lstat(ownership.Layout().MutationMarker)
	assert.ErrorIs(t, err, os.ErrNotExist)
	identity, err := store.Identity(ctx)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), identity.Revision)
}

func TestMutateRejectsWithoutQueuing(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	store, err := Initialize(ctx, ownership, testInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- store.Mutate(ctx, func(*sql.Tx) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	assert.ErrorIs(t, store.Mutate(ctx, func(*sql.Tx) error { return nil }), ErrMutationBusy)
	close(release)
	require.NoError(t, <-finished)
}

func TestArmFaultsPreventSQLiteWorkAndLatchCurrentProcess(t *testing.T) {
	for _, point := range []FaultPoint{
		FaultArmCreate,
		FaultArmWrite,
		FaultArmFileSync,
		FaultArmRename,
		FaultArmDirectorySync,
	} {
		t.Run(string(point), func(t *testing.T) {
			ctx := context.Background()
			ownership := newOwnership(t)
			store, err := initializeWithOptions(ctx, ownership, testInstallationID, testOptions{
				fault: failOnce(point),
			})
			require.NoError(t, err)
			defer func() { require.NoError(t, store.Close()) }()
			called := false
			err = store.Mutate(ctx, func(*sql.Tx) error {
				called = true
				return nil
			})
			assert.ErrorIs(t, err, ErrStorageLatched)
			assert.False(t, called)
			assert.True(t, store.Latched())
		})
	}
}

func TestCommitAndDisarmFaultsPersistFailClosedState(t *testing.T) {
	for _, point := range []FaultPoint{
		FaultAfterCommit,
		FaultDisarmRename,
		FaultDisarmDirectorySync,
		FaultDisarmDelete,
		FaultDisarmFinalDirectorySync,
	} {
		t.Run(string(point), func(t *testing.T) {
			ctx := context.Background()
			ownership := newOwnership(t)
			store, err := initializeWithOptions(ctx, ownership, testInstallationID, testOptions{
				fault: failOnce(point),
			})
			require.NoError(t, err)
			err = store.Mutate(ctx, func(transaction *sql.Tx) error {
				_, err := transaction.ExecContext(ctx, `UPDATE gateway_meta SET revision = revision + 1`)
				return err
			})
			assert.ErrorIs(t, err, ErrStorageLatched)
			assert.True(t, store.Latched())
			identity, identityErr := store.Identity(ctx)
			require.NoError(t, identityErr)
			assert.Equal(t, uint64(1), identity.Revision)
			require.NoError(t, store.Close())

			reopened, openErr := Open(ctx, ownership)
			require.NoError(t, openErr)
			if point == FaultDisarmFinalDirectorySync {
				assert.False(t, reopened.Latched(), "a known commit may restart clean when the durable tombstone deletion won")
			} else {
				assert.True(t, reopened.Latched())
			}
			require.NoError(t, reopened.Close())
		})
	}
}

func TestKnownRollbackDisarmsButStorageFailureLatches(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	store, err := Initialize(ctx, ownership, testInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	domainErr := errors.New("domain conflict")
	err = store.Mutate(ctx, func(*sql.Tx) error { return domainErr })
	assert.ErrorIs(t, err, domainErr)
	assert.False(t, store.Latched())

	err = store.Mutate(ctx, func(*sql.Tx) error {
		return sqlite3.IOERR
	})
	assert.ErrorIs(t, err, ErrStorageLatched)
	assert.True(t, store.Latched())
}

func readStoppedAudit(t *testing.T, root string, filters contract.AuditFilters) []contract.AuditSummary {
	t.Helper()
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	defer func() { require.NoError(t, ownership.Close()) }()
	store, err := openConfigured(t.Context(), ownership.Layout(), testOptions{})
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	reader, err := audit.NewRepository(store)
	require.NoError(t, err)
	page, err := reader.List(t.Context(), audit.Query{Limit: 100, Filters: filters})
	require.NoError(t, err)
	return page.Items
}

func TestOfflineVerificationAuditReflectsMarkerSettlement(t *testing.T) {
	for _, phase := range []string{"attempt", "outcome", "success"} {
		t.Run(phase, func(t *testing.T) {
			ownership := newOwnership(t)
			store, err := initializeWithOptions(t.Context(), ownership, testInstallationID, testOptions{fault: failOnce(FaultAfterCommit)})
			require.NoError(t, err)
			err = store.Mutate(t.Context(), func(tx *sql.Tx) error {
				if phase != "success" {
					_, err := tx.ExecContext(t.Context(), fmt.Sprintf(`CREATE TRIGGER refuse_verify_audit BEFORE INSERT ON control_audit_events WHEN NEW.category = 'storage' AND NEW.action = 'verify' AND json_extract(NEW.event, '$.phase') = '%s' BEGIN SELECT RAISE(ABORT, 'refused audit'); END`, phase))
					if err != nil {
						return err
					}
				}
				_, err := tx.ExecContext(t.Context(), `UPDATE gateway_meta SET revision = revision + 1`)
				return err
			})
			require.ErrorIs(t, err, ErrStorageLatched)
			require.NoError(t, store.Close())
			layout := ownership.Layout()
			require.NoError(t, ownership.Close())
			_, err = VerifyCurrent(t.Context(), layout.Root)
			if phase == "success" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "refused audit")
			}
			if phase == "attempt" {
				assert.FileExists(t, layout.MutationMarker)
			} else {
				assert.NoFileExists(t, layout.MutationMarker)
			}
			events := readStoppedAudit(t, layout.Root, contract.AuditFilters{Category: "storage", Action: "verify"})
			require.Len(t, events, map[string]int{"attempt": 0, "outcome": 1, "success": 2}[phase])
			for _, event := range events {
				assert.Equal(t, contract.AuditOffline, event.Actor.Type)
				assert.Nil(t, event.Actor.Credential)
				assert.Nil(t, event.Initiator)
				assert.Equal(t, testInstallationID, event.Target.ID)
			}
			if phase == "outcome" {
				assert.Equal(t, "pending", events[0].Outcome)
			}
			if phase == "success" {
				assert.Equal(t, "succeeded", events[0].Outcome)
				assert.Equal(t, events[0].CorrelationID, events[1].CorrelationID)
			}
		})
	}
}

func TestVerifyCurrentClearsLatchOnlyAfterCompleteOfflineVerification(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	store, err := initializeWithOptions(ctx, ownership, testInstallationID, testOptions{fault: failOnce(FaultAfterCommit)})
	require.NoError(t, err)
	err = store.Mutate(ctx, func(transaction *sql.Tx) error {
		_, err := transaction.ExecContext(ctx, `UPDATE gateway_meta SET revision = revision + 1`)
		return err
	})
	assert.ErrorIs(t, err, ErrStorageLatched)
	require.NoError(t, store.Close())
	root := ownership.Layout().Root
	require.NoError(t, ownership.Close())

	identity, err := VerifyCurrent(ctx, root)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), identity.Revision)

	reopenedOwnership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopenedOwnership.Close()) }()
	reopened, err := Open(ctx, reopenedOwnership)
	require.NoError(t, err)
	assert.False(t, reopened.Latched())
	require.NoError(t, reopened.Close())
}

func TestMalformedMarkerAndDatabaseSizeRemainFailClosed(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	store, err := Initialize(ctx, ownership, testInstallationID)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	require.NoError(t, os.WriteFile(ownership.Layout().MutationMarker, []byte("not-json\n"), 0o600))

	reopened, err := Open(ctx, ownership)
	require.NoError(t, err)
	assert.True(t, reopened.Latched())
	require.NoError(t, reopened.Close())
	root := ownership.Layout().Root
	require.NoError(t, ownership.Close())

	_, err = VerifyCurrent(ctx, root)
	require.NoError(t, err)
	reopenedOwnership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopenedOwnership.Close()) }()
	reopened, err = openWithOptions(ctx, reopenedOwnership, testOptions{databaseByteLimit: 1})
	require.NoError(t, err)
	assert.True(t, reopened.Latched())
	assert.ErrorIs(t, reopened.Mutate(ctx, func(*sql.Tx) error { return nil }), ErrStorageLatched)
	require.NoError(t, reopened.Close())
}

func TestVerifyCurrentRefusesRunningOwnerAndKeepsMarkerOnCorruption(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	store, err := initializeWithOptions(ctx, ownership, testInstallationID, testOptions{fault: failOnce(FaultAfterCommit)})
	require.NoError(t, err)
	err = store.Mutate(ctx, func(*sql.Tx) error { return nil })
	assert.ErrorIs(t, err, ErrStorageLatched)
	root := ownership.Layout().Root
	_, err = VerifyCurrent(ctx, root)
	assert.ErrorIs(t, err, gatewaypaths.ErrInUse)
	require.NoError(t, store.Close())
	require.NoError(t, ownership.Close())

	require.NoError(t, os.WriteFile(filepath.Join(root, gatewaypaths.DatabaseName), []byte("corrupt"), 0o600))
	_, err = VerifyCurrent(ctx, root)
	assert.Error(t, err)
	_, markerErr := os.Lstat(filepath.Join(root, gatewaypaths.MutationMarkerName))
	assert.NoError(t, markerErr)
}

func TestUncleanRestartRunsIntegrityVerification(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "gateway")
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err := Initialize(ctx, ownership, testInstallationID)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	require.NoError(t, ownership.Close())

	databasePath := filepath.Join(root, gatewaypaths.DatabaseName)
	require.NoError(t, os.WriteFile(databasePath, []byte("corrupt"), 0o600))
	restarted, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	defer func() { require.NoError(t, restarted.Close()) }()
	assert.True(t, restarted.WasUnclean())
	_, err = Open(ctx, restarted)
	assert.Error(t, err)
}

func TestSuccessfulQueriesNeverClearLatch(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	store, err := initializeWithOptions(ctx, ownership, testInstallationID, testOptions{fault: failOnce(FaultAfterCommit)})
	require.NoError(t, err)
	err = store.Mutate(ctx, func(*sql.Tx) error { return nil })
	assert.ErrorIs(t, err, ErrStorageLatched)
	_, err = store.Identity(ctx)
	require.NoError(t, err)
	_, err = store.Settings(ctx)
	require.NoError(t, err)
	assert.True(t, store.Latched())
	require.NoError(t, store.Close())
}

func failOnce(want FaultPoint) func(FaultPoint) error {
	failed := false
	return func(got FaultPoint) error {
		if !failed && got == want {
			failed = true
			return errors.New("injected marker fault")
		}
		return nil
	}
}
