package composition

import (
	"sync"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/diagnostics"
)

// This private projection is not a resource lookup API. Identity keys never
// cross the observer boundary. Retaining up to the durable identity cap keeps
// correlation stable through OAuth/reconciliation handoffs, including deletion.
// The graph owns the map for its process-local lifetime; it has no global cache.
// Contention/capacity omit correlation rather than waiting or affecting work.
type diagnosticReferences struct {
	mu     sync.Mutex
	values map[string]uint64
}

func (references *diagnosticReferences) reference(serverID string) uint64 {
	if serverID == "" || !references.mu.TryLock() {
		return 0
	}
	defer references.mu.Unlock()
	if ref := references.values[serverID]; ref != 0 {
		return ref
	}
	if len(references.values) >= contract.DiagnosticReferenceOwners {
		return 0
	}
	if references.values == nil {
		references.values = make(map[string]uint64)
	}
	ref := diagnostics.UpstreamReference()
	if ref != 0 {
		references.values[serverID] = ref
	}
	return ref
}
