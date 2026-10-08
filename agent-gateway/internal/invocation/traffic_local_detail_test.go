package invocation

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/stretchr/testify/require"
)

type trafficUnlockedError struct {
	t     *testing.T
	store *TrafficStore
}

func (e *trafficUnlockedError) Error() string {
	if e.store.mu.TryLock() {
		e.store.mu.Unlock()
	} else {
		e.t.Error("formatted while holding traffic state lock")
	}
	if e.store.writerGate.TryLock() {
		e.store.writerGate.Unlock()
	} else {
		e.t.Error("formatted while holding traffic writer lock")
	}
	return "benign traffic device: native I/O refused"
}

func TestTrafficLocalCauseFormattedAfterWriterUnlock(t *testing.T) {
	var store *TrafficStore
	store, _ = trafficFixture(t, nil, func(stage string) error {
		if stage == "statement" {
			return &trafficUnlockedError{t: t, store: store}
		}
		return nil
	})
	var output bytes.Buffer
	observer := diagnostics.New(&output, diagnostics.Warn)
	store.SetTrafficDiagnostics(observer)
	require.NotNil(t, store.ObserveMCP(trafficPrepared(1)))
	require.Eventually(t, func() bool { return observer.Status().Accepted > 0 }, 5*time.Second, time.Millisecond)
	require.True(t, observer.Finish(nil))
	require.Contains(t, output.String(), "native I/O refused")
	var record map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &record))
	wantResource := store.path
	// The final encoder reserves space for the truncation marker.
	if len(wantResource) > 160-len("...[truncated]") {
		wantResource = wantResource[:160-len("...[truncated]")] + "...[truncated]"
	}
	require.Equal(t, wantResource, record["resource"])
	require.Contains(t, output.String(), `"settlement":"rolled_back"`)
	require.Equal(t, "unknown", store.Status(t.Context()).Incident.Cause)
}
