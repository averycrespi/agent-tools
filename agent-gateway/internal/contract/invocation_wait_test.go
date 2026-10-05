package contract

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInvocationMutationQueueRetired(t *testing.T) {
	_, found := FixedLimitByName("invocation_mutation_waiters")
	require.False(t, found, "optional traffic recording has no control-writer FIFO")
}
