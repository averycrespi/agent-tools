package main

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

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
