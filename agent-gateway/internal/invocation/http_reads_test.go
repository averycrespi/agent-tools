package invocation

import (
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPTrafficReadsSeparateDomainsAndBindCursors(t *testing.T) {
	_, audits, authority, _, _ := newAdmissionCoordinator(t, nil)
	traffic, _ := trafficFixture(t, nil, nil)
	audits.traffic = traffic
	reader, err := NewReadService(audits, authority)
	require.NoError(t, err)
	for n := 1; n <= 3; n++ {
		observation := recordHTTP(t, traffic, httpTrafficAdmission(n))
		if n == 3 {
			recordHTTPCompletion(t, traffic, observation, httpTrafficCompletion())
		}
	}
	recordMCP(t, traffic, trafficPrepared(4))
	query := contract.HTTPTrafficQuery{Limit: 1}
	page, err := reader.ListHTTP(t.Context(), query)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.NotNil(t, page.NextCursor)
	assert.Equal(t, invocationID(3), page.Items[0].ID)
	assert.Equal(t, "succeeded", page.Items[0].Outcome)
	query.Cursor = *page.NextCursor
	older, err := reader.ListHTTP(t.Context(), query)
	require.NoError(t, err)
	require.Len(t, older.Items, 1)
	assert.Equal(t, invocationID(2), older.Items[0].ID)
	assert.Equal(t, "outcome_unknown", older.Items[0].Outcome)
	query.Filters.Outcome = "succeeded"
	_, err = reader.ListHTTP(t.Context(), query)
	assert.ErrorIs(t, err, ErrStaleCursor)
	query.Cursor = ""
	filtered, err := reader.ListHTTP(t.Context(), query)
	require.NoError(t, err)
	require.Len(t, filtered.Items, 1)
	assert.Nil(t, filtered.NextCursor)
	query.Filters = contract.HTTPTrafficFilters{Destination: "example.com", Type: "request", Decision: "allow", PrincipalID: httpTrafficAdmission(1).Principal.ID}
	query.Limit = 100
	filtered, err = reader.ListHTTP(t.Context(), query)
	require.NoError(t, err)
	assert.Len(t, filtered.Items, 3)
	_, err = reader.GetHTTP(t.Context(), invocationID(4))
	assert.ErrorIs(t, err, ErrNotFound)
	item, err := reader.GetHTTP(t.Context(), invocationID(3))
	require.NoError(t, err)
	require.NotNil(t, item.Completion)
	assert.Equal(t, 200, item.Completion.Status)
	query.Filters.Destination = "example.com/private?secret"
	filtered, err = reader.ListHTTP(t.Context(), query)
	require.NoError(t, err)
	assert.Empty(t, filtered.Items, "destination searches only retained host, never paths")
}
