package storage

import (
	"context"
	"errors"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/diagnostics"
)

// SetDiagnostics binds the startup-owned observer before concurrent use.
func (store *Store) SetDiagnostics(observer diagnostics.StorageObserver) {
	store.diagnostics = observer
}

type mutationDiagnosticKey struct{}

func (store *Store) mutationContext(ctx context.Context) context.Context {
	if store.diagnostics == nil {
		return ctx
	}
	return context.WithValue(ctx, mutationDiagnosticKey{}, diagnostics.NextID(&store.diagnosticIDs))
}
func (store *Store) mutationEvent(ctx context.Context, event diagnostics.Event, cause diagnostics.Cause, stage diagnostics.Stage, duration time.Duration) {
	if store.diagnostics == nil {
		return
	}
	if event <= diagnostics.StorageReject && !store.diagnostics.DebugEnabled() {
		return
	}
	correlation := diagnostics.FromContext(ctx)
	mutation, _ := ctx.Value(mutationDiagnosticKey{}).(uint64)
	facts := diagnostics.Facts{Event: event, Cause: cause, Stage: stage, Call: correlation.Call, Mutation: mutation, Writer: correlation.Writer, Duration: duration}
	if event <= diagnostics.StorageReject {
		owned, waiting := store.MutationOccupancy()
		if owned {
			facts.Owned = 1
		}
		facts.Waiting = waiting
		facts.Limit = 31
	}
	store.diagnostics.Storage(facts)
}
func mutationCause(err error) diagnostics.Cause {
	switch {
	case err == nil:
		return diagnostics.Success
	case errors.Is(err, ErrMutationBusy), errors.Is(err, ErrMutationWaitFull):
		return diagnostics.Capacity
	case errors.Is(err, ErrMutationWaitExpired):
		return diagnostics.Expired
	case errors.Is(err, ErrMutationWaitStopped):
		return diagnostics.Stopped
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return diagnostics.Cancelled
	case errors.Is(err, ErrStorageLatched):
		return diagnostics.Latched
	default:
		return diagnostics.Unavailable
	}
}
func (store *Store) observedAcquire(ctx context.Context, stop <-chan struct{}, wait bool) error {
	if store.diagnostics == nil || !store.diagnostics.DebugEnabled() {
		return store.acquireMutation(ctx, stop, wait)
	}
	started := time.Now()
	err := store.acquireMutation(ctx, stop, wait)
	event := diagnostics.StorageAcquire
	if err != nil {
		event = diagnostics.StorageReject
	}
	store.mutationEvent(ctx, event, mutationCause(err), diagnostics.NoStage, time.Since(started))
	return err
}
func (store *Store) observedRelease(ctx context.Context, started time.Time) {
	store.releaseMutation()
	if !started.IsZero() {
		store.mutationEvent(ctx, diagnostics.StorageRelease, diagnostics.Success, diagnostics.NoStage, time.Since(started))
	}
}
func (store *Store) diagnosticStart() time.Time {
	if store.diagnostics != nil && store.diagnostics.DebugEnabled() {
		return time.Now()
	}
	return time.Time{}
}
