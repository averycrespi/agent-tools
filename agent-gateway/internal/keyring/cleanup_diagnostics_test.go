package keyring

import (
	"bytes"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/require"
)

type cleanupUnlockedError struct {
	t           *testing.T
	coordinator *Coordinator
	secret      string
}

func (e *cleanupUnlockedError) Error() string {
	require.Zero(e.t, len(e.coordinator.operation), "cleanup cause formatted under cutover admission")
	return "retained generation deletion refused " + e.secret
}

func TestSuccessfulCutoverWarnsAfterAdmissionWhenCleanupFails(t *testing.T) {
	store, _ := newCutoverStore(t)
	backend := newMemoryAdapter()
	provider, err := newProviderWithAdapter(testInstallationID, backend)
	require.NoError(t, err)
	coordinator := NewCoordinator(provider, store, testutil.NewFakeClock(time.Now()), testutil.NewFakeEntropy(uniqueEntropy(2)))
	namespace, err := NewNamespace(testInstallationID, testOwnerID, RecordStaticCredential)
	require.NoError(t, err)
	first, err := coordinator.Replace(t.Context(), namespace, []byte("old-material"))
	require.NoError(t, err)
	var output bytes.Buffer
	observer := diagnostics.New(&output, diagnostics.Warn)
	t.Cleanup(func() { observer.Finish(nil); <-observer.Done() })
	coordinator.SetDiagnostics(observer)
	const secret = "actual-new-generation-canary"
	coordinator.hooks.afterCommit = func() error {
		backend.mu.Lock()
		backend.operationErr = &cleanupUnlockedError{t: t, coordinator: coordinator, secret: secret}
		backend.mu.Unlock()
		return nil
	}
	current, err := coordinator.Replace(t.Context(), namespace, []byte(secret))
	require.NoError(t, err)
	require.NotEqual(t, first.Handle, current.Handle)
	require.Equal(t, "2", current.Revision)
	require.True(t, observer.Finish(nil))
	require.Contains(t, output.String(), "publication acknowledged")
	require.Contains(t, output.String(), "cleanup=unconfirmed")
	require.Contains(t, output.String(), "retained generation deletion refused")
	require.NotContains(t, output.String(), secret)
	backend.mu.Lock()
	backend.operationErr = nil
	backend.mu.Unlock()
	loaded, active, err := coordinator.ReadActive(t.Context(), namespace)
	require.NoError(t, err)
	require.Equal(t, secret, string(loaded))
	require.Equal(t, current, active)
	status, err := coordinator.CandidateStatus(t.Context())
	require.NoError(t, err)
	require.EqualValues(t, 1, status.InUse)
}
