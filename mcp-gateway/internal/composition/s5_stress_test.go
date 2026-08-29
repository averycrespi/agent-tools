//go:build stress

package composition

import "testing"

func TestS5StressLocalInvocationDrainCleanup(t *testing.T) {
	TestDrainWaitsForDetachedLocalCallThroughTerminalAnnotation(t)
}
