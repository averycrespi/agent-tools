package composition

import (
	"fmt"
	"strconv"
	"sync"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestUpstreamDiagnosticReferenceLifetimeAndConcurrency(t *testing.T) {
	references := &diagnosticReferences{process: "0123456789abcdef0123456789abcdef"}
	require.Nil(t, references.lookup("private-server-identity-canary"))
	require.Empty(t, references.values, "reads must not allocate")
	first := references.reference("private-server-identity-canary")
	require.NotZero(t, first)
	correlation := references.lookup("private-server-identity-canary")
	require.Equal(t, references.process, correlation.ProcessID)
	require.Equal(t, strconv.FormatUint(first, 10), correlation.UpstreamRef)
	values := make(chan uint64, 64)
	var workers sync.WaitGroup
	for range 64 {
		workers.Add(1)
		go func() { defer workers.Done(); values <- references.reference("private-server-identity-canary") }()
	}
	workers.Wait()
	close(values)
	for value := range values {
		if value != 0 {
			require.Equal(t, first, value)
		}
	}
	other := references.reference("another-private-identity")
	require.NotZero(t, other)
	require.NotEqual(t, first, other)
	references.mu.Lock()
	require.Nil(t, references.lookup("private-server-identity-canary"))
	require.Zero(t, references.reference("private-server-identity-canary"), "projection contention omits correlation without waiting")
	references.mu.Unlock()
	require.Equal(t, first, references.reference("private-server-identity-canary"))
	for index := 2; index < contract.DiagnosticReferenceOwners; index++ {
		require.NotZero(t, references.reference(fmt.Sprint(index)))
	}
	require.Zero(t, references.reference("capacity-canary"))
	require.Len(t, references.values, contract.DiagnosticReferenceOwners)
	require.Equal(t, first, references.reference("private-server-identity-canary"), "capacity never evicts or remaps an existing owner")
	bound, ok := contract.FixedLimitByName("server_identities")
	require.True(t, ok)
	require.EqualValues(t, bound.Maximum, contract.DiagnosticReferenceOwners)
	nextGraph := &diagnosticReferences{process: "abcdef0123456789abcdef0123456789"}
	require.Nil(t, nextGraph.lookup("private-server-identity-canary"))
	require.NotEqual(t, first, nextGraph.reference("private-server-identity-canary"), "new owners never reuse process counters")
}
