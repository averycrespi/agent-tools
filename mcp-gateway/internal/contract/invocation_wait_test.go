package contract

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestInvocationMutationWaitContract(t *testing.T) {
	require.Equal(t, 31, InvocationMutationWaiters)
	require.Equal(t, 250*time.Millisecond, InvocationMutationWaitDeadline)
	limit, found := FixedLimitByName("invocation_mutation_waiters")
	require.True(t, found)
	require.EqualValues(t, InvocationMutationWaiters, limit.Maximum)
	require.True(t, limit.Allows(31))
	require.False(t, limit.Allows(32))
}
