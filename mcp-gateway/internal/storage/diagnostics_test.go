package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/diagnostics"
	"github.com/stretchr/testify/require"
)

func storageDiagnosticRecords(t *testing.T, output []byte) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range bytes.Split(bytes.TrimSpace(output), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var record map[string]any
		require.NoError(t, json.Unmarshal(line, &record))
		records = append(records, record)
	}
	return records
}
func TestStorageDiagnosticsDistinguishWaitExpiryAndAcquire(t *testing.T) {
	var output bytes.Buffer
	adapter := diagnostics.New(&output, diagnostics.Debug)
	store, err := Initialize(t.Context(), newOwnership(t), testInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()); adapter.Finish(nil); <-adapter.Done() }()
	store.SetDiagnostics(adapter)
	require.NoError(t, store.acquireMutation(t.Context(), nil, false))
	ctx := diagnostics.WithCall(t.Context(), 17)
	require.ErrorIs(t, store.MutateInvocation(ctx, nil, func(*sql.Tx) error { t.Error("expired callback executed"); return nil }), ErrMutationWaitExpired)
	done := make(chan error, 1)
	go func() { done <- store.MutateInvocation(ctx, nil, func(*sql.Tx) error { return nil }) }()
	waitMutationOccupancy(t, store, true, 1)
	store.releaseMutation()
	require.NoError(t, <-done)
	require.False(t, store.Latched())
	require.True(t, adapter.Finish(nil))
	seen := map[string]bool{}
	for _, record := range storageDiagnosticRecords(t, output.Bytes()) {
		event, _ := record["event"].(string)
		cause, _ := record["cause"].(string)
		seen[event+"/"+cause] = true
		require.EqualValues(t, 17, record["call_id"])
		require.NotContains(t, record, "invocation_id")
		require.Equal(t, "invocation_admission", record["writer_kind"])
		if event == "storage_reject" {
			require.GreaterOrEqual(t, record["duration_ms"].(float64), float64(250))
		}
	}
	require.True(t, seen["storage_wait/"])
	require.True(t, seen["storage_reject/expired"])
	require.True(t, seen["storage_acquire/success"])
	require.True(t, seen["storage_release/success"])
}
func TestStorageDiagnosticsClosedDurabilityStagesAndPrivacy(t *testing.T) {
	for _, test := range []struct {
		point FaultPoint
		stage string
	}{{FaultArmWrite, "intent_arm"}, {FaultAfterCommit, "transaction_commit"}, {FaultDisarmDelete, "intent_cleanup"}} {
		t.Run(test.stage, func(t *testing.T) {
			var armed atomic.Bool
			store, err := InitializeWithFaultInjection(t.Context(), newOwnership(t), testInstallationID, func(point FaultPoint) error {
				if armed.Load() && point == test.point {
					return errors.New("secret-token https://sensitive.example/?key=SECRET\nforged-event")
				}
				return nil
			})
			require.NoError(t, err)
			defer func() { require.NoError(t, store.Close()) }()
			var output bytes.Buffer
			adapter := diagnostics.New(&output, diagnostics.Warn)
			store.SetDiagnostics(adapter)
			armed.Store(true)
			err = store.Mutate(context.Background(), func(*sql.Tx) error { return nil })
			require.ErrorIs(t, err, ErrStorageLatched)
			require.True(t, adapter.Finish(nil))
			<-adapter.Done()
			got := storageDiagnosticRecords(t, output.Bytes())
			require.Len(t, got, 2)
			require.Equal(t, "durability_failure", got[0]["event"])
			require.Equal(t, "storage_latch", got[1]["event"])
			for _, record := range got {
				require.Equal(t, test.stage, record["stage"])
				require.Equal(t, "ERROR", record["level"])
			}
			require.NotContains(t, output.String(), "secret")
			require.NotContains(t, output.String(), "sensitive")
			require.NotContains(t, output.String(), "forged-event")
		})
	}
}
func TestStorageDiagnosticDisabledObserverAvoidsConstruction(t *testing.T) {
	var output bytes.Buffer
	adapter := diagnostics.New(&output, diagnostics.Warn)
	store := &Store{}
	store.SetDiagnostics(adapter)
	require.True(t, store.diagnosticStart().IsZero())
	ctx := t.Context()
	require.NotNil(t, store.mutationContext(ctx))
	require.NoError(t, store.observedAcquire(ctx, nil, false))
	store.observedRelease(ctx, time.Time{})
	require.True(t, adapter.Finish(nil))
	require.Empty(t, output.String())
	require.EqualValues(t, 1, store.diagnosticIDs.Load(), "cheap correlation remains available to warning/error events")
}
