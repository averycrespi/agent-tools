package keyring

import (
	"errors"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/stretchr/testify/require"
)

func TestNativeFailureDetailPreservesCauseAndMasksMaterial(t *testing.T) {
	secret := "actual-native-secret-123456"
	original := errors.New("credential write refused: native status -25308; material=" + secret)
	err := classifyOperationError(original, secret)
	var capability *CapabilityError
	require.ErrorAs(t, err, &capability)
	require.NotContains(t, err.Error(), "-25308")
	detail := diagnostics.Snapshot("credentials", "replace", "inventory.example", err)
	require.Contains(t, detail.Explanation, "native status -25308")
	require.Contains(t, detail.Explanation, "credential write refused")
	require.NotContains(t, detail.Explanation, secret)
}
