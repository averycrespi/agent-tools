package keyring

import (
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/stretchr/testify/require"
)

type admissionError struct{ check func() }

func (e admissionError) Error() string { e.check(); return "native credential access refused" }
func TestNativeFormattingAfterWorkRelease(t *testing.T) {
	namespace, err := NewNamespace(testInstallationID, testOwnerID, RecordStaticCredential)
	require.NoError(t, err)
	backend := &fakeAdapter{}
	provider, err := newProviderWithAdapter(testInstallationID, backend)
	require.NoError(t, err)
	calls := 0
	native := admissionError{check: func() {
		calls++
		release, err := provider.acquireWork()
		require.NoError(t, err, "formatter retains no keyring admission")
		release()
	}}
	backend.setErr, backend.getErr, backend.deleteErr = native, native, native
	for _, operation := range []func() error{func() error { return provider.set(t.Context(), namespace, "item", "private-value") }, func() error { _, err := provider.get(t.Context(), namespace, "item"); return err }, func() error { return provider.delete(t.Context(), namespace, "item") }} {
		err := operation()
		require.Error(t, err)
		require.Contains(t, diagnostics.Snapshot("test", "operation", "", err).Explanation, "native credential access refused")
	}
	require.Equal(t, 3, calls)
}
