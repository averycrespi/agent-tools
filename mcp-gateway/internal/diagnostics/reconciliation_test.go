package diagnostics

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReconciliationDiagnosticLevelsAndPrivacy(t *testing.T) {
	for _, level := range []Level{Warn, Info, Debug} {
		t.Run(map[Level]string{Warn: "warn", Info: "info", Debug: "debug"}[level], func(t *testing.T) {
			var output bytes.Buffer
			adapter := New(&output, level)
			adapter.Reconciliation(Facts{Event: ReconciliationDisplaced})
			adapter.Reconciliation(Facts{Event: ReconciliationSettlementFailure, Cause: Capacity})
			require.True(t, adapter.Finish(nil))
			got := records(t, output.Bytes())
			expected := 2
			if level == Warn {
				expected = 1
			}
			require.Len(t, got, expected)
			if level != Warn {
				require.Equal(t, "reconciliation_displaced", got[0]["event"])
				require.Equal(t, "INFO", got[0]["level"])
			}
			last := got[len(got)-1]
			require.Equal(t, "reconciliation_settlement_failure", last["event"])
			require.Equal(t, "WARN", last["level"])
			require.Equal(t, "capacity", last["cause"])
			require.Len(t, last, 6)
		})
	}
	for _, event := range []Event{ReconciliationDisplaced, ReconciliationSettlementFailure} {
		facts := validEventExample(event)
		facts.InvocationID = "01ARZ3NDEKTSV4RRFFQ69G5FA0"
		require.False(t, validFacts(facts))
		facts = validEventExample(event)
		facts.Call = 1
		require.False(t, validFacts(facts))
		facts = validEventExample(event)
		facts.Stage = TransactionCommit
		require.False(t, validFacts(facts))
	}
}
