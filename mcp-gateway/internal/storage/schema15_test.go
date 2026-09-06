package storage

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestControlAuditMigrationPreservesPredecessorFacts(t *testing.T) {
	ctx := t.Context()
	ownership := newOwnership(t)
	writePopulatedSchemaNineFixture(t, ctx, ownership)
	fixture, err := openConfigured(ctx, ownership.Layout(), testOptions{})
	require.NoError(t, err)
	require.NoError(t, fixture.migrateThrough(ctx, 9, 14))
	before, err := fixture.Identity(ctx)
	require.NoError(t, err)
	require.NoError(t, fixture.Checkpoint(ctx))
	require.NoError(t, fixture.Close())
	store, err := Open(ctx, ownership)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	after, err := store.Identity(ctx)
	require.NoError(t, err)
	assert.Equal(t, 15, after.SchemaVersion)
	assert.Equal(t, before.InstallationID, after.InstallationID)
	assert.Equal(t, before.Revision, after.Revision)
	assertPopulatedSchemaNineFacts(t, ctx, store.database)
	var generation string
	var count, pruned int
	require.NoError(t, store.database.QueryRowContext(ctx, `SELECT generation, pruned, (SELECT count(*) FROM control_audit_events) FROM control_audit_history WHERE singleton = 1`).Scan(&generation, &pruned, &count))
	assert.Len(t, generation, 64)
	assert.Zero(t, pruned)
	assert.Equal(t, 2, count)
	reader, err := audit.NewRepository(store)
	require.NoError(t, err)
	page, err := reader.List(ctx, audit.Query{Limit: 100, Filters: contract.AuditFilters{Category: "storage", Action: "migrate"}})
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	assert.Equal(t, contract.AuditSystem, page.Items[0].Actor.Type)
	assert.Equal(t, "succeeded", page.Items[0].Outcome)
	assert.Equal(t, "pending", page.Items[1].Outcome)
	assert.Equal(t, page.Items[0].CorrelationID, page.Items[1].CorrelationID)
}

func TestControlAuditStartupRejectsMalformedHistory(t *testing.T) {
	for _, fault := range []string{"missing trigger", "malformed event", "missing history"} {
		t.Run(fault, func(t *testing.T) {
			ctx := t.Context()
			ownership := newOwnership(t)
			store, err := Initialize(ctx, ownership, testInstallationID)
			require.NoError(t, err)
			require.NoError(t, store.Close())
			raw := openRaw(t, ownership.Layout().Database)
			switch fault {
			case "missing trigger":
				_, err = raw.ExecContext(ctx, `DROP TRIGGER control_audit_immutable`)
			case "missing history":
				_, err = raw.ExecContext(ctx, `DELETE FROM control_audit_history`)
			case "malformed event":
				contents, encodeErr := json.Marshal(contract.AuditEvent{AuditSummary: contract.AuditSummary{
					ID: testInstallationID, Sequence: "3", Timestamp: time.Now().UTC().Add(time.Minute).Format(contract.AuditTimestampLayout), Category: "storage", Action: "verify",
					Phase: "outcome", Outcome: "succeeded", Actor: contract.AuditActor{Type: contract.AuditSystem}, CorrelationID: testInstallationID,
					Target: contract.AuditTarget{Type: "installation", ID: testInstallationID},
				}})
				require.NoError(t, encodeErr)
				_, err = raw.ExecContext(ctx, `INSERT INTO control_audit_events (insertion_sequence, event) VALUES (3, json_set(?, '$.unrestricted', 'canary'))`, string(contents))
			}
			require.NoError(t, err)
			require.NoError(t, raw.Close())
			_, err = Open(ctx, ownership)
			assert.ErrorIs(t, err, ErrInvalidDatabase)
		})
	}
}
