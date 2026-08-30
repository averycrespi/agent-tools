//go:build stress

package composition

import "testing"

func TestLocalInvocationDrainCleanupStress(t *testing.T) {
	TestDrainWaitsForDetachedLocalCallThroughTerminalAnnotation(t)
}
