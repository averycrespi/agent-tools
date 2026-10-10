package invocation

import (
	"context"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func protocolReader(t *testing.T, store *TrafficStore, now time.Time) *ReadService {
	t.Helper()
	repo, err := NewTrafficRepository(store, &repositoryClock{now: now}, entropyBytes(64), func(contract.Invalidation) {})
	require.NoError(t, err)
	return &ReadService{repository: repo}
}
func TestProtocolActivityOutcomesAndScope(t *testing.T) {
	store, _ := trafficFixture(t, nil, nil)
	reader := protocolReader(t, store, invocationTestTime.Add(time.Minute))
	mcp := recordMCP(t, store, trafficPrepared(1))
	recordMCPCompletion(t, store, mcp, trafficCompletion())
	recordMCP(t, store, trafficPrepared(2))
	blockedMCP := trafficPrepared(50)
	blockedMCP.admission.Authorization.Decision = contract.DecisionDeny
	recordMCP(t, store, blockedMCP)
	failedMCP := recordMCP(t, store, trafficPrepared(51))
	mcpFailure := trafficCompletion()
	mcpFailure.Class = contract.TerminalDownstreamFailure
	recordMCPCompletion(t, store, failedMCP, mcpFailure)
	rejectedMCP := trafficPrepared(52)
	rejectedMCP.admission.Class = contract.AdmissionInvalidParams
	rejectedMCP.admission.Authorization = nil
	rejectedMCP.admission.MCP = MCPDetails{}
	recordMCP(t, store, rejectedMCP)
	http := recordHTTP(t, store, httpTrafficAdmission(3))
	recordHTTPCompletion(t, store, http, httpTrafficCompletion())
	http = recordHTTP(t, store, httpTrafficAdmission(4))
	failure := httpTrafficCompletion()
	failure.Status = 503
	recordHTTPCompletion(t, store, http, failure)
	denied := httpTrafficAdmission(5)
	denied.Decision.Allowed = false
	denied.Default = contract.HTTPDefaultBlock
	recordHTTP(t, store, denied)
	tunnel := httpTrafficAdmission(6)
	tunnel.Target.Scheme = ""
	tunnel.Target.Method = ""
	tunnel.Decision.Transport = contract.HTTPTransportIntercept
	tunnel.Decision.Allowed = false
	tunnel.Decision.Reason = contract.HTTPReasonIntercept
	recordHTTP(t, store, tunnel)
	for i, report := range []string{"", "reported_success", "reported_failure", "reported_partial"} {
		git := recordGit(t, store, gitTrafficAdmission(10+i))
		completion := gitTrafficCompletion()
		completion.ReportedResult = report
		recordGitCompletion(t, store, git, completion)
	}
	discovery := gitTrafficAdmission(14)
	discovery.Operation = "push_discovery"
	discovery.Commands = 0
	git := recordGit(t, store, discovery)
	completion := gitTrafficCompletion()
	completion.Outcome = "nonmutation"
	recordGitCompletion(t, store, git, completion)
	git = recordGit(t, store, gitTrafficAdmission(15))
	completion = gitTrafficCompletion()
	completion.TransferComplete = false
	recordGitCompletion(t, store, git, completion)
	blocked := gitTrafficAdmission(16)
	blocked.Allowed = false
	recordGit(t, store, blocked)
	result, err := reader.ProtocolActivity(t.Context(), "1h")
	require.NoError(t, err)
	require.Equal(t, "retained", result.Coverage)
	require.NotNil(t, result.Counts)
	require.Equal(t, contract.ProtocolOutcomes{Total: 5, Success: 1, Unknown: 1, Denied: 1, Failed: 1, Rejected: 1}, result.Counts.MCP)
	require.Equal(t, contract.ProtocolOutcomes{Total: 3, Success: 1, Failed: 1, Denied: 1}, result.Counts.HTTP)
	require.Equal(t, contract.ProtocolOutcomes{Total: 7, Success: 1, ReportedSuccess: 1, Failed: 1, Denied: 1, Unknown: 1, Incomplete: 1, ReportedPartial: 1}, result.Counts.Git)
	// The existing System observer still reports HTTP admissions independently.
	require.NotNil(t, store.RecordedActivity().Buckets)
}
func TestProtocolActivityExactWindowsAndFilteredHistory(t *testing.T) {
	store, _ := trafficFixture(t, nil, nil)
	now := invocationTestTime.Add(25 * time.Hour)
	reader := protocolReader(t, store, now)
	times := []time.Time{now.Add(-24*time.Hour - time.Nanosecond), now.Add(-24 * time.Hour), now.Add(-time.Hour), now.Add(-15 * time.Minute), now.Add(-time.Nanosecond), now}
	for i, at := range times {
		h := httpTrafficAdmission(i + 1)
		h.AdmittedAt = canonicalInvocationTime(at)
		h.EvaluatedAt = h.AdmittedAt
		recordHTTP(t, store, h)
		g := gitTrafficAdmission(i + 21)
		g.AdmittedAt = h.AdmittedAt
		g.EvaluatedAt = h.AdmittedAt
		recordGit(t, store, g)
		m := trafficPrepared(i + 41)
		m.AdmittedAt = h.AdmittedAt
		m.admission.Authorization.EvaluatedAt = h.AdmittedAt
		recordMCP(t, store, m)
	}
	for _, tc := range []struct {
		window string
		count  int64
	}{{"15m", 2}, {"1h", 3}, {"24h", 4}} {
		result, err := reader.ProtocolActivity(t.Context(), tc.window)
		require.NoError(t, err)
		require.NotNil(t, result.Counts)
		require.Equal(t, tc.count, result.Counts.HTTP.Total)
		require.Equal(t, tc.count, result.Counts.Git.Total)
		require.Equal(t, tc.count, result.Counts.MCP.Total)
		h, err := reader.ListHTTP(t.Context(), contract.HTTPTrafficQuery{Limit: 50, Filters: contract.HTTPTrafficFilters{From: result.From, Until: result.Until, Type: "request"}})
		require.NoError(t, err)
		require.Len(t, h.Items, int(tc.count))
		g, err := reader.ListGit(t.Context(), contract.GitTrafficQuery{Limit: 50, Filters: contract.GitTrafficFilters{From: result.From, Until: result.Until}})
		require.NoError(t, err)
		require.Len(t, g.Items, int(tc.count))
		m, err := reader.List(t.Context(), contract.InvocationListQuery{Limit: 50, Filters: contract.InvocationFilters{From: result.From, Until: result.Until}})
		require.NoError(t, err)
		require.Len(t, m.Items, int(tc.count))
	}
	_, err := reader.ProtocolActivity(t.Context(), "7d")
	require.ErrorIs(t, err, ErrInvalidInput)
}
func TestProtocolActivityCoverageAndUnavailableAreNotZero(t *testing.T) {
	store, _ := trafficFixture(t, func(c *TrafficConfig) { c.RetainedRecords = 1; c.BatchRecords = 1 }, nil)
	reader := protocolReader(t, store, invocationTestTime.Add(time.Minute))
	result, err := reader.ProtocolActivity(t.Context(), "1h")
	require.NoError(t, err)
	require.NotNil(t, result.Counts)
	require.Zero(t, result.Counts.MCP.Total)
	recordMCP(t, store, trafficPrepared(1))
	recordHTTP(t, store, httpTrafficAdmission(2))
	result, err = reader.ProtocolActivity(t.Context(), "1h")
	require.NoError(t, err)
	require.Equal(t, "partial", result.Coverage)
	require.EqualValues(t, 1, result.Counts.HTTP.Total)
	require.Zero(t, result.Counts.MCP.Total)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err = reader.ProtocolActivity(ctx, "1h")
	require.NoError(t, err)
	require.Equal(t, "unavailable", result.Coverage)
	require.Nil(t, result.Counts)
	require.True(t, store.Healthy(), "cancelled optional aggregation must not fault recording")
	store.readGate.Lock()
	result, err = reader.ProtocolActivity(t.Context(), "1h")
	store.readGate.Unlock()
	require.NoError(t, err)
	require.Nil(t, result.Counts)
	require.True(t, store.Healthy())
	store.config.ReadLifetime = time.Nanosecond
	result, err = reader.ProtocolActivity(t.Context(), "1h")
	require.NoError(t, err)
	require.Nil(t, result.Counts)
	require.True(t, store.Healthy(), "summary budget must not fault recording")
}
