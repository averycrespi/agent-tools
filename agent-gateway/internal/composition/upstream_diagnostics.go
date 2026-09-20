package composition

import (
	"strconv"
	"sync"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
)

// Authenticated status reads may observe this projection. Identity keys never
// cross the observer boundary. Retaining up to the durable identity cap keeps
// correlation stable through OAuth/reconciliation handoffs, including deletion.
// The graph owns the map for its process-local lifetime; it has no global cache.
// Contention/capacity omit correlation rather than waiting or affecting work.
type diagnosticReferences struct {
	process string
	mu      sync.Mutex
	values  map[string]uint64
}

// Lookup never allocates: reads cannot consume correlation capacity.
func (references *diagnosticReferences) lookup(serverID string) *contract.DiagnosticCorrelation {
	if references == nil || references.process == "" || !references.mu.TryLock() {
		return nil
	}
	defer references.mu.Unlock()
	ref := references.values[serverID]
	if ref == 0 {
		return nil
	}
	return &contract.DiagnosticCorrelation{ProcessID: references.process, UpstreamRef: strconv.FormatUint(ref, 10)}
}

func (built *Composition) DiagnosticCorrelation(serverID string) *contract.DiagnosticCorrelation {
	return built.diagnosticReferences.lookup(serverID)
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
