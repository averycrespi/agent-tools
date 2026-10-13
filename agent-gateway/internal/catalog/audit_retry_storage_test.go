package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/runtimes"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestCatalogAuditAdmissionDoesNotRepeatUnknownCommit(t *testing.T) {
	// Fault evidence uses real initialization, never the ordinary fixture image.
	ctx := audit.WithSystem(t.Context())
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ownership.Close()) })
	store, err := storage.Initialize(ctx, ownership, catalogInstallationID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	scheduler := newCatalogScheduler()
	candidate := runtimes.Candidate{Server: servers.Server{ID: catalogInstallationID}}
	coordinator := &Coordinator{repository: &Repository{store: store}, scheduler: scheduler,
		current: func(runtimes.Candidate) bool { return true }, work: make(map[string]*refreshWork), candidates: make(map[string]runtimes.Candidate)}
	_, _, work := installCatalogAuditWork(t, catalogAuditFixture{coordinator: coordinator, candidate: candidate})
	event, err := audit.NewAttempt(ctx, catalogTime, "catalog", "refresh", contract.AuditTarget{Type: "server", ID: candidate.Server.ID})
	require.NoError(t, err)
	var baseline int
	require.NoError(t, store.View(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM control_audit_events`).Scan(&baseline)
	}))
	calls := 0
	err = coordinator.persistAudit(ctx, ctx, work, false, func(writeCtx context.Context) error {
		calls++
		return store.Mutate(writeCtx, func(tx *sql.Tx) error {
			if _, err := audit.AppendTx(writeCtx, tx, event); err != nil {
				return err
			}
			// Remove only this disposable fixture's armed marker. SQL commits,
			// but marker cleanup then fails and acknowledgment must remain unknown.
			return os.Remove(ownership.Layout().MutationMarker)
		})
	})
	require.ErrorIs(t, err, storage.ErrStorageLatched)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.True(t, store.Latched())
	require.Equal(t, 1, calls)
	require.Zero(t, scheduler.count())
	reader, err := audit.NewRepository(store)
	require.NoError(t, err)
	_, err = reader.List(ctx, audit.Query{Limit: 100})
	require.ErrorIs(t, err, audit.ErrUnavailable)
	// Inspect committed fixture facts directly; the public audit reader must
	// continue refusing the latched installation, not turn uncertainty into success.
	var count int
	var encoded []byte
	require.NoError(t, store.View(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM control_audit_events`).Scan(&count); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `SELECT event FROM control_audit_events WHERE id = ?`, event.ID).Scan(&encoded)
	}))
	require.Equal(t, baseline+1, count)
	var persisted contract.AuditEvent
	require.NoError(t, json.Unmarshal(encoded, &persisted))
	require.Equal(t, event.ID, persisted.ID)
	// A latched store also blocks acquisition before invoking another callback.
	err = coordinator.persistAudit(ctx, ctx, work, false, func(context.Context) error { calls++; return storage.ErrMutationBusy })
	require.ErrorIs(t, err, storage.ErrStorageLatched)
	require.Equal(t, 1, calls)
	require.Zero(t, scheduler.count())
}
