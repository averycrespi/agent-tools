package servers

import (
	"database/sql"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestDisplacedSettlementIsAtomicAndPreservesTerminalWinner(t *testing.T) {
	for _, terminalWinner := range []bool{false, true} {
		name := "running"
		if terminalWinner {
			name = "terminal winner"
		}
		t.Run(name, func(t *testing.T) {
			repository, store, _ := newRepository(t, new(sequenceReader))
			server := mustCreateServer(t, repository, "settlement", false)
			ctx := audit.WithSystem(t.Context())
			request := OperationRequest{ServerID: server.ID, Kind: contract.OperationActivate, ExpectedDesiredRevision: server.DesiredRevision, Idempotency: idempotency("settlement", "settlement", "")}
			created, err := repository.CreateOperation(ctx, request)
			require.NoError(t, err)
			before, err := repository.TransitionOperation(ctx, created.Operation.ID, contract.OperationRunning, nil)
			require.NoError(t, err)
			if terminalWinner {
				before, err = repository.TransitionOperation(ctx, before.ID, contract.OperationFailed, ptrReason(contract.ReasonConnectivity))
				require.NoError(t, err)
			}
			attempt, err := audit.NewAttempt(ctx, testTime, "server", "reconcile", contract.AuditTarget{Type: "server", ID: server.ID})
			require.NoError(t, err)
			require.NoError(t, repository.RecordReconciliation(ctx, attempt))
			outcome, err := audit.Outcome(attempt, testTime, "failed")
			require.NoError(t, err)
			outcome.Detail.Reason = ptrReason(contract.ReasonSuperseded)
			reader, err := audit.NewRepository(store)
			require.NoError(t, err)
			query := audit.Query{Limit: 100, Filters: contract.AuditFilters{CorrelationID: attempt.CorrelationID}}
			initial, err := reader.List(ctx, query)
			require.NoError(t, err)
			require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, `CREATE TRIGGER refuse_settlement BEFORE INSERT ON control_audit_events WHEN NEW.category = 'server' AND json_extract(NEW.event, '$.action') = 'reconcile' AND json_extract(NEW.event, '$.phase') = 'outcome' BEGIN SELECT RAISE(ABORT, 'refused settlement'); END`)
				return err
			}))
			_, changed, err := repository.SettleDisplacedReconciliation(ctx, before.ID, &outcome)
			require.Error(t, err)
			require.False(t, changed)
			unchanged, err := repository.GetOperation(ctx, before.ID)
			require.NoError(t, err)
			require.Equal(t, before, unchanged)
			afterFailure, err := reader.List(ctx, query)
			require.NoError(t, err)
			require.Equal(t, initial, afterFailure)
			require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error { _, err := tx.ExecContext(ctx, `DROP TRIGGER refuse_settlement`); return err }))
			settled, changed, err := repository.SettleDisplacedReconciliation(ctx, before.ID, &outcome)
			require.NoError(t, err)
			require.Equal(t, !terminalWinner, changed)
			if terminalWinner {
				require.Equal(t, before, settled)
			} else {
				require.Equal(t, contract.OperationSuperseded, settled.State)
				require.Equal(t, before.StartedAt, settled.StartedAt)
				require.Equal(t, before.Cause, settled.Cause)
				require.Equal(t, before.ID, settled.ID)
			}
			replayed, err := repository.CreateOperation(ctx, request)
			require.NoError(t, err)
			require.True(t, replayed.Replayed)
			// Safe idempotency snapshots deliberately omit private audit attribution.
			snapshot := settled
			snapshot.Cause = audit.Cause{}
			require.Equal(t, snapshot, replayed.Operation)
			after, err := reader.List(ctx, query)
			require.NoError(t, err)
			require.Len(t, after.Items, len(initial.Items)+1)
			again, changed, err := repository.SettleDisplacedReconciliation(ctx, before.ID, nil)
			require.NoError(t, err)
			require.False(t, changed)
			require.Equal(t, settled, again)
		})
	}
}
