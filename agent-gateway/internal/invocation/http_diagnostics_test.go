package invocation

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func invalidHTTPAdmission(n int) contract.HTTPTrafficAdmission {
	a := httpTrafficAdmission(n)
	a.Class = "invalid_request"
	a.Default = ""
	a.Target = nil
	a.Decision = nil
	return a
}

func TestTrafficHTTPDiagnosticsBoundedAndLegacyReadable(t *testing.T) {
	s, owner := trafficFixture(t, nil, nil)
	legacy := invalidHTTPAdmission(1)
	modern := invalidHTTPAdmission(2)
	modern.Rejection = &contract.HTTPRejection{Stage: "target", Reason: "invalid_request_target"}
	modern.Connect = &contract.HTTPConnectContext{ID: invocationID(3), Host: "example.com", Port: 443}
	for _, a := range []contract.HTTPTrafficAdmission{legacy, modern} {
		receipt, err := s.AdmitHTTP(t.Context(), a)
		require.NoError(t, err)
		s.Release(receipt)
	}
	// Optional fields must not rewrite historical canonical JSON or charges.
	encoded, err := encodeHTTPAdmission(legacy)
	require.NoError(t, err)
	require.NotContains(t, encoded, "rejection")
	require.NotContains(t, encoded, "connect")
	for _, mutate := range []func(*contract.HTTPTrafficAdmission){
		func(a *contract.HTTPTrafficAdmission) {
			a.Rejection = &contract.HTTPRejection{Stage: "headers", Reason: "invalid_request_target"}
		},
		func(a *contract.HTTPTrafficAdmission) {
			a.Rejection = &contract.HTTPRejection{Stage: "target", Reason: strings.Repeat("secret", 1000)}
		},
		func(a *contract.HTTPTrafficAdmission) {
			a.Connect = &contract.HTTPConnectContext{ID: invocationID(3), Host: "example.com/path-secret", Port: 443}
		},
		func(a *contract.HTTPTrafficAdmission) {
			a.Connect = &contract.HTTPConnectContext{ID: a.ID, Host: "example.com", Port: 443}
		},
		func(a *contract.HTTPTrafficAdmission) { a.Class = "evaluated" },
	} {
		a := modern
		mutate(&a)
		_, err := s.AdmitHTTP(t.Context(), a)
		require.ErrorIs(t, err, ErrInvalidInput)
	}
	require.NoError(t, s.Close())
	reopened, err := OpenTraffic(t.Context(), owner, invocationTestInstallationID, invocationID(90), s.config)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	history, err := reopened.HTTPHistory(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, history.Records, 2)
	require.Equal(t, legacy, history.Records[0].Admission)
	require.Equal(t, modern, history.Records[1].Admission)
	require.Nil(t, history.Records[0].Completion)
	require.Nil(t, history.Records[1].Completion)
	raw, err := json.Marshal(history)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "secret")
}

func TestTrafficHTTPResponseProvenance(t *testing.T) {
	a := httpTrafficAdmission(1)
	c := httpTrafficCompletion()
	c.ResponseSource = "upstream"
	_, err := encodeHTTPCompletion(a, c)
	require.NoError(t, err)
	c.ResponseSource = "gateway"
	_, err = encodeHTTPCompletion(a, c)
	require.ErrorIs(t, err, ErrInvalidInput)
	c.Status = 0
	c.GatewayStatus = 502
	c.Outcome = "outcome_unknown"
	_, err = encodeHTTPCompletion(a, c)
	require.NoError(t, err)
	c.GatewayStatus = 999
	_, err = encodeHTTPCompletion(a, c)
	require.ErrorIs(t, err, ErrInvalidInput)
	c.GatewayStatus = 502
	c.ResponseSource = "response-secret"
	_, err = encodeHTTPCompletion(a, c)
	require.ErrorIs(t, err, ErrInvalidInput)
}
