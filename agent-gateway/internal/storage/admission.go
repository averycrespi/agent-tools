package storage

import (
	"context"
	"sync"
)

type mutationAdmission struct {
	mu    sync.Mutex
	owned bool
}

// MutationOccupancy reports the sole actual control writer. Control mutations
// never queue; optional traffic persistence has its own bounded writer.
func (store *Store) MutationOccupancy() (owned bool, waiting int) {
	store.mutations.mu.Lock()
	defer store.mutations.mu.Unlock()
	return store.mutations.owned, 0
}

func (store *Store) acquireMutation(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mutations.mu.Lock()
	defer store.mutations.mu.Unlock()
	if store.mutations.owned {
		return ErrMutationBusy
	}
	store.mutations.owned = true
	return nil
}
func (store *Store) releaseMutation() {
	store.mutations.mu.Lock()
	store.mutations.owned = false
	store.mutations.mu.Unlock()
}
