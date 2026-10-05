package invocation

import (
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestHTTPTrafficConnectSelectionAndRecordedRelations(t *testing.T) {
	_, audits, authority, _, _ := newAdmissionCoordinator(t, nil)
	traffic, _ := trafficFixture(t, nil, nil)
	audits.traffic = traffic
	reader, err := NewReadService(audits, authority)
	require.NoError(t, err)
	intercepted := httpTrafficAdmission(1)
	intercepted.Target = &contract.HTTPTrafficTarget{Host: "example.com", Port: 443}
	intercepted.Decision.Transport = contract.HTTPTransportIntercept
	intercepted.Decision.Reason = contract.HTTPReasonIntercept
	intercepted.Decision.Allowed = false
	denied := httpTrafficAdmission(2)
	denied.Target = &contract.HTTPTrafficTarget{Host: "example.com", Port: 443}
	denied.Decision.Transport = contract.HTTPTransportNone
	denied.Decision.Reason = contract.HTTPReasonDestinationBlock
	denied.Decision.Allowed = false
	grant := contract.HTTPRevisionRef{ID: invocationID(90), Revision: 1}
	denied.Decision.Grant = &grant
	denied.Grants = []contract.HTTPTrafficGrant{{Reference: grant, Policy: contract.HTTPPolicy{Version: 1, Type: contract.HTTPBlockDestination, Destination: &contract.HTTPDestinationSelector{Host: "example.com", Port: 443}}}}
	tunnel := httpTrafficAdmission(3)
	tunnel.Target = &contract.HTTPTrafficTarget{Host: "example.com", Port: 443}
	tunnel.Decision.Transport = contract.HTTPTransportTunnel
	tunnel.Decision.Reason = contract.HTTPReasonTunnelAllow
	tunnel.Decision.Grant = &grant
	private := false
	tunnel.Grants = []contract.HTTPTrafficGrant{{Reference: grant, Policy: contract.HTTPPolicy{Version: 1, Type: contract.HTTPAllowTunnel, Destination: &contract.HTTPDestinationSelector{Host: "example.com", Port: 443}, AllowPrivate: &private}}}
	inner := httpTrafficAdmission(4)
	inner.Connect = &contract.HTTPConnectContext{ID: intercepted.ID, Host: "example.com", Port: 443}
	invalid := invalidHTTPAdmission(5)
	invalid.Connect = inner.Connect
	legacy := httpTrafficAdmission(6)
	for _, a := range []contract.HTTPTrafficAdmission{intercepted, denied, tunnel, inner, invalid, legacy} {
		recordHTTP(t, traffic, a)
	}
	for _, tc := range []struct {
		outcome string
		ids     []string
	}{
		{contract.HTTPOutcomeInterceptionSelected, []string{intercepted.ID}},
		{"not_dispatched", []string{invalid.ID, denied.ID}},
		{"outcome_unknown", []string{legacy.ID, inner.ID, tunnel.ID}},
	} {
		page, readErr := reader.ListHTTP(t.Context(), contract.HTTPTrafficQuery{Limit: 100, Filters: contract.HTTPTrafficFilters{Outcome: tc.outcome}})
		require.NoError(t, readErr)
		ids := []string{}
		for _, row := range page.Items {
			ids = append(ids, row.ID)
			require.Equal(t, tc.outcome, row.Outcome)
		}
		require.Equal(t, tc.ids, ids)
	}
	record, err := reader.GetHTTP(t.Context(), intercepted.ID)
	require.NoError(t, err)
	require.Equal(t, intercepted, record.Admission, "read projection must not rewrite historical admission")
	require.Nil(t, record.Completion, "selection must not manufacture completion")
	require.False(t, record.Admission.Decision.Allowed)
	query := contract.HTTPTrafficQuery{Limit: 1, Filters: contract.HTTPTrafficFilters{ConnectID: intercepted.ID}}
	page, err := reader.ListHTTP(t.Context(), query)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, invalid.ID, page.Items[0].ID)
	require.NotNil(t, page.NextCursor)
	query.Cursor = *page.NextCursor
	page, err = reader.ListHTTP(t.Context(), query)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, inner.ID, page.Items[0].ID)
	require.Nil(t, page.NextCursor)
	query.Filters.ConnectID = tunnel.ID
	_, err = reader.ListHTTP(t.Context(), query)
	require.ErrorIs(t, err, ErrStaleCursor)
	query.Cursor = ""
	page, err = reader.ListHTTP(t.Context(), query)
	require.NoError(t, err)
	require.Empty(t, page.Items, "same host/time is not correlation; opaque tunnels have no inner records")
	query.Filters.ConnectID = "not-an-id"
	_, err = reader.ListHTTP(t.Context(), query)
	require.ErrorIs(t, err, ErrInvalidInput)
}
