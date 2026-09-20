package contract

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const validationFixture = `{"version":2,"category":"response_contract","phase":"response_validation","validation":{"schema":"models_result","version":1,"violations":[{"code":"missing","path":"$.models.[].name","rule":"required"},{"code":"type","path":"$.models.[].description","rule":"type","expected":"string","observed":"number"}],"truncated":true}}`

func TestValidationDetailsClosedContract(t *testing.T) {
	d := ParseServerFailureDiagnostic([]byte(validationFixture))
	require.NotNil(t, d)
	require.Len(t, d.Validation.Violations, 2)
	require.True(t, d.Validation.Truncated)
	full := &FailureDiagnostics{GatewayObserved: FailureObservation{Source: "tool", Reason: "reported_error"}, ServerReported: d}
	raw, err := json.Marshal(full)
	require.NoError(t, err)
	require.LessOrEqual(t, len(raw), FailureDiagnosticMaxBytes)
	parsed, err := ParseFailureDiagnostics(raw, TerminalDownstreamFailure)
	require.NoError(t, err)
	require.Equal(t, full, parsed)
	for _, bad := range []string{
		strings.Replace(validationFixture, `"version":2`, `"version":1`, 1),
		strings.Replace(validationFixture, `"models_result"`, `"PRIVATE-CANARY"`, 1),
		strings.Replace(validationFixture, `$.models.[].name`, `$.models.0.name`, 1),
		strings.Replace(validationFixture, `$.models.[].name`, `$.PRIVATE-CANARY`, 1),
		strings.Replace(validationFixture, `"missing"`, `"PRIVATE-CANARY"`, 1),
		strings.Replace(validationFixture, `"required"`, `"PRIVATE-CANARY"`, 1),
		strings.Replace(validationFixture, `"expected":"string"`, `"expected":"PRIVATE-CANARY"`, 1),
		strings.Replace(validationFixture, `"schema":`, `"Schema":`, 1),
		strings.Replace(validationFixture, `"truncated":true`, `"truncated":null`, 1),
		strings.Replace(validationFixture, `,"truncated":true`, ``, 1),
		strings.Replace(validationFixture, `"rule":"required"`, `"rule":"required","message":"PRIVATE-CANARY"`, 1),
		strings.Replace(validationFixture, `"rule":"required"`, `"rule":"required","rule":"required"`, 1),
		strings.Replace(validationFixture, `"path":`, `"Path":`, 1),
		strings.Replace(validationFixture, `"code":"missing"`, `"code":"invalid_date"`, 1),
		strings.Replace(validationFixture, `"code":"missing"`, `"code":"missing","expected":""`, 1),
		strings.Repeat(" ", 512) + validationFixture,
	} {
		require.Nil(t, ParseServerFailureDiagnostic([]byte(bad)), bad)
	}
}

func TestReleaseDateExplanation(t *testing.T) {
	v := ValidationViolation{Code: "invalid_date", Path: "$.models.[].release_date", Rule: "date"}
	require.True(t, v.valid())
	require.Equal(t, "Invalid release date; expected YYYY-MM-DD or RFC 3339 timestamp", v.Explanation())
}

func TestValidationDetailsBounds(t *testing.T) {
	d := ParseServerFailureDiagnostic([]byte(validationFixture))
	require.NotNil(t, d)
	d.Validation.Violations = nil
	require.False(t, d.Valid())
	d.Validation.Violations = []ValidationViolation{}
	require.False(t, d.Valid())
	for _, path := range []string{"$", "$.models", "$.models.[]", "$.models.[].name"} {
		d.Validation.Violations = append(d.Validation.Violations, ValidationViolation{Code: "missing", Path: path, Rule: "required"})
	}
	require.False(t, d.Valid(), "four violations exceed count bound")
	d = ParseServerFailureDiagnostic([]byte(validationFixture))
	d.Validation.Violations[0], d.Validation.Violations[1] = d.Validation.Violations[1], d.Validation.Violations[0]
	require.False(t, d.Valid(), "order is canonical")
	d.Validation.Violations[0] = d.Validation.Violations[1]
	require.False(t, d.Valid(), "duplicates are rejected")
	d = ParseServerFailureDiagnostic([]byte(validationFixture))
	d.Validation.Schema = "evaluate_result"
	d.Validation.Violations = []ValidationViolation{}
	for _, path := range []string{"$.answers.*.confidence", "$.answers.*.legend.*", "$.answers.*.probabilities.*"} {
		d.Validation.Violations = append(d.Validation.Violations, ValidationViolation{Code: "type", Path: path, Rule: "type", Expected: "number", Observed: "string"})
	}
	require.True(t, d.Validation.valid())
	raw, err := json.Marshal(d)
	require.NoError(t, err)
	require.Greater(t, len(raw), ValidationDiagnosticMaxBytes)
	require.False(t, d.Valid(), "byte bound applies independently of count")
	require.Nil(t, ParseServerFailureDiagnostic(raw))
	deep := strings.Replace(validationFixture, `"truncated":true`, `"truncated":[[[[[[[true]]]]]]]`, 1)
	require.Nil(t, ParseServerFailureDiagnostic([]byte(deep)))
}
