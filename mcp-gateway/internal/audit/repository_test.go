package audit_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const credentialID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

var eventBase = time.Now().UTC().Add(time.Hour)

func fixture(t *testing.T) (*storage.Store, *audit.Repository) {
	t.Helper()
	ownership, err := gatewaypaths.Acquire(filepath.Join(t.TempDir(), "gateway"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ownership.Close()) })
	store, err := storage.Initialize(t.Context(), ownership, credentialID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	repository, err := audit.NewRepository(store)
	require.NoError(t, err)
	return store, repository
}

func event(number int) contract.AuditEvent {
	return contract.AuditEvent{AuditSummary: contract.AuditSummary{
		ID: fmt.Sprintf("%026d", number), Timestamp: eventBase.Add(time.Duration(number) * time.Second).Format(contract.AuditTimestampLayout),
		Category: "grant", Action: "create", Phase: "outcome", Outcome: "succeeded",
		Actor:         contract.AuditActor{Type: contract.AuditOperator, Credential: &contract.AuditCredential{ID: credentialID, Fingerprint: "0123456789abcdef"}},
		CorrelationID: credentialID, Target: contract.AuditTarget{Type: "grant", ID: credentialID},
	}}
}

func TestAuditSettlementSurvivesEffectCancellation(t *testing.T) {
	store, repository := fixture(t)
	attempt := event(1)
	attempt.Phase, attempt.Outcome = "attempt", "pending"
	require.NoError(t, audit.Append(t.Context(), store, attempt))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.NoError(t, audit.Finish(ctx, store, attempt, time.Now(), "unknown"))
	assert.False(t, store.Latched())
	page, err := repository.List(t.Context(), audit.Query{Limit: 100, Filters: contract.AuditFilters{Category: "grant", CorrelationID: credentialID}})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	assert.Equal(t, "unknown", page.Items[0].Outcome)
	assert.Equal(t, page.Items[0].CorrelationID, page.Items[1].CorrelationID)
}

