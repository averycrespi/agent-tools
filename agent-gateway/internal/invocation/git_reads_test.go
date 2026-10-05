package invocation

import (
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestGitTrafficReadsBindHistoryAndPreserveReports(t *testing.T) {
	_, audits, authority, _, _ := newAdmissionCoordinator(t, nil)
	traffic, _ := trafficFixture(t, nil, nil)
	audits.traffic = traffic
	reader, err := NewReadService(audits, authority)
	require.NoError(t, err)
	for n := 1; n <= 3; n++ {
		a := gitTrafficAdmission(n)
		a.Policy = &contract.GitTrafficPolicy{RepositoryName: "Original configured name", RepositoryURL: "https://example.com:443/team/repo", Grants: []contract.GitRevisionRef{{ID: invocationID(80), Revision: "7"}}, GrantCount: 1, Updates: 1}
		observation := recordGit(t, traffic, a)
		a.Policy.RepositoryName = "Changed caller metadata"
		a.Policy.Grants[0].Revision = "9"
		if n == 3 {
			c := gitTrafficCompletion()
			c.ReportedResult = "reported_partial"
			recordGitCompletion(t, traffic, observation, c)
		}
	}
	recordHTTP(t, traffic, httpTrafficAdmission(4))
	page, err := reader.ListGit(t.Context(), contract.GitTrafficQuery{Limit: 1})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.NotNil(t, page.NextCursor)
	require.Equal(t, invocationID(3), page.Items[0].Admission.ID)
	require.Equal(t, "reported_partial", page.Items[0].Completion.ReportedResult)
	require.Equal(t, "Original configured name", page.Items[0].Admission.Policy.RepositoryName)
	require.Equal(t, "7", page.Items[0].Admission.Policy.Grants[0].Revision)
	older, err := reader.ListGit(t.Context(), contract.GitTrafficQuery{Limit: 1, Cursor: *page.NextCursor})
	require.NoError(t, err)
	require.Len(t, older.Items, 1)
	require.Nil(t, older.Items[0].Completion)
	require.Equal(t, invocationID(2), older.Items[0].Admission.ID)
	item, err := reader.GetGit(t.Context(), invocationID(3))
	require.NoError(t, err)
	require.Equal(t, page.Items[0], item)
	_, err = reader.GetGit(t.Context(), invocationID(4))
	require.ErrorIs(t, err, ErrNotFound)
	_, err = reader.ListHTTP(t.Context(), contract.HTTPTrafficQuery{Limit: 1, Cursor: *page.NextCursor})
	require.ErrorIs(t, err, ErrStaleCursor)
	_, err = traffic.db.ExecContext(t.Context(), `UPDATE traffic_meta SET pruning=pruning+1`)
	require.NoError(t, err)
	_, err = reader.ListGit(t.Context(), contract.GitTrafficQuery{Limit: 1, Cursor: *page.NextCursor})
	require.ErrorIs(t, err, ErrStaleCursor)
}

func TestGitTrafficReportRequiresCompletePushEvidence(t *testing.T) {
	a := gitTrafficAdmission(1)
	c := gitTrafficCompletion()
	c.ReportedResult = "reported_success"
	_, err := encodeGitCompletion(a, c)
	require.NoError(t, err)
	for _, change := range []func(*contract.GitTrafficCompletion){func(c *contract.GitTrafficCompletion) { c.TransferComplete = false }, func(c *contract.GitTrafficCompletion) { c.Status = 500 }, func(c *contract.GitTrafficCompletion) { c.ReportedResult = "secret upstream message" }, func(c *contract.GitTrafficCompletion) { c.Outcome = "succeeded" }} {
		changed := c
		change(&changed)
		_, err = encodeGitCompletion(a, changed)
		require.ErrorIs(t, err, ErrInvalidInput)
	}
	a.Operation = "read"
	a.Commands = 0
	_, err = encodeGitCompletion(a, c)
	require.ErrorIs(t, err, ErrInvalidInput)
	c.ReportedResult = ""
	_, err = encodeGitCompletion(a, c)
	require.NoError(t, err)
}
