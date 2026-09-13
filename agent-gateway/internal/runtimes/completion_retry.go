package runtimes

import (
	"errors"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
)

var errCompletionDisplaced = errors.New("reconciliation completion displaced")

// persistCompletionLocked returns with the lifecycle lock held. Only refused
// acquisition may be repeated: it ran no transaction, audit append, or external
// work. The prepared outcome and worker occupancy survive each unlocked wait.
func (manager *Manager) persistCompletionLocked(serverID string, original entry, persist func() error) error {
	var err error
	delay := contract.ReconciliationCompletionDelay
	for attempt := 0; attempt < contract.ReconciliationCompletionAttempts; attempt++ {
		current := manager.entries[serverID]
		if manager.draining || current == nil || current.generation != original.generation || current.work != original.work {
			return errCompletionDisplaced
		}
		err = persist()
		if !errors.Is(err, storage.ErrMutationBusy) || errors.Is(err, storage.ErrStorageLatched) || attempt == contract.ReconciliationCompletionAttempts-1 {
			break
		}
		ready := make(chan struct{})
		timer := manager.scheduler.AfterFunc(delay, func() { close(ready) })
		manager.mu.Unlock()
		select {
		case <-ready:
		case <-manager.ctx.Done():
		}
		timer.Stop()
		manager.mu.Lock()
		delay *= 2
	}
	if original.work != nil {
		original.work.settled, original.work.failed = err == nil, err != nil
		if errors.Is(err, storage.ErrMutationBusy) && !errors.Is(err, storage.ErrStorageLatched) {
			original.work.failureCause = diagnostics.Capacity
		}
	}
	return err
}
