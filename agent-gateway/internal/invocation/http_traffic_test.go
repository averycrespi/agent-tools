package invocation

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
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
	first, err := s.AdmitHTTP(t.Context(), httpTrafficAdmission(1))
	require.NoError(t, err)
	require.True(t, s.Confirm(t.Context(), first))
	mcp, err := s.Admit(t.Context(), trafficPrepared(2))
	require.NoError(t, err)
	s.Release(mcp)
	last, err := s.AdmitHTTP(t.Context(), httpTrafficAdmission(3))
	require.NoError(t, err)
	s.Release(last)
	history, err := s.History(t.Context(), 0, 10)
	require.NoError(t, err)
	assert.Empty(t, history.Records, "shared retention must evict unpinned MCP before pinned HTTP")
	assert.Equal(t, int64(1), history.Pruning)
	require.NoError(t, s.CompleteHTTP(t.Context(), first, httpTrafficCompletion()))
	require.ErrorIs(t, s.CompleteHTTP(t.Context(), first, httpTrafficCompletion()), ErrInvalidInput)
	require.NoError(t, s.Close())
	reopened, err := OpenTraffic(t.Context(), owner, invocationTestInstallationID, invocationID(90), s.config)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	httpHistory, err := reopened.HTTPHistory(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, httpHistory.Records, 2)
	assert.Equal(t, int64(3), httpHistory.HighWater)
	assert.Equal(t, int64(1), httpHistory.Pruning)
	require.NotNil(t, httpHistory.Records[0].Completion)
	assert.Equal(t, "succeeded", httpHistory.Records[0].Completion.Outcome)
	assert.Nil(t, httpHistory.Records[1].Completion, "missing terminal must stay unknown")
	assert.False(t, reopened.Confirm(t.Context(), first), "restart must not reconstruct dispatch authority")
}

func TestTrafficHTTPRejectsUnsafeEvidenceAndPreservesOriginalCancellation(t *testing.T) {
	s, _ := trafficFixture(t, nil, nil)
	for _, unsafe := range []string{"example.com/private-secret", "example.com?token=secret", "example.com\nsecret"} {
		admission := httpTrafficAdmission(1)
		admission.Target.Host = unsafe
		receipt, err := s.AdmitHTTP(t.Context(), admission)
		require.ErrorIs(t, err, ErrInvalidInput)
		require.Nil(t, receipt)
	}
	ctx, cancel := context.WithCancel(t.Context())
	admission := httpTrafficAdmission(1)
	receipt, err := s.AdmitHTTP(ctx, admission)
	require.NoError(t, err)
	admission.Target.Host = "mutated.example.com"
	admission.Decision.Allowed = false
	cancel()
	assert.False(t, s.Confirm(context.Background(), receipt))
	s.Release(receipt)
	history, err := s.HTTPHistory(t.Context(), 0, 10)
	require.NoError(t, err)
	require.Len(t, history.Records, 1)
	assert.Equal(t, "example.com", history.Records[0].Admission.Target.Host)
	assert.True(t, history.Records[0].Admission.Decision.Allowed)
	raw, err := json.Marshal(history)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "private-secret")
	assert.NotContains(t, string(raw), "mutated")
}
