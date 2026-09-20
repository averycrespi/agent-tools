package contract

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFailureDiagnosticClosedContract(t *testing.T) {
	valid := `{"version":1,"category":"rate_limit","phase":"response_status","http_status":429,"retry_after_seconds":12}`
	diagnostic := ParseServerFailureDiagnostic([]byte(valid))
	require.NotNil(t, diagnostic)
	require.Equal(t, 429, *diagnostic.HTTPStatus)
	require.Equal(t, 12, *diagnostic.RetryAfterSeconds)
	for _, raw := range []string{
		`null`, `{}`, `[]`, strings.Replace(valid, `"version":1`, `"version":2`, 1),
		strings.Replace(valid, `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(valid, `"version"`, `"Version"`, 1),
		strings.Replace(valid, `"rate_limit"`, `"SECRET-CANARY"`, 1),
		strings.Replace(valid, `"response_status"`, `"SECRET-CANARY"`, 1),
		strings.Replace(valid, `429`, `null`, 1), strings.Replace(valid, `429`, `600`, 1),
		strings.Replace(valid, `12`, `86401`, 1), strings.Replace(valid, `12`, `-1`, 1),
		strings.Replace(valid, `12`, `1.5`, 1), strings.Replace(valid, `12`, `"12"`, 1),
		strings.Replace(valid, `12`, `12,"message":"SECRET-CANARY"`, 1), valid + `{}`,
		strings.Repeat(" ", FailureDiagnosticMaxBytes) + valid,
	} {
		require.Nil(t, ParseServerFailureDiagnostic([]byte(raw)), raw)
	}
	d := &FailureDiagnostics{GatewayObserved: FailureObservation{Source: "tool", Reason: "reported_error"}, ServerReported: diagnostic}
	raw, err := json.Marshal(d)
	require.NoError(t, err)
	parsed, err := ParseFailureDiagnostics(raw, TerminalDownstreamFailure)
	require.NoError(t, err)
	require.Equal(t, d, parsed)
	require.False(t, d.ValidFor(TerminalSucceeded))
	require.False(t, d.ValidFor(TerminalOutcomeUnknown))
	for _, bad := range []string{`null`, `{"gateway_observed":null}`, `{"gateway_observed":{"source":"tool","reason":"SECRET-CANARY"}}`, strings.Replace(string(raw), `"server_reported":{`, `"server_reported":{"raw":"SECRET-CANARY",`, 1)} {
		_, err := ParseFailureDiagnostics([]byte(bad), TerminalDownstreamFailure)
		require.Error(t, err)
		require.NotContains(t, err.Error(), "SECRET-CANARY")
	}
}
