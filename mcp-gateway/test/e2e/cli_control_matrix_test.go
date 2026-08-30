//go:build e2e

package e2e

import "testing"

func TestCLIControlInputMatrix(t *testing.T) {
	t.Run("servers", runCLIServerInputMatrix)
	t.Run("principals", runCLIPrincipalInputMatrix)
	t.Run("operations", runCLIServerOperationInputMatrix)
	t.Run("grants", runCLIGrantInputMatrix)
	t.Run("grant requests", runCLIGrantRequestInputMatrix)
}
