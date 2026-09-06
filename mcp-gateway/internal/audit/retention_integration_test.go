//go:build integration

package audit_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuditRetentionIntegration(t *testing.T) {
	store, repository := fixture(t)
	ctx := t.Context()
	require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error {
		statement, err := tx.PrepareContext(ctx, `INSERT INTO control_audit_events (insertion_sequence, event) VALUES (?, ?)`)
		if err != nil {
			return err
		}
		defer func() { _ = statement.Close() }()
		var first int
		if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(insertion_sequence), 0) + 1 FROM control_audit_events`).Scan(&first); err != nil {
			return err
		}
		for index := first; index <= contract.AuditRetention; index++ {
			candidate := event(index)
			candidate.Sequence = strconv.Itoa(index)
			contents, err := json.Marshal(candidate)
			if err != nil {
				return err
			}
			if _, err := statement.ExecContext(ctx, index, string(contents)); err != nil {
				return err
			}
		}
		return statement.Close()
	}))
	before, err := repository.List(ctx, audit.Query{Limit: 1})
	require.NoError(t, err)
	require.NotNil(t, before.NextCursor)
	assert.False(t, before.History.Pruned)
	assert.Equal(t, "1", before.History.OldestRetained.Sequence)

	injected := errors.New("roll back after pruning")
	err = store.Mutate(ctx, func(tx *sql.Tx) error {
		if _, err := audit.AppendTx(ctx, tx, event(contract.AuditRetention+1)); err != nil {
			return err
		}
		return injected
	})
	require.ErrorIs(t, err, injected)
	rolledBack, err := repository.List(ctx, audit.Query{Limit: 1, Cursor: *before.NextCursor})
	require.NoError(t, err)
	assert.Equal(t, before.History, rolledBack.History)
	_, err = repository.Read(ctx, before.History.OldestRetained.ID, before.History.Generation)
	require.NoError(t, err)

	_, err = repository.Append(ctx, event(contract.AuditRetention+1))
	require.NoError(t, err)
	_, err = repository.List(ctx, audit.Query{Limit: 1, Cursor: *before.NextCursor})
	assert.ErrorIs(t, err, audit.ErrStaleCursor)
	_, err = repository.Read(ctx, before.History.OldestRetained.ID, before.History.Generation)
	assert.ErrorIs(t, err, audit.ErrNotFound)
	after, err := repository.List(ctx, audit.Query{Limit: 1, Generation: before.History.Generation})
	require.NoError(t, err)
	assert.True(t, after.History.Pruned)
	assert.Equal(t, before.History.Generation, after.History.Generation)
	assert.Equal(t, "2", after.History.OldestRetained.Sequence)
	require.NoError(t, store.View(ctx, func(tx *sql.Tx) error {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM control_audit_events`).Scan(&count); err != nil {
			return err
		}
		assert.Equal(t, contract.AuditRetention, count)
		return audit.ValidateTx(ctx, tx)
	}))
}
