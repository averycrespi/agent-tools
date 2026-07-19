package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDoctorV1ContractAndExitSemantics(t *testing.T) {
	report := doctorReport{Version: 1, Checks: []doctorCheck{
		{ID: "tools.git", Status: doctorOK, Summary: "Git is available", Details: []string{}, Recovery: ""},
		{ID: "config.runtime", Status: doctorWarning, Summary: "no repositories are registered", Details: []string{}, Recovery: "run wts repo add"},
	}}
	data, err := json.Marshal(report)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, float64(1), decoded["version"])
	checks := decoded["checks"].([]any)
	require.Equal(t, []any{}, checks[0].(map[string]any)["details"])
	require.Equal(t, "", checks[0].(map[string]any)["recovery"])
	require.False(t, doctorHasErrors(report))
	require.Contains(t, renderDoctorHuman(report), "warning config.runtime")

	report.Checks = append(report.Checks, doctorCheck{ID: "config.syntax", Status: doctorError, Summary: "configuration is invalid", Details: []string{}, Recovery: "run wts config edit"})
	require.True(t, doctorHasErrors(report))
}