func TestAuditReplacementGenerationRollsBackWithoutEvidence(t *testing.T) {
	store, repository := fixture(t)
	ctx := t.Context()
	before, err := repository.List(ctx, audit.Query{Limit: 1})
	require.NoError(t, err)
	require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE TRIGGER reject_restore_audit BEFORE INSERT ON control_audit_events BEGIN SELECT RAISE(ABORT, 'injected'); END`)
		return err
	}))
	attempt, err := audit.NewAttempt(audit.WithOffline(ctx), time.Now(), "backup", "restore", contract.AuditTarget{Type: "backup", ID: credentialID})
	require.NoError(t, err)
	err = store.Mutate(ctx, func(tx *sql.Tx) error { return audit.ReplaceHistoryTx(ctx, tx, attempt) })
	require.Error(t, err)
	after, err := repository.List(ctx, audit.Query{Limit: 1})
	require.NoError(t, err)
	assert.Equal(t, before.History, after.History)
	assert.Equal(t, before.Items, after.Items)
}

func TestAuditAppendOrderingCorrelationAndAtomicFailure(t *testing.T) {
	store, repository := fixture(t)
	ctx := t.Context()
	attempt := event(1)
	attempt.Phase, attempt.Outcome = "attempt", "pending"
	first, err := repository.Append(ctx, attempt)
	require.NoError(t, err)
	outcome := event(2)
	outcome.Timestamp = "2025-01-01T00:00:00.000000000Z"
	outcome.Actor = contract.AuditActor{Type: contract.AuditSystem}
	outcome.Initiator = attempt.Actor.Credential
	second, err := repository.Append(ctx, outcome)
	require.NoError(t, err)
	assert.Equal(t, "3", first.Sequence)
	assert.Equal(t, "4", second.Sequence)
	assert.Equal(t, first.Timestamp, second.Timestamp)
	assert.Equal(t, first.CorrelationID, second.CorrelationID)
	assert.Equal(t, first.Actor.Credential, second.Initiator)
	assert.Nil(t, second.Actor.Credential)

	for _, statement := range []string{
		`UPDATE control_audit_events SET event = event`,
		`DELETE FROM control_audit_events`,
	} {
		err := store.Mutate(ctx, func(tx *sql.Tx) error { _, err := tx.ExecContext(ctx, statement); return err })
		require.Error(t, err)
	}
	require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE TRIGGER reject_audit BEFORE INSERT ON control_audit_events BEGIN SELECT RAISE(ABORT, 'injected'); END`)
		return err
	}))
	err = store.Mutate(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE gateway_meta SET revision = revision + 1`); err != nil {
			return err
		}
		_, err := audit.AppendTx(ctx, tx, event(3))
		return err
	})
	require.Error(t, err)
	identity, err := store.Identity(ctx)
	require.NoError(t, err)
	assert.Zero(t, identity.Revision)
	page, err := repository.List(ctx, audit.Query{Limit: 100, Filters: contract.AuditFilters{Category: "grant"}})
	require.NoError(t, err)
	assert.Len(t, page.Items, 2)
	assert.Equal(t, first.AuditSummary, page.Items[1])
}

func TestAuditAuthoritativeFiltersAndSnapshotCursor(t *testing.T) {
	_, repository := fixture(t)
	ctx := t.Context()
	for index := 1; index <= 3; index++ {
		_, err := repository.Append(ctx, event(index))
		require.NoError(t, err)
	}
	page, err := repository.List(ctx, audit.Query{Limit: 1, Filters: contract.AuditFilters{Category: "grant"}})
	require.NoError(t, err)
	require.NotNil(t, page.NextCursor)
	assert.Equal(t, "5", page.Items[0].Sequence)
	_, err = repository.Append(ctx, event(4))
	require.NoError(t, err)
	next, err := repository.List(ctx, audit.Query{Limit: 100, Cursor: *page.NextCursor, Generation: page.History.Generation, Filters: contract.AuditFilters{Category: "grant"}})
	require.NoError(t, err)
	require.Len(t, next.Items, 2)
	assert.Equal(t, "4", next.Items[0].Sequence)
	assert.Equal(t, "3", next.Items[1].Sequence)
	assert.Nil(t, next.NextCursor)

	filters := contract.AuditFilters{ActorType: contract.AuditOperator, CredentialID: credentialID, Category: "grant", Action: "create", TargetType: "grant", TargetID: credentialID, Outcome: "succeeded", CorrelationID: credentialID, From: event(1).Timestamp, Until: event(4).Timestamp}
	filtered, err := repository.List(ctx, audit.Query{Limit: 100, Filters: filters})
	require.NoError(t, err)
	assert.Len(t, filtered.Items, 3)
	for _, change := range []func(*contract.AuditFilters){
		func(f *contract.AuditFilters) { f.ActorType = contract.AuditSystem },
		func(f *contract.AuditFilters) { f.CredentialID = event(9).ID },
		func(f *contract.AuditFilters) { f.Category, f.Action = "principal", "create" },
		func(f *contract.AuditFilters) { f.Action = "delete" },
		func(f *contract.AuditFilters) { f.TargetType = "principal" },
		func(f *contract.AuditFilters) { f.TargetID = event(9).ID },
		func(f *contract.AuditFilters) { f.Outcome = "failed" },
		func(f *contract.AuditFilters) { f.CorrelationID = event(9).ID },
	} {
		miss := filters
		change(&miss)
		result, err := repository.List(ctx, audit.Query{Limit: 100, Filters: miss})
		require.NoError(t, err)
		assert.Empty(t, result.Items)
	}
	_, err = repository.List(ctx, audit.Query{Limit: 1, Cursor: *page.NextCursor, Filters: filters})
	assert.ErrorIs(t, err, audit.ErrInvalidCursor)
	_, err = repository.List(ctx, audit.Query{Limit: 1, Generation: strings.Repeat("0", 64)})
	assert.ErrorIs(t, err, audit.ErrHistoryReplaced)
	_, err = repository.Read(ctx, event(1).ID, strings.Repeat("0", 64))
	assert.ErrorIs(t, err, audit.ErrHistoryReplaced)
}

func TestAuditRejectsSecretsAndMalformedPersistedEvents(t *testing.T) {
	store, repository := fixture(t)
	ctx := t.Context()
	secret := "mgw_admin_private_canary"
	for _, change := range []func(*contract.AuditEvent){
		func(e *contract.AuditEvent) { e.ID = secret },
		func(e *contract.AuditEvent) { e.Actor.Credential.Fingerprint = secret },
		func(e *contract.AuditEvent) { e.Action = secret },
		func(e *contract.AuditEvent) { e.Detail.Problem = &secret },
		func(e *contract.AuditEvent) { reason := contract.PublicReason(secret); e.Detail.Reason = &reason },
		func(e *contract.AuditEvent) { e.CorrelationID = secret },
		func(e *contract.AuditEvent) { e.Target.ID = secret },
	} {
		candidate := event(1)
		change(&candidate)
		_, err := repository.Append(ctx, candidate)
		assert.ErrorIs(t, err, audit.ErrInvalidInput)
	}
	page, err := repository.List(ctx, audit.Query{Limit: 100, Filters: contract.AuditFilters{Category: "grant"}})
	require.NoError(t, err)
	assert.Empty(t, page.Items)
	_, err = repository.Append(ctx, event(1))
	require.NoError(t, err)
	require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DROP TRIGGER control_audit_immutable; UPDATE control_audit_events SET event = json_remove(event, '$.detail')`)
		return err
	}))
	_, err = repository.Read(ctx, event(1).ID, "")
	assert.ErrorIs(t, err, audit.ErrInvalidState)
	_, err = repository.List(ctx, audit.Query{Limit: 100})
	assert.ErrorIs(t, err, audit.ErrInvalidState)
	err = store.View(ctx, func(tx *sql.Tx) error { return audit.ValidateTx(ctx, tx) })
	assert.ErrorIs(t, err, audit.ErrInvalidState)
}

func TestAuditRollbackDoesNotConsumeSequence(t *testing.T) {
	store, repository := fixture(t)
	injected := errors.New("rollback")
	err := store.Mutate(t.Context(), func(tx *sql.Tx) error {
		_, err := audit.AppendTx(t.Context(), tx, event(1))
		if err != nil {
			return err
		}
		return injected
	})
	require.ErrorIs(t, err, injected)
	result, err := repository.Append(context.Background(), event(2))
	require.NoError(t, err)
	assert.Equal(t, "3", result.Sequence)
}
