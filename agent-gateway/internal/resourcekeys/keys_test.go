package resourcekeys

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

var template = storagefixture.New("01ARZ3NDEKTSV4RRFFQ69G5FAV")

func fixture(t *testing.T) *storage.Store {
	t.Helper()
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0700))
	owner, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Close()) })
	store, err := template.Open(t.Context(), owner)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

func TestResourceKeysScopeGrammarImmutabilityAndRollback(t *testing.T) {
	store := fixture(t)
	ctx := t.Context()
	for i, kind := range []Kind{Agent, MCPGrant, HTTPGrant, GitGrant, HTTPCredential, GitCredential, GitRepository} {
		require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error {
			key, err := ReserveTx(ctx, tx, kind, fmt.Sprintf("%026d", i+1), "build-agent_1")
			require.Equal(t, "build-agent_1", key)
			return err
		}))
		err := store.Mutate(ctx, func(tx *sql.Tx) error {
			_, err := ReserveTx(ctx, tx, kind, fmt.Sprintf("%026d", i+100), "build-agent_1")
			return err
		})
		require.ErrorIs(t, err, ErrConflict)
	}
	for _, key := range []string{"Upper", "0agent", "with space", "a.b", "a/b", "é", string(make([]byte, 65))} {
		require.False(t, contract.ValidResourceKey(key))
		err := store.Mutate(ctx, func(tx *sql.Tx) error { _, err := ReserveTx(ctx, tx, Agent, fmt.Sprintf("%026d", 99), key); return err })
		require.ErrorIs(t, err, ErrInvalid)
	}
	for _, statement := range []string{`UPDATE resource_keys SET key='replacement'`, `DELETE FROM resource_keys`} {
		require.Error(t, store.Mutate(ctx, func(tx *sql.Tx) error { _, err := tx.ExecContext(ctx, statement); return err }))
	}
	rollback := errors.New("rollback")
	err := store.Mutate(ctx, func(tx *sql.Tx) error {
		_, err := ReserveTx(ctx, tx, Agent, fmt.Sprintf("%026d", 99), "rolled-back")
		if err != nil {
			return err
		}
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error {
		_, err := ReserveTx(ctx, tx, Agent, fmt.Sprintf("%026d", 99), "rolled-back")
		return err
	}))
}

func TestGeneratedResourceKeyCollisionsAreBounded(t *testing.T) {
	store := fixture(t)
	ctx := t.Context()
	require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error {
		_, err := ReserveTx(ctx, tx, Agent, fmt.Sprintf("%026d", 1), "occupied")
		return err
	}))
	calls := 0
	require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error {
		key, err := reserveTx(ctx, tx, Agent, fmt.Sprintf("%026d", 2), "", func(string) string {
			calls++
			if calls < 3 {
				return "occupied"
			}
			return "available"
		})
		require.Equal(t, "available", key)
		return err
	}))
	require.Equal(t, 3, calls)
	calls = 0
	err := store.Mutate(ctx, func(tx *sql.Tx) error {
		_, err := reserveTx(ctx, tx, Agent, fmt.Sprintf("%026d", 3), "", func(string) string { calls++; return "occupied" })
		return err
	})
	require.ErrorIs(t, err, ErrGeneration)
	require.Equal(t, 8, calls)
	// A deleted owner's ID must not acquire a different key either.
	err = store.Mutate(ctx, func(tx *sql.Tx) error {
		_, err := ReserveTx(ctx, tx, Agent, fmt.Sprintf("%026d", 1), "another")
		return err
	})
	require.ErrorIs(t, err, ErrConflict)
}

func TestConcurrentResourceKeyCollisionNeverRebinds(t *testing.T) {
	store := fixture(t)
	ctx := t.Context()
	start := make(chan struct{})
	results := make(chan error, 8)
	var workers sync.WaitGroup
	for i := range 8 {
		workers.Go(func() {
			<-start
			results <- store.Mutate(ctx, func(tx *sql.Tx) error {
				_, err := ReserveTx(ctx, tx, MCPGrant, fmt.Sprintf("%026d", i+1), "same-grant")
				return err
			})
		})
	}
	close(start)
	workers.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		require.True(t, errors.Is(err, ErrConflict) || errors.Is(err, storage.ErrMutationBusy), "%v", err)
	}
	require.Equal(t, 1, successes)
	require.NoError(t, store.View(ctx, func(tx *sql.Tx) error {
		var count int
		err := tx.QueryRowContext(ctx, `SELECT count(*) FROM resource_keys WHERE kind='mcp_grant' AND key='same-grant'`).Scan(&count)
		require.Equal(t, 1, count)
		return err
	}))
}
