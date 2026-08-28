//go:build stress

package composition

import "testing"

func TestS5StressLocalInvocationDrainCleanup(t *testing.T) {
	TestS5DrainWaitsForDetachedLocalCallThroughTerminalAnnotation(t)
}
