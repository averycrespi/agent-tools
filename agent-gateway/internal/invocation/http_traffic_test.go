package invocation

import (
	"encoding/json"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func httpTrafficAdmission(id int) contract.HTTPTrafficAdmission {
	p := trafficPrepared(id)
	principal := contract.HTTPRevisionRef{ID: p.admission.PrincipalID, Revision: 1}
	return contract.HTTPTrafficAdmission{ID: p.InvocationID, AdmittedAt: p.AdmittedAt, Principal: principal, AgentCredential: contract.HTTPRevisionRef{ID: p.admission.CredentialID, Revision: 1}, CredentialFingerprint: p.admission.CredentialFingerprint, Class: "evaluated", Default: contract.HTTPDefaultAllow, Target: &contract.HTTPTrafficTarget{Host: "example.com", Port: 443, Scheme: "https", Method: "GET"}, EvaluatedAt: p.admission.Authorization.EvaluatedAt, Decision: &contract.HTTPDecision{Version: 1, Principal: principal, PolicyRevision: 1, DefaultRevision: 1, Transport: contract.HTTPTransportRequest, Allowed: true, Reason: contract.HTTPReasonDefault}, Grants: []contract.HTTPTrafficGrant{}}
}
func httpTrafficCompletion() contract.HTTPTrafficCompletion {
	return contract.HTTPTrafficCompletion{CompletedAt: trafficCompletion().CompletedAt, Outcome: "succeeded", Status: 200, BytesSent: 12, BytesReceived: 32, DurationMS: 4}
}

func TestTrafficHTTPSharedRetentionAndRestart(t *testing.T) {
	s, owner := trafficFixture(t, func(c *TrafficConfig) { c.RetainedRecords = 2; c.BatchRecords = 1 }, nil)
	first := recordHTTP(t, s, httpTrafficAdmission(1))
	recordMCP(t, s, trafficPrepared(2))
	recordHTTP(t, s, httpTrafficAdmission(3))
	h, err := s.History(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, h.Records, 1)
	require.EqualValues(t, 1, h.Pruning)
	recordHTTPCompletion(t, s, first, httpTrafficCompletion())
	recordHTTPCompletion(t, s, first, httpTrafficCompletion())
	require.NoError(t, s.Close())
	reopened, err := OpenTraffic(t.Context(), owner, invocationTestInstallationID, invocationID(90), s.config)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	history, err := reopened.HTTPHistory(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, history.Records, 2)
	require.EqualValues(t, 4, history.HighWater)
	require.EqualValues(t, 2, history.Pruning)
	require.Nil(t, history.Records[0].Completion)
	require.NotNil(t, history.Records[1].Completion)
	require.Equal(t, "succeeded", history.Records[1].Completion.Outcome)
}
func TestTrafficHTTPRejectsUnsafeCaptureAndCopiesCallerFacts(t *testing.T) {
	s, _ := trafficFixture(t, nil, nil)
	for _, unsafe := range []string{"example.com/private-secret", "example.com?token=secret", "example.com\nsecret"} {
		a := httpTrafficAdmission(1)
		a.Target.Host = unsafe
		require.Nil(t, s.ObserveHTTP(a))
	}
	a := httpTrafficAdmission(1)
	require.NotNil(t, s.ObserveHTTP(a))
	a.Target.Host = "mutated.example.com"
	a.Decision.Allowed = false
	waitTraffic(t, s)
	history, err := s.HTTPHistory(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, history.Records, 1)
	require.Equal(t, "example.com", history.Records[0].Admission.Target.Host)
	require.True(t, history.Records[0].Admission.Decision.Allowed)
	raw, err := json.Marshal(history)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "private-secret")
	require.NotContains(t, string(raw), "mutated")
}
