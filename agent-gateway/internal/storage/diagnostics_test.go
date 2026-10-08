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

	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
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
func TestStorageDiagnosticsDistinguishBusyAndAcquire(t *testing.T) {
	var output bytes.Buffer
	adapter := diagnostics.New(&output, diagnostics.Debug)
	store, err := Initialize(t.Context(), newOwnership(t), testInstallationID)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()); adapter.Finish(nil); <-adapter.Done() }()
	store.SetDiagnostics(adapter)
	require.NoError(t, store.acquireMutation(t.Context()))
	ctx := diagnostics.WithCall(t.Context(), 17)
	require.ErrorIs(t, store.Mutate(ctx, func(*sql.Tx) error { t.Error("busy callback executed"); return nil }), ErrMutationBusy)
	waitMutationOccupancy(t, store, true, 0)
	store.releaseMutation()
	require.NoError(t, store.Mutate(ctx, func(*sql.Tx) error { return nil }))
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
	}
	require.False(t, seen["storage_wait/"])
	require.True(t, seen["storage_reject/capacity"])
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
					return errors.New("device permission denied Authorization: Bearer actual-storage-secret https://storage.example/?key=actual-query-secret\nforged-event")
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
			require.Contains(t, output.String(), "device permission denied")
			require.Contains(t, output.String(), "storage.example")
			wantResource := store.path
			if len(wantResource) > 160 {
				wantResource = wantResource[:160-len("...[truncated]")] + "...[truncated]"
			}
			require.Equal(t, wantResource, got[0]["resource"])
			require.NotContains(t, output.String(), "actual-storage-secret")
			require.NotContains(t, output.String(), "actual-query-secret")
			require.NotContains(t, output.String(), "\nforged-event")
		})
	}
}

type admissionFormattingError struct {
	store                  *Store
	called, underAdmission bool
}

func (e *admissionFormattingError) Error() string {
	owned, _ := e.store.MutationOccupancy()
	e.called = true
	e.underAdmission = e.underAdmission || owned
	return "native storage device refused write"
}

func TestStorageFailureFormattingAfterAdmissionRelease(t *testing.T) {
	for _, point := range []FaultPoint{FaultArmWrite, FaultAfterCommit, FaultDisarmDelete} {
		var armed bool
		cause := &admissionFormattingError{}
		store, err := InitializeWithFaultInjection(t.Context(), newOwnership(t), testInstallationID, func(p FaultPoint) error {
			if armed && p == point {
				return cause
			}
			return nil
		})
		require.NoError(t, err)
		cause.store = store
		var output bytes.Buffer
		adapter := diagnostics.New(&output, diagnostics.Warn)
		store.SetDiagnostics(adapter)
		armed = true
		err = store.Mutate(t.Context(), func(*sql.Tx) error { return nil })
		require.ErrorIs(t, err, ErrStorageLatched)
		require.True(t, cause.called)
		require.False(t, cause.underAdmission)
		require.True(t, adapter.Finish(nil))
		require.Contains(t, output.String(), "native storage device refused write")
		require.NoError(t, store.Close())
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
	require.NoError(t, store.observedAcquire(ctx))
	store.observedRelease(ctx, time.Time{})
	require.True(t, adapter.Finish(nil))
	require.Empty(t, output.String())
	require.EqualValues(t, 1, store.diagnosticIDs.Load(), "cheap correlation remains available to warning/error events")
}
