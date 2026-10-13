package catalog

import (
	"context"
	"errors"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

func (coordinator *Coordinator) finishAudit(ctx context.Context, work *refreshWork, attempt contract.AuditEvent, outcome string) error {
	// Preserve Finish's first cancellation-resistant settlement chance and its
	// single deadline. Refused acquisitions reuse the prepared outcome, not the
	// traversal or a newly identified/timestamped event.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), contract.SQLiteBusyDeadline)
	defer cancel()
	event, err := audit.Outcome(attempt, coordinator.clock.Now(), outcome)
	if err != nil {
		return err
	}
	return coordinator.persistAudit(ctx, writeCtx, work, true, func(writeCtx context.Context) error {
		return audit.Append(writeCtx, coordinator.repository.store, event)
	})
}

func (coordinator *Coordinator) persistAudit(ctx, writeCtx context.Context, work *refreshWork, settlement bool, persist func(context.Context) error) error {
	var last error
	delay := contract.CatalogAuditAdmissionDelay
	for attempt := 0; attempt < contract.CatalogAuditAdmissionAttempts; attempt++ {
		if attempt > 0 || !settlement {
			if err := coordinator.auditAdmissionFence(ctx, writeCtx, work); err != nil {
				return errors.Join(last, err)
			}
		}
		last = persist(writeCtx)
		// Only the bare, pre-mutation admission refusal proves no callback or
		// transaction ran. Wrapped/joined failures are deliberately not retried.
		if last != storage.ErrMutationBusy || attempt+1 == contract.CatalogAuditAdmissionAttempts { //nolint:errorlint // Exact refusal is the safety boundary
			return last
		}
		if err := coordinator.auditAdmissionFence(ctx, writeCtx, work); err != nil {
			return errors.Join(last, err)
		}
		ready := make(chan struct{})
		timer := coordinator.scheduler.AfterFunc(delay, func() { close(ready) })
		select {
		case <-ready:
		case <-ctx.Done():
		case <-writeCtx.Done():
		}
		timer.Stop()
		delay *= 2
	}
	return last
}

func (coordinator *Coordinator) auditAdmissionFence(ctx, writeCtx context.Context, work *refreshWork) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := writeCtx.Err(); err != nil {
		return err
	}
	if coordinator.repository.store.Latched() {
		return storage.ErrStorageLatched
	}
	candidate := work.candidate
	coordinator.mu.Lock()
	current, exists := coordinator.candidates[candidate.Server.ID]
	live := !coordinator.stopped && coordinator.work[candidate.Server.ID] == work && exists && current.Key() == candidate.Key()
	coordinator.mu.Unlock()
	if !live || !coordinator.current(candidate) {
		return servers.ErrStaleRevision
	}
	return nil
}
