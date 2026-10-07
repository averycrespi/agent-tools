package main

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestStatusTrafficRecoveryPresentations(t *testing.T) {
	for _, health := range []string{"healthy", "degraded", "recovering", "operator_action_required", "recovered"} {
		t.Run(health, func(t *testing.T) {
			traffic := &contract.TrafficStatus{State: "ready", Health: health, LastAcknowledged: "2026-10-07T08:00:00Z", Ready: true}
			if health != "healthy" {
				traffic.Incident = &contract.TrafficIncident{FirstFailure: "2026-10-07T08:01:00Z", Cause: "locked", Stage: "begin", Settlement: "not_started", Recovery: health, RecoveryCause: "integrity", RecoveryStage: "validation", Affected: 3, Discarded: 3}
			}
			body, err := json.Marshal(contract.SystemStatus{Process: contract.ProcessStatus{Ready: true}, Traffic: traffic})
			require.NoError(t, err)
			table, err := statusTable(body)
			require.NoError(t, err)
			text := fmt.Sprint(table.Rows)
			require.Contains(t, text, health)
			require.Contains(t, text, "ready=true")
			require.NotContains(t, text, "test-access")
			if health != "healthy" {
				require.Contains(t, text, "discarded=3")
				require.Contains(t, text, "stage=begin")
			}
			if health == "recovered" {
				require.Contains(t, text, "not reconstructed")
			}
			if health == "operator_action_required" {
				require.Contains(t, text, "stopped recovery plan")
			}
		})
	}
}

func TestStatusSeparatesServingAndUnavailableEvidence(t *testing.T) {
	status := contract.SystemStatus{
		Process:     contract.ProcessStatus{State: contract.ProcessReady, Ready: true},
		Traffic:     &contract.TrafficStatus{State: "unavailable", DatabaseMeasurement: contract.ByteMeasurement{State: "unavailable"}, WALMeasurement: contract.ByteMeasurement{State: "absent"}, Delivery: contract.DeliveryCounters{Discarded: 42}},
		Diagnostics: &contract.DiagnosticDeliveryStatus{State: "failed", Dropped: 17, WriteFailures: 1},
	}
	body, err := json.Marshal(status)
	require.NoError(t, err)
	table, err := statusTable(body)
	require.NoError(t, err)
	text := fmt.Sprint(table.Rows)
	require.Contains(t, text, "ready=true")
	require.Contains(t, text, "database=unavailable WAL=absent")
	require.Contains(t, text, "discarded=42")
	require.Contains(t, text, "dropped=17")
	require.Contains(t, text, "last_successful_write=not observed")
	require.Contains(t, text, "no request replay")
}
