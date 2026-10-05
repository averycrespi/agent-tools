package invocation

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestHistoryExportByteBoundReturnsHonestCoverage(t *testing.T) {
	_, repository, authority, _, _ := newAdmissionCoordinator(t, nil)
	traffic, _ := trafficFixture(t, func(c *TrafficConfig) { c.BudgetBytes = 64 << 20 }, nil)
	repository.traffic = traffic
	reader, err := NewReadService(repository, authority)
	require.NoError(t, err)
	for i := 1; i <= 128; i++ {
		prepared := trafficPrepared(i)
		prepared.admission.MCP.RedactedArguments = []byte(`{"value":"` + strings.Repeat("x", 8000) + `"}`)
		recordMCP(t, traffic, prepared)
	}
	page, err := reader.ExportHistory(t.Context(), 0, 256)
	require.NoError(t, err)
	require.Equal(t, int64(128), page.Retained)
	require.True(t, page.Truncated)
	require.NotEmpty(t, page.Records)
	require.Less(t, len(page.Records), 128)
	body, err := json.Marshal(page)
	require.NoError(t, err)
	require.LessOrEqual(t, len(body), contract.HistoryExportMaxBytes)
	next, err := strconv.ParseInt(page.NextSequence, 10, 64)
	require.NoError(t, err)
	remainder, err := reader.ExportHistory(t.Context(), next, 256)
	require.NoError(t, err)
	require.False(t, remainder.Truncated)
	require.Equal(t, 128, len(page.Records)+len(remainder.Records))
}

func TestHistoryExportSeparatesSnapshotCoverageFromExecution(t *testing.T) {
	_, repository, authority, _, _ := newAdmissionCoordinator(t, nil)
	traffic, _ := trafficFixture(t, nil, nil)
	repository.traffic = traffic
	reader, err := NewReadService(repository, authority)
	require.NoError(t, err)
	empty, err := reader.ExportHistory(t.Context(), 0, 256)
	require.NoError(t, err)
	require.Empty(t, empty.Records)
	require.Equal(t, "0", empty.HighWater)
	require.False(t, empty.CompleteTrafficAudit)
	recordMCP(t, traffic, trafficPrepared(1))
	recordHTTP(t, traffic, httpTrafficAdmission(2))
	recordGit(t, traffic, gitTrafficAdmission(3))
	page, err := reader.ExportHistory(t.Context(), 0, 2)
	require.NoError(t, err)
	require.Equal(t, invocationTestInstallationID, page.InstallationID)
	require.Equal(t, invocationID(90), page.Generation)
	require.Equal(t, "3", page.HighWater)
	require.Equal(t, int64(3), page.Retained)
	require.Equal(t, contract.HistoryExportAbsence, page.Absence)
	require.True(t, page.Truncated)
	require.False(t, page.CompleteTrafficAudit)
	require.Len(t, page.Records, 2)
	require.Equal(t, "mcp", page.Records[0].Protocol)
	require.Equal(t, contract.InvocationOutcomeUnknown, page.Records[0].MCP.Outcome.Class)
	require.Equal(t, "http", page.Records[1].Protocol)
	require.Nil(t, page.Records[1].HTTP.Completion)
	require.Equal(t, "2", page.NextSequence)
	next, err := reader.ExportHistory(t.Context(), 2, 2)
	require.NoError(t, err)
	require.Len(t, next.Records, 1)
	require.Equal(t, "git", next.Records[0].Protocol)
	require.Nil(t, next.Records[0].Git.Completion)
	require.False(t, next.Truncated)
	encoded, err := json.Marshal(page)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), contract.HistoryExportMaxBytes)
	// Export never acquires the writer gate or observes a control repository.
	traffic.writerGate.Lock()
	_, err = reader.ExportHistory(t.Context(), 0, 256)
	traffic.writerGate.Unlock()
	require.NoError(t, err)
	for n := 0; n < cap(traffic.readSlots); n++ {
		traffic.readSlots <- struct{}{}
	}
	_, err = reader.ExportHistory(t.Context(), 0, 256)
	require.ErrorIs(t, err, ErrTrafficCapacity)
	for n := 0; n < cap(traffic.readSlots); n++ {
		<-traffic.readSlots
	}
	repository.traffic = NewOptionalTraffic(DefaultTrafficConfig())
	defer func() { require.NoError(t, repository.traffic.Close()) }()
	_, err = reader.ExportHistory(t.Context(), 0, 256)
	require.ErrorIs(t, err, ErrTrafficFault, "unavailable is not an empty successful export")
}
